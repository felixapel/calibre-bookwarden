package inspect_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/inspect"
	"github.com/felixapel/calibre-bookwarden/internal/inspect/inspecttest"
)

var states = map[evidence.State]bool{
	evidence.StateOK: true, evidence.StateCorrupt: true, evidence.StateEmpty: true, evidence.StateDRM: true,
	evidence.StateUnsupported: true, evidence.StateNotInspected: true, evidence.StateMissing: true,
}

// checkInvariants holds for every file: no panic, a known state, and metadata
// that seals.
func checkInvariants(t *testing.T, res inspect.Result) {
	t.Helper()
	if res.Reason == evidence.ReasonInspectorError {
		t.Fatalf("the inspector panicked: %s", res.Panic)
	}
	if !states[res.State] {
		t.Errorf("state %q isn't one of the 7", res.State)
	}
	if _, err := evidence.Canonical(res.Metadata); err != nil {
		t.Errorf("the metadata doesn't seal: %v", err)
	}
	if h := res.ISBNHints; h != nil {
		if _, err := evidence.Canonical(h); err != nil {
			t.Errorf("the ISBN hints don't seal: %v", err)
		}
		if res.State != evidence.StateOK || h.Method != evidence.HintEPUBText || len(h.Context) != len(h.ISBNs) || len(h.ISBNs) > 16 || h.Pages == nil {
			t.Errorf("odd ISBN hints %+v in a %s result", h, res.State)
		}
		for i, isbn := range h.ISBNs {
			if h.Context[i].ISBN != isbn {
				t.Errorf("context %d is for %s, not %s", i, h.Context[i].ISBN, isbn)
			}
		}
	}
}

func calibreEPUB() inspecttest.EPUBOptions {
	return inspecttest.EPUBOptions{
		Title: "Binary Tides", Authors: []string{"Cy Placeholder"}, Language: "en", ISBN: "9780000000026",
		Text: "Two formats carry the same generated evidence.",
	}
}

func with(o inspecttest.EPUBOptions, change func(*inspecttest.EPUBOptions)) inspecttest.EPUBOptions {
	change(&o)
	return o
}

var epubCases = []struct {
	name  string
	opts  inspecttest.EPUBOptions
	state evidence.State
	text  string
	check func(t *testing.T, m *evidence.FileMetadata)
}{
	{"Calibre-shaped EPUB 2", calibreEPUB(), evidence.StateOK, evidence.TextPresent, func(t *testing.T, m *evidence.FileMetadata) {
		want := evidence.FileMetadata{Limits: &evidence.MetadataLimits{Version: 1}, Title: "Binary Tides", Authors: []string{"Cy Placeholder"}, Languages: []string{"en"}, ISBNs: []string{"9780000000026"}}
		if m == nil || m.Title != want.Title || strings.Join(m.Authors, "|") != "Cy Placeholder" ||
			strings.Join(m.Languages, "|") != "en" || strings.Join(m.ISBNs, "|") != "9780000000026" || m.Publisher != "" || m.Date != "" {
			t.Errorf("metadata %+v, want %+v", m, want)
		}
	}},
	{"EPUB 3 roles", with(calibreEPUB(), func(o *inspecttest.EPUBOptions) {
		o.EPUB3, o.Illustrators = true, []string{"Ida Lustrator"}
	}), evidence.StateOK, evidence.TextPresent, func(t *testing.T, m *evidence.FileMetadata) {
		if m == nil || strings.Join(m.Authors, "|") != "Cy Placeholder" {
			t.Errorf("authors %+v, want only the aut creator", m)
		}
	}},
	{"EPUB 2 roles", with(calibreEPUB(), func(o *inspecttest.EPUBOptions) {
		o.Illustrators = []string{"Ida Lustrator"}
	}), evidence.StateOK, evidence.TextPresent, func(t *testing.T, m *evidence.FileMetadata) {
		if m == nil || strings.Join(m.Authors, "|") != "Cy Placeholder" {
			t.Errorf("authors %+v, want only the aut creator", m)
		}
	}},
	{"publication and modification dates", with(calibreEPUB(), func(o *inspecttest.EPUBOptions) {
		o.Date, o.ModifiedDate, o.Publisher = "2019-05-04", "2021-01-01", "Nobody Press"
	}), evidence.StateOK, evidence.TextPresent, func(t *testing.T, m *evidence.FileMetadata) {
		if m == nil || m.Date != "2019-05-04" || m.Publisher != "Nobody Press" {
			t.Errorf("metadata %+v, want date 2019-05-04 and publisher Nobody Press", m)
		}
	}},
	{"EPUB 3 urn:isbn and uuid identifiers", with(calibreEPUB(), func(o *inspecttest.EPUBOptions) {
		o.EPUB3, o.ISBN = true, "urn:isbn:978-0-00-000002-6"
	}), evidence.StateOK, evidence.TextPresent, func(t *testing.T, m *evidence.FileMetadata) {
		if m == nil || strings.Join(m.ISBNs, "|") != "9780000000026" {
			t.Errorf("ISBNs %+v, want only 9780000000026", m)
		}
	}},
	{"only an image in the spine", with(calibreEPUB(), func(o *inspecttest.EPUBOptions) { o.Text = "" }),
		evidence.StateOK, evidence.TextAbsent, nil},
	{"IDPF font obfuscation", with(calibreEPUB(), func(o *inspecttest.EPUBOptions) {
		o.Encryption = inspecttest.EncryptionXML("http://www.idpf.org/2008/embedding", "OEBPS/fonts/f.otf")
	}), evidence.StateOK, evidence.TextPresent, nil},
	{"Adobe font obfuscation", with(calibreEPUB(), func(o *inspecttest.EPUBOptions) {
		o.Encryption = inspecttest.EncryptionXML("http://ns.adobe.com/pdf/enc#RC", "OEBPS/fonts/f.otf")
	}), evidence.StateOK, evidence.TextPresent, nil},
	{"AES-encrypted content", with(calibreEPUB(), func(o *inspecttest.EPUBOptions) {
		o.Encryption = inspecttest.EncryptionXML("http://www.w3.org/2001/04/xmlenc#aes128-cbc", "OEBPS/text/chapter%201.xhtml")
	}), evidence.StateDRM, evidence.TextUnverified, func(t *testing.T, m *evidence.FileMetadata) {
		if m == nil || m.Title != "Binary Tides" {
			t.Errorf("metadata %+v, want it read despite the DRM", m)
		}
	}},
	{"Adobe ADEPT rights", with(calibreEPUB(), func(o *inspecttest.EPUBOptions) { o.Rights = true }),
		evidence.StateDRM, evidence.TextUnverified, nil},
	{"no container", with(calibreEPUB(), func(o *inspecttest.EPUBOptions) { o.NoContainer = true }),
		evidence.StateCorrupt, "", nil},
	{"the container points at a missing OPF", with(calibreEPUB(), func(o *inspecttest.EPUBOptions) { o.NoOPF = true }),
		evidence.StateCorrupt, "", nil},
	{"the spine points at a missing chapter", with(calibreEPUB(), func(o *inspecttest.EPUBOptions) { o.NoChapter = true }),
		evidence.StateCorrupt, "", nil},
	{"XML 1.1 documents", with(calibreEPUB(), func(o *inspecttest.EPUBOptions) { o.XML11 = true }),
		evidence.StateOK, evidence.TextPresent, func(t *testing.T, m *evidence.FileMetadata) {
			if m == nil || strings.Join(m.ISBNs, "|") != "9780000000026" {
				t.Errorf("metadata %+v, want the OPF read", m)
			}
		}},
	{"mimetype last", with(calibreEPUB(), func(o *inspecttest.EPUBOptions) { o.MimetypeLast = true }),
		evidence.StateOK, evidence.TextPresent, nil},
	{"a hostile title", with(calibreEPUB(), func(o *inspecttest.EPUBOptions) {
		o.Title = "Bad\x00Title\xff" + strings.Repeat("x", 2000)
	}), evidence.StateOK, evidence.TextPresent, func(t *testing.T, m *evidence.FileMetadata) {
		if m == nil || !strings.HasPrefix(m.Title, "BadTitle�x") || utf8.RuneCountInString(m.Title) != 512 {
			t.Errorf("metadata %+v, want the title cleaned and 512 runes", m)
		}
	}},
	{"HTML entities and unclosed tags", with(calibreEPUB(), func(o *inspecttest.EPUBOptions) {
		o.ContentDoc = `<html><head><meta charset="utf-8"><title>x</title></head><body><p>&nbsp;Caf&eacute; at night<br>and more</p></body></html>`
	}), evidence.StateOK, evidence.TextPresent, nil},
	{"only whitespace and entities in the body", with(calibreEPUB(), func(o *inspecttest.EPUBOptions) {
		o.ContentDoc = `<html><head><title>Lots of title text</title></head><body><p>&nbsp; <br></p><script>var x = 1;</script></body></html>`
	}), evidence.StateOK, evidence.TextAbsent, nil},
}

func TestEPUB(t *testing.T) {
	for _, tt := range epubCases {
		t.Run(tt.name, func(t *testing.T) {
			data := inspecttest.EPUB(tt.opts)
			res := inspect.Inspect(context.Background(), "EPUB", bytes.NewReader(data), int64(len(data)), inspect.Limits{})
			checkInvariants(t, res)
			if res.State != tt.state || res.Text != tt.text {
				t.Errorf("got %s/%s text %q, want %s text %q", res.State, res.Reason, res.Text, tt.state, tt.text)
			}
			if tt.check != nil {
				tt.check(t, res.Metadata)
			}
		})
	}
}

const imageDoc = `<?xml version="1.0"?><html xmlns="http://www.w3.org/1999/xhtml"><head><title>t</title></head><body><div><img src="../images/cover.png" alt="plate"/></div></body></html>`
const textDoc = `<?xml version="1.0"?><html xmlns="http://www.w3.org/1999/xhtml"><head><title>t</title></head><body><p>Once upon a time.</p></body></html>`

// A scan that stopped early or couldn't read a chapter must not seal "no
// text"; only a complete scan that found none may.
func TestEPUBTextAbsenceNeedsACompleteScan(t *testing.T) {
	plates := func(n int, tail ...string) []string {
		docs := make([]string, 0, n+len(tail))
		for range n {
			docs = append(docs, imageDoc)
		}
		return append(docs, tail...)
	}
	// Random bytes inside a comment don't compress, so the chapter is read
	// to the 4 MiB cut without tripping the ratio limit.
	noise := make([]byte, 5<<20)
	for i := range noise {
		noise[i] = 'a' + byte((i*2654435761)>>13)%26
	}
	big := `<html><body><div><img src="x.png"/></div><!-- ` + string(noise) + ` --><p>late text</p></body></html>`
	// The first 4 MiB parse cleanly and hold no text, so only the declared
	// size shows the chapter was cut.
	cleanCut := `<html><body><div><img/></div></body></html>` + string(noise) + `<body><p>late text</p></body>`
	cases := []struct {
		name  string
		extra []string
		text  string
	}{
		{"every document scanned, none has text", plates(49), evidence.TextAbsent},
		{"the bound is hit with a document left", plates(50, textDoc), evidence.TextUnverified},
		{"the bound is hit and the rest is images", plates(50, imageDoc), evidence.TextUnverified},
		{"a malformed chapter and no text before it", []string{`<html><body><div><img src="x.png"/>`}, evidence.TextUnverified},
		{"a malformed chapter and text after it", []string{`<html><body><div><img src="x.png"/>`, textDoc}, evidence.TextPresent},
		{"a chapter cut at the size bound", []string{big}, evidence.TextUnverified},
		{"a chapter cut cleanly at the size bound", []string{cleanCut}, evidence.TextUnverified},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			o := with(calibreEPUB(), func(o *inspecttest.EPUBOptions) { o.Text, o.Extra = "", tt.extra })
			data := inspecttest.EPUB(o)
			res := inspect.Inspect(context.Background(), "EPUB", bytes.NewReader(data), int64(len(data)), inspect.Limits{})
			if res.State != evidence.StateOK || res.Text != tt.text {
				t.Errorf("got %s/%s text %q, want ok text %q", res.State, res.Reason, res.Text, tt.text)
			}
		})
	}
}

func TestEPUBThatIsNotAZip(t *testing.T) {
	data := []byte("abcd")
	if res := inspect.Inspect(context.Background(), "EPUB", bytes.NewReader(data), 4, inspect.Limits{}); res.State != evidence.StateCorrupt {
		t.Errorf("got %s/%s, want corrupt", res.State, res.Reason)
	}
}

func FuzzEPUB(f *testing.F) {
	for _, tt := range epubCases {
		f.Add(inspecttest.EPUB(tt.opts))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		checkInvariants(t, inspect.Inspect(context.Background(), "EPUB", bytes.NewReader(data), int64(len(data)), inspect.Limits{}))
	})
}
