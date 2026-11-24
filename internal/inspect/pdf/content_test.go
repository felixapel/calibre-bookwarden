package pdf_test

import (
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/inspect/pdf"
)

func TestContentHasText(t *testing.T) {
	tests := []struct {
		content string
		want    bool
	}{
		{"BT /F1 12 Tf 72 700 Td (Hello) Tj ET", true},
		{"BT /F1 12 Tf [(He) -20 (llo)] TJ ET", true},
		{"BT (line) ' ET", true},
		{`BT 1 2 (line) " ET`, true},
		{"BT <0048> Tj ET", true},
		{"BT () Tj [] TJ [-20] TJ ET", false},
		{"q 0 0 1 rg 0 0 10 10 re f Q", false},
		{"q /Im1 Do Q", false},
		{"BI /W 2 /H 2 /BPC 8 /CS /G ID \x00(Tj)\xff\nEI Q", false},
		{"BI /W 1 /H 1 ID xyEIz EI BT (after) Tj ET", true},
		{") > junk BT (still found) Tj ET", true},
	}
	for _, tt := range tests {
		if got := pdf.ContentHasText([]byte(tt.content)); got != tt.want {
			t.Errorf("ContentHasText(%q) = %v, want %v", tt.content, got, tt.want)
		}
	}
}
