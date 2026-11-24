package evidence

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/library"
)

// SchemaVersion is the version of Package this code writes. Open also reads
// SchemaVersionV4, SchemaVersionV3, SchemaVersionV2 and SchemaVersionV1.
// Version 1 has none of the fields ADR-032 added; version 2 has no excerpts;
// version 3 has no extraction (M4); version 4 has no series or series index
// (the series read path); version 5 also holds the cover candidates (ADR-043).
// A package of an earlier version that carries a field
// of a later one is refused.
const (
	SchemaVersion   = 5
	SchemaVersionV5 = 5
	SchemaVersionV4 = 4
	SchemaVersionV3 = 3
	SchemaVersionV2 = 2
	SchemaVersionV1 = 1
)

// State is a format's readability (spec section 6).
type State string

const (
	StateOK           State = "ok"
	StateCorrupt      State = "corrupt"
	StateEmpty        State = "empty"
	StateDRM          State = "drm"
	StateUnsupported  State = "unsupported"
	StateNotInspected State = "not_inspected"
	StateMissing      State = "missing"
)

// Reasons qualify a state.
const (
	ReasonTooLarge      = "too_large"
	ReasonSizeUnknown   = "size_unknown"
	ReasonSizeMismatch  = "size_mismatch"
	ReasonNotInCalibre  = "not_in_calibre"
	ReasonMissingOnDisk = "missing_on_disk"
	ReasonNoInspector   = "no_inspector"
	ReasonTimeout       = "timeout"

	ReasonXrefRepaired         = "xref_repaired"
	ReasonEncrypted            = "encrypted"
	ReasonHeaderOnly           = "header_only"
	ReasonHuffCDIC             = "huff_cdic"
	ReasonZipMethod            = "zip_method"
	ReasonTooManyEntries       = "too_many_entries"
	ReasonDecompressedTooLarge = "decompressed_too_large"
	ReasonCompressionRatio     = "compression_ratio"
	ReasonInspectorError       = "inspector_error"
)

// Whether a format holds text, where the inspector can tell.
const (
	TextPresent    = "present"
	TextAbsent     = "absent"
	TextUnverified = "unverified"
)

var (
	ErrSealBroken    = errors.New("evidence: the seal doesn't match the package")
	ErrUnknownSchema = errors.New("evidence: unknown schema version")
	// ErrInvalidEvidence is a package whose contents contradict its schema
	// version or the closed sets of its fields.
	ErrInvalidEvidence = errors.New("evidence: invalid package")
)

// Record is Calibre's record of the book as it was read.
type Record struct {
	Title        string            `json:"title"`
	Authors      []string          `json:"authors"`
	AuthorSort   string            `json:"author_sort"`
	Languages    []string          `json:"languages"`
	Identifiers  map[string]string `json:"identifiers"`
	Publisher    string            `json:"publisher"`
	PubDate      string            `json:"pubdate"`
	LastModified string            `json:"last_modified"` // RFC 3339, UTC
	HasCover     bool              `json:"has_cover"`
	// TitleSort is nil when it wasn't captured, as in packages sealed
	// before M2.4; those keep their bytes and seal.
	TitleSort *string `json:"title_sort,omitempty"`
	// AuthorSortMap is each author's sort in Calibre's authors table; nil
	// when it wasn't captured, as in packages sealed before ADR-028.
	AuthorSortMap map[string]string `json:"author_sort_map,omitempty"`
	// Series is Calibre's series name and SeriesIndex its position as a
	// canonical decimal string (package seriesindex: "1", "2.5"; a string
	// because the seal's canonical JSON has no fractional numbers). Both are
	// empty for a book without a series, and for every package of schema 4
	// and earlier (schema 5).
	Series      string `json:"series,omitempty"`
	SeriesIndex string `json:"series_index,omitempty"`
}

// FormatEvidence is what was learnt about one format. SHA256 is set when the
// bytes were read; Detected, Text and Metadata when they were inspected.
type FormatEvidence struct {
	Format   string        `json:"format"`
	Size     int64         `json:"size"`
	SHA256   string        `json:"sha256,omitempty"`
	State    State         `json:"state"`
	Reason   string        `json:"reason,omitempty"`
	Detected string        `json:"detected,omitempty"`
	Text     string        `json:"text,omitempty"`
	Metadata *FileMetadata `json:"metadata,omitempty"`
	// ISBNHints is set for a PDF whose text layer or OCR was read, and for an
	// EPUB whose front matter was.
	ISBNHints *ISBNHints `json:"isbn_hints,omitempty"`
	// Excerpt describes the format's stored front-matter text (schema 3,
	// ADR-032 part 2); the body is not in the package.
	Excerpt *Excerpt `json:"excerpt,omitempty"`
	// Extraction is what the extractors and parsers grounded in the excerpt
	// (schema 4, M4); shadow evidence no decision reads yet.
	Extraction *Extraction `json:"extraction,omitempty"`
	// FileCover is the measures of the cover image an EPUB's own OPF names
	// (schema 5, ADR-043); never the image.
	FileCover *CoverCandidate `json:"file_cover,omitempty"`
}

// ISBN hints come from a PDF's text layer or OCR, or an EPUB's front matter.
// They seed lookups and are never authority (spec section 7).
const (
	HintTextLayer = "text_layer"
	HintOCR       = "ocr"
	HintEPUBText  = "epub_text" // schema 2

	HintOK          = "ok"
	HintTimeout     = "timeout"
	HintFailed      = "failed"
	HintUnavailable = "unavailable"
)

// Where in a book an ISBN hint was found (ISBNContext.Landmark): the
// copyright page, the title page, other front matter, the last documents of
// the book, or "" for a PDF, whose pages carry no landmarks.
const (
	LandmarkCopyright = "copyright"
	LandmarkTitle     = "title"
	LandmarkFront     = "front"
	LandmarkBack      = "back"
)

// What an ISBN's label says it identifies (ISBNContext.Qualifier), or "".
const (
	QualifierEbook     = "ebook"
	QualifierPDF       = "pdf"
	QualifierHardcover = "hardcover"
	QualifierPaperback = "paperback"
	QualifierPrint     = "print"
)

// ISBNContext says where an ISBN was found and what its label said. Both are
// from closed sets and may be empty.
type ISBNContext struct {
	ISBN      string `json:"isbn"`
	Landmark  string `json:"landmark,omitempty"`
	Qualifier string `json:"qualifier,omitempty"`
}

// ISBNHints records which pages (or, for an EPUB, 1-based spine positions) a
// tool read and the checksum-valid ISBNs it found there. No text is kept.
type ISBNHints struct {
	Method string   `json:"method"`
	State  string   `json:"state"`
	Pages  []int    `json:"pages"`           // pages or spine positions processed, ascending; an EPUB document read only to its scan window (ADR-032) counts as processed
	ISBNs  []string `json:"isbns,omitempty"` // canonical ISBN-13, first seen, at most 16
	// Context has one entry per ISBN, in the same order (schema 2).
	Context []ISBNContext `json:"context,omitempty"`
	// Lang is the tesseract model an OCR read used (schema 2).
	Lang string `json:"lang,omitempty"`
}

// Sources and their outcomes (spec sections 6 and 9).
const (
	SourceDNB         = "dnb"
	SourceBNE         = "bne"
	SourceOpenLibrary = "openlibrary"
	SourceGoogleBooks = "googlebooks"
	// Wikidata and Hardcover are recorded and sealed only; decisions up to
	// version 5 ignore them (ADR-035).
	SourceWikidata  = "wikidata"
	SourceHardcover = "hardcover"

	SourceOK            = "ok"
	SourceNoMatch       = "no_match"
	SourceNotConfigured = "not_configured"
	SourceUnavailable   = "unavailable"
	SourceNotQueried    = "not_queried"
)

// Seed classes: where a looked-up ISBN came from (spec section 9). Hint
// seeds use the hint method: HintTextLayer, HintOCR or HintEPUBText.
const (
	SeedFile    = "file"
	SeedCalibre = "calibre"
)

// SourceResult is one source's answer for one ISBN.
type SourceResult struct {
	Source  string         `json:"source"`
	Outcome string         `json:"outcome"`
	Records []SourceRecord `json:"records,omitempty"`
}

// Lookup is every source's answer for one ISBN, in adapter order: dnb, bne,
// openlibrary, googlebooks, then wikidata and hardcover. Packages sealed
// before ADR-035 end at googlebooks, and readers must not assume more.
type Lookup struct {
	ISBN    string         `json:"isbn"`
	Seeds   []string       `json:"seeds"`
	Results []SourceResult `json:"results"`
}

// SourceRecord is one validated record, stored as the source states it and
// capped. ISBNs always contains the ISBN it was found by.
type SourceRecord struct {
	ID            string   `json:"id"`
	Title         string   `json:"title,omitempty"`
	Subtitle      string   `json:"subtitle,omitempty"`
	Authors       []string `json:"authors,omitempty"`
	Languages     []string `json:"languages,omitempty"`
	Publisher     string   `json:"publisher,omitempty"`
	Date          string   `json:"date,omitempty"`
	ISBNs         []string `json:"isbns"`
	SourceRecords []string `json:"source_records,omitempty"` // Open Library only
	// Series and SeriesIndex are the series the source says the book is part
	// of and its canonical position (Wikidata P179 and P1545; schema 5). They
	// are recorded only: no decision reads them (ADR-035).
	Series      string `json:"series,omitempty"`
	SeriesIndex string `json:"series_index,omitempty"`
}

// FileMetadata is what a file says about itself, cleaned and capped by the
// inspector (spec section 6).
type FileMetadata struct {
	Title     string   `json:"title,omitempty"`
	Authors   []string `json:"authors,omitempty"`
	Languages []string `json:"languages,omitempty"`
	ISBNs     []string `json:"isbns,omitempty"`
	Publisher string   `json:"publisher,omitempty"`
	Date      string   `json:"date,omitempty"`
	// Series and SeriesIndex are what the OPF says (Calibre's calibre:series
	// meta, or an EPUB 3 belongs-to-collection of type series), cleaned and
	// capped; the index is a canonical decimal string (schema 5).
	Series      string          `json:"series,omitempty"`
	SeriesIndex string          `json:"series_index,omitempty"`
	Limits      *MetadataLimits `json:"limits,omitempty"`
}

// MetadataLimits records completeness without retaining discarded metadata.
// A nil marker is historical evidence whose capture completeness is unknown.
type MetadataLimits struct {
	Version   int  `json:"version"`
	Title     bool `json:"title,omitempty"`
	Authors   bool `json:"authors,omitempty"`
	Languages bool `json:"languages,omitempty"`
	ISBNs     bool `json:"isbns,omitempty"`
	Publisher bool `json:"publisher,omitempty"`
	Date      bool `json:"date,omitempty"`
	// Series: the name was cut at the cap. SeriesIndex: the file gave an
	// index that is not a plain decimal within the caps; it is not kept
	// (schema 5).
	Series      bool `json:"series,omitempty"`
	SeriesIndex bool `json:"series_index,omitempty"`
}

// Package is one book's sealed evidence. It holds no book text (only the
// hash of each format's excerpt, ADR-032), no file copies and no server paths.
type Package struct {
	SchemaVersion   int              `json:"schema_version"`
	LibraryID       string           `json:"library_id"`
	BookID          int              `json:"book_id"`
	UUID            string           `json:"uuid"`
	Snapshot        string           `json:"snapshot"`
	Calibre         Record           `json:"calibre_record"`
	Formats         []FormatEvidence `json:"formats"`
	FindingsVersion int              `json:"findings_version,omitempty"`
	Findings        []Finding        `json:"findings,omitempty"`
	// Lookups is empty for packages sealed before M3.1, so their seals hold.
	Lookups []Lookup `json:"lookups,omitempty"`
	// The decision (ADR-020) is empty for packages sealed before M3.2.
	DecisionVersion int        `json:"decision_version,omitempty"`
	Tier            string     `json:"tier,omitempty"`
	TierReasons     []string   `json:"tier_reasons,omitempty"`
	Proposals       []Proposal `json:"proposals,omitempty"`
	// Cover (ADR-023) is nil for packages sealed before M5.1, so their seals
	// hold; since then every package has one.
	Cover *CoverEvidence `json:"cover,omitempty"`
}

// CoverEvidence is what was learnt about a book's cover: missing (Calibre has
// none, or lists one it can't send), corrupt, not_inspected, or ok with its
// measures. Flatness is -1 unless the cover was decoded.
type CoverEvidence struct {
	State    State  `json:"state"`
	Reason   string `json:"reason,omitempty"`
	Bytes    int64  `json:"bytes"`
	SHA256   string `json:"sha256,omitempty"`
	Detected string `json:"detected,omitempty"`
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
	DHash    string `json:"dhash,omitempty"`
	Flatness int    `json:"flatness"`
}

// Tiers (spec section 7) and the internal root's name; source roots use the
// source names.
const (
	TierA    = "A"
	TierB    = "B"
	TierC    = "C"
	RootFile = "file"
	// RootText is the book's printed text, read from the sealed extraction
	// (ADR-036).
	RootText = "text"
	// RootCalibreRule marks a sort proposal: Calibre's own rule applied to
	// Calibre's record (ADR-028).
	RootCalibreRule = "calibre_rule"
)

// Proposal is one field's fix: Calibre's value as read, the value two
// independent roots agree on, and those roots.
type Proposal struct {
	Field    string   `json:"field"`
	Current  []string `json:"current"`
	Proposed []string `json:"proposed"`
	Roots    []string `json:"roots"`
}

// RecordFrom copies the book's record, with empty lists and maps instead of
// nil so the package always has the same shape.
func RecordFrom(b library.Book) Record {
	return Record{
		Title:         b.Title,
		Authors:       orEmpty(b.Authors),
		AuthorSort:    b.AuthorSort,
		Languages:     orEmpty(b.Languages),
		Identifiers:   orEmptyMap(b.Identifiers),
		Publisher:     b.Publisher,
		PubDate:       b.PubDate,
		LastModified:  b.LastModified.UTC().Format(time.RFC3339Nano),
		HasCover:      b.HasCover,
		TitleSort:     copyString(b.TitleSort),
		AuthorSortMap: maps.Clone(b.AuthorSortMap),
		Series:        b.Series,
		SeriesIndex:   seriesIndexOf(b),
	}
}

// seriesIndexOf is the book's series index when it has a series. Calibre
// reports 1.0 for a book without one, which says nothing.
func seriesIndexOf(b library.Book) string {
	if b.Series == "" {
		return ""
	}
	return b.SeriesIndex
}

func copyString(s *string) *string {
	if s == nil {
		return nil
	}
	c := *s
	return &c
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func orEmptyMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

// Snapshot is the SHA-256 over what decides whether the book changed while
// it was checked: uuid, last_modified, identifiers, and each format's name,
// size and mtime.
func Snapshot(b library.Book) (string, error) {
	type format struct {
		Name  string `json:"name"`
		Size  int64  `json:"size"`
		MTime string `json:"mtime"`
	}
	formats := make([]format, 0, len(b.Formats))
	for _, f := range b.Formats {
		formats = append(formats, format{Name: f.Name, Size: f.Size, MTime: f.MTime.UTC().Format(time.RFC3339Nano)})
	}
	slices.SortFunc(formats, func(x, y format) int { return strings.Compare(x.Name, y.Name) })
	raw, err := Canonical(struct {
		UUID         string            `json:"uuid"`
		LastModified string            `json:"last_modified"`
		Identifiers  map[string]string `json:"identifiers"`
		Formats      []format          `json:"formats"`
	}{b.UUID, b.LastModified.UTC().Format(time.RFC3339Nano), orEmptyMap(b.Identifiers), formats})
	if err != nil {
		return "", fmt.Errorf("evidence: snapshot: %w", err)
	}
	return hash(raw), nil
}

// Seal returns the package's canonical bytes and their SHA-256.
func Seal(p Package) (raw []byte, seal string, err error) {
	raw, err = Canonical(p)
	if err != nil {
		return nil, "", fmt.Errorf("evidence: seal: %w", err)
	}
	return raw, hash(raw), nil
}

// Open checks raw against its seal and decodes it. raw may be reformatted,
// as jsonb returns it: it is canonicalised again before the seal is checked.
func Open(raw []byte, seal string) (Package, error) {
	canon, err := Canonical(json.RawMessage(raw))
	if err != nil {
		return Package{}, fmt.Errorf("evidence: open: %w", err)
	}
	if hash(canon) != seal {
		return Package{}, ErrSealBroken
	}
	var head struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(canon, &head); err != nil {
		return Package{}, fmt.Errorf("evidence: open: %w", err)
	}
	if head.SchemaVersion < SchemaVersionV1 || head.SchemaVersion > SchemaVersion {
		return Package{}, fmt.Errorf("%w: %d", ErrUnknownSchema, head.SchemaVersion)
	}
	dec := json.NewDecoder(bytes.NewReader(canon))
	dec.DisallowUnknownFields()
	var p Package
	if err := dec.Decode(&p); err != nil {
		return Package{}, fmt.Errorf("evidence: open: %w", err)
	}
	if err := p.checkHints(); err != nil {
		return Package{}, err
	}
	if err := p.checkExcerpts(); err != nil {
		return Package{}, err
	}
	if err := p.checkExtraction(); err != nil {
		return Package{}, err
	}
	if err := p.checkSeries(); err != nil {
		return Package{}, err
	}
	if err := p.checkCoverCandidates(); err != nil {
		return Package{}, err
	}
	// complete is required, not defaulted: an excerpt that leaves it out
	// doesn't say whether every document was read.
	var probe struct {
		Formats []struct {
			Excerpt *struct {
				Complete *bool `json:"complete"`
			} `json:"excerpt"`
		} `json:"formats"`
	}
	if err := json.Unmarshal(canon, &probe); err != nil {
		return Package{}, fmt.Errorf("evidence: open: %w", err)
	}
	for _, f := range probe.Formats {
		if f.Excerpt != nil && f.Excerpt.Complete == nil {
			return Package{}, fmt.Errorf("%w: an excerpt has no complete field", ErrInvalidEvidence)
		}
	}
	return p, nil
}

// checkHints refuses hints that only a later schema may carry in a version 1
// package, and a Context that doesn't follow its ISBNs or the closed sets.
func (p Package) checkHints() error {
	for _, f := range p.Formats {
		h := f.ISBNHints
		if h == nil {
			continue
		}
		if p.SchemaVersion == SchemaVersionV1 && (h.Method == HintEPUBText || len(h.Context) > 0 || h.Lang != "") {
			return fmt.Errorf("%w: schema 1 holds a schema 2 field in the %s hints", ErrInvalidEvidence, f.Format)
		}
		if !slices.Contains(langs, h.Lang) || h.Lang != "" && h.Method != HintOCR {
			return fmt.Errorf("%w: %s hints have the language %q with method %s", ErrInvalidEvidence, f.Format, h.Lang, h.Method)
		}
		if len(h.Context) == 0 {
			continue
		}
		if len(h.Context) != len(h.ISBNs) {
			return fmt.Errorf("%w: %s hints have %d contexts for %d ISBNs", ErrInvalidEvidence, f.Format, len(h.Context), len(h.ISBNs))
		}
		for i, c := range h.Context {
			if c.ISBN != h.ISBNs[i] || !slices.Contains(landmarks, c.Landmark) || !slices.Contains(qualifiers, c.Qualifier) {
				return fmt.Errorf("%w: %s hints have a context that doesn't match ISBN %d", ErrInvalidEvidence, f.Format, i)
			}
		}
	}
	return nil
}

var (
	langs      = []string{"", "eng", "deu", "spa"} // the tessdata in the image
	landmarks  = []string{"", LandmarkCopyright, LandmarkTitle, LandmarkFront, LandmarkBack}
	qualifiers = []string{"", QualifierEbook, QualifierPDF, QualifierHardcover, QualifierPaperback, QualifierPrint}
)

func hash(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
