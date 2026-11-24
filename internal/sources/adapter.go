package sources

import (
	"context"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/language"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/findings"
)

// Adapter looks one ISBN up in one source.
type Adapter interface {
	Name() string
	Lookup(ctx context.Context, f Fetcher, isbn string) (outcome string, records []evidence.SourceRecord)
}

// Configurer is a source that needs a key or token. One that isn't
// configured answers not_configured without touching the cache, so its old
// cached answers are never served after its key was removed.
type Configurer interface {
	Configured() bool
}

// Prober is a source that can check, at the start of a run, that its
// lookups answer truthfully. A source that fails its probe is unavailable for
// the whole run, so its empty answers are never sealed or cached as no_match.
type Prober interface {
	Probe(ctx context.Context, f Fetcher) bool
}

// MappingVersion names the rules that turn responses into records. Change it
// whenever a mapping changes, so cached records built by older rules aren't
// replayed (ADR-020).
//
// Deliberately not bumped for the series (Wikidata P179 and P1545, schema 5):
// that would ask every source again for every ISBN. Wikidata answers cached
// before it have no series and gain one only as their 30-day cache rows
// expire.
const MappingVersion = 1

// Caps on what a record keeps (ADR-019).
const (
	maxRunes   = 512
	maxItems   = 16
	maxRecords = 8
	maxIDBytes = 128
)

// text cleans one value: control and format characters go, whitespace
// collapses, and the value is cut at 512 runes.
func text(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == utf8.RuneError, unicode.IsControl(r) && !unicode.IsSpace(r), unicode.Is(unicode.Cf, r):
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > maxRunes {
		s = string([]rune(s)[:maxRunes])
	}
	return s
}

// appendText adds a cleaned, non-empty, new value while the list has room.
func appendText(list []string, s string) []string {
	if s = text(s); s == "" || len(list) >= maxItems || slices.Contains(list, s) {
		return list
	}
	return append(list, s)
}

// id cleans a record ID and cuts it at 128 bytes.
func id(s string) string {
	s = text(s)
	for len(s) > maxIDBytes {
		_, n := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-n]
	}
	return s
}

// appendISBN adds a checksum-valid ISBN as canonical ISBN-13.
func appendISBN(list []string, raw string) []string {
	c, ok := findings.CanonicalISBN(raw)
	if !ok || len(list) >= maxItems || slices.Contains(list, c) {
		return list
	}
	return append(list, c)
}

// bibliographic maps the 20 ISO 639-2/B codes, which MARC uses, to their T
// codes; x/text keeps the B form.
var bibliographic = map[string]string{
	"alb": "sqi", "arm": "hye", "baq": "eus", "bur": "mya", "chi": "zho", "cze": "ces", "dut": "nld",
	"fre": "fra", "geo": "kat", "ger": "deu", "gre": "ell", "ice": "isl", "mac": "mkd", "mao": "mri",
	"may": "msa", "per": "fas", "rum": "ron", "slo": "slk", "tib": "bod", "wel": "cym",
}

// lang maps a language code to ISO 639-3; "" for codes that aren't a
// language (und, mul, zxx, mis) or don't parse.
func lang(code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	switch code {
	case "", "und", "mul", "zxx", "mis":
		return ""
	}
	if t, ok := bibliographic[code]; ok {
		code = t
	}
	b, err := language.ParseBase(code)
	if err != nil {
		return ""
	}
	return b.ISO3()
}

// accept keeps the records whose own ISBNs contain isbn, at most 8, and
// returns the outcome.
func accept(isbn string, recs []evidence.SourceRecord) (string, []evidence.SourceRecord) {
	var out []evidence.SourceRecord
	for _, r := range recs {
		if slices.Contains(r.ISBNs, isbn) && len(out) < maxRecords {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return evidence.SourceNoMatch, nil
	}
	return evidence.SourceOK, out
}
