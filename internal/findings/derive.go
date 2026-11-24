package findings

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

// Derive returns unique, ordered observations without modifying its input.
func Derive(ctx context.Context, p evidence.Package) ([]evidence.Finding, error) {
	return derive(ctx, p, Version)
}

// DeriveV1 retains the original rules for historical seals and projections.
func DeriveV1(ctx context.Context, p evidence.Package) ([]evidence.Finding, error) {
	return derive(ctx, p, 1)
}

// DeriveV2 retains the capture-aware rules from before title sort (M2.3).
func DeriveV2(ctx context.Context, p evidence.Package) ([]evidence.Finding, error) {
	return derive(ctx, p, 2)
}

// DeriveVersion derives with the rules of one sealed version.
func DeriveVersion(ctx context.Context, p evidence.Package, version int) ([]evidence.Finding, error) {
	switch version {
	case 1, 2, 3, 4, 5, Version:
		return derive(ctx, p, version)
	default:
		return nil, ErrUnknownVersion
	}
}

// derive applies the rules of version rules: 2 adds capture completeness,
// 3 adds title sort, 4 adds covers, 5 adds the Calibre-record hygiene
// detectors of v5.go, 6 adds the series comparison of series.go.
func derive(ctx context.Context, p evidence.Package, rules int) ([]evidence.Finding, error) {
	captureAware := rules >= 2
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d := derivation{ctx: ctx, found: map[evidence.Finding]struct{}{}}
	if len(p.Formats) == 0 {
		d.add("no_formats", "", "", "")
	}
	cal := metadataValues(p.Calibre.Title, p.Calibre.Authors, p.Calibre.Languages)
	for field, v := range cal {
		d.qualify(field, "", v.state)
	}
	d.authorSortFinding(p.Calibre)
	if rules >= 3 {
		d.titleSortFinding(p.Calibre)
	}
	if rules >= 5 {
		d.calibreHygiene(p.Calibre)
	}
	if rules >= 6 {
		if err := d.seriesFindings(p); err != nil {
			return nil, err
		}
	}
	if rules >= 6 {
		if err := d.coverCandidateFindings(p); err != nil {
			return nil, err
		}
	}
	if p.Cover != nil {
		if rules < 4 {
			return nil, invalidEvidence()
		}
		if err := d.coverFindings(*p.Cover); err != nil {
			return nil, err
		}
	}
	var rawISBNs []string
	for key, val := range p.Calibre.Identifiers {
		if strings.EqualFold(key, "isbn") {
			rawISBNs = append(rawISBNs, val)
		}
	}
	calISBNs, err := d.isbns(rawISBNs, "")
	if err != nil {
		return nil, err
	}
	calDate, calDateState := dateValue(p.Calibre.PubDate)
	d.dateQualification("", calDateState)
	if calDateState == fieldValid && strings.HasSuffix(calDate, "-01-01") {
		d.add("date_precision_unverified", "", "pubdate", "")
	}
	seen := map[string]bool{}
	cross := map[string]string{}
	var common map[string]bool
	isbnFormats := 0
	for _, f := range p.Formats {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if f.Format == "" || seen[f.Format] || !knownReason(f.Reason) {
			return nil, invalidEvidence()
		}
		seen[f.Format] = true
		code, known := fileCode(f.State)
		if !known {
			return nil, invalidEvidence()
		}
		if code != "" {
			d.add(code, f.Format, "", f.Reason)
		}
		if formatMismatch(f.Format, f.Detected) {
			d.add("format_mismatch", f.Format, "", "")
		}
		var capture map[string]string
		if f.Metadata != nil && (captureAware || f.Metadata.Limits != nil) {
			qualifications, err := captureQualifications(f.Metadata)
			if err != nil {
				return nil, err
			}
			if captureAware {
				capture = qualifications
			}
		}
		if !metadataEligible(f) {
			continue
		}
		m := f.Metadata
		if m == nil {
			m = &evidence.FileMetadata{}
		}
		vals := metadataValues(m.Title, m.Authors, m.Languages)
		for field, v := range vals {
			if reason := capture[field]; reason != "" {
				d.add("metadata_unverified", f.Format, field, reason)
				continue
			}
			d.qualify(field, f.Format, v.state)
			if v.state != fieldValid {
				continue
			}
			if cv := cal[field]; cv.state == fieldValid && cv.key != v.key {
				d.add(mismatchCodes[field], f.Format, field, "")
			}
			if prev, ok := cross[field]; ok && prev != v.key {
				d.add(conflictCodes[field], "", field, "")
			}
			cross[field] = v.key
		}
		var candidates map[string]bool
		if reason := capture["isbn"]; reason != "" {
			d.add("metadata_unverified", f.Format, "isbn", reason)
		} else {
			var err error
			candidates, err = d.isbns(m.ISBNs, f.Format)
			if err != nil {
				return nil, err
			}
		}
		if len(calISBNs) == 1 && len(candidates) > 0 {
			for isbn := range calISBNs {
				if !candidates[isbn] {
					d.add("isbn_mismatch", f.Format, "isbn", "")
				}
			}
		}
		if len(candidates) > 0 {
			isbnFormats++
			if common == nil {
				common = candidates
			} else {
				for isbn := range common {
					if !candidates[isbn] {
						delete(common, isbn)
					}
				}
			}
		}
		if reason := capture["pubdate"]; reason != "" {
			d.add("metadata_unverified", f.Format, "pubdate", reason)
		} else {
			date, state := dateValue(m.Date)
			d.dateQualification(f.Format, state)
			if state == fieldValid && calDateState == fieldValid && date != calDate {
				d.add("date_mismatch", f.Format, "pubdate", "")
			}
		}
		if reason := capture["publisher"]; reason != "" {
			d.add("metadata_unverified", f.Format, "publisher", reason)
		}
	}
	if isbnFormats > 1 && len(common) == 0 {
		d.add("formats_isbn_conflict", "", "isbn", "")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result := make([]evidence.Finding, 0, len(d.found))
	for f := range d.found {
		result = append(result, f)
	}
	slices.SortFunc(result, compareFinding)
	return result, nil
}

type derivation struct {
	ctx   context.Context
	found map[evidence.Finding]struct{}
}

func (d *derivation) add(code, format, field, reason string) {
	d.found[evidence.Finding{Code: code, Format: format, Field: field, Reason: reason}] = struct{}{}
}

var missingCodes = map[string]string{"title": "title_missing", "authors": "authors_missing", "languages": "language_missing"}
var mismatchCodes = map[string]string{"title": "title_mismatch", "authors": "authors_mismatch", "languages": "language_mismatch"}
var conflictCodes = map[string]string{"title": "formats_title_conflict", "authors": "formats_authors_conflict", "languages": "formats_language_conflict"}

func (d *derivation) qualify(field, format string, state fieldState) {
	switch state {
	case fieldMissing:
		d.add(missingCodes[field], format, field, "")
	case fieldInvalid:
		d.add("language_invalid", format, field, "")
	case fieldTooLong:
		d.add("metadata_unverified", format, field, "field_too_long")
	case fieldUndetermined:
		d.add("metadata_unverified", format, field, "language_undetermined")
	}
}

type value struct {
	key   string
	state fieldState
}

func metadataValues(title string, authors, languages []string) map[string]value {
	t, ts := normalizeField(title)
	var a, l []string
	as, ls := fieldTooLong, fieldTooLong
	if !listTooLong(authors) {
		a, as = normalizeStrings(authors)
	}
	if !listTooLong(languages) {
		l, ls = normalizeLanguages(languages)
	}
	ab, _ := json.Marshal(a)
	lb, _ := json.Marshal(l)
	return map[string]value{"title": {t, ts}, "authors": {string(ab), as}, "languages": {string(lb), ls}}
}
func listTooLong(in []string) bool {
	count := len(in) - 1
	if count > maxFieldRunes {
		return true
	}
	for _, s := range in {
		count += utf8.RuneCountInString(s)
		if count > maxFieldRunes {
			return true
		}
	}
	return false
}
func (d *derivation) isbns(raw []string, format string) (map[string]bool, error) {
	valid := map[string]bool{}
	if listTooLong(raw) {
		d.add("metadata_unverified", format, "isbn", "field_too_long")
		return valid, nil
	}
	present := false
	for _, s := range raw {
		if err := d.ctx.Err(); err != nil {
			return nil, err
		}
		if strings.TrimSpace(s) == "" {
			continue
		}
		present = true
		if isbn, ok := CanonicalISBN(s); ok {
			valid[isbn] = true
		} else {
			d.add("isbn_invalid", format, "isbn", "")
		}
	}
	if !present {
		d.add("isbn_missing", format, "isbn", "")
	}
	if len(valid) > 1 {
		d.add("isbn_ambiguous", format, "isbn", "")
	}
	return valid, nil
}

func (d *derivation) dateQualification(format string, state fieldState) {
	switch state {
	case fieldInvalid:
		d.add("date_invalid", format, "pubdate", "")
	case fieldUndetermined:
		d.add("date_imprecise", format, "pubdate", "")
	case fieldTooLong:
		d.add("metadata_unverified", format, "pubdate", "field_too_long")
	}
}
func dateValue(raw string) (string, fieldState) {
	if utf8.RuneCountInString(raw) > maxFieldRunes {
		return "", fieldTooLong
	}
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", fieldMissing
	}
	for _, layout := range []string{"2006-01-02", time.RFC3339Nano} {
		if v, err := time.Parse(layout, s); err == nil && v.Year() > 0 {
			return v.Format("2006-01-02"), fieldValid
		}
	}
	for _, layout := range []string{"2006", "2006-01"} {
		if v, err := time.Parse(layout, s); err == nil && v.Year() > 0 {
			return "", fieldUndetermined
		}
	}
	return "", fieldInvalid
}

func fileCode(state evidence.State) (string, bool) {
	switch state {
	case evidence.StateOK:
		return "", true
	case evidence.StateEmpty:
		return "file_empty", true
	case evidence.StateCorrupt:
		return "file_corrupt", true
	case evidence.StateMissing:
		return "file_missing", true
	case evidence.StateDRM:
		return "file_drm", true
	case evidence.StateUnsupported:
		return "file_unsupported", true
	case evidence.StateNotInspected:
		return "file_uninspected", true
	default:
		return "", false
	}
}
func knownReason(reason string) bool {
	switch reason {
	case "", evidence.ReasonTooLarge, evidence.ReasonSizeUnknown, evidence.ReasonSizeMismatch, evidence.ReasonNotInCalibre, evidence.ReasonMissingOnDisk, evidence.ReasonNoInspector, evidence.ReasonTimeout,
		evidence.ReasonXrefRepaired, evidence.ReasonEncrypted, evidence.ReasonHeaderOnly, evidence.ReasonHuffCDIC, evidence.ReasonZipMethod, evidence.ReasonTooManyEntries, evidence.ReasonDecompressedTooLarge, evidence.ReasonCompressionRatio, evidence.ReasonInspectorError:
		return true
	default:
		return false
	}
}
func declaredKind(format string) string {
	switch strings.TrimPrefix(strings.ToUpper(format), "ORIGINAL_") {
	case "EPUB", "KEPUB":
		return "epub"
	case "MOBI", "AZW", "AZW3", "PRC":
		return "mobi"
	case "CBZ":
		return "zip"
	case "CBR":
		return "rar"
	case "JPG", "JPEG":
		return "jpeg"
	case "PDF", "PNG", "GIF", "WEBP":
		return strings.ToLower(strings.TrimPrefix(strings.ToUpper(format), "ORIGINAL_"))
	default:
		return ""
	}
}
func formatMismatch(format, detected string) bool {
	declared := declaredKind(format)
	if declared == "" || detected == "" || detected == "unknown" {
		return false
	}
	if detected == "zip" && declared == "epub" {
		return false
	}
	if detected == "palmdoc" && declared == "mobi" {
		return false
	}
	switch detected {
	case "epub", "pdf", "zip", "mobi", "palmdoc", "rar", "jpeg", "png", "gif", "webp":
		return declared != detected
	}
	return false
}
func metadataEligible(f evidence.FormatEvidence) bool {
	if f.State != evidence.StateOK || f.Reason == evidence.ReasonEncrypted {
		return false
	}
	kind := f.Detected
	if kind == "" || kind == "unknown" || kind == "zip" {
		kind = declaredKind(f.Format)
	}
	return kind == "epub" || kind == "mobi" || kind == "palmdoc" || kind == "pdf"
}
func compareFinding(a, b evidence.Finding) int {
	for _, pair := range [][2]string{{a.Code, b.Code}, {a.Format, b.Format}, {a.Field, b.Field}, {a.Reason, b.Reason}} {
		if c := strings.Compare(pair[0], pair[1]); c != 0 {
			return c
		}
	}
	return 0
}

// Keep errors at public boundaries static; book metadata must not reach logs.
func invalidEvidence() error { return fmt.Errorf("findings: structure: %w", ErrInvalidEvidence) }
