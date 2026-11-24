package evidence_test

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

func sliceRunes(s string, start, end int) string { return string([]rune(s)[start:end]) }

func TestNormalizeExcerptText(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"plain", "one\ntwo", "one\ntwo"},
		{"crlf and lone cr", "a\r\nb\rc", "a\nb\nc"},
		{"unicode line separators", "a\u2028b\u2029c\u0085d", "a\nb\nc\nd"},
		{"collapses spaces and tabs", "a \t  b  c", "a b c"},
		{"no-break space is a space", "a\u00a0\u00a0b", "a b"},
		{"trims line edges", "  a  \n \t b \t", "a\nb"},
		{"one blank line at most", "a\n\n\n\n\nb", "a\n\nb"},
		{"blank lines of spaces count as blank", "a\n \n\t\n  \nb", "a\n\nb"},
		{"leading and trailing blanks go", "\n\n a \n\n", "a"},
		{"nfc composes", "e\u0301", "\u00e9"},
		{"nul and controls become spaces", "a\x00b\x07c", "a b c"},
		{"invalid utf8 becomes U+FFFD", "a\xffb", "a�b"},
		{"format and bidi characters stay", "a\u202eb\u200bc", "a\u202eb\u200bc"},
		{"form feed is a line break", "a\fb", "a\nb"},
		{"empty", "", ""},
		{"only space", " \n\t ", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := evidence.NormalizeExcerptText(tt.in); got != tt.want {
				t.Errorf("NormalizeExcerptText(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestExcerptBuilderUnits(t *testing.T) {
	b := evidence.NewExcerptBuilder(evidence.HintEPUBText)
	b.Add(1, evidence.LandmarkTitle, "Title\nAuthor")
	b.Add(2, evidence.LandmarkFront, " \n ") // nothing left: no unit
	b.Add(3, evidence.LandmarkCopyright, "é ISBN 978-0-00-000002-6")
	b.Add(9, evidence.LandmarkBack, "Ads")
	ex := b.Excerpt()
	if ex == nil {
		t.Fatal("no excerpt")
	}
	parts := []string{"Title\nAuthor", "é ISBN 978-0-00-000002-6", "Ads"}
	wantText := strings.Join(parts, "\n\n")
	if ex.Text != wantText {
		t.Fatalf("text = %q, want %q", ex.Text, wantText)
	}
	want := []evidence.ExcerptUnit{
		{Pos: 1, Landmark: evidence.LandmarkTitle, Start: 0, End: 12},
		{Pos: 3, Landmark: evidence.LandmarkCopyright, Start: 14, End: 14 + 24},
		{Pos: 9, Landmark: evidence.LandmarkBack, Start: 40, End: 43},
	}
	if !reflect.DeepEqual(ex.Units, want) {
		t.Errorf("units = %+v, want %+v", ex.Units, want)
	}
	for i, u := range ex.Units {
		if got := sliceRunes(ex.Text, u.Start, u.End); got != parts[i] {
			t.Errorf("unit %d slices %q, want %q", i, got, parts[i])
		}
	}
	sum := sha256.Sum256([]byte(ex.Text))
	if ex.SHA256 != hex.EncodeToString(sum[:]) || ex.Chars != utf8.RuneCountInString(ex.Text) || ex.Truncated || ex.Method != evidence.HintEPUBText {
		t.Errorf("excerpt = %+v", ex)
	}
}

func TestExcerptBuilderEmpty(t *testing.T) {
	b := evidence.NewExcerptBuilder(evidence.HintTextLayer)
	b.Add(1, "", "  \n")
	if ex := b.Excerpt(); ex != nil {
		t.Errorf("excerpt of nothing = %+v", ex)
	}
}

func TestExcerptBuilderTruncation(t *testing.T) {
	line := func(n int) string { return strings.Repeat("x", n) }
	tests := []struct {
		name      string
		text      string
		wantChars int
	}{
		{"exactly at the cap is whole", line(evidence.MaxExcerptRunes), evidence.MaxExcerptRunes},
		{"one over, no line break: hard cut", line(evidence.MaxExcerptRunes + 1), evidence.MaxExcerptRunes},
		{"line break within 200 of the cap wins", line(11_900) + "\n" + line(500), 11_900},
		{"line break exactly 200 before the cap wins", line(11_800) + "\n" + line(500), 11_800},
		{"line break 201 before the cap loses", line(11_799) + "\n" + line(500), evidence.MaxExcerptRunes},
		{"multibyte runes cut on a rune boundary", strings.Repeat("é", evidence.MaxExcerptRunes+5), evidence.MaxExcerptRunes},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := evidence.NewExcerptBuilder(evidence.HintEPUBText)
			b.Add(1, evidence.LandmarkFront, tt.text)
			ex := b.Excerpt()
			if ex == nil {
				t.Fatal("no excerpt")
			}
			wantTrunc := utf8.RuneCountInString(tt.text) > evidence.MaxExcerptRunes
			if ex.Chars != tt.wantChars || ex.Truncated != wantTrunc {
				t.Errorf("chars %d truncated %v, want %d %v", ex.Chars, ex.Truncated, tt.wantChars, wantTrunc)
			}
			if ex.Chars != utf8.RuneCountInString(ex.Text) || !utf8.ValidString(ex.Text) || !norm.NFC.IsNormalString(ex.Text) {
				t.Errorf("stored text is not valid NFC of %d runes", ex.Chars)
			}
			if strings.HasSuffix(ex.Text, "\n") {
				t.Error("stored text ends in a line break")
			}
			if u := ex.Units[len(ex.Units)-1]; u.End != ex.Chars {
				t.Errorf("last unit ends at %d, text has %d", u.End, ex.Chars)
			}
		})
	}
}

func TestExcerptBuilderCapAcrossUnits(t *testing.T) {
	b := evidence.NewExcerptBuilder(evidence.HintTextLayer)
	b.Add(1, "", strings.Repeat("a", 11_000))
	b.Add(2, "", strings.Repeat("b", 5_000)) // 998 runes fit after the separator
	b.Add(3, "", "never read")
	ex := b.Excerpt()
	if ex.Chars != evidence.MaxExcerptRunes || !ex.Truncated || len(ex.Units) != 2 {
		t.Fatalf("chars %d truncated %v units %+v", ex.Chars, ex.Truncated, ex.Units)
	}
	if u := ex.Units[1]; u.Start != 11_002 || u.End != evidence.MaxExcerptRunes {
		t.Errorf("second unit = %+v", u)
	}
	if strings.Contains(ex.Text, "never") {
		t.Error("a unit after the cap was stored")
	}
}

func TestExcerptBuilderCapFullAfterSeparator(t *testing.T) {
	b := evidence.NewExcerptBuilder(evidence.HintTextLayer)
	b.Add(1, "", strings.Repeat("a", evidence.MaxExcerptRunes-1))
	b.Add(2, "", "b") // the separator alone would pass the cap
	ex := b.Excerpt()
	if ex.Chars != evidence.MaxExcerptRunes-1 || !ex.Truncated || len(ex.Units) != 1 {
		t.Fatalf("chars %d truncated %v units %d", ex.Chars, ex.Truncated, len(ex.Units))
	}
}

func excerptPackage(t *testing.T, version int, ex *evidence.Excerpt) (raw []byte, seal string, p evidence.Package) {
	t.Helper()
	return excerptPackageOn(t, version, "EPUB", ex)
}

// excerptPackageOn puts the excerpt on a format of that name.
func excerptPackageOn(t *testing.T, version int, format string, ex *evidence.Excerpt) (raw []byte, seal string, p evidence.Package) {
	t.Helper()
	p = pkg(t)
	p.SchemaVersion = version
	p.Formats = append([]evidence.FormatEvidence(nil), p.Formats...)
	p.Formats[1].Format = format
	p.Formats[1].Excerpt = ex
	raw, seal, err := evidence.Seal(p)
	if err != nil {
		t.Fatal(err)
	}
	return raw, seal, p
}

func goodExcerpt() *evidence.Excerpt {
	return &evidence.Excerpt{
		Method: evidence.HintEPUBText,
		Units: []evidence.ExcerptUnit{
			{Pos: 1, Landmark: evidence.LandmarkTitle, Start: 0, End: 10},
			{Pos: 4, Landmark: evidence.LandmarkCopyright, Start: 12, End: 30},
		},
		SHA256: strings.Repeat("ab", 32), Chars: 30, Complete: true,
	}
}

func TestOpenExcerptRoundTripAtSchema3(t *testing.T) {
	ex := goodExcerpt()
	ex.Truncated = true
	raw, seal, p := excerptPackage(t, evidence.SchemaVersion, ex)
	got, err := evidence.Open(raw, seal)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, p) {
		t.Errorf("Open(Seal(p)) = %+v, want %+v", got, p)
	}
}

func TestExcerptBodyIsNotSealed(t *testing.T) {
	ex := goodExcerpt()
	raw1, seal1, _ := excerptPackage(t, evidence.SchemaVersion, ex)
	withBody := *ex
	withBody.Text = "synthetic body text"
	raw2, seal2, _ := excerptPackage(t, evidence.SchemaVersion, &withBody)
	if seal1 != seal2 {
		t.Error("the in-memory body changed the seal")
	}
	if strings.Contains(string(raw2), "synthetic") || string(raw1) != string(raw2) {
		t.Error("the excerpt body is in the sealed package")
	}
}

func TestOpenExcerptRefusals(t *testing.T) {
	mod := func(f func(*evidence.Excerpt)) *evidence.Excerpt {
		ex := goodExcerpt()
		f(ex)
		return ex
	}
	tests := []struct {
		name    string
		version int
		format  string // "" is EPUB
		ex      *evidence.Excerpt
		wantErr error
	}{
		{"schema 1 with an excerpt", 1, "", goodExcerpt(), evidence.ErrInvalidEvidence},
		{"schema 2 with an excerpt", 2, "", goodExcerpt(), evidence.ErrInvalidEvidence},
		{"schema 3 text layer of a PDF without landmarks", 3, "PDF", mod(func(e *evidence.Excerpt) {
			e.Method = evidence.HintTextLayer
			e.Units[0].Landmark, e.Units[1].Landmark = "", ""
		}), nil},
		{"original formats follow their kind", 3, "ORIGINAL_EPUB", goodExcerpt(), nil},
		{"text layer with a landmark", 3, "PDF", mod(func(e *evidence.Excerpt) { e.Method = evidence.HintTextLayer }), evidence.ErrInvalidEvidence},
		{"epub_text unit without a landmark", 3, "", mod(func(e *evidence.Excerpt) { e.Units[0].Landmark = "" }), evidence.ErrInvalidEvidence},
		{"epub_text excerpt on a PDF", 3, "PDF", goodExcerpt(), evidence.ErrInvalidEvidence},
		{"text_layer excerpt on an EPUB", 3, "EPUB", mod(func(e *evidence.Excerpt) {
			e.Method = evidence.HintTextLayer
			e.Units[0].Landmark, e.Units[1].Landmark = "", ""
		}), evidence.ErrInvalidEvidence},
		{"an excerpt on another format", 3, "AZW3", goodExcerpt(), evidence.ErrInvalidEvidence},
		{"method ocr", 3, "", mod(func(e *evidence.Excerpt) { e.Method = evidence.HintOCR }), evidence.ErrInvalidEvidence},
		{"method empty", 3, "", mod(func(e *evidence.Excerpt) { e.Method = "" }), evidence.ErrInvalidEvidence},
		{"unknown landmark", 3, "", mod(func(e *evidence.Excerpt) { e.Units[0].Landmark = "toc" }), evidence.ErrInvalidEvidence},
		{"no units", 3, "", mod(func(e *evidence.Excerpt) { e.Units = nil }), evidence.ErrInvalidEvidence},
		{"pos zero", 3, "", mod(func(e *evidence.Excerpt) { e.Units[0].Pos = 0 }), evidence.ErrInvalidEvidence},
		{"pos not ascending", 3, "", mod(func(e *evidence.Excerpt) { e.Units[1].Pos = 1 }), evidence.ErrInvalidEvidence},
		{"start after end", 3, "", mod(func(e *evidence.Excerpt) { e.Units[0].Start, e.Units[0].End = 10, 5 }), evidence.ErrInvalidEvidence},
		{"empty unit", 3, "", mod(func(e *evidence.Excerpt) { e.Units[0].End = 0 }), evidence.ErrInvalidEvidence},
		{"negative start", 3, "", mod(func(e *evidence.Excerpt) { e.Units[0].Start = -1 }), evidence.ErrInvalidEvidence},
		{"units overlap", 3, "", mod(func(e *evidence.Excerpt) { e.Units[1].Start = 9 }), evidence.ErrInvalidEvidence},
		{"unit past chars", 3, "", mod(func(e *evidence.Excerpt) { e.Units[1].End = 31 }), evidence.ErrInvalidEvidence},
		{"chars over the cap", 3, "", mod(func(e *evidence.Excerpt) { e.Chars = evidence.MaxExcerptRunes + 1 }), evidence.ErrInvalidEvidence},
		{"sha too short", 3, "", mod(func(e *evidence.Excerpt) { e.SHA256 = strings.Repeat("a", 63) }), evidence.ErrInvalidEvidence},
		{"sha uppercase", 3, "", mod(func(e *evidence.Excerpt) { e.SHA256 = strings.Repeat("AB", 32) }), evidence.ErrInvalidEvidence},
		{"sha not hex", 3, "", mod(func(e *evidence.Excerpt) { e.SHA256 = strings.Repeat("zz", 32) }), evidence.ErrInvalidEvidence},
		{"sha empty", 3, "", mod(func(e *evidence.Excerpt) { e.SHA256 = "" }), evidence.ErrInvalidEvidence},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, seal, _ := excerptPackageOn(t, tt.version, cmp.Or(tt.format, "EPUB"), tt.ex)
			_, err := evidence.Open(raw, seal)
			if tt.wantErr == nil {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Open = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestSchemaV2StillOpens(t *testing.T) {
	p := pkg(t)
	p.SchemaVersion = evidence.SchemaVersionV2
	raw, seal, err := evidence.Seal(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := evidence.Open(raw, seal); err != nil {
		t.Fatal(err)
	}
}

func TestOpenExcerptRequiresComplete(t *testing.T) {
	raw, _, _ := excerptPackage(t, evidence.SchemaVersion, goodExcerpt())
	var tree map[string]any
	if err := json.Unmarshal(raw, &tree); err != nil {
		t.Fatal(err)
	}
	ex := tree["formats"].([]any)[1].(map[string]any)["excerpt"].(map[string]any)
	if ex["complete"] != true {
		t.Fatalf("complete is not written: %v", ex)
	}
	delete(ex, "complete")
	cut, err := evidence.Canonical(tree)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(cut)
	if _, err := evidence.Open(cut, hex.EncodeToString(sum[:])); !errors.Is(err, evidence.ErrInvalidEvidence) {
		t.Fatalf("an excerpt without complete: %v", err)
	}
	// An incomplete excerpt is written as false, not left out.
	incomplete := goodExcerpt()
	incomplete.Complete = false
	raw, seal, p := excerptPackage(t, evidence.SchemaVersion, incomplete)
	if !strings.Contains(string(raw), `"complete":false`) {
		t.Errorf("complete:false is not in the package: %s", raw)
	}
	if got, err := evidence.Open(raw, seal); err != nil || !reflect.DeepEqual(got, p) {
		t.Errorf("incomplete excerpt: %v", err)
	}
}

func TestOpenRefusesOneHashWithTwoLengths(t *testing.T) {
	p := pkg(t)
	p.SchemaVersion = evidence.SchemaVersion
	a, b := goodExcerpt(), goodExcerpt()
	b.Chars = 31
	p.Formats = []evidence.FormatEvidence{
		{Format: "EPUB", State: evidence.StateOK, Excerpt: a},
		{Format: "KEPUB", State: evidence.StateOK, Excerpt: b},
	}
	raw, seal, err := evidence.Seal(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := evidence.Open(raw, seal); !errors.Is(err, evidence.ErrInvalidEvidence) {
		t.Fatalf("Open = %v", err)
	}
	b.Chars = 30
	raw, seal, _ = evidence.Seal(p)
	if _, err := evidence.Open(raw, seal); err != nil {
		t.Fatalf("the same excerpt on two formats: %v", err)
	}
	if _, err := evidence.ExcerptRefs(p); err != nil {
		t.Fatal(err)
	}
	b.Chars = 31
	if _, err := evidence.ExcerptRefs(p); !errors.Is(err, evidence.ErrInvalidEvidence) {
		t.Fatalf("ExcerptRefs = %v", err)
	}
}

func TestExcerptBuilderIncomplete(t *testing.T) {
	b := evidence.NewExcerptBuilder(evidence.HintEPUBText)
	b.Add(1, evidence.LandmarkFront, "one")
	if ex := b.Excerpt(); !ex.Complete || ex.Truncated {
		t.Fatalf("a whole excerpt = %+v", ex)
	}
	b.MarkIncomplete()
	if ex := b.Excerpt(); ex.Complete || ex.Truncated {
		t.Fatalf("an incomplete excerpt = %+v", ex)
	}
	b.MarkTruncated()
	if ex := b.Excerpt(); ex.Complete || !ex.Truncated {
		t.Fatalf("an incomplete, truncated excerpt = %+v", ex)
	}
	// Nothing read at all is no excerpt, whatever failed.
	none := evidence.NewExcerptBuilder(evidence.HintEPUBText)
	none.MarkIncomplete()
	if none.Excerpt() != nil {
		t.Error("an excerpt of no text")
	}
}

func TestExcerptMethodFor(t *testing.T) {
	for format, want := range map[string]string{
		"EPUB": evidence.ExcerptEPUBText, "KEPUB": evidence.ExcerptEPUBText, "ORIGINAL_EPUB": evidence.ExcerptEPUBText, "epub": evidence.ExcerptEPUBText,
		"PDF": evidence.ExcerptTextLayer, "ORIGINAL_PDF": evidence.ExcerptTextLayer,
		"AZW3": "", "MOBI": "", "CBZ": "", "": "",
	} {
		if got := evidence.ExcerptMethodFor(format); got != want {
			t.Errorf("ExcerptMethodFor(%q) = %q, want %q", format, got, want)
		}
	}
}
