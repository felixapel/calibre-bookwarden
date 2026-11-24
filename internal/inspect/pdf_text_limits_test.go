package inspect_test

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/inspect"
	"github.com/felixapel/calibre-bookwarden/internal/inspect/pdf"
)

func formPDF(text bool) []byte {
	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n")
	for i := 1; i <= 3; i++ {
		body := "%" + strings.Repeat("p", 126) + "\n"
		if i == 1 && text {
			body = "(visible) Tj\n%" + strings.Repeat("p", 113) + "\n"
		}
		fmt.Fprintf(&b, "%d 0 obj\n<< /Subtype /Form /Length %d >>\nstream\n%s\nendstream\nendobj\n", i, len(body), body)
	}
	b.WriteString("4 0 obj\n<< /Type /Catalog /Pages 5 0 R >>\nendobj\n5 0 obj\n<< /Type /Page /Contents [] /Resources << /XObject << /A 1 0 R /B 2 0 R /C 3 0 R >> >> >>\nendobj\n")
	return b.Bytes()
}

func TestPDFTextResourceBudget(t *testing.T) {
	for _, tc := range []struct {
		name string
		text bool
		want string
	}{
		{"text in first named form is deterministic", true, evidence.TextPresent},
		{"unread forms are unverified", false, evidence.TextUnverified},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := formPDF(tc.text)
			d, err := pdf.Open(context.Background(), bytes.NewReader(b), int64(len(b)))
			if err != nil {
				t.Fatal(err)
			}
			v, err := d.Resolve(pdf.Ref{Num: 5})
			if err != nil {
				t.Fatal(err)
			}
			for i := range 100 {
				if got := inspect.PDFText(context.Background(), d, v.(pdf.Dict), 128); got != tc.want {
					t.Fatalf("iteration %d: got %q, want %q", i, got, tc.want)
				}
			}
		})
	}
}

func TestPDFLateMaterializationLimitIsExplicit(t *testing.T) {
	data := []byte("%PDF-1.7\n1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n" +
		"2 0 obj\n<< /Type /Page /Contents [] >>\nendobj\n" +
		"3 0 obj\n<< /Title (" + strings.Repeat("p", 16<<20) + ") >>\nendobj\n" +
		"trailer\n<< /Root 1 0 R /Info 3 0 R >>\n")
	got := inspect.Inspect(context.Background(), "PDF", bytes.NewReader(data), int64(len(data)), inspect.Limits{})
	checkInvariants(t, got)
	if got.State != evidence.StateNotInspected || got.Reason != evidence.ReasonDecompressedTooLarge || got.Metadata != nil {
		t.Fatalf("late limit returned %s/%s with metadata %v", got.State, got.Reason, got.Metadata)
	}
}
