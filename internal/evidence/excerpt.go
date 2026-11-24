package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// Front-matter excerpts (ADR-032 part 2): a bounded text of the pages a hint
// scan read, so that an admin can see where a hint came from. The package
// seals only the excerpt's hash and its unit map; the body lives in the
// excerpts table, addressed by that hash. Nothing in findings or decide
// reads an excerpt.
const (
	// MaxExcerptRunes bounds one format's stored excerpt.
	MaxExcerptRunes = 12_000
	// excerptLineWindow: a cut prefers the last line break inside the bound
	// when that is within this many runes of it.
	excerptLineWindow = 200
	// ExcerptSeparator joins two units in the stored text. It belongs to no
	// unit.
	ExcerptSeparator = "\n\n"
)

// Methods an excerpt can have: the hint method whose pages it holds. OCR text
// is never stored.
const (
	ExcerptEPUBText  = HintEPUBText
	ExcerptTextLayer = HintTextLayer
)

// Excerpt describes one format's stored excerpt. Everything but Text is
// sealed in the package; Text is the body, carried in memory from the
// inspector to the store and never serialised.
type Excerpt struct {
	Method string        `json:"method"`
	Units  []ExcerptUnit `json:"units"`
	SHA256 string        `json:"sha256"` // lowercase hex of the UTF-8 body
	Chars  int           `json:"chars"`  // runes in the body
	// Truncated: a bound cut some text, either a document's read limit or
	// the rune cap.
	Truncated bool `json:"truncated"`
	// Complete: every document or page range the scan meant to read was read.
	// A document that failed to read or parse adds no text and clears it.
	Complete bool `json:"complete"`

	Text string `json:"-"`
}

// ExcerptUnit says which part of the body came from one spine document or
// page: its 1-based position, its landmark (empty for a PDF) and its rune
// range [Start, End) in the body.
type ExcerptUnit struct {
	Pos      int    `json:"pos"`
	Landmark string `json:"landmark"`
	Start    int    `json:"start"`
	End      int    `json:"end"`
}

var excerptHash = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ValidExcerptHash reports whether s is 64 lowercase hex characters.
func ValidExcerptHash(s string) bool { return excerptHash.MatchString(s) }

// ExcerptHash returns the lowercase hex SHA-256 of body.
func ExcerptHash(body string) string {
	h := sha256.Sum256([]byte(body))
	return hex.EncodeToString(h[:])
}

// NormalizeExcerptText returns s as stored: valid UTF-8, LF line endings, no
// control characters (NUL included), whitespace inside a line collapsed to
// one space, lines trimmed, at most one blank line in a row and none at either
// end, in NFC. Format and bidi characters stay, so offsets stay true.
func NormalizeExcerptText(s string) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\n':
			return '\n'
		case r == '\r', r == '\f', r == '\v', r == 0x85, r == 0x2028, r == 0x2029:
			return '\n'
		case unicode.IsSpace(r), unicode.IsControl(r):
			return ' '
		}
		return r
	}, s)
	var out strings.Builder
	out.Grow(len(s))
	blank := false
	for _, line := range strings.Split(s, "\n") {
		line = strings.Join(strings.FieldsFunc(line, func(r rune) bool { return r == ' ' }), " ")
		if line == "" {
			blank = out.Len() > 0
			continue
		}
		if out.Len() > 0 {
			out.WriteByte('\n')
			if blank {
				out.WriteByte('\n')
			}
		}
		blank = false
		out.WriteString(line)
	}
	return norm.NFC.String(out.String())
}

// ExcerptBuilder collects the units of one format's excerpt, in order, under
// the rune bound.
type ExcerptBuilder struct {
	method     string
	text       []rune
	units      []ExcerptUnit
	truncated  bool
	incomplete bool
	full       bool
}

// NewExcerptBuilder starts an excerpt of the given method (ExcerptEPUBText or
// ExcerptTextLayer).
func NewExcerptBuilder(method string) *ExcerptBuilder { return &ExcerptBuilder{method: method} }

// Add appends one spine document's or page's text, normalised. A unit with no
// text left is skipped; text past the bound is cut and marks the excerpt
// truncated.
func (b *ExcerptBuilder) Add(pos int, landmark, text string) {
	n := []rune(NormalizeExcerptText(text))
	if len(n) == 0 {
		return
	}
	if b.full {
		b.truncated = true
		return
	}
	sep := 0
	if len(b.text) > 0 {
		sep = utf8.RuneCountInString(ExcerptSeparator)
	}
	room := MaxExcerptRunes - len(b.text) - sep
	if room <= 0 {
		b.full, b.truncated = true, true
		return
	}
	if len(n) > room {
		n = cutExcerpt(n, room)
		b.full, b.truncated = true, true
	}
	if sep > 0 {
		b.text = append(b.text, []rune(ExcerptSeparator)...)
	}
	start := len(b.text)
	b.text = append(b.text, n...)
	b.units = append(b.units, ExcerptUnit{Pos: pos, Landmark: landmark, Start: start, End: len(b.text)})
}

// cutExcerpt cuts n to at most room runes, at the last line break when that
// is within excerptLineWindow of the bound, and drops trailing white space.
func cutExcerpt(n []rune, room int) []rune {
	head := n[:room]
	for i := len(head) - 1; i > 0 && room-i <= excerptLineWindow; i-- {
		if head[i] == '\n' {
			head = head[:i]
			break
		}
	}
	for len(head) > 0 && (head[len(head)-1] == ' ' || head[len(head)-1] == '\n') {
		head = head[:len(head)-1]
	}
	return head
}

// Full reports whether the bound is reached: later units are not stored, and
// the excerpt is already truncated.
func (b *ExcerptBuilder) Full() bool { return b.full }

// MarkTruncated records that the reader cut text off before the builder saw it.
func (b *ExcerptBuilder) MarkTruncated() { b.truncated = true }

// MarkIncomplete records that a document or page range the scan meant to read
// added no text because it failed to read or parse.
func (b *ExcerptBuilder) MarkIncomplete() { b.incomplete = true }

// Excerpt returns the collected excerpt, or nil if no unit had any text.
func (b *ExcerptBuilder) Excerpt() *Excerpt {
	if len(b.units) == 0 {
		return nil
	}
	body := string(b.text)
	return &Excerpt{
		Method: b.method, Units: slices.Clone(b.units), SHA256: ExcerptHash(body),
		Chars: len(b.text), Truncated: b.truncated, Complete: !b.incomplete, Text: body,
	}
}

// ExcerptMethodFor is the excerpt method a format's name allows: epub_text for
// EPUB and KEPUB, text_layer for PDF (with Calibre's ORIGINAL_ prefix
// ignored), and "" for every other format, which has no excerpt.
func ExcerptMethodFor(format string) string {
	switch strings.TrimPrefix(strings.ToUpper(format), "ORIGINAL_") {
	case "EPUB", "KEPUB":
		return ExcerptEPUBText
	case "PDF":
		return ExcerptTextLayer
	}
	return ""
}

// ExcerptRefs returns the length the package states for each excerpt hash it
// refers to. One hash with two lengths is invalid evidence.
func ExcerptRefs(p Package) (map[string]int, error) {
	refs := map[string]int{}
	for _, f := range p.Formats {
		e := f.Excerpt
		if e == nil {
			continue
		}
		if n, ok := refs[e.SHA256]; ok && n != e.Chars {
			return nil, fmt.Errorf("%w: excerpt %.8s has two lengths", ErrInvalidEvidence, e.SHA256)
		}
		refs[e.SHA256] = e.Chars
	}
	return refs, nil
}

// checkExcerpts refuses an excerpt a schema before 3 can't carry, one with a
// method outside the set, units that are out of order or out of range, or a
// hash that isn't 64 lowercase hex characters.
func (p Package) checkExcerpts() error {
	for _, f := range p.Formats {
		e := f.Excerpt
		if e == nil {
			continue
		}
		if p.SchemaVersion < SchemaVersionV3 {
			return fmt.Errorf("%w: schema %d holds a schema 3 excerpt in %s", ErrInvalidEvidence, p.SchemaVersion, f.Format)
		}
		if m := ExcerptMethodFor(f.Format); m == "" || m != e.Method {
			return fmt.Errorf("%w: %s can't have a %q excerpt", ErrInvalidEvidence, f.Format, e.Method)
		}
		if err := e.check(); err != nil {
			return fmt.Errorf("%w: %s excerpt: %w", ErrInvalidEvidence, f.Format, err)
		}
	}
	_, err := ExcerptRefs(p)
	return err
}

func (e *Excerpt) check() error {
	if e.Method != ExcerptEPUBText && e.Method != ExcerptTextLayer {
		return fmt.Errorf("method %q", e.Method)
	}
	if !ValidExcerptHash(e.SHA256) {
		return fmt.Errorf("sha256 %q", e.SHA256)
	}
	if e.Chars <= 0 || e.Chars > MaxExcerptRunes {
		return fmt.Errorf("%d chars", e.Chars)
	}
	if len(e.Units) == 0 {
		return fmt.Errorf("no units")
	}
	prevPos, prevEnd := 0, 0
	for i, u := range e.Units {
		switch {
		case u.Pos <= prevPos:
			return fmt.Errorf("unit %d position %d is not after %d", i, u.Pos, prevPos)
		case !slices.Contains(landmarks, u.Landmark) || e.Method == ExcerptTextLayer && u.Landmark != "" || e.Method == ExcerptEPUBText && u.Landmark == "":
			return fmt.Errorf("unit %d landmark %q", i, u.Landmark)
		case u.Start < prevEnd || u.End <= u.Start || u.End > e.Chars:
			return fmt.Errorf("unit %d range %d-%d of %d chars after %d", i, u.Start, u.End, e.Chars, prevEnd)
		}
		prevPos, prevEnd = u.Pos, u.End
	}
	return nil
}
