package verify_test

import (
	"context"
	"io"
	"log/slog"
	"maps"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/sync/semaphore"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
	"github.com/felixapel/calibre-bookwarden/internal/verify/verifytest"
)

// reuseCase is what an incremental run would be offered for one book, and the
// facts it is judged against.
type reuseCase struct {
	prior    verify.Prior
	book     library.Book
	snapshot string
	key      string
	now      time.Time
}

// priorFor checks a one-format EPUB with a cover in full and returns what an
// incremental run would be offered, with the book and its snapshot.
func priorFor(t *testing.T) *reuseCase {
	t.Helper()
	b := coverBook(1, coverPNG(t, 40, 60))
	lib := verifytest.NewLibrary("library", b)
	out, err := verify.CheckBook(context.Background(), lib, "library", library.BookRef{ID: 1, UUID: b.Book.UUID}, t.TempDir(), verify.DefaultLimits,
		semaphore.NewWeighted(verify.DefaultLimits.InFlight), slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	if err != nil || out.State != verify.BookChecked {
		t.Fatalf("CheckBook = %+v, %v", out, err)
	}
	p, err := evidence.Open(out.Evidence, out.Seal)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := evidence.ExcerptRefs(p)
	if err != nil || len(refs) != 1 {
		t.Fatalf("the test package has %d excerpts, %v", len(refs), err)
	}
	book, err := lib.Book(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := evidence.Snapshot(book)
	if err != nil {
		t.Fatal(err)
	}
	key := verify.InspectKey(verify.DefaultLimits, false, "")
	if out.InspectKey != key {
		t.Fatalf("outcome key %q, want %q", out.InspectKey, key)
	}
	if out.MeasuredAt.IsZero() {
		t.Fatal("a full check does not say when it measured the files")
	}
	return &reuseCase{
		prior:    verify.Prior{Package: p, InspectKey: key, Excerpts: maps.Clone(out.Excerpts), MeasuredAt: out.MeasuredAt},
		book:     book,
		snapshot: snapshot,
		key:      key,
		now:      out.MeasuredAt.Add(time.Hour),
	}
}

func TestReusable(t *testing.T) {
	tests := []struct {
		name   string
		change func(c *reuseCase)
		want   bool
	}{
		{"nothing changed", func(*reuseCase) {}, true},
		{"measured a week ago", func(c *reuseCase) { c.now = c.prior.MeasuredAt.Add(7 * 24 * time.Hour) }, true},
		{"measured exactly at the limit", func(c *reuseCase) { c.now = c.prior.MeasuredAt.Add(verify.ReuseMaxAge) }, true},
		{"measured too long ago", func(c *reuseCase) { c.now = c.prior.MeasuredAt.Add(verify.ReuseMaxAge + time.Second) }, false},
		{"no measurement time (a package from before it was kept)", func(c *reuseCase) { c.prior.MeasuredAt = time.Time{} }, false},
		{"measured in the future", func(c *reuseCase) { c.prior.MeasuredAt = c.now.Add(time.Hour) }, false},
		{"the schema is older", func(c *reuseCase) { c.prior.Package.SchemaVersion = evidence.SchemaVersionV2 }, false},
		// Schema 5 adds the file's series, which no earlier package read: the
		// first run after the upgrade measures every file again.
		{"schema 3 file evidence", func(c *reuseCase) { c.prior.Package.SchemaVersion = evidence.SchemaVersionV3 }, false},
		{"schema 4 file evidence", func(c *reuseCase) { c.prior.Package.SchemaVersion = evidence.SchemaVersionV4 }, false},
		{"a later schema", func(c *reuseCase) { c.prior.Package.SchemaVersion = evidence.SchemaVersion + 1 }, false},
		{"the snapshot differs", func(c *reuseCase) { c.snapshot = "other" }, false},
		{"the inspector key differs", func(c *reuseCase) { c.key = "inspect=0" }, false},
		{"the package has no key", func(c *reuseCase) { c.prior.InspectKey = "" }, false},
		{"another uuid", func(c *reuseCase) { c.prior.Package.UUID = "other" }, false},
		{"another book id", func(c *reuseCase) { c.prior.Package.BookID = 99 }, false},
		{"another library", func(c *reuseCase) { c.prior.Package.LibraryID = "other" }, false},
		{"the languages changed", func(c *reuseCase) { c.book.Languages = []string{"deu"} }, false},
		{"a language was removed", func(c *reuseCase) {
			c.prior.Package.Calibre.Languages = []string{"eng"}
		}, false},
		{"a format is gone", func(c *reuseCase) { c.book.Formats = nil }, false},
		{"a format has another size", func(c *reuseCase) { c.book.Formats[0].Size++ }, false},
		{"a format is listed missing now", func(c *reuseCase) { c.book.Formats[0].Missing = true }, false},
		{"the cover is gone", func(c *reuseCase) { c.book.HasCover = false }, false},
		{"the cover is listed but was found missing", func(c *reuseCase) {
			c.prior.Package.Cover.State, c.prior.Package.Cover.Reason = evidence.StateMissing, evidence.ReasonMissingOnDisk
		}, false},
		{"the package has no cover record", func(c *reuseCase) { c.prior.Package.Cover = nil }, false},
		{"a body is missing", func(c *reuseCase) { c.prior.Excerpts = map[string]string{} }, false},
		{"a body is the wrong text", func(c *reuseCase) {
			for h := range c.prior.Excerpts {
				c.prior.Excerpts[h] += "x"
			}
		}, false},
		{"a format timed out", func(c *reuseCase) {
			c.prior.Package.Formats[0].State, c.prior.Package.Formats[0].Reason = evidence.StateNotInspected, evidence.ReasonTimeout
		}, false},
		{"a format did not match its size", func(c *reuseCase) {
			c.prior.Package.Formats[0].State, c.prior.Package.Formats[0].Reason = evidence.StateNotInspected, evidence.ReasonSizeMismatch
		}, false},
		{"an inspector failed", func(c *reuseCase) { c.prior.Package.Formats[0].Reason = evidence.ReasonInspectorError }, false},
		{"a format was not in Calibre after all", func(c *reuseCase) {
			c.prior.Package.Formats[0].State, c.prior.Package.Formats[0].Reason = evidence.StateMissing, evidence.ReasonNotInCalibre
		}, false},
		{"EPUB hints timed out", func(c *reuseCase) { c.prior.Package.Formats[0].ISBNHints.State = evidence.HintTimeout }, false},
		{"EPUB hints were unavailable", func(c *reuseCase) { c.prior.Package.Formats[0].ISBNHints.State = evidence.HintUnavailable }, false},
		{"EPUB hints failed", func(c *reuseCase) { c.prior.Package.Formats[0].ISBNHints.State = evidence.HintFailed }, false},
		{"the cover timed out", func(c *reuseCase) {
			c.prior.Package.Cover.State, c.prior.Package.Cover.Reason = evidence.StateNotInspected, evidence.ReasonTimeout
		}, false},
		{"a format is missing on disk and still is", func(c *reuseCase) {
			c.book.Formats[0].Missing = true
			c.prior.Package.Formats[0] = evidence.FormatEvidence{Format: "EPUB", Size: c.book.Formats[0].Size, State: evidence.StateMissing, Reason: evidence.ReasonMissingOnDisk}
			c.prior.Excerpts = map[string]string{}
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := priorFor(t)
			tt.change(c)
			if got := verify.Reusable(c.prior, "library", c.book, c.snapshot, c.key, c.now); got != tt.want {
				t.Errorf("Reusable() = %v, want %v", got, tt.want)
			}
		})
	}
}

// Every input of the key invalidates reuse (ADR-039), and the key says what
// it is made of.
func TestInspectKeyInputs(t *testing.T) {
	defer verify.SetBuildStamp(t, "go1.test;image=v1;text=v2")()
	base := verify.InspectKey(verify.DefaultLimits, true, "pdftotext=1;tesseract=5;tessdata=abc")
	for _, part := range []string{"inspect=", "go1.test", "image=v1", "text=v2", "pdftotext=1", "tessdata=abc", "ocr=true"} {
		if !strings.Contains(base, part) {
			t.Errorf("key %q lacks %q", base, part)
		}
	}
	smaller := verify.DefaultLimits
	smaller.Inspect.MaxPixels--
	tests := []struct {
		name string
		key  func() string
	}{
		{"a tool version", func() string {
			return verify.InspectKey(verify.DefaultLimits, true, "pdftotext=2;tesseract=5;tessdata=abc")
		}},
		{"the tessdata commit", func() string {
			return verify.InspectKey(verify.DefaultLimits, true, "pdftotext=1;tesseract=5;tessdata=def")
		}},
		{"the limits", func() string { return verify.InspectKey(smaller, true, "pdftotext=1;tesseract=5;tessdata=abc") }},
		{"no hinter", func() string {
			return verify.InspectKey(verify.DefaultLimits, false, "pdftotext=1;tesseract=5;tessdata=abc")
		}},
		{"the Go version or a module", func() string {
			defer verify.SetBuildStamp(t, "go1.test;image=v1;text=v3")()
			return verify.InspectKey(verify.DefaultLimits, true, "pdftotext=1;tesseract=5;tessdata=abc")
		}},
	}
	for _, tt := range tests {
		if got := tt.key(); got == base {
			t.Errorf("changing %s leaves the key as it was: %q", tt.name, got)
		}
	}
}

// The real stamp holds the Go version and the two modules that decode images
// and text.
func TestBuildStampNamesTheToolchain(t *testing.T) {
	got := verify.BuildStamp()
	for _, part := range []string{runtime.Version(), "golang.org/x/image=", "golang.org/x/text="} {
		if !strings.Contains(got, part) {
			t.Errorf("BuildStamp() = %q, lacks %q", got, part)
		}
	}
	if strings.Contains(got, "=unknown") {
		t.Errorf("BuildStamp() = %q: a module version is unknown in the test binary", got)
	}
}

// Other tool versions or another build leave the earlier file evidence
// unused; the same settings reuse it.
func TestIncrementalRunChecksInFullUnderOtherTools(t *testing.T) {
	tests := []struct {
		name       string
		first      verify.Options
		second     verify.Options
		secondStmp string
		wantReused int
	}{
		{"same tools", verify.Options{ToolVersions: "a"}, verify.Options{ToolVersions: "a"}, "", 3},
		{"other tool versions", verify.Options{ToolVersions: "a"}, verify.Options{ToolVersions: "b"}, "", 0},
		{"other toolchain", verify.Options{ToolVersions: "a"}, verify.Options{ToolVersions: "a"}, "go9;image=x;text=y", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lib := incrementalLibrary(t)
			store := verifytest.NewMemStore()
			runOnce(t, store, lib, tt.first, verify.Scope{})
			if tt.secondStmp != "" {
				defer verify.SetBuildStamp(t, tt.secondStmp)()
			}
			v := runOnce(t, store, lib, tt.second, verify.Scope{Incremental: true})
			if v.Progress.Checked != 3 || v.Progress.Reused != tt.wantReused {
				t.Errorf("incremental run = %+v, want %d reused", v.Progress, tt.wantReused)
			}
		})
	}
}

// File evidence expires: it is never reused more than ReuseMaxAge after it
// was measured, and reuse does not renew it (ADR-039).
func TestReuseExpires(t *testing.T) {
	lib := incrementalLibrary(t)
	store := verifytest.NewMemStore()
	t0 := time.Date(2026, 11, 2, 4, 0, 0, 0, time.UTC)
	at := func(d time.Duration) verify.Options {
		return verify.Options{Now: func() time.Time { return t0.Add(d) }}
	}

	first := runOnce(t, store, lib, at(0), verify.Scope{})
	day := 24 * time.Hour
	// Seven days on: reused, and the measurement time is carried forward.
	second := runOnce(t, store, lib, at(7*day), verify.Scope{Incremental: true})
	if second.Progress.Reused != 3 {
		t.Fatalf("after 7 days: %+v, want all reused", second.Progress)
	}
	if got := store.MeasuredAt(second.Run.ID, 1); !got.Equal(t0) {
		t.Errorf("a reused book's measured_at = %v, want the original %v", got, t0)
	}
	if got := store.MeasuredAt(first.Run.ID, 1); !got.Equal(t0) {
		t.Errorf("a measured book's measured_at = %v, want %v", got, t0)
	}
	// Nine days after the measurement, though only two after the last run:
	// measured too long ago, so checked in full.
	third := runOnce(t, store, lib, at(9*day), verify.Scope{Incremental: true})
	if third.Progress.Reused != 0 || third.Progress.Checked != 3 {
		t.Fatalf("after 9 days: %+v, want a full check of everything", third.Progress)
	}
	if got := store.MeasuredAt(third.Run.ID, 1); !got.Equal(t0.Add(9 * day)) {
		t.Errorf("a freshly measured book's measured_at = %v", got)
	}
	// And that run renews it.
	fourth := runOnce(t, store, lib, at(10*day), verify.Scope{Incremental: true})
	if fourth.Progress.Reused != 3 {
		t.Errorf("a day after the renewal: %+v, want all reused", fourth.Progress)
	}
}
