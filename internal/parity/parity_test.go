package parity_test

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/parity"
	"github.com/felixapel/calibre-bookwarden/internal/report"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

var (
	shaA = strings.Repeat("a", 64)
	shaB = strings.Repeat("b", 64)
	shaC = strings.Repeat("c", 64)
)

// fakeRun serves a run's books in id order, 2 per page, and counts loads.
type fakeRun struct {
	books map[int]report.Book
	ids   []int
	loads int
}

func newRun(books ...report.Book) *fakeRun {
	r := &fakeRun{books: map[int]report.Book{}}
	for _, b := range books {
		r.books[b.Ref.ID] = b
		r.ids = append(r.ids, b.Ref.ID)
	}
	slices.Sort(r.ids)
	return r
}

func (r *fakeRun) EachBook(_ context.Context, runID int64, visit func(report.Book) error) error {
	if runID != 7 {
		return verify.ErrNoSuchRun
	}
	for _, id := range r.ids {
		r.loads++
		b := r.books[id]
		if b.Reason == "corrupt-seal" {
			return fmt.Errorf("book %d: %w", id, report.ErrIntegrity)
		}
		if err := visit(b); err != nil {
			return err
		}
	}
	return nil
}

type fmtSpec struct {
	name, sha string
	state     evidence.State
	isbns     []string
}

func book(id int, tier string, formats ...fmtSpec) report.Book {
	p := &evidence.Package{BookID: id}
	for _, f := range formats {
		p.Formats = append(p.Formats, evidence.FormatEvidence{Format: f.name, SHA256: f.sha, Size: 10, State: f.state, Detected: strings.ToLower(f.name),
			Metadata: &evidence.FileMetadata{ISBNs: f.isbns, Limits: &evidence.MetadataLimits{Version: 1}}})
	}
	b := report.Book{Ref: library.BookRef{ID: id}, State: verify.BookChecked, Package: p}
	b.Decision.Tier = tier
	return b
}

func compare(t *testing.T, run *fakeRun, export string) ([][]string, parity.Summary) {
	t.Helper()
	a, err := parity.ReadExport(strings.NewReader(export))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	sum, err := parity.Compare(context.Background(), run, 7, a, &out)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(&out).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	return rows, sum
}

func TestParity(t *testing.T) {
	run := newRun(
		book(1, "A", fmtSpec{"EPUB", shaA, evidence.StateOK, []string{"9780000000002"}}),                                                               // agrees
		book(2, "B", fmtSpec{"EPUB", shaB, evidence.StateCorrupt, nil}),                                                                                // state differs
		book(3, "B", fmtSpec{"EPUB", shaC, evidence.StateOK, []string{"9780000000019"}}),                                                               // isbn differs
		book(4, "C", fmtSpec{"PDF", strings.Repeat("d", 64), evidence.StateCorrupt, nil}),                                                              // file changed
		book(5, "B", fmtSpec{"EPUB", strings.Repeat("e", 64), evidence.StateOK, nil}, fmtSpec{"AZW3", strings.Repeat("f", 64), evidence.StateOK, nil}), // formats
		book(6, "C", fmtSpec{"DJVU", strings.Repeat("6", 64), evidence.StateNotInspected, nil}),                                                        // unsupported ~ not_inspected
		book(9, "B"), // only bookwarden
		report.Book{Ref: library.BookRef{ID: 10}, State: verify.BookFailed, Reason: "calibre_error"}, // unchecked
	)
	export := strings.Join([]string{
		`{"book_id":1,"tier":"B","risk_flags":[],"formats":[{"format":"EPUB","sha256":"` + shaA + `","status":"readable","isbns":["978-0-00-000000-2"]}]}`,
		`{"book_id":2,"tier":"B","risk_flags":[],"formats":[{"format":"EPUB","sha256":"` + shaB + `","status":"readable","isbns":[]}]}`,
		`{"book_id":3,"tier":"B","risk_flags":[],"formats":[{"format":"EPUB","sha256":"` + shaC + `","status":"readable","isbns":["9780000000002"]}]}`,
		`{"book_id":4,"tier":"C","risk_flags":[],"formats":[{"format":"PDF","sha256":"` + shaA + `","status":"readable","isbns":["9780000000002"]}]}`,
		`{"book_id":5,"tier":"B","risk_flags":[],"formats":[{"format":"EPUB","sha256":"` + strings.Repeat("e", 64) + `","status":"readable","isbns":[]}]}`,
		`{"book_id":6,"tier":"C","risk_flags":[],"formats":[{"format":"DJVU","sha256":"` + strings.Repeat("6", 64) + `","status":"unsupported","isbns":[]}]}`,
		`{"book_id":8,"tier":"B","risk_flags":[],"formats":[]}`,
		`{"book_id":10,"tier":"B","risk_flags":[],"formats":[]}`,
	}, "\n") + "\n"
	rows, sum := compare(t, run, export)
	want := [][]string{
		{"book_id", "format", "kind", "certificate_a", "bookwarden"},
		{"2", "EPUB", "state", "readable", "corrupt"},
		{"3", "EPUB", "isbn", "9780000000002", "9780000000019"},
		{"4", "PDF", "file_changed", shaA, strings.Repeat("d", 64)},
		{"5", "", "formats", "EPUB", "AZW3 EPUB"},
		{"8", "", "only_certificate_a", "", ""},
		{"9", "", "only_bookwarden", "", ""},
		{"10", "", "unchecked", "", "failed calibre_error"},
	}
	if !slices.EqualFunc(rows, want, slices.Equal) {
		t.Errorf("rows\n got %q\nwant %q", rows, want)
	}
	if sum.Books != 9 || sum.Kinds["state"] != 1 || sum.Kinds["file_changed"] != 1 || sum.Tiers["B→A"] != 1 || sum.Tiers["C→C"] != 2 {
		t.Errorf("summary %+v", sum)
	}
}

func TestParityDuplicateFormats(t *testing.T) {
	// Two EPUBs on one book: matched by hash, so a reordering isn't a change.
	run := newRun(book(1, "B", fmtSpec{"EPUB", shaB, evidence.StateOK, nil}, fmtSpec{"EPUB", shaA, evidence.StateCorrupt, nil}))
	export := `{"book_id":1,"tier":"B","risk_flags":[],"formats":[` +
		`{"format":"EPUB","sha256":"` + shaA + `","status":"corrupt","isbns":[]},` +
		`{"format":"EPUB","sha256":"` + shaB + `","status":"readable","isbns":[]}]}` + "\n"
	if rows, _ := compare(t, run, export); len(rows) != 1 {
		t.Errorf("rows %q, want only the header", rows)
	}
}

func TestParityIntegrity(t *testing.T) {
	b := book(1, "B")
	b.Reason = "corrupt-seal"
	a, _ := parity.ReadExport(strings.NewReader(`{"book_id":1,"tier":"B","risk_flags":[],"formats":[]}` + "\n"))
	if _, err := parity.Compare(context.Background(), newRun(b), 7, a, &bytes.Buffer{}); !errors.Is(err, report.ErrIntegrity) {
		t.Fatalf("an unverifiable package: %v", err)
	}
}

func TestParityExportErrors(t *testing.T) {
	good := `{"book_id":1,"tier":"B","risk_flags":[],"formats":[]}`
	for _, tt := range []struct{ name, input, want string }{
		{"bad json", good + "\n{nope\n", "line 2"},
		{"missing id", `{"tier":"B","formats":[]}` + "\n", "line 1"},
		{"duplicate id", good + "\n" + good + "\n", "line 2"},
		{"unknown status", `{"book_id":1,"tier":"B","formats":[{"format":"EPUB","sha256":"` + shaA + `","status":"odd","isbns":[]}]}`, "line 1"},
		{"empty", "", "no books"},
	} {
		if _, err := parity.ReadExport(strings.NewReader(tt.input)); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: %v, want %q", tt.name, err, tt.want)
		}
	}
}

func TestParityStreams(t *testing.T) {
	var books []report.Book
	for id := 1; id <= 101; id++ {
		books = append(books, book(id, "B"))
	}
	run := newRun(books...)
	a, err := parity.ReadExport(strings.NewReader(`{"book_id":1,"tier":"B","risk_flags":[],"formats":[]}` + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	sum, err := parity.Compare(context.Background(), run, 7, a, &bytes.Buffer{})
	if err != nil || sum.Books != 101 || sum.Kinds["only_bookwarden"] != 100 {
		t.Fatalf("%+v, %v", sum, err)
	}
	if run.loads != 101 {
		t.Fatalf("loaded %d packages, want each once", run.loads)
	}
}

// Bookwarden's ISBNs are the file's own and its text-layer or OCR hints;
// Certificate A kept one, mostly from the text. Only a Certificate A ISBN
// bookwarden didn't find is a difference.
func TestParityISBNSources(t *testing.T) {
	hinted := book(1, "B", fmtSpec{"PDF", shaA, evidence.StateOK, nil})
	hinted.Package.Formats[0].ISBNHints = &evidence.ISBNHints{ISBNs: []string{"9780000000002"}}
	run := newRun(hinted,
		book(2, "B", fmtSpec{"EPUB", shaB, evidence.StateOK, []string{"9780000000002", "9780000000019"}}),
		book(3, "B", fmtSpec{"EPUB", shaC, evidence.StateOK, []string{"9780000000019"}}),
	)
	line := func(id int, name, sha, isbns string) string {
		return fmt.Sprintf(`{"book_id":%d,"tier":"B","risk_flags":[],"formats":[{"format":%q,"sha256":%q,"status":"readable","isbns":[%s]}]}`, id, name, sha, isbns)
	}
	export := strings.Join([]string{
		line(1, "PDF", shaA, `"9780000000002"`),
		line(2, "EPUB", shaB, `"9780000000002"`),
		line(3, "EPUB", shaC, ``),
	}, "\n")
	if rows, _ := compare(t, run, export); len(rows) != 1 {
		t.Errorf("rows %q, want only the header", rows)
	}
}

// A format bookwarden couldn't read isn't a changed file.
func TestParityUnread(t *testing.T) {
	gone := book(1, "B", fmtSpec{"EPUB", "", evidence.StateMissing, nil})
	gone.Package.Formats[0].Reason = evidence.ReasonMissingOnDisk
	export := `{"book_id":1,"tier":"B","risk_flags":[],"formats":[{"format":"EPUB","sha256":"` + shaA + `","status":"readable","isbns":[]}]}`
	rows, _ := compare(t, newRun(gone), export)
	want := [][]string{{"book_id", "format", "kind", "certificate_a", "bookwarden"}, {"1", "EPUB", "unread", "readable", "missing missing_on_disk"}}
	if !slices.EqualFunc(rows, want, slices.Equal) {
		t.Errorf("rows %q, want %q", rows, want)
	}
}

// Certificate A's risk flags explain a book it has no formats for.
func TestParityRiskFlags(t *testing.T) {
	run := newRun(book(1, "B", fmtSpec{"EPUB", shaA, evidence.StateOK, nil}))
	export := `{"book_id":1,"tier":"C","risk_flags":["pipeline_error"],"formats":[]}` + "\n" +
		`{"book_id":2,"tier":"C","risk_flags":["source_changed","pipeline_error"],"formats":[]}`
	rows, _ := compare(t, run, export)
	want := [][]string{{"book_id", "format", "kind", "certificate_a", "bookwarden"},
		{"1", "", "formats", "none: pipeline_error", "EPUB"},
		{"2", "", "only_certificate_a", "pipeline_error source_changed", ""}}
	if !slices.EqualFunc(rows, want, slices.Equal) {
		t.Errorf("rows %q, want %q", rows, want)
	}
}
