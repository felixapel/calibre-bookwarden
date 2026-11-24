package inspect_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/inspect"
	"github.com/felixapel/calibre-bookwarden/internal/inspect/inspecttest"
)

func TestMetadataCaptureLimits(t *testing.T) {
	items := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("a%d", i)
		}
		return out
	}
	isbns := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("978000000%04d", i)
		}
		return out
	}
	for _, tt := range []struct {
		name, field string
		in          evidence.FileMetadata
		limited     bool
	}{
		{"complete_title", "title", evidence.FileMetadata{Title: strings.Repeat("a", 512)}, false},
		{"title", "title", evidence.FileMetadata{Title: strings.Repeat("a", 513)}, true},
		{"pending_space", "title", evidence.FileMetadata{Title: strings.Repeat("a", 511) + " suffix"}, true},
		{"trailing_space", "title", evidence.FileMetadata{Title: strings.Repeat("a", 512) + "   "}, false},
		{"authors_complete", "authors", evidence.FileMetadata{Authors: items(32)}, false},
		{"authors_count", "authors", evidence.FileMetadata{Authors: items(33)}, true},
		{"authors_member", "authors", evidence.FileMetadata{Authors: []string{strings.Repeat("a", 513)}}, true},
		{"collapsed_duplicate", "authors", evidence.FileMetadata{Authors: []string{strings.Repeat("a", 512), strings.Repeat("a", 513)}}, true},
		{"languages_count", "languages", evidence.FileMetadata{Languages: items(33)}, true},
		{"isbns_complete", "isbns", evidence.FileMetadata{ISBNs: isbns(16)}, false},
		{"isbns_count", "isbns", evidence.FileMetadata{ISBNs: isbns(17)}, true},
		{"publisher", "publisher", evidence.FileMetadata{Publisher: strings.Repeat("a", 513)}, true},
		{"date", "date", evidence.FileMetadata{Date: strings.Repeat("a", 513)}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := inspect.NewMeta(tt.in)
			raw, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				Limits map[string]any `json:"limits"`
			}
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if got.Limits["version"] != float64(1) || (got.Limits[tt.field] == true) != tt.limited {
				t.Fatalf("capture qualification=%s; %s limited=%v", raw, tt.field, tt.limited)
			}
			if len(m.Authors) > 32 || len(m.Languages) > 32 || len(m.ISBNs) > 16 {
				t.Fatal("metadata storage bounds changed")
			}
		})
	}
}

func TestSelectedMetadataCapture(t *testing.T) {
	long := strings.Repeat("a", 513)
	for _, tt := range []struct {
		name, format, title string
		data                []byte
		limited             bool
	}{
		{"pdf_complete_xmp_over_long_info", "PDF", "Selected", pdfWith(func(o *inspecttest.PDFOptions) {
			o.Info["Title"] = long
			o.XMP = xmpPacket(`<rdf:Description ` + xmpNamespaces + `><dc:title>Selected</dc:title></rdf:Description>`)
		}), false},
		{"pdf_long_xmp_over_complete_info", "PDF", long[:512], pdfWith(func(o *inspecttest.PDFOptions) {
			o.XMP = xmpPacket(`<rdf:Description ` + xmpNamespaces + `><dc:title>` + long + `</dc:title></rdf:Description>`)
		}), true},
		{"mobi_complete_exth_over_long_name", "MOBI", "Selected", mobiWith(6, func(o *inspecttest.MOBIOptions) {
			o.Title, o.EXTH[503] = long, []string{"Selected"}
		}), false},
		{"mobi_long_exth_over_complete_name", "MOBI", long[:512], mobiWith(6, func(o *inspecttest.MOBIOptions) {
			o.EXTH[503] = []string{long}
		}), true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			res := inspect.Inspect(context.Background(), tt.format, bytes.NewReader(tt.data), int64(len(tt.data)), inspect.DefaultLimits)
			m := res.Metadata
			if res.State != evidence.StateOK || m == nil || m.Title != tt.title || m.Limits == nil || m.Limits.Title != tt.limited {
				t.Fatalf("selected metadata capture=%+v; title=%q, limited=%v", res, tt.title, tt.limited)
			}
		})
	}
}
