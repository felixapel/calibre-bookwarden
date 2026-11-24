package inspect

import (
	"errors"
)

var errPalmDOC = errors.New("inspect: broken PalmDOC compression")

// palmdocDecompress undoes PalmDOC's LZ77 compression, refusing output past
// limit bytes and back-references before the start.
func palmdocDecompress(in []byte, limit int) ([]byte, error) {
	out := make([]byte, 0, min(limit, 8192))
	for i := 0; i < len(in); {
		c := in[i]
		i++
		switch {
		case c == 0 || c >= 0x09 && c <= 0x7F:
			out = append(out, c)
		case c <= 0x08: // a run of c literal bytes
			if i+int(c) > len(in) {
				return nil, errPalmDOC
			}
			out = append(out, in[i:i+int(c)]...)
			i += int(c)
		case c <= 0xBF: // distance and length in 14 bits
			if i == len(in) {
				return nil, errPalmDOC
			}
			pair := int(c)<<8 | int(in[i])
			i++
			dist, length := pair&0x3FFF>>3, pair&7+3
			if dist == 0 || dist > len(out) {
				return nil, errPalmDOC
			}
			for range length {
				out = append(out, out[len(out)-dist])
			}
		default: // a space and a character
			out = append(out, ' ', c^0x80)
		}
		if len(out) > limit {
			return nil, errPalmDOC
		}
	}
	return out, nil
}
