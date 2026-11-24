package verify_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/sync/semaphore"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/inspect"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
	"github.com/felixapel/calibre-bookwarden/internal/verify/verifytest"
)

type hintCall struct {
	pages      int
	textLayer  bool
	left       time.Duration
	fileExists bool
	languages  string // the Calibre record's, joined by commas
}

// recHinter records each call, charges charge to the book's budget and
// answers with state.
type recHinter struct {
	calls  []hintCall
	charge time.Duration
	state  string
}

func (h *recHinter) PDFHints(_ context.Context, path string, pages int, textLayer bool, languages []string, left *time.Duration) *evidence.ISBNHints {
	_, err := os.Stat(path)
	h.calls = append(h.calls, hintCall{pages, textLayer, *left, err == nil, strings.Join(languages, ",")})
	*left -= h.charge
	state := h.state
	if state == "" {
		state = evidence.HintOK
	}
	method := evidence.HintOCR
	if textLayer {
		method = evidence.HintTextLayer
	}
	return &evidence.ISBNHints{Method: method, State: state, Pages: []int{1}, ISBNs: []string{"9780000000040"}}
}

func inspectAs(t *testing.T, r inspect.Result) {
	t.Helper()
	verify.SetInspectFile(t, func(context.Context, string, string, inspect.Limits) (inspect.Result, error) { return r, nil })
}

func checkWith(t *testing.T, h verify.Hinter, b verifytest.FakeBook) (verify.Outcome, []evidence.FormatEvidence) {
	t.Helper()
	lib := verifytest.NewLibrary("library", b)
	out, err := verify.CheckBook(context.Background(), lib, "library", library.BookRef{ID: b.Book.ID, UUID: b.Book.UUID}, t.TempDir(), verify.DefaultLimits, semaphore.NewWeighted(1<<30), slog.New(slog.NewTextHandler(io.Discard, nil)), h)
	if err != nil {
		t.Fatal(err)
	}
	if out.State != verify.BookChecked {
		return out, nil
	}
	p, err := evidence.Open(out.Evidence, out.Seal)
	if err != nil {
		t.Fatal(err)
	}
	return out, p.Formats
}

func TestPDFHintsRecorded(t *testing.T) {
	inspectAs(t, inspect.Result{State: evidence.StateOK, Detected: "pdf", Text: evidence.TextAbsent, Pages: 12})
	h := &recHinter{}
	b := fileBook(3, "PDF", paperPDF)
	b.Book.Languages = []string{"deu", "eng"}
	_, formats := checkWith(t, h, b)
	if len(h.calls) != 1 || h.calls[0] != (hintCall{12, false, 180 * time.Second, true, "deu,eng"}) {
		t.Fatalf("calls = %+v", h.calls)
	}
	got := formats[0].ISBNHints
	if got == nil || got.Method != evidence.HintOCR || got.State != evidence.HintOK || !slices.Equal(got.ISBNs, []string{"9780000000040"}) {
		t.Fatalf("sealed hints = %+v", got)
	}
}

func TestPDFTextLayerHints(t *testing.T) {
	inspectAs(t, inspect.Result{State: evidence.StateOK, Detected: "pdf", Text: evidence.TextPresent, Pages: 3})
	h := &recHinter{}
	_, formats := checkWith(t, h, fileBook(3, "PDF", paperPDF))
	if len(h.calls) != 1 || !h.calls[0].textLayer || formats[0].ISBNHints.Method != evidence.HintTextLayer {
		t.Fatalf("calls = %+v, hints %+v", h.calls, formats[0].ISBNHints)
	}
}

func TestEPUBHintsFromTheInspector(t *testing.T) {
	scanned := &evidence.ISBNHints{
		Method: evidence.HintEPUBText, State: evidence.HintOK, Pages: []int{1, 2}, ISBNs: []string{"9780000000040"},
		Context: []evidence.ISBNContext{{ISBN: "9780000000040", Landmark: evidence.LandmarkCopyright, Qualifier: evidence.QualifierEbook}},
	}
	for _, tt := range []struct {
		name string
		res  inspect.Result
		want *evidence.ISBNHints
	}{
		{"readable", inspect.Result{State: evidence.StateOK, Detected: "epub", Text: evidence.TextPresent, ISBNHints: scanned}, scanned},
		{"not scanned", inspect.Result{State: evidence.StateOK, Detected: "epub", Text: evidence.TextPresent}, nil},
		{"detected as a zip", inspect.Result{State: evidence.StateOK, Detected: "zip", Text: evidence.TextPresent, ISBNHints: scanned}, scanned},
		{"locked", inspect.Result{State: evidence.StateDRM, Detected: "epub", Text: evidence.TextUnverified, ISBNHints: scanned}, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			inspectAs(t, tt.res)
			h := &recHinter{}
			_, formats := checkWith(t, h, fileBook(3, "EPUB", paperPDF))
			if len(h.calls) != 0 || !reflect.DeepEqual(formats[0].ISBNHints, tt.want) {
				t.Fatalf("calls %+v, hints %+v, want %+v", h.calls, formats[0].ISBNHints, tt.want)
			}
		})
	}
	// With no hinter at all, as when the tools are missing, an EPUB still has its hints.
	inspectAs(t, inspect.Result{State: evidence.StateOK, Detected: "epub", Text: evidence.TextPresent, ISBNHints: scanned})
	_, formats := checkWith(t, nil, fileBook(3, "EPUB", paperPDF))
	if !reflect.DeepEqual(formats[0].ISBNHints, scanned) {
		t.Fatalf("hints without a hinter: %+v", formats[0].ISBNHints)
	}
}

func TestHintsSkipped(t *testing.T) {
	for _, tt := range []struct {
		name, format string
		res          inspect.Result
	}{
		{"encrypted", "PDF", inspect.Result{State: evidence.StateOK, Reason: evidence.ReasonEncrypted, Detected: "pdf", Text: evidence.TextUnverified}},
		{"unverified text", "PDF", inspect.Result{State: evidence.StateOK, Detected: "pdf", Text: evidence.TextUnverified}},
		{"an EPUB", "EPUB", inspect.Result{State: evidence.StateOK, Detected: "epub", Text: evidence.TextAbsent}},
		{"corrupt", "PDF", inspect.Result{State: evidence.StateCorrupt, Detected: "pdf"}},
		{"a PDF that is an EPUB", "PDF", inspect.Result{State: evidence.StateOK, Detected: "epub", Text: evidence.TextAbsent}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			inspectAs(t, tt.res)
			h := &recHinter{}
			_, formats := checkWith(t, h, fileBook(3, tt.format, paperPDF))
			if len(h.calls) != 0 || formats[0].ISBNHints != nil {
				t.Fatalf("calls %+v, hints %+v", h.calls, formats[0].ISBNHints)
			}
		})
	}
}

func TestHintBudgetPerBook(t *testing.T) {
	inspectAs(t, inspect.Result{State: evidence.StateOK, Detected: "pdf", Text: evidence.TextAbsent, Pages: 1})
	b := fileBook(3, "PDF", paperPDF)
	b.Book.Formats = append(b.Book.Formats, library.Format{Name: "ORIGINAL_PDF", Size: int64(len(paperPDF)), MTime: t0})
	b.Files["ORIGINAL_PDF"] = paperPDF
	h := &recHinter{charge: 100 * time.Second}
	checkWith(t, h, b)
	if len(h.calls) != 2 || h.calls[0].left != 180*time.Second || h.calls[1].left != 80*time.Second {
		t.Fatalf("calls = %+v", h.calls)
	}
	checkWith(t, h, b)
	if h.calls[2].left != 180*time.Second {
		t.Fatalf("the next book started with %v", h.calls[2].left)
	}
}

func TestUnavailableHintsStillCheck(t *testing.T) {
	inspectAs(t, inspect.Result{State: evidence.StateOK, Detected: "pdf", Text: evidence.TextAbsent})
	out, formats := checkWith(t, &recHinter{state: evidence.HintUnavailable}, fileBook(3, "PDF", paperPDF))
	if out.State != verify.BookChecked || formats[0].ISBNHints.State != evidence.HintUnavailable {
		t.Fatalf("%+v %+v", out, formats)
	}
}

func TestFailedEPUBScanIsLoggedAtDebug(t *testing.T) {
	hints := func(state string) *evidence.ISBNHints {
		return &evidence.ISBNHints{Method: evidence.HintEPUBText, State: state, Pages: []int{1}, ISBNs: []string{"9780000000040"}}
	}
	for _, tt := range []struct {
		name    string
		res     inspect.Result
		wantLog bool
	}{
		{"failed", inspect.Result{State: evidence.StateOK, Detected: "epub", ISBNHints: hints(evidence.HintFailed), FailReason: inspect.FailTextBudget}, true},
		{"timeout", inspect.Result{State: evidence.StateOK, Detected: "epub", ISBNHints: hints(evidence.HintTimeout), FailReason: inspect.FailDeadline}, true},
		{"ok", inspect.Result{State: evidence.StateOK, Detected: "epub", ISBNHints: hints(evidence.HintOK)}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			inspectAs(t, tt.res)
			b := fileBook(3, "EPUB", paperPDF)
			var log bytes.Buffer
			lib := verifytest.NewLibrary("library", b)
			logger := slog.New(slog.NewJSONHandler(&log, &slog.HandlerOptions{Level: slog.LevelDebug}))
			_, err := verify.CheckBook(context.Background(), lib, "library", library.BookRef{ID: b.Book.ID, UUID: b.Book.UUID}, t.TempDir(), verify.DefaultLimits, semaphore.NewWeighted(1<<30), logger, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !tt.wantLog {
				if log.Len() > 0 {
					t.Fatalf("unexpected log: %s", log.String())
				}
				return
			}
			var rec map[string]any
			if err := json.Unmarshal(bytes.TrimSpace(log.Bytes()), &rec); err != nil {
				t.Fatalf("want one JSON record, got %q: %v", log.String(), err)
			}
			if rec["level"] != "DEBUG" || rec["book"] != float64(3) || rec["format"] != "EPUB" || rec["reason"] != tt.res.FailReason {
				t.Fatalf("record %v", rec)
			}
			if strings.Contains(log.String(), "9780000000040") {
				t.Fatalf("the log holds an ISBN: %s", log.String())
			}
		})
	}
}
