package pdf

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
)

const (
	maxObjects        = 131_072  // cross-reference entries
	maxCached         = 100_000  // parsed objects kept
	maxObjStms        = 16       // decoded object streams kept
	maxResolve        = 32       // references followed, and objects loaded inside each other
	maxSections       = 64       // cross-reference sections followed through /Prev
	maxStream         = 64 << 20 // decoded bytes of a cross-reference or object stream
	maxStreamCache    = 32 << 20 // retained decoded object streams, including capacity
	maxRepairTrailers = 4096
)

// errMisplaced is an xref entry that doesn't point at its object.
var errMisplaced = errors.New("pdf: object not where the cross-reference says")

type entry struct {
	kind   byte  // 1: at offset; 2: in object stream stream, at index
	hint   byte  // repair only: what the object's first bytes suggest
	offset int64 //
	stream int
	index  int
}

// Hints a repair scan takes from an object's first bytes.
const (
	hintObjStm byte = 1 << iota
	hintCatalog
	hintXRef
)

type objStm struct {
	data  []byte
	first int64
	pairs [][2]int64 // object number, offset after first
}

// Doc is an open PDF. It is not safe for concurrent use.
type Doc struct {
	ctx         context.Context
	r           io.ReaderAt
	size        int64
	xref        map[int]entry
	trailer     Dict
	repaired    bool
	cache       map[int]Object
	streams     map[int]*objStm
	order       []int // object streams in the order they were decoded
	depth       int
	budget      *materialization
	limitErr    error
	streamBytes int64
}

func newDoc(ctx context.Context, r io.ReaderAt, size int64) *Doc {
	return &Doc{ctx: ctx, r: r, size: size, xref: map[int]entry{}, cache: map[int]Object{}, streams: map[int]*objStm{}, budget: &materialization{bytes: maxParsedBytes, nodes: maxParsedNodes}}
}

func (d *Doc) lexer(r io.ReaderAt, size, pos int64) *lexer {
	l := newLexer(r, size, pos)
	l.ctx, l.budget = d.ctx, d.budget
	return l
}

// Err reports a document-wide resource limit or cancellation. Such failures
// remain sticky even when a caller tolerates a malformed optional object.
func (d *Doc) Err() error {
	if err := d.ctx.Err(); err != nil {
		return err
	}
	if d.budget.err != nil {
		return d.budget.err
	}
	return d.limitErr
}

// Open reads a PDF's cross-reference data, or rebuilds it by scanning the
// file when it is missing or doesn't match the file.
func Open(ctx context.Context, r io.ReaderAt, size int64) (*Doc, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	head := make([]byte, min(size, 1024))
	n, _ := r.ReadAt(head, 0)
	if !bytes.Contains(head[:n], []byte("%PDF-")) {
		return nil, ErrNotPDF
	}
	d := newDoc(ctx, r, size)
	err := d.readXref()
	if err == nil && d.rootLoads() {
		if err := d.Err(); err != nil {
			return nil, err
		}
		return d, nil
	}
	if fatal := d.Err(); fatal != nil {
		return nil, fatal
	}
	if errors.Is(err, ErrTooLarge) {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := d.repair(); err != nil {
		return nil, err
	}
	if err := d.Err(); err != nil {
		return nil, err
	}
	return d, nil
}

// Trailer is the trailer dictionary, merged over incremental updates.
func (d *Doc) Trailer() Dict { return d.trailer }

// Repaired reports whether the cross-reference data had to be rebuilt.
func (d *Doc) Repaired() bool { return d.repaired }

// Resolve follows references until o isn't one. A missing object is nil.
func (d *Doc) Resolve(o Object) (Object, error) {
	if err := d.Err(); err != nil {
		return nil, err
	}
	for i := 0; ; i++ {
		ref, ok := o.(Ref)
		if !ok {
			return o, nil
		}
		if i == maxResolve {
			return nil, fmt.Errorf("%w: references nested deeper than %d", errSyntax, maxResolve)
		}
		var err error
		if o, err = d.object(ref.Num); err != nil {
			return nil, err
		}
	}
}

func (d *Doc) rootLoads() bool {
	root, ok := d.trailer["Root"].(Ref)
	if !ok {
		return false
	}
	o, err := d.load(root.Num)
	if errors.Is(err, ErrTooLarge) {
		d.limitErr = err
	}
	_, isDict := o.(Dict)
	return err == nil && isDict
}

// object returns object num, from the cache or the file. A misplaced object
// rebuilds the cross-reference data once.
func (d *Doc) object(num int) (obj Object, err error) {
	defer func() {
		if errors.Is(err, ErrTooLarge) {
			d.limitErr = err
		}
	}()
	if err := d.Err(); err != nil {
		return nil, err
	}
	if o, ok := d.cache[num]; ok {
		return o, nil
	}
	o, err := d.load(num)
	if errors.Is(err, errMisplaced) && !d.repaired {
		if err := d.repairEntries(); err != nil {
			return nil, err
		}
		o, err = d.load(num)
	}
	if err != nil {
		return nil, err
	}
	if len(d.cache) >= maxCached {
		clear(d.cache)
	}
	d.cache[num] = o
	return o, nil
}

func (d *Doc) load(num int) (Object, error) {
	if err := d.ctx.Err(); err != nil {
		return nil, err
	}
	if d.depth >= maxResolve {
		return nil, fmt.Errorf("%w: objects nested deeper than %d", errSyntax, maxResolve)
	}
	d.depth++
	defer func() { d.depth-- }()
	e, ok := d.xref[num]
	switch {
	case !ok:
		return nil, nil
	case e.kind == 2:
		stm, err := d.objStream(e.stream)
		if err != nil {
			return nil, err
		}
		return stm.get(d, num, e.index)
	}
	n, obj, err := d.lexer(d.r, d.size, e.offset).indirect()
	if fatal := d.Err(); fatal != nil {
		return nil, fatal
	}
	if err != nil || n != num {
		return nil, fmt.Errorf("%w: object %d at %d: %w", errMisplaced, num, e.offset, err)
	}
	if s, ok := obj.(Stream); ok {
		s = d.settle(s)
		return s, d.Err()
	}
	return obj, nil
}

// settle sets a stream's Length: its /Length when endstream follows it,
// otherwise the distance to the next endstream.
func (d *Doc) settle(s Stream) Stream {
	length := int64(-1)
	if v, err := d.Resolve(s.Dict["Length"]); err == nil {
		if n, ok := v.(int64); ok && n >= 0 {
			length = n
		}
	}
	if length >= 0 && s.Offset+length <= d.size && d.endstreamAt(s.Offset+length) {
		s.Length = length
		return s
	}
	if p := d.findEndstream(s.Offset); p >= 0 {
		s.Length = p - s.Offset
		return s
	}
	s.Length = max(min(length, d.size-s.Offset), 0)
	return s
}

func (d *Doc) endstreamAt(p int64) bool {
	b := make([]byte, 32)
	n, _ := d.r.ReadAt(b, p)
	return bytes.HasPrefix(bytes.TrimLeft(b[:n], "\x00\t\n\f\r "), []byte("endstream"))
}

// findEndstream returns where a stream's data ends: before the end of line
// that precedes the next endstream within maxStream bytes, or -1.
func (d *Doc) findEndstream(from int64) int64 {
	const win = 1 << 20
	marker := []byte("endstream")
	buf := make([]byte, win)
	for off := from; off < d.size && off < from+maxStream; off += win - int64(len(marker)) {
		if d.ctx.Err() != nil {
			return -1
		}
		n, _ := d.r.ReadAt(buf[:min(win, d.size-off)], off)
		if i := bytes.Index(buf[:n], marker); i >= 0 {
			p := off + int64(i)
			end := make([]byte, 2)
			if p-from >= 2 {
				_, _ = d.r.ReadAt(end, p-2)
				switch {
				case end[0] == '\r' && end[1] == '\n':
					p -= 2
				case end[1] == '\n' || end[1] == '\r':
					p--
				}
			}
			return p
		}
		if int64(n) < win {
			break
		}
	}
	return -1
}

// objStream decodes object stream num, keeping the last few.
func (d *Doc) objStream(num int) (*objStm, error) {
	if s, ok := d.streams[num]; ok {
		return s, nil
	}
	o, err := d.object(num)
	if err != nil {
		return nil, err
	}
	s, ok := o.(Stream)
	if !ok || s.Dict["Type"] != Name("ObjStm") {
		return nil, fmt.Errorf("%w: object %d isn't an object stream", errSyntax, num)
	}
	n, _ := s.Dict["N"].(int64)
	first, _ := s.Dict["First"].(int64)
	if n < 0 || n > maxItems || first < 0 {
		return nil, fmt.Errorf("%w: object stream %d holds %d objects", ErrTooLarge, num, n)
	}
	data, err := d.Decode(s, maxStream)
	if err != nil {
		return nil, err
	}
	stm := &objStm{data: data, first: first, pairs: make([][2]int64, 0, n)}
	l := d.lexer(bytes.NewReader(data), min(first, int64(len(data))), 0)
	for range n {
		if err := d.Err(); err != nil {
			return nil, err
		}
		t1, err1 := l.next()
		t2, err2 := l.next()
		num, ok1 := t1.num.(int64)
		off, ok2 := t2.num.(int64)
		if err1 != nil || err2 != nil || !ok1 || !ok2 {
			break
		}
		stm.pairs = append(stm.pairs, [2]int64{num, off})
	}
	if err := d.Err(); err != nil {
		return nil, err
	}
	d.cacheStream(num, stm)
	return stm, nil
}

func (s *objStm) retainedBytes() int64 {
	return int64(cap(s.data)) + 16*int64(cap(s.pairs)) + 64
}

func (d *Doc) cacheStream(num int, stm *objStm) {
	weight := stm.retainedBytes()
	if weight > maxStreamCache {
		return // usable for this lookup, but too large to retain
	}
	for len(d.order) >= maxObjStms || d.streamBytes+weight > maxStreamCache {
		old := d.order[0]
		d.streamBytes -= d.streams[old].retainedBytes()
		delete(d.streams, old)
		d.order = d.order[1:]
	}
	d.streams[num] = stm
	d.order = append(d.order, num)
	d.streamBytes += weight
}

func (s *objStm) get(d *Doc, num, index int) (Object, error) {
	i := -1
	if index >= 0 && index < len(s.pairs) && s.pairs[index][0] == int64(num) {
		i = index
	} else {
		for j, p := range s.pairs {
			if err := d.Err(); err != nil {
				return nil, err
			}
			if p[0] == int64(num) {
				i = j
			}
		}
	}
	if i < 0 {
		return nil, nil
	}
	off := s.first + s.pairs[i][1]
	if off < 0 || off >= int64(len(s.data)) {
		return nil, fmt.Errorf("%w: object %d outside its object stream", errSyntax, num)
	}
	return d.lexer(bytes.NewReader(s.data), int64(len(s.data)), off).object(0)
}
