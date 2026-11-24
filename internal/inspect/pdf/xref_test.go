package pdf_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/inspect/inspecttest"
	"github.com/felixapel/calibre-bookwarden/internal/inspect/pdf"
)

func open(t *testing.T, data []byte) *pdf.Doc {
	t.Helper()
	d, err := pdf.Open(context.Background(), bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return d
}

func dict(t *testing.T, d *pdf.Doc, o pdf.Object) pdf.Dict {
	t.Helper()
	v, err := d.Resolve(o)
	if err != nil {
		t.Fatalf("Resolve(%v): %v", o, err)
	}
	m, ok := v.(pdf.Dict)
	if !ok {
		t.Fatalf("Resolve(%v) = %#v, want a dictionary", o, v)
	}
	return m
}

// firstContent decodes the first page's content stream.
func firstContent(t *testing.T, d *pdf.Doc) string {
	t.Helper()
	root := dict(t, d, d.Trailer()["Root"])
	pages := dict(t, d, root["Pages"])
	kids, _ := pages["Kids"].(pdf.Array)
	page := dict(t, d, kids[0])
	c, err := d.Resolve(page["Contents"])
	if err != nil {
		t.Fatal(err)
	}
	s, ok := c.(pdf.Stream)
	if !ok {
		t.Fatalf("Contents = %#v, want a stream", c)
	}
	b, err := d.Decode(s, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestOpen(t *testing.T) {
	text := inspecttest.PDFOptions{Info: map[string]string{"Title": "Paper Trail"}, Text: "A tiny generated book."}
	with := func(change func(*inspecttest.PDFOptions)) []byte {
		o := text
		change(&o)
		return inspecttest.PDF(o)
	}
	tests := []struct {
		name     string
		data     []byte
		repaired bool
		check    func(t *testing.T, d *pdf.Doc)
	}{
		{"classic xref", inspecttest.PDF(text), false, nil},
		{"xref stream and object streams", with(func(o *inspecttest.PDFOptions) { o.XRefStream, o.ObjectStreams = true, true }), false, nil},
		{"an incremental update", with(func(o *inspecttest.PDFOptions) { o.Update = map[string]string{"Title": "Second Edition"} }), false,
			func(t *testing.T, d *pdf.Doc) {
				if title := dict(t, d, d.Trailer()["Info"])["Title"]; pdf.TextString(title.(pdf.String)) != "Second Edition" {
					t.Errorf("Info title %q, want the update's", title)
				}
			}},
		{"a hybrid file", with(func(o *inspecttest.PDFOptions) { o.Hybrid, o.ObjectStreams = true, true }), false, nil},
		{"offsets shifted by a CRLF conversion", with(func(o *inspecttest.PDFOptions) { o.CRLFShift = true }), true, nil},
		{"broken offsets", with(func(o *inspecttest.PDFOptions) { o.BreakOffsets = true }), true, nil},
		{"broken offsets into object streams", with(func(o *inspecttest.PDFOptions) {
			o.BreakOffsets, o.XRefStream, o.ObjectStreams = true, true, true
		}), true, nil},
		{"no startxref", with(func(o *inspecttest.PDFOptions) { o.NoStartxref = true }), true, nil},
		{"a /Prev pointing at itself", with(func(o *inspecttest.PDFOptions) { o.PrevLoop = true }), false, nil},
		{"an indirect /Length", with(func(o *inspecttest.PDFOptions) { o.IndirectLength = true }), false, nil},
		{"a wrong /Length", with(func(o *inspecttest.PDFOptions) { o.WrongLength = true }), false, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := open(t, tt.data)
			if d.Repaired() != tt.repaired {
				t.Errorf("Repaired() = %v, want %v", d.Repaired(), tt.repaired)
			}
			if typ := dict(t, d, d.Trailer()["Root"])["Type"]; typ != pdf.Name("Catalog") {
				t.Errorf("Root /Type = %v, want Catalog", typ)
			}
			if c := firstContent(t, d); !strings.Contains(c, "(A tiny generated book.) Tj") {
				t.Errorf("page 1 content %q, want the text", c)
			}
			if tt.check != nil {
				tt.check(t, d)
			}
		})
	}
}

func TestOpenRefusesNonsense(t *testing.T) {
	for name, data := range map[string]string{
		"junk":          "this is not a PDF at all",
		"a header only": "%PDF-1.4\n%%EOF\n",
	} {
		if _, err := pdf.Open(context.Background(), strings.NewReader(data), int64(len(data))); err == nil {
			t.Errorf("%s: Open succeeded", name)
		}
	}
	junk := "this is not a PDF at all"
	if _, err := pdf.Open(context.Background(), strings.NewReader(junk), int64(len(junk))); !errors.Is(err, pdf.ErrNotPDF) {
		t.Errorf("junk: %v, want ErrNotPDF", err)
	}
}

func TestResolveEndsOnASelfReference(t *testing.T) {
	data := "%PDF-1.4\n1 0 obj\n1 0 R\nendobj\n2 0 obj\n<< /Type /Catalog >>\nendobj\ntrailer\n<< /Root 2 0 R /Info 1 0 R >>\n%%EOF\n"
	d := open(t, []byte(data))
	if v, err := d.Resolve(d.Trailer()["Info"]); err == nil {
		t.Errorf("Resolve(1 0 R → 1 0 R) = %#v, want an error", v)
	}
}

func TestOpenHonoursTheContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	data := inspecttest.PDF(inspecttest.PDFOptions{NoStartxref: true, Pages: 50})
	if _, err := pdf.Open(ctx, bytes.NewReader(data), int64(len(data))); !errors.Is(err, context.Canceled) {
		t.Errorf("Open with a cancelled context: %v, want context.Canceled", err)
	}
}

// With no usable trailer, repair builds one around the last catalog it finds.
func TestRepairFindsTheCatalog(t *testing.T) {
	noTrailer := "%PDF-1.4\n1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n2 0 obj\n<< /Type /Pages /Kids [] /Count 0 >>\nendobj\n%%EOF\n"
	xrefStreamOnly := inspecttest.PDF(inspecttest.PDFOptions{Text: "x", XRefStream: true, ObjectStreams: true, NoStartxref: true})
	catalogInObjStm := bytes.ReplaceAll(bytes.Clone(xrefStreamOnly), []byte("/Type /XRef"), []byte("/Type /XRaf"))
	for name, data := range map[string][]byte{
		"no trailer at all":                      []byte(noTrailer),
		"only an xref stream's dictionary":       xrefStreamOnly,
		"a catalog inside an object stream only": catalogInObjStm,
	} {
		t.Run(name, func(t *testing.T) {
			d := open(t, data)
			if typ := dict(t, d, d.Trailer()["Root"])["Type"]; !d.Repaired() || typ != pdf.Name("Catalog") {
				t.Errorf("Repaired %v, Root /Type %v; want true, Catalog", d.Repaired(), typ)
			}
		})
	}
}
