package inspect_test

import (
	"bytes"
	"context"
	"reflect"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/inspect"
	"github.com/felixapel/calibre-bookwarden/internal/inspect/inspecttest"
)

func paperTrail() inspecttest.PDFOptions {
	return inspecttest.PDFOptions{Info: map[string]string{"Title": "Paper Trail", "Author": "Fay Sample"}, Text: "A tiny generated book."}
}

func pdfWith(change func(*inspecttest.PDFOptions)) []byte {
	o := paperTrail()
	change(&o)
	return inspecttest.PDF(o)
}

func metaIs(want *evidence.FileMetadata) func(t *testing.T, m *evidence.FileMetadata) {
	return func(t *testing.T, m *evidence.FileMetadata) {
		t.Helper()
		if !reflect.DeepEqual(m, want) {
			t.Errorf("metadata %+v, want %+v", m, want)
		}
	}
}

var pdfCases = []struct {
	name   string
	data   []byte
	state  evidence.State
	reason string
	text   string
	check  func(t *testing.T, m *evidence.FileMetadata)
}{
	{"Info and a text layer", inspecttest.PDF(paperTrail()), evidence.StateOK, "", evidence.TextPresent,
		metaIs(&evidence.FileMetadata{Limits: &evidence.MetadataLimits{Version: 1}, Title: "Paper Trail", Authors: []string{"Fay Sample"}})},
	{"XMP over Info, field by field", pdfWith(func(o *inspecttest.PDFOptions) {
		o.XMP = xmpPacket(`<rdf:Description ` + xmpNamespaces + `><dc:title><rdf:Alt><rdf:li xml:lang="x-default">XMP Title</rdf:li></rdf:Alt></dc:title><prism:isbn>9780000000040</prism:isbn></rdf:Description>`)
	}), evidence.StateOK, "", evidence.TextPresent,
		metaIs(&evidence.FileMetadata{Limits: &evidence.MetadataLimits{Version: 1}, Title: "XMP Title", Authors: []string{"Fay Sample"}, ISBNs: []string{"9780000000040"}})},
	{"the catalog's language", pdfWith(func(o *inspecttest.PDFOptions) { o.Lang = "de-DE" }), evidence.StateOK, "", evidence.TextPresent,
		metaIs(&evidence.FileMetadata{Limits: &evidence.MetadataLimits{Version: 1}, Title: "Paper Trail", Authors: []string{"Fay Sample"}, Languages: []string{"de-DE"}})},
	{"a UTF-16 title with a NUL", pdfWith(func(o *inspecttest.PDFOptions) {
		o.Info = map[string]string{"Title": "\xfe\xff\x00P\x00\x00\x00a\x00p\x00e\x00r"}
	}), evidence.StateOK, "", evidence.TextPresent, metaIs(&evidence.FileMetadata{Limits: &evidence.MetadataLimits{Version: 1}, Title: "Paper"})},
	{"an incremental update", pdfWith(func(o *inspecttest.PDFOptions) { o.Update = map[string]string{"Title": "Second Edition"} }),
		evidence.StateOK, "", evidence.TextPresent, metaIs(&evidence.FileMetadata{Limits: &evidence.MetadataLimits{Version: 1}, Title: "Second Edition"})},
	{"image-only pages", pdfWith(func(o *inspecttest.PDFOptions) { o.Text, o.Pages = "", 3 }), evidence.StateOK, "", evidence.TextAbsent, nil},
	{"text only on page 3", pdfWith(func(o *inspecttest.PDFOptions) { o.Pages, o.TextPage = 3, 3 }), evidence.StateOK, "", evidence.TextPresent, nil},
	{"text only past page 50", pdfWith(func(o *inspecttest.PDFOptions) { o.Pages, o.TextPage = 60, 60 }), evidence.StateOK, "", evidence.TextAbsent, nil},
	{"text only in a form XObject", pdfWith(func(o *inspecttest.PDFOptions) { o.TextInForm = true }), evidence.StateOK, "", evidence.TextPresent, nil},
	{"LZW content", pdfWith(func(o *inspecttest.PDFOptions) { o.LZW = true }), evidence.StateOK, "", evidence.TextUnverified, nil},
	{"broken offsets", pdfWith(func(o *inspecttest.PDFOptions) { o.BreakOffsets = true }), evidence.StateOK, evidence.ReasonXrefRepaired, evidence.TextPresent, nil},
	{"the standard security handler", pdfWith(func(o *inspecttest.PDFOptions) { o.Encrypt = "Standard" }),
		evidence.StateOK, evidence.ReasonEncrypted, evidence.TextUnverified, metaIs(nil)},
	{"Adobe ADEPT", pdfWith(func(o *inspecttest.PDFOptions) { o.Encrypt = "EBX_HANDLER" }), evidence.StateDRM, "", evidence.TextUnverified, metaIs(nil)},
	{"no catalog", pdfWith(func(o *inspecttest.PDFOptions) { o.NoRoot = true }), evidence.StateCorrupt, "", "", nil},
	{"no page tree", pdfWith(func(o *inspecttest.PDFOptions) { o.NoPages = true }), evidence.StateCorrupt, "", "", nil},
	{"a page tree that contains itself", pdfWith(func(o *inspecttest.PDFOptions) { o.PageCycle = true }), evidence.StateOK, "", evidence.TextPresent, nil},
	{"not a PDF", []byte("%PDF-1.4\nthis is where a PDF would be\n"), evidence.StateCorrupt, "", "", nil},
}

func TestPDF(t *testing.T) {
	for _, tt := range pdfCases {
		t.Run(tt.name, func(t *testing.T) {
			res := inspect.Inspect(context.Background(), "PDF", bytes.NewReader(tt.data), int64(len(tt.data)), inspect.Limits{})
			checkInvariants(t, res)
			if res.State != tt.state || res.Reason != tt.reason || res.Text != tt.text {
				t.Errorf("got %s/%s text %q, want %s/%s text %q", res.State, res.Reason, res.Text, tt.state, tt.reason, tt.text)
			}
			if tt.check != nil {
				tt.check(t, res.Metadata)
			}
		})
	}
}

func TestPDFPageCount(t *testing.T) {
	for _, tt := range []struct {
		name  string
		data  []byte
		pages int
	}{
		{"seven pages", pdfWith(func(o *inspecttest.PDFOptions) { o.Pages = 7 }), 7},
		{"no count", pdfWith(func(o *inspecttest.PDFOptions) { o.Count = "-" }), 0},
		{"a negative count", pdfWith(func(o *inspecttest.PDFOptions) { o.Count = "-3" }), 0},
		{"a real count", pdfWith(func(o *inspecttest.PDFOptions) { o.Count = "2.5" }), 0},
		{"a huge count", pdfWith(func(o *inspecttest.PDFOptions) { o.Count = "99999999999" }), 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			res := inspect.Inspect(context.Background(), "PDF", bytes.NewReader(tt.data), int64(len(tt.data)), inspect.Limits{})
			if res.Pages != tt.pages {
				t.Fatalf("Pages = %d, want %d", res.Pages, tt.pages)
			}
		})
	}
}

func FuzzPDF(f *testing.F) {
	for _, tt := range pdfCases {
		f.Add(tt.data)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		checkInvariants(t, inspect.Inspect(context.Background(), "PDF", bytes.NewReader(data), int64(len(data)), inspect.Limits{}))
	})
}
