package pdf

import (
	"bytes"
	"fmt"
)

// readXref follows startxref and every /Prev, newest section first; the
// first entry seen for an object wins.
func (d *Doc) readXref() error {
	tail := make([]byte, min(d.size, 2048))
	n, _ := d.r.ReadAt(tail, d.size-int64(len(tail)))
	i := bytes.LastIndex(tail[:n], []byte("startxref"))
	if i < 0 {
		return fmt.Errorf("%w: no startxref", errSyntax)
	}
	t, err := d.lexer(d.r, d.size, d.size-int64(len(tail))+int64(i)+9).next()
	off, ok := t.num.(int64)
	if err != nil || !ok {
		return fmt.Errorf("%w: startxref without an offset", errSyntax)
	}
	seen := map[int64]bool{}
	for range maxSections {
		if err := d.ctx.Err(); err != nil {
			return err
		}
		if seen[off] {
			break
		}
		seen[off] = true
		tr, err := d.section(off)
		if err != nil {
			return err
		}
		d.mergeTrailer(tr)
		prev, ok := tr["Prev"].(int64)
		if !ok {
			break
		}
		off = prev
	}
	return nil
}

// mergeTrailer keeps the newest trailer, filling in what it lacks from older ones.
func (d *Doc) mergeTrailer(tr Dict) {
	if d.trailer == nil {
		d.trailer = tr
		return
	}
	for _, k := range []Name{"Root", "Info", "Encrypt", "ID"} {
		if _, ok := d.trailer[k]; !ok && tr[k] != nil {
			if d.budget.reserve(128+int64(len(k)), 0) != nil {
				return
			}
			d.trailer[k] = tr[k]
		}
	}
}

// section reads the cross-reference section at off: a table and its trailer,
// or an xref stream whose dictionary is the trailer.
func (d *Doc) section(off int64) (Dict, error) {
	if off < 0 || off >= d.size {
		return nil, fmt.Errorf("%w: xref offset %d out of range", errSyntax, off)
	}
	l := d.lexer(d.r, d.size, off)
	t, err := l.next()
	if err != nil {
		return nil, err
	}
	if t.kind == tKeyword && string(t.b) == "xref" {
		return d.table(l)
	}
	l.unread(t)
	s, err := d.xrefStreamObject(l)
	if err != nil {
		return nil, err
	}
	return s.Dict, d.xrefStream(s)
}

func (d *Doc) xrefStreamObject(l *lexer) (Stream, error) {
	_, obj, err := l.indirect()
	if err != nil {
		return Stream{}, err
	}
	s, ok := obj.(Stream)
	if !ok || s.Dict["Type"] != Name("XRef") {
		return Stream{}, fmt.Errorf("%w: no xref stream", errSyntax)
	}
	s = d.settle(s)
	return s, d.Err()
}

func (d *Doc) table(l *lexer) (Dict, error) {
	for {
		if err := d.ctx.Err(); err != nil {
			return nil, err
		}
		t, err := l.next()
		if err != nil {
			return nil, err
		}
		if t.kind == tKeyword && string(t.b) == "trailer" {
			obj, err := l.object(0)
			if err != nil {
				return nil, err
			}
			tr, ok := obj.(Dict)
			if !ok {
				return nil, fmt.Errorf("%w: trailer isn't a dictionary", errSyntax)
			}
			if off, ok := tr["XRefStm"].(int64); ok && off >= 0 && off < d.size { // a hybrid file
				if s, err := d.xrefStreamObject(d.lexer(d.r, d.size, off)); err == nil {
					if err := d.xrefStream(s); err != nil {
						return nil, err
					}
				}
			}
			return tr, d.Err()
		}
		t2, err := l.next()
		start, ok1 := t.num.(int64)
		count, ok2 := t2.num.(int64)
		if err != nil || !ok1 || !ok2 || start < 0 || count < 0 {
			return nil, fmt.Errorf("%w: bad xref subsection at %d", errSyntax, t.pos)
		}
		for j := range count {
			if j%4096 == 0 {
				if err := d.ctx.Err(); err != nil {
					return nil, err
				}
			}
			a, err1 := l.next()
			_, err2 := l.next()
			c, err3 := l.next()
			off, ok := a.num.(int64)
			if err1 != nil || err2 != nil || err3 != nil || !ok || c.kind != tKeyword {
				return nil, fmt.Errorf("%w: bad xref entry at %d", errSyntax, a.pos)
			}
			if string(c.b) == "n" && off > 0 {
				if err := d.add(int(start+j), entry{kind: 1, offset: off}); err != nil {
					return nil, err
				}
			}
		}
	}
}

func (d *Doc) add(num int, e entry) error {
	return d.put(d.xref, num, e, false)
}

func (d *Doc) put(entries map[int]entry, num int, e entry, replace bool) error {
	if num < 0 {
		return nil
	}
	if _, ok := entries[num]; ok {
		if replace {
			entries[num] = e
		}
		return nil
	}
	if len(entries) >= maxObjects {
		d.limitErr = fmt.Errorf("%w: more than %d objects", ErrTooLarge, maxObjects)
		return d.limitErr
	}
	entries[num] = e
	return nil
}

// xrefStream adds the entries of a cross-reference stream.
func (d *Doc) xrefStream(s Stream) error {
	data, err := d.Decode(s, maxStream)
	if err != nil {
		return err
	}
	w, ok := s.Dict["W"].(Array)
	if !ok || len(w) != 3 {
		return fmt.Errorf("%w: xref stream /W", errSyntax)
	}
	var widths [3]int
	sum := 0
	for i, v := range w {
		n, ok := v.(int64)
		if !ok || n < 0 || n > 8 {
			return fmt.Errorf("%w: xref stream /W", errSyntax)
		}
		widths[i] = int(n)
		sum += int(n)
	}
	if sum == 0 {
		return fmt.Errorf("%w: xref stream /W", errSyntax)
	}
	index, ok := s.Dict["Index"].(Array)
	if !ok {
		size, _ := s.Dict["Size"].(int64)
		index = Array{int64(0), size}
	}
	p := 0
	for k := 0; k+1 < len(index); k += 2 {
		start, ok1 := index[k].(int64)
		count, ok2 := index[k+1].(int64)
		if !ok1 || !ok2 || start < 0 {
			return fmt.Errorf("%w: xref stream /Index", errSyntax)
		}
		for j := int64(0); j < count && p+sum <= len(data); j++ {
			if j%4096 == 0 {
				if err := d.Err(); err != nil {
					return err
				}
			}
			var f [3]int64
			for i, width := range widths {
				for range width {
					f[i] = f[i]<<8 | int64(data[p])
					p++
				}
			}
			if widths[0] == 0 {
				f[0] = 1
			}
			var err error
			switch f[0] {
			case 1:
				if f[1] > 0 {
					err = d.add(int(start+j), entry{kind: 1, offset: f[1]})
				}
			case 2:
				err = d.add(int(start+j), entry{kind: 2, stream: int(f[1]), index: int(f[2])})
			}
			if err != nil {
				return err
			}
		}
	}
	return nil
}
