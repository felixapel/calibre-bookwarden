package inspect_test

import (
	"bytes"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/inspect"
)

func TestPalmDOC(t *testing.T) {
	tests := []struct {
		name    string
		in      []byte
		want    string
		wantErr bool
	}{
		{"a literal run", []byte{0x01, 'a'}, "a", false},
		{"bytes that copy themselves", []byte{0x00, 0x09, 'z', 0x7F}, "\x00\tz\x7f", false},
		{"space plus a character", []byte{0xC1}, " A", false},
		{"a back-reference", []byte{'a', 'b', 'c', 'd', 0x80, 0x19}, "abcdbcdb", false},
		{"a back-reference before the start", []byte{'a', 0x80, 0x19}, "", true},
		{"a back-reference of distance 0", []byte{'a', 0x80, 0x01}, "", true},
		{"a truncated back-reference", []byte{'a', 0x80}, "", true},
		{"a truncated literal run", []byte{0x03, 'a'}, "", true},
		{"output past the limit", bytes.Repeat([]byte{'a'}, 17), "", true},
	}
	for _, tt := range tests {
		got, err := inspect.PalmDOCDecompress(tt.in, 16)
		if (err != nil) != tt.wantErr || !tt.wantErr && string(got) != tt.want {
			t.Errorf("%s: got %q, %v; want %q, error %v", tt.name, got, err, tt.want, tt.wantErr)
		}
	}
}
