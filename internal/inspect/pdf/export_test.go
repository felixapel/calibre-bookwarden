package pdf

import (
	"bytes"
	"context"
)

// ParseObject parses one object from b.
func ParseObject(b []byte) (Object, error) {
	return newLexer(bytes.NewReader(b), int64(len(b)), 0).object(0)
}

// DecodeBytes decodes raw as the data of a stream with dict.
func DecodeBytes(raw []byte, dict Dict, limit int64) ([]byte, error) {
	d := newDoc(context.Background(), bytes.NewReader(raw), int64(len(raw)))
	return d.Decode(Stream{Dict: dict, Offset: 0, Length: int64(len(raw))}, limit)
}
