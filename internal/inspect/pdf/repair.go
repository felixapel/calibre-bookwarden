package pdf

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
)

// repair rebuilds the cross-reference data and the trailer by scanning the file.
func (d *Doc) repair() error {
	entries, trailers, err := d.scan()
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return fmt.Errorf("%w: no objects", errSyntax)
	}
	d.xref, d.repaired = entries, true
	clear(d.cache)
	clear(d.streams)
	d.order = nil
	d.streamBytes = 0
	if err := d.addMembers(); err != nil {
		return err
	}
	d.trailer, err = d.findTrailer(trailers)
	return err
}

// repairEntries rebuilds only the cross-reference data, keeping the trailer.
func (d *Doc) repairEntries() error {
	entries, _, err := d.scan()
	if err != nil {
		return err
	}
	d.repaired = true
	for num, e := range entries {
		if err := d.Err(); err != nil {
			return err
		}
		if err := d.put(d.xref, num, e, true); err != nil {
			return err
		}
	}
	clear(d.cache)
	clear(d.streams)
	d.order = nil
	d.streamBytes = 0
	return d.addMembers()
}

// scan finds every "num gen obj" at a line start, keeping the last one for
// each object, and every trailer keyword.
func (d *Doc) scan() (map[int]entry, []int64, error) {
	const win, overlap = 1 << 20, 64
	entries := map[int]entry{}
	var trailers []int64
	buf := make([]byte, win)
	for off := int64(0); off < d.size; off += win - overlap {
		if err := d.ctx.Err(); err != nil {
			return nil, nil, err
		}
		n, _ := d.r.ReadAt(buf[:min(win, d.size-off)], off)
		b := buf[:n]
		for i := 0; ; {
			if err := d.Err(); err != nil {
				return nil, nil, err
			}
			j := bytes.Index(b[i:], []byte("obj"))
			if j < 0 {
				break
			}
			p := i + j
			i = p + 3
			if num, start, ok := objHeader(b, p, off == 0); ok {
				if err := d.put(entries, num, entry{kind: 1, offset: off + int64(start), hint: hints(b[p:min(p+512, len(b))])}, true); err != nil {
					return nil, nil, err
				}
			}
		}
		for i := 0; ; {
			if err := d.Err(); err != nil {
				return nil, nil, err
			}
			j := bytes.Index(b[i:], []byte("trailer"))
			if j < 0 {
				break
			}
			i += j + 7
			if t := off + int64(i); len(trailers) == 0 || t > trailers[len(trailers)-1] {
				if len(trailers) >= maxRepairTrailers {
					return nil, nil, fmt.Errorf("%w: more than %d repair trailers", ErrTooLarge, maxRepairTrailers)
				}
				trailers = append(trailers, t)
			}
		}
		if int64(n) < win {
			break
		}
	}
	return entries, trailers, nil
}

func hints(b []byte) byte {
	var h byte
	if bytes.Contains(b, []byte("/ObjStm")) {
		h |= hintObjStm
	}
	if bytes.Contains(b, []byte("/Catalog")) {
		h |= hintCatalog
	}
	if bytes.Contains(b, []byte("/XRef")) {
		h |= hintXRef
	}
	return h
}

// objHeader matches "num gen obj" ending at b[p:p+3], starting a line or
// following endobj, and returns the object number and where it starts.
func objHeader(b []byte, p int, fileStart bool) (int, int, bool) {
	if p+3 < len(b) && !isSpace(b[p+3]) && !isDelim(b[p+3]) {
		return 0, 0, false
	}
	k := p - 1
	digits := func(maxLen int) (int, bool) {
		spaces := 0
		for k >= 0 && isSpace(b[k]) {
			k--
			spaces++
		}
		end := k
		for k >= 0 && b[k] >= '0' && b[k] <= '9' {
			k--
		}
		if spaces == 0 || end-k < 1 || end-k > maxLen {
			return 0, false
		}
		v, err := strconv.Atoi(string(b[k+1 : end+1]))
		return v, err == nil && v <= 1<<31
	}
	if _, ok := digits(5); !ok {
		return 0, 0, false
	}
	num, ok := digits(10)
	if !ok {
		return 0, 0, false
	}
	start := k + 1
	switch {
	case k < 0:
		return num, start, fileStart
	case b[k] == '\r' || b[k] == '\n':
		return num, start, true
	}
	return num, start, bytes.HasSuffix(bytes.TrimRight(b[:k+1], "\x00\t\f "), []byte("endobj"))
}

// addMembers lists the objects inside object streams that weren't found on
// their own.
func (d *Doc) addMembers() error {
	nums := slices.Sorted(maps.Keys(d.xref))
	for _, num := range nums {
		if err := d.Err(); err != nil {
			return err
		}
		if e := d.xref[num]; e.kind != 1 || e.hint&hintObjStm == 0 {
			continue
		}
		stm, err := d.objStream(num)
		if err != nil {
			if fatal := d.Err(); fatal != nil {
				return fatal
			}
			if errors.Is(err, ErrTooLarge) {
				return err
			}
			continue
		}
		for i, p := range stm.pairs {
			if err := d.Err(); err != nil {
				return err
			}
			if _, ok := d.xref[int(p[0])]; !ok && p[0] >= 0 && p[0] <= 1<<31 {
				if err := d.add(int(p[0]), entry{kind: 2, stream: num, index: i}); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// findTrailer picks the last trailer whose /Root loads, else the last xref
// stream's dictionary with one, else the last parsable trailer with /Root
// pointing at the last catalog found.
func (d *Doc) findTrailer(trailers []int64) (Dict, error) {
	var last Dict
	for i := len(trailers) - 1; i >= 0; i-- {
		if err := d.Err(); err != nil {
			return nil, err
		}
		obj, err := d.lexer(d.r, d.size, trailers[i]).object(0)
		if fatal := d.Err(); fatal != nil {
			return nil, fatal
		}
		tr, ok := obj.(Dict)
		if err != nil || !ok {
			continue
		}
		if last == nil {
			last = tr
		}
		d.trailer = tr
		if d.rootLoads() {
			return tr, d.Err()
		}
	}
	byOffset := slices.SortedFunc(maps.Keys(d.xref), func(a, b int) int {
		return int(d.xref[b].offset - d.xref[a].offset)
	})
	for _, num := range byOffset {
		if err := d.Err(); err != nil {
			return nil, err
		}
		if e := d.xref[num]; e.kind != 1 || e.hint&hintXRef == 0 {
			continue
		}
		if s, ok := d.loadQuietly(num).(Stream); ok && s.Dict["Type"] == Name("XRef") {
			d.trailer = s.Dict
			if d.rootLoads() {
				return s.Dict, d.Err()
			}
			if last == nil {
				last = s.Dict
			}
		}
	}
	if err := d.Err(); err != nil {
		return nil, err
	}
	tr := last
	if tr == nil {
		tr = Dict{}
	}
	delete(tr, "Root")
	for _, num := range byOffset {
		if err := d.Err(); err != nil {
			return nil, err
		}
		if e := d.xref[num]; e.kind != 1 || e.hint&hintCatalog == 0 {
			continue
		}
		if c, ok := d.loadQuietly(num).(Dict); ok && c["Type"] == Name("Catalog") {
			if err := d.budget.reserve(128, 1); err != nil {
				return nil, err
			}
			tr["Root"] = Ref{Num: num}
			break
		}
	}
	if _, ok := tr["Root"]; ok {
		return tr, d.Err()
	}
	for _, num := range slices.Backward(slices.Sorted(maps.Keys(d.xref))) { // a catalog inside an object stream
		if err := d.Err(); err != nil {
			return nil, err
		}
		if d.xref[num].kind != 2 {
			continue
		}
		if c, ok := d.loadQuietly(num).(Dict); ok && c["Type"] == Name("Catalog") {
			if err := d.budget.reserve(128, 1); err != nil {
				return nil, err
			}
			tr["Root"] = Ref{Num: num}
			break
		}
	}
	return tr, d.Err()
}

func (d *Doc) loadQuietly(num int) Object {
	o, err := d.object(num)
	if err != nil {
		return nil
	}
	return o
}
