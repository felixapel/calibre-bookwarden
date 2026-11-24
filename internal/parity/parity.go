// Package parity compares a bookwarden run with Certificate A's archived
// evidence (ADR-025): per Calibre book, the formats, their bytes, states and
// embedded ISBNs. It reads only.
package parity

import (
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/findings"
	"github.com/felixapel/calibre-bookwarden/internal/report"
)

// Format is one of Certificate A's formats of a book.
type Format struct {
	Name   string   `json:"format"`
	SHA256 string   `json:"sha256"`
	Status string   `json:"status"`
	ISBNs  []string `json:"isbns"`
}

// Book is Certificate A's view of one Calibre book.
type Book struct {
	Tier      string   `json:"tier"`
	RiskFlags []string `json:"risk_flags"`
	Formats   []Format `json:"formats"`
}

// Export is Certificate A's books by Calibre book id.
type Export map[int]Book

// compatible lists the bookwarden states each Certificate A status agrees with.
var compatible = map[string][]evidence.State{
	"readable":    {evidence.StateOK},
	"corrupt":     {evidence.StateCorrupt},
	"unsupported": {evidence.StateUnsupported, evidence.StateNotInspected, evidence.StateDRM},
}

// ReadExport reads one JSON object per line. Any malformed line fails the
// whole export, with its line number.
func ReadExport(r io.Reader) (Export, error) {
	out := Export{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
	for n := 1; sc.Scan(); n++ {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var rec struct {
			BookID *int `json:"book_id"`
			Book
		}
		dec := json.NewDecoder(bytes.NewReader(line))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&rec); err != nil {
			return nil, fmt.Errorf("parity: export line %d: %w", n, err)
		}
		if rec.BookID == nil || *rec.BookID <= 0 {
			return nil, fmt.Errorf("parity: export line %d: no book_id", n)
		}
		if _, dup := out[*rec.BookID]; dup {
			return nil, fmt.Errorf("parity: export line %d: book %d is listed twice", n, *rec.BookID)
		}
		for _, f := range rec.Formats {
			if _, ok := compatible[f.Status]; !ok {
				return nil, fmt.Errorf("parity: export line %d: unknown status %q", n, f.Status)
			}
		}
		out[*rec.BookID] = rec.Book
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("parity: read export: %w", err)
	}
	if len(out) == 0 {
		return nil, errors.New("parity: the export has no books")
	}
	return out, nil
}

// BookSource is the part of report.Service a comparison reads.
type BookSource interface {
	EachBook(ctx context.Context, runID int64, visit func(report.Book) error) error
}

// Summary counts the books compared, the rows per kind, and the books per
// tier change ("B→A": Certificate A's tier, then bookwarden's).
type Summary struct {
	Books int
	Kinds map[string]int
	Tiers map[string]int
}

// Compare writes one CSV row per difference, in book id order, and returns
// the counts. It walks the run in one snapshot, loading each package once
// through the report service, so every one is seal-checked; an unverifiable
// package stops it.
func Compare(ctx context.Context, src BookSource, runID int64, a Export, w io.Writer) (Summary, error) {
	sum := Summary{Kinds: map[string]int{}, Tiers: map[string]int{}}
	cw := csv.NewWriter(w)
	row := func(id int, format, kind, certA, bw string) error {
		sum.Kinds[kind]++
		return cw.Write([]string{fmt.Sprint(id), report.CSVText(format), kind, report.CSVText(certA), report.CSVText(bw)})
	}
	if err := cw.Write([]string{"book_id", "format", "kind", "certificate_a", "bookwarden"}); err != nil {
		return Summary{}, err
	}
	onlyA := make([]int, 0, len(a))
	for id := range a {
		onlyA = append(onlyA, id)
	}
	slices.Sort(onlyA)
	seen := map[int]bool{}
	// flushA writes Certificate A's books below id that the run lacks.
	flushA := func(below int) error {
		for len(onlyA) > 0 && onlyA[0] < below {
			id := onlyA[0]
			onlyA = onlyA[1:]
			if seen[id] {
				continue
			}
			sum.Books++
			if err := row(id, "", "only_certificate_a", flags(a[id].RiskFlags), ""); err != nil {
				return err
			}
		}
		return nil
	}
	err := src.EachBook(ctx, runID, func(b report.Book) error {
		id := b.Ref.ID
		if err := flushA(id); err != nil {
			return err
		}
		seen[id] = true
		sum.Books++
		return compareBook(id, a, b, row, &sum)
	})
	if err != nil {
		return Summary{}, fmt.Errorf("parity: %w", err)
	}
	if err := flushA(int(^uint(0) >> 1)); err != nil {
		return Summary{}, err
	}
	cw.Flush()
	return sum, cw.Error()
}

func compareBook(id int, a Export, b report.Book, row func(int, string, string, string, string) error, sum *Summary) error {
	certA, inA := a[id]
	if b.Package == nil {
		return row(id, "", "unchecked", "", strings.TrimSpace(string(b.State)+" "+b.Reason))
	}
	if !inA {
		return row(id, "", "only_bookwarden", "", "")
	}
	sum.Tiers[certA.Tier+"→"+b.Decision.Tier]++
	var namesA, namesB []string
	for _, f := range certA.Formats {
		namesA = append(namesA, f.Name)
	}
	for _, f := range b.Package.Formats {
		namesB = append(namesB, f.Format)
	}
	slices.Sort(namesA)
	slices.Sort(namesB)
	if !slices.Equal(namesA, namesB) {
		// Certificate A's risk flags say why it has no formats for a book.
		certNames := strings.Join(namesA, " ")
		if len(namesA) == 0 && len(certA.RiskFlags) > 0 {
			certNames = "none: " + flags(certA.RiskFlags)
		}
		if err := row(id, "", "formats", certNames, strings.Join(namesB, " ")); err != nil {
			return err
		}
	}
	// Pair formats by bytes first, then the rest by name.
	used := make([]bool, len(b.Package.Formats))
	pair := func(match func(evidence.FormatEvidence) bool) int {
		for i, f := range b.Package.Formats {
			if !used[i] && match(f) {
				used[i] = true
				return i
			}
		}
		return -1
	}
	var changed []Format
	for _, f := range certA.Formats {
		i := pair(func(g evidence.FormatEvidence) bool { return g.SHA256 != "" && g.SHA256 == f.SHA256 })
		if i < 0 {
			changed = append(changed, f)
			continue
		}
		g := b.Package.Formats[i]
		if !slices.Contains(compatible[f.Status], g.State) {
			if err := row(id, g.Format, "state", f.Status, string(g.State)); err != nil {
				return err
			}
		}
		// Certificate A kept one ISBN per format, mostly from the text;
		// bookwarden keeps the file's own and its text-layer, OCR or EPUB
		// front-matter hints.
		// Only a Certificate A ISBN bookwarden didn't find is a difference.
		var isbnsB []string
		if g.Metadata != nil {
			isbnsB = append(isbnsB, g.Metadata.ISBNs...)
		}
		if g.ISBNHints != nil {
			isbnsB = append(isbnsB, g.ISBNHints.ISBNs...)
		}
		ia, ib := canonical(f.ISBNs), canonical(isbnsB)
		for _, isbn := range strings.Fields(ia) {
			if !slices.Contains(strings.Fields(ib), isbn) {
				if err := row(id, g.Format, "isbn", ia, ib); err != nil {
					return err
				}
				break
			}
		}
	}
	for _, f := range changed {
		i := pair(func(g evidence.FormatEvidence) bool { return g.Format == f.Name })
		if i < 0 {
			continue // the format names differ; the formats row says so
		}
		g := b.Package.Formats[i]
		// Without bytes there is no hash to differ: bookwarden couldn't
		// read the file, which the library's churn doesn't explain.
		kind, certVal, bw := "file_changed", f.SHA256, g.SHA256
		if g.SHA256 == "" {
			kind, certVal, bw = "unread", f.Status, strings.TrimSpace(string(g.State)+" "+g.Reason)
		}
		if err := row(id, f.Name, kind, certVal, bw); err != nil {
			return err
		}
	}
	return nil
}

// canonical is the sorted, unique, checksum-valid ISBN-13s, space-separated.
func canonical(raw []string) string {
	var out []string
	for _, r := range raw {
		if isbn, ok := findings.CanonicalISBN(r); ok {
			out = append(out, isbn)
		}
	}
	slices.Sort(out)
	return strings.Join(slices.Compact(out), " ")
}

// flags is a sorted, space-separated list of Certificate A's risk flags.
func flags(fs []string) string {
	out := slices.Clone(fs)
	slices.Sort(out)
	return strings.Join(out, " ")
}
