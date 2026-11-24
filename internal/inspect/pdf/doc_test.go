package pdf_test

import (
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/inspect/pdf"
)

func TestTextString(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"\xfe\xff\x00A\x00B", "AB"},
		{"\xfe\xff\xd8\x3d\xde\x00", "😀"},
		{"\xef\xbb\xbfCaf\xc3\xa9", "Café"},
		{"Plain", "Plain"},
		{"\x92", "™"},
		{"\xe9", "é"},
		{"\x80\x84\xa0", "•—€"},
		{"\x9f", "�"},
	}
	for _, tt := range tests {
		if got := pdf.TextString(pdf.String(tt.in)); got != tt.want {
			t.Errorf("TextString(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
