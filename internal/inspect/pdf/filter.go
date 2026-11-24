package pdf

import (
	"bytes"
	"compress/flate"
	"compress/zlib"
	"context"
	"encoding/ascii85"
	"errors"
	"fmt"
	"io"
)

// Decode returns a stream's data with its filters undone: Flate (with PNG
// predictors), ASCIIHex and ASCII85. Data past limit bytes is ErrTooLarge.
func (d *Doc) Decode(s Stream, limit int64) ([]byte, error) {
	if err := d.Err(); err != nil {
		return nil, err
	}
	if s.Offset < 0 || s.Length < 0 || s.Offset > d.size {
		return nil, fmt.Errorf("%w: stream outside the file", errSyntax)
	}
	if limit < 0 {
		return nil, fmt.Errorf("%w: negative decode limit", errSyntax)
	}
	limit = min(limit, maxStream)
	if s.Length > maxStream {
		d.limitErr = fmt.Errorf("%w: encoded stream of %d bytes", ErrTooLarge, s.Length)
		return nil, d.limitErr
	}
	if s.Length > 2*limit+64<<10 {
		return nil, fmt.Errorf("%w: a stream of %d bytes", ErrTooLarge, s.Length)
	}
	// Resolve indirect filters before retaining an encoded buffer: resolution
	// may recursively decode object streams.
	filters, parms, err := d.filters(s.Dict)
	if err != nil {
		return nil, err
	}
	data := make([]byte, min(s.Length, d.size-s.Offset))
	n, _ := d.r.ReadAt(data, s.Offset)
	data = data[:n]
	for i, f := range filters {
		if err := d.Err(); err != nil {
			return nil, err
		}
		var err error
		switch f {
		case "FlateDecode", "Fl":
			if data, err = inflate(d.ctx, data, limit); err == nil {
				data, err = unpredict(d.ctx, data, parms[i], limit)
			}
		case "ASCIIHexDecode", "AHx":
			data, err = unhex(d.ctx, data)
		case "ASCII85Decode", "A85":
			data, err = una85(d.ctx, data, limit)
		default:
			err = fmt.Errorf("%w: %s", ErrUnsupportedFilter, f)
		}
		if err != nil {
			return nil, err
		}
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%w: a stream past %d bytes", ErrTooLarge, limit)
	}
	return data, nil
}

// filters lists a stream's filters and the parameters of each.
func (d *Doc) filters(dict Dict) ([]Name, []Dict, error) {
	var names []Name
	f, err := d.Resolve(dict["Filter"])
	if err != nil {
		return nil, nil, err
	}
	switch f := f.(type) {
	case Name:
		names = []Name{f}
	case Array:
		for _, v := range f {
			v, err := d.Resolve(v)
			if err != nil {
				return nil, nil, err
			}
			n, _ := v.(Name)
			names = append(names, n)
		}
	}
	parms := make([]Dict, len(names))
	p, err := d.Resolve(dict["DecodeParms"])
	if err != nil {
		return nil, nil, err
	}
	switch p := p.(type) {
	case Dict:
		if len(parms) > 0 {
			parms[0] = p
		}
	case Array:
		for i, v := range p {
			if i < len(parms) {
				v, err := d.Resolve(v)
				if err != nil {
					return nil, nil, err
				}
				parms[i], _ = v.(Dict)
			}
		}
	}
	return names, parms, nil
}

// readDecoded bounds backing capacity as well as length, and checks context
// between decoder reads. io.ReadAll can grow its final allocation past limit.
func readDecoded(ctx context.Context, r io.Reader, limit int64) ([]byte, error) {
	out := make([]byte, 0, min(4096, limit))
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if int64(len(out)) == limit {
			// Probe EOF without growing/copying a full limit-sized buffer.
			var probe [1]byte
			n, err := r.Read(probe[:])
			if n != 0 {
				return nil, fmt.Errorf("%w: a stream past %d bytes", ErrTooLarge, limit)
			}
			if err == io.EOF {
				return out, nil
			}
			if err != nil {
				return out, err
			}
			continue
		}
		if len(out) == cap(out) {
			capacity := min(2*int64(cap(out)), limit)
			next := make([]byte, len(out), capacity)
			copy(next, out)
			out = next
		}
		n, err := r.Read(out[len(out):cap(out)])
		out = out[:len(out)+n]
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return out, err
		}
	}
}

// inflate undoes Flate, with or without the zlib wrapper. A damaged end is
// accepted when data came out, as readers do.
func inflate(ctx context.Context, data []byte, limit int64) ([]byte, error) {
	var r io.ReadCloser
	if zr, err := zlib.NewReader(bytes.NewReader(data)); err == nil {
		r = zr
	} else {
		r = flate.NewReader(bytes.NewReader(data))
	}
	defer func() { _ = r.Close() }()
	out, err := readDecoded(ctx, r, limit)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if errors.Is(err, ErrTooLarge) {
		return nil, err
	}
	if int64(len(out)) > limit {
		return nil, fmt.Errorf("%w: a stream past %d bytes", ErrTooLarge, limit)
	}
	if err != nil && len(out) == 0 {
		return nil, fmt.Errorf("%w: Flate: %w", errSyntax, err)
	}
	return out, nil
}

// unpredict undoes the PNG predictors (10 to 15) a Flate stream's
// parameters name.
func unpredict(ctx context.Context, data []byte, p Dict, limit int64) ([]byte, error) {
	predictor := intOr(p["Predictor"], 1)
	switch {
	case predictor <= 1:
		return data, nil
	case predictor < 10:
		return nil, fmt.Errorf("%w: predictor %d", ErrUnsupportedFilter, predictor)
	}
	colors, bpc, columns := intOr(p["Colors"], 1), intOr(p["BitsPerComponent"], 8), intOr(p["Columns"], 1)
	if colors < 1 || colors > 32 || bpc < 1 || bpc > 16 || columns < 1 || columns > 1<<24 {
		return nil, fmt.Errorf("%w: predictor parameters", errSyntax)
	}
	bpp := int(max(colors*bpc/8, 1))
	rowLen := int((colors*bpc*columns + 7) / 8)
	if int64(rowLen) > limit {
		return nil, fmt.Errorf("%w: predictor rows of %d bytes", ErrTooLarge, rowLen)
	}
	out := data[:0]
	var prev []byte
	for i := 0; i+1+rowLen <= len(data); i += rowLen + 1 {
		filter := data[i]
		row := data[i+1 : i+1+rowLen]
		for j := range row {
			if j%4096 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			var left, up, upLeft byte
			if prev != nil {
				up = prev[j]
			}
			if j >= bpp {
				left = row[j-bpp]
				if prev != nil {
					upLeft = prev[j-bpp]
				}
			}
			switch filter {
			case 0:
			case 1:
				row[j] += left
			case 2:
				row[j] += up
			case 3:
				row[j] += byte((int(left) + int(up)) / 2) //nolint:gosec // at most 255
			case 4:
				row[j] += paeth(left, up, upLeft)
			default:
				return nil, fmt.Errorf("%w: PNG filter %d", errSyntax, filter)
			}
		}
		out = append(out, row...)
		prev = out[len(out)-rowLen:]
	}
	return out, nil
}

func paeth(a, b, c byte) byte {
	p := int(a) + int(b) - int(c)
	pa, pb, pc := abs(p-int(a)), abs(p-int(b)), abs(p-int(c))
	switch {
	case pa <= pb && pa <= pc:
		return a
	case pb <= pc:
		return b
	}
	return c
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func intOr(o Object, def int64) int64 {
	if n, ok := o.(int64); ok {
		return n
	}
	return def
}

func unhex(ctx context.Context, data []byte) ([]byte, error) {
	if i := bytes.IndexByte(data, '>'); i >= 0 {
		data = data[:i]
	}
	l := newLexer(bytes.NewReader(append(bytes.Clone(data), '>')), int64(len(data)+1), 0)
	l.ctx = ctx
	return l.hex()
}

func una85(ctx context.Context, data []byte, limit int64) ([]byte, error) {
	data = bytes.TrimLeft(data, "\x00\t\n\f\r ")
	data = bytes.TrimPrefix(data, []byte("<~"))
	if i := bytes.Index(data, []byte("~>")); i >= 0 {
		data = data[:i]
	}
	out, err := readDecoded(ctx, ascii85.NewDecoder(bytes.NewReader(data)), limit)
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, ErrTooLarge) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: ASCII85: %w", errSyntax, err)
	}
	return out, nil
}
