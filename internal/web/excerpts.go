package web

import (
	"context"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

// ExcerptReader returns a stored excerpt body by the hash a package seals
// (ADR-032 part 2), having checked the body against that hash. *store.ReportRepo
// implements it. Only the book page reads excerpts.
type ExcerptReader interface {
	Excerpt(ctx context.Context, hash string) (string, error)
}

// excerptView is one format's excerpt as the book page shows it. Parts is nil
// when the text is unavailable: missing, corrupt, or no store behind the page.
type excerptView struct {
	Chars     int
	Truncated bool
	Complete  bool
	Available bool
	Parts     []excerptPart
	// Extraction is the format's shadow extraction (M4), nil when there is
	// none. It needs no text, so it shows when the text is unavailable.
	Extraction *extractionView
}

// excerptPart is a run of text: Mark when it is a hint ISBN, and Claim the
// grounded claims that point at it.
type excerptPart struct {
	Text  string
	Mark  bool
	Claim string
}

// extractionView is what the book page shows of an extraction: the
// extractors' calls, each field's outcome next to Calibre's value, and how
// many claims the text did not show.
type extractionView struct {
	Extractors []evidence.ExtractorRun
	Facts      []factView
	Dropped    int
}

// factView is one agreed fact. Calibre is nil with NoCalibre when the
// snapshot has no such field.
type factView struct {
	Key, Outcome string
	Values       []string
	Voices       string
	Calibre      []string
	NoCalibre    bool
}

// claimSpan is a grounded claim's quote in the view text, with its label.
type claimSpan struct {
	from, to int
	label    string
}

// bookExcerpts reads the excerpt of each format of a book, by format name.
// Any failure shows that format's text as unavailable and nothing more: the
// error is not logged or shown, since it could name the text.
func (s *Server) bookExcerpts(ctx context.Context, p *evidence.Package) map[string]*excerptView {
	if p == nil {
		return nil
	}
	var out map[string]*excerptView
	for _, f := range p.Formats {
		e := f.Excerpt
		if e == nil {
			continue
		}
		if out == nil {
			out = map[string]*excerptView{}
		}
		v := &excerptView{Chars: e.Chars, Truncated: e.Truncated, Complete: e.Complete}
		if f.Extraction != nil {
			v.Extraction = newExtractionView(f.Extraction, p.Calibre)
		}
		out[f.Format] = v
		if s.opts.Excerpts == nil || ctx.Err() != nil {
			continue
		}
		body, err := s.opts.Excerpts.Excerpt(ctx, e.SHA256)
		// The reader checks the hash; the page does too, since the text is
		// shown as evidence.
		if err != nil || evidence.ExcerptHash(body) != e.SHA256 {
			continue
		}
		var isbns []string
		if f.ISBNHints != nil {
			isbns = f.ISBNHints.ISBNs
		}
		text, at := viewMap(body)
		runes := []rune(text)
		v.Available, v.Parts = true, segments(runes, isbnMarks(runes, isbns), claimSpans(f.Extraction, at))
	}
	return out
}

func newExtractionView(x *evidence.Extraction, c evidence.Record) *extractionView {
	v := &extractionView{Extractors: x.Extractors}
	for _, cl := range x.Claims {
		if cl.Status != "grounded" {
			v.Dropped++
		}
	}
	for _, a := range x.Agreed {
		fv := factView{Key: a.Key, Outcome: a.Outcome, Voices: strings.Join(a.Voices, ", ")}
		for _, val := range a.Values {
			fv.Values = append(fv.Values, withQualifier(val.Value, val.Role, val.Label))
		}
		fv.Calibre, fv.NoCalibre = calibreValue(a.Key, c)
		v.Facts = append(v.Facts, fv)
	}
	return v
}

// withQualifier writes a value with its role or label, as "Jo Ray
// (translator)".
func withQualifier(value string, quals ...string) string {
	for _, q := range quals {
		if q != "" {
			value += " (" + q + ")"
		}
	}
	return value
}

// calibreValue is Calibre's value for an agreed key, or none when the
// snapshot has no such field.
func calibreValue(key string, c evidence.Record) ([]string, bool) {
	switch {
	case key == "title":
		return []string{c.Title}, false
	case key == "contributor" || key == "translator":
		return c.Authors, false
	case key == "imprint":
		return []string{c.Publisher}, false
	case key == "isbn":
		if isbn := c.Identifiers["isbn"]; isbn != "" {
			return []string{isbn}, false
		}
		return nil, false
	case strings.HasPrefix(key, "year/"):
		return []string{c.PubDate}, false
	}
	return nil, true
}

// claimSpans places each grounded claim's quote in the view text: at maps an
// offset of the stored text to one of the view text. Claims on one quote and
// field share a label naming their sources.
func claimSpans(x *evidence.Extraction, at []int) []claimSpan {
	if x == nil {
		return nil
	}
	type quote struct {
		start, end int
		what       string
	}
	sources := map[quote][]string{}
	var order []quote
	for _, c := range x.Claims {
		if c.Status != "grounded" || c.End >= len(at) {
			continue
		}
		q := quote{c.Start, c.End, withQualifier(c.Field, c.Role, c.Label)}
		if sources[q] == nil {
			order = append(order, q)
		}
		sources[q] = append(sources[q], c.Source)
	}
	var out []claimSpan
	for _, q := range order {
		src := sources[q]
		slices.Sort(src)
		out = append(out, claimSpan{from: at[q.start], to: at[q.end], label: q.what + " · " + strings.Join(slices.Compact(src), ", ")})
	}
	return out
}

// viewMap strips format characters (Unicode category Cf, which includes the
// bidi controls U+202A to U+202E and U+2066 to U+2069) from stored text. The
// stored text keeps them, so that offsets stay true; viewMap also returns the
// view offset of each rune offset of s (and of its end).
func viewMap(s string) (string, []int) {
	var b strings.Builder
	at := make([]int, 0, utf8.RuneCountInString(s)+1)
	n := 0
	for _, r := range s {
		at = append(at, n)
		if unicode.Is(unicode.Cf, r) || r >= 0x202A && r <= 0x202E || r >= 0x2066 && r <= 0x2069 {
			continue
		}
		b.WriteRune(r)
		n++
	}
	return b.String(), append(at, n)
}

// isSeparator reports whether r may sit between the digits of a printed ISBN:
// a space or any of the hyphens and dashes.
func isSeparator(r rune) bool {
	return r == ' ' || r == '-' || r >= 0x2010 && r <= 0x2015 || r == 0x2212
}

// isbnMarks returns the rune ranges where a hint ISBN is printed: its 13
// digits, with spaces or hyphens between them, not on two lines and not part
// of a longer number. Only the ISBN-13 form of a hint is found; a printed
// ISBN-10 is not.
func isbnMarks(runes []rune, isbns []string) [][2]int {
	if len(isbns) == 0 {
		return nil
	}
	var marks [][2]int
	for i := 0; i < len(runes); {
		if !isDigit(runes[i]) {
			i++
			continue
		}
		// A run of digits and separators, ending in a digit.
		end := i
		for j := i; j < len(runes) && (isDigit(runes[j]) || isSeparator(runes[j])); j++ {
			if isDigit(runes[j]) {
				end = j + 1
			}
		}
		var digits []int // rune index of each digit in the run
		var ds strings.Builder
		for j := i; j < end; j++ {
			if isDigit(runes[j]) {
				digits = append(digits, j)
				ds.WriteRune(runes[j])
			}
		}
		for _, isbn := range isbns {
			if !isISBN13Digits(isbn) {
				continue
			}
			from := 0
			for {
				k := strings.Index(ds.String()[from:], isbn)
				if k < 0 {
					break
				}
				k += from
				from = k + 1
				first, last := digits[k], digits[k+len(isbn)-1]
				// Digits right before or after would make it a longer number.
				if k > 0 && digits[k-1] == first-1 || k+len(isbn) < len(digits) && digits[k+len(isbn)] == last+1 {
					continue
				}
				marks = append(marks, [2]int{first, last + 1})
			}
		}
		i = end
	}
	return marks
}

// isISBN13Digits reports whether s is 13 ASCII digits, the only form of a hint
// the page looks for.
func isISBN13Digits(s string) bool {
	if len(s) != 13 {
		return false
	}
	for _, r := range s {
		if !isDigit(r) {
			return false
		}
	}
	return true
}

func isDigit(r rune) bool { return r >= '0' && r <= '9' }

// segments cuts runes at every mark's edges. A part is Mark when a hint
// ISBN covers it, and its Claim lists the labels of the claims that cover it;
// neighbours with the same marks are joined.
func segments(runes []rune, isbns [][2]int, claims []claimSpan) []excerptPart {
	cuts := []int{0, len(runes)}
	for _, m := range isbns {
		cuts = append(cuts, m[0], m[1])
	}
	for _, c := range claims {
		cuts = append(cuts, c.from, c.to)
	}
	slices.Sort(cuts)
	cuts = slices.Compact(cuts)
	var parts []excerptPart
	for i := 0; i+1 < len(cuts); i++ {
		a, b := cuts[i], cuts[i+1]
		p := excerptPart{Text: string(runes[a:b])}
		for _, m := range isbns {
			p.Mark = p.Mark || m[0] <= a && b <= m[1]
		}
		var labels []string
		for _, c := range claims {
			if c.from <= a && b <= c.to && !slices.Contains(labels, c.label) {
				labels = append(labels, c.label)
			}
		}
		p.Claim = strings.Join(labels, "; ")
		if n := len(parts); n > 0 && parts[n-1].Mark == p.Mark && parts[n-1].Claim == p.Claim {
			parts[n-1].Text += p.Text
			continue
		}
		parts = append(parts, p)
	}
	if len(parts) == 0 {
		return []excerptPart{{Text: ""}}
	}
	return parts
}
