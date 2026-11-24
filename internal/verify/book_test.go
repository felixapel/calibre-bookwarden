package verify_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/sync/semaphore"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/findings"
	"github.com/felixapel/calibre-bookwarden/internal/inspect"
	"github.com/felixapel/calibre-bookwarden/internal/inspect/inspecttest"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
	"github.com/felixapel/calibre-bookwarden/internal/verify/verifytest"
)

var t0 = time.Date(2026, 10, 12, 21, 5, 55, 0, time.UTC)

func shaHex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func epubBook(id int, size int64, content string) verifytest.FakeBook {
	return verifytest.FakeBook{
		Book: library.Book{
			ID: id, UUID: "uuid-" + string(rune('a'+id%26)), Title: "Book", LastModified: t0,
			Formats: []library.Format{{Name: "EPUB", Size: size, MTime: t0}},
		},
		Files: map[string][]byte{"EPUB": []byte(content)},
	}
}

// fileBook is a book with one format holding content.
func fileBook(id int, format string, content []byte) verifytest.FakeBook {
	b := epubBook(id, int64(len(content)), "")
	b.Book.Formats[0].Name = format
	b.Files = map[string][]byte{format: content}
	return b
}

var (
	tidesEPUB = inspecttest.EPUB(inspecttest.EPUBOptions{
		Title: "Binary Tides", Authors: []string{"Cy Placeholder"}, Language: "en", ISBN: "9780000000026",
		Text: "Two formats carry the same generated evidence.",
	})
	tidesMeta = &evidence.FileMetadata{
		Limits: &evidence.MetadataLimits{Version: 1},
		Title:  "Binary Tides", Authors: []string{"Cy Placeholder"}, Languages: []string{"en"}, ISBNs: []string{"9780000000026"},
	}
	lockedEPUB  = inspecttest.EPUB(inspecttest.EPUBOptions{Title: "Locked", Text: "x", Rights: true})
	paperPDF    = inspecttest.PDF(inspecttest.PDFOptions{Info: map[string]string{"Title": "Paper Trail"}, Text: "A tiny generated book."})
	hostileEPUB = inspecttest.EPUB(inspecttest.EPUBOptions{Title: "Bad\x00Title\xff" + strings.Repeat("x", 2000), Text: "x"})
)

func sha(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// oneDocScanned is the front-matter hints of a one-chapter EPUB with no ISBN
// in its text.
func oneDocScanned() *evidence.ISBNHints {
	return &evidence.ISBNHints{Method: evidence.HintEPUBText, State: evidence.HintOK, Pages: []int{1}}
}

// oneDocExcerpt is the sealed excerpt of a one-chapter EPUB whose text is
// text: one unit on spine position 1, which no landmark labels.
func oneDocExcerpt(text string) *evidence.Excerpt {
	n := len([]rune(text))
	return &evidence.Excerpt{
		Method: evidence.ExcerptEPUBText, Units: []evidence.ExcerptUnit{{Pos: 1, Landmark: evidence.LandmarkFront, Start: 0, End: n}},
		SHA256: shaHex(text), Chars: n, Complete: true,
	}
}

func TestCheckBook(t *testing.T) {
	const id = 7
	tests := []struct {
		name        string
		book        verifytest.FakeBook
		setup       func(t *testing.T, lib *verifytest.FakeLibrary)
		refUUID     string // "" means the book's own uuid
		wantState   verify.BookState
		wantReason  string
		wantFormats []evidence.FormatEvidence
		wantErr     error
		after       func(t *testing.T, lib *verifytest.FakeLibrary)
		wantLog     func(t *testing.T, log string)
	}{
		{
			name: "one EPUB", book: fileBook(id, "EPUB", tidesEPUB), wantState: verify.BookChecked,
			wantFormats: []evidence.FormatEvidence{{
				Format: "EPUB", Size: int64(len(tidesEPUB)), SHA256: sha(tidesEPUB), State: evidence.StateOK,
				Detected: "epub", Text: evidence.TextPresent, Metadata: tidesMeta, ISBNHints: oneDocScanned(),
				Excerpt: oneDocExcerpt("Two formats carry the same generated evidence."),
			}},
		},
		{
			name: "four bytes that aren't an EPUB", book: epubBook(id, 4, "abcd"), wantState: verify.BookChecked,
			wantFormats: []evidence.FormatEvidence{{Format: "EPUB", Size: 4, SHA256: shaHex("abcd"), State: evidence.StateCorrupt, Detected: "unknown"}},
		},
		{
			name: "an EPUB with Adobe DRM", book: fileBook(id, "EPUB", lockedEPUB), wantState: verify.BookChecked,
			wantFormats: []evidence.FormatEvidence{{
				Format: "EPUB", Size: int64(len(lockedEPUB)), SHA256: sha(lockedEPUB), State: evidence.StateDRM,
				Detected: "epub", Text: evidence.TextUnverified, Metadata: &evidence.FileMetadata{Limits: &evidence.MetadataLimits{Version: 1}, Title: "Locked"},
			}},
		},
		{
			name: "a PDF stored as EPUB", book: fileBook(id, "EPUB", paperPDF), wantState: verify.BookChecked,
			wantFormats: []evidence.FormatEvidence{{
				Format: "EPUB", Size: int64(len(paperPDF)), SHA256: sha(paperPDF), State: evidence.StateOK,
				Detected: "pdf", Text: evidence.TextPresent, Metadata: &evidence.FileMetadata{Limits: &evidence.MetadataLimits{Version: 1}, Title: "Paper Trail"},
			}},
		},
		{
			name: "hostile metadata", book: fileBook(id, "EPUB", hostileEPUB), wantState: verify.BookChecked,
			wantFormats: []evidence.FormatEvidence{{
				Format: "EPUB", Size: int64(len(hostileEPUB)), SHA256: sha(hostileEPUB), State: evidence.StateOK,
				Detected: "epub", Text: evidence.TextPresent, Metadata: &evidence.FileMetadata{Limits: &evidence.MetadataLimits{Version: 1, Title: true}, Title: "BadTitle\uFFFD" + strings.Repeat("x", 503)},
				ISBNHints: oneDocScanned(), Excerpt: oneDocExcerpt("x"),
			}},
		},
		{
			name: "inspection too slow", book: fileBook(id, "EPUB", tidesEPUB),
			setup: func(t *testing.T, _ *verifytest.FakeLibrary) {
				verify.SetFormatTimeout(t, 30*time.Millisecond)
				verify.SetInspectFile(t, func(ctx context.Context, _, _ string, _ inspect.Limits) (inspect.Result, error) {
					<-ctx.Done() // the deadline reached inspection, or the test hangs
					return inspect.Result{State: evidence.StateNotInspected, Reason: evidence.ReasonTimeout}, nil
				})
			},
			wantState: verify.BookChecked,
			wantFormats: []evidence.FormatEvidence{{
				Format: "EPUB", Size: int64(len(tidesEPUB)), SHA256: sha(tidesEPUB), State: evidence.StateNotInspected, Reason: evidence.ReasonTimeout,
			}},
		},
		{
			name: "an inspector that panicked", book: fileBook(id, "PDF", paperPDF),
			setup: func(t *testing.T, _ *verifytest.FakeLibrary) {
				verify.SetInspectFile(t, func(context.Context, string, string, inspect.Limits) (inspect.Result, error) {
					return inspect.Result{State: evidence.StateNotInspected, Reason: evidence.ReasonInspectorError, Detected: "pdf", Panic: "boom\ngoroutine 1 [running]:"}, nil
				})
			},
			wantState: verify.BookChecked,
			wantFormats: []evidence.FormatEvidence{{
				Format: "PDF", Size: int64(len(paperPDF)), SHA256: sha(paperPDF), State: evidence.StateNotInspected, Reason: evidence.ReasonInspectorError, Detected: "pdf",
			}},
			wantLog: func(t *testing.T, log string) {
				lines := strings.Split(strings.TrimSpace(log), "\n")
				if len(lines) != 1 || !strings.Contains(lines[0], `"level":"ERROR"`) || !strings.Contains(lines[0], `"book":7`) ||
					!strings.Contains(lines[0], `"format":"PDF"`) || !strings.Contains(lines[0], "boom") || strings.Contains(lines[0], "Paper Trail") {
					t.Errorf("log = %s, want one ERROR line with book 7, format PDF and the panic, and no title", log)
				}
			},
		},
		{
			name: "an inspector that can't open the file", book: fileBook(id, "EPUB", tidesEPUB),
			setup: func(t *testing.T, _ *verifytest.FakeLibrary) {
				verify.SetInspectFile(t, func(context.Context, string, string, inspect.Limits) (inspect.Result, error) {
					return inspect.Result{}, errors.New("inspect: open: no such file")
				})
			},
			wantState: verify.BookFailed, wantReason: "scratch_error",
		},
		{
			name: "no formats", book: func() verifytest.FakeBook {
				b := epubBook(id, 0, "")
				b.Book.Formats = []library.Format{}
				return b
			}(),
			wantState: verify.BookChecked, wantFormats: []evidence.FormatEvidence{},
		},
		{
			name: "too large", book: epubBook(id, 600<<20, "abcd"), wantState: verify.BookChecked,
			wantFormats: []evidence.FormatEvidence{{Format: "EPUB", Size: 600 << 20, State: evidence.StateNotInspected, Reason: evidence.ReasonTooLarge}},
			after: func(t *testing.T, lib *verifytest.FakeLibrary) {
				if n := lib.ExportCalls(id, "EPUB"); n != 0 {
					t.Errorf("a 600 MiB format was requested %d times", n)
				}
			},
		},
		{
			name: "size unknown", book: epubBook(id, -1, "abcd"), wantState: verify.BookChecked,
			wantFormats: []evidence.FormatEvidence{{Format: "EPUB", Size: -1, State: evidence.StateNotInspected, Reason: evidence.ReasonSizeUnknown}},
			after: func(t *testing.T, lib *verifytest.FakeLibrary) {
				if n := lib.ExportCalls(id, "EPUB"); n != 0 {
					t.Errorf("a format of unknown size was requested %d times", n)
				}
			},
		},
		{
			name: "not in Calibre", book: func() verifytest.FakeBook {
				b := epubBook(id, 4, "abcd")
				b.Status = map[string]library.ExportStatus{"EPUB": library.ExportNotInCalibre}
				return b
			}(),
			wantState:   verify.BookChecked,
			wantFormats: []evidence.FormatEvidence{{Format: "EPUB", Size: 4, State: evidence.StateMissing, Reason: evidence.ReasonNotInCalibre}},
		},
		{
			name: "missing on disk", book: func() verifytest.FakeBook {
				b := epubBook(id, 2326, "")
				b.Status = map[string]library.ExportStatus{"EPUB": library.ExportMissingOnDisk}
				return b
			}(),
			wantState:   verify.BookChecked,
			wantFormats: []evidence.FormatEvidence{{Format: "EPUB", Size: 2326, State: evidence.StateMissing, Reason: evidence.ReasonMissingOnDisk}},
		},
		{
			name: "file gone before Calibre started", book: func() verifytest.FakeBook {
				b := epubBook(id, -1, "")
				b.Book.Formats[0].Missing = true
				return b
			}(),
			wantState:   verify.BookChecked,
			wantFormats: []evidence.FormatEvidence{{Format: "EPUB", Size: -1, State: evidence.StateMissing, Reason: evidence.ReasonMissingOnDisk}},
			after: func(t *testing.T, lib *verifytest.FakeLibrary) {
				if n := lib.ExportCalls(id, "EPUB"); n != 0 {
					t.Errorf("a format Calibre can't find was requested %d times", n)
				}
			},
		},
		{
			name: "empty file", book: epubBook(id, 0, ""), wantState: verify.BookChecked,
			wantFormats: []evidence.FormatEvidence{{Format: "EPUB", Size: 0, SHA256: shaHex(""), State: evidence.StateEmpty}},
		},
		{
			name: "size mismatch", book: epubBook(id, 10, "abcd"), wantState: verify.BookChecked,
			wantFormats: []evidence.FormatEvidence{{Format: "EPUB", Size: 10, State: evidence.StateNotInspected, Reason: evidence.ReasonSizeMismatch}},
		},
		{
			name: "edited during the check", book: epubBook(id, 4, "abcd"),
			setup: func(_ *testing.T, lib *verifytest.FakeLibrary) {
				lib.MutateOnRead(id, 2, func(b *library.Book) bool {
					b.LastModified = b.LastModified.Add(time.Second)
					return false
				})
			},
			wantState: verify.BookSourceChanged, wantReason: "changed",
		},
		{
			name: "deleted during the check", book: epubBook(id, 4, "abcd"),
			setup: func(_ *testing.T, lib *verifytest.FakeLibrary) {
				lib.MutateOnRead(id, 2, func(*library.Book) bool { return true })
			},
			wantState: verify.BookSourceChanged, wantReason: "deleted",
		},
		{
			name: "deleted before the check", book: epubBook(id, 4, "abcd"),
			setup: func(_ *testing.T, lib *verifytest.FakeLibrary) {
				lib.MutateOnRead(id, 1, func(*library.Book) bool { return true })
			},
			wantState: verify.BookSourceChanged, wantReason: "deleted",
		},
		{
			name: "uuid changed", book: epubBook(id, 4, "abcd"), refUUID: "another-uuid",
			wantState: verify.BookSourceChanged, wantReason: "uuid_changed",
			after: func(t *testing.T, lib *verifytest.FakeLibrary) {
				if n := lib.ExportCalls(id, "EPUB"); n != 0 {
					t.Errorf("a different book was exported %d times", n)
				}
			},
		},
		{
			name: "Calibre goes away", book: epubBook(id, 4, "abcd"),
			setup: func(_ *testing.T, lib *verifytest.FakeLibrary) {
				lib.SetExportError(func(int, string) error { return library.ErrUnreachable })
			},
			wantErr: library.ErrUnreachable,
		},
		{
			name: "Calibre refuses the credentials", book: epubBook(id, 4, "abcd"),
			setup: func(_ *testing.T, lib *verifytest.FakeLibrary) {
				lib.SetExportError(func(int, string) error { return library.ErrAuth })
			},
			wantErr: library.ErrAuth,
		},
		{
			name: "unsealable text", book: func() verifytest.FakeBook {
				b := epubBook(id, 4, "abcd")
				b.Book.Title = "a\x00b"
				return b
			}(),
			wantState: verify.BookFailed, wantReason: "unsealable",
		},
		{
			name: "Calibre error", book: epubBook(id, 4, "abcd"),
			setup: func(_ *testing.T, lib *verifytest.FakeLibrary) {
				lib.SetExportError(func(int, string) error {
					return &library.CalibreError{Command: "export", Message: "boom"}
				})
			},
			wantState: verify.BookFailed, wantReason: "calibre_error",
		},
		{
			name: "export too slow", book: epubBook(id, 4, "abcd"),
			setup: func(t *testing.T, lib *verifytest.FakeLibrary) {
				verify.SetFormatTimeout(t, 20*time.Millisecond)
				lib.SetExportDelay(time.Second)
			},
			wantState:   verify.BookChecked,
			wantFormats: []evidence.FormatEvidence{{Format: "EPUB", Size: 4, State: evidence.StateNotInspected, Reason: evidence.ReasonTimeout}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lib := verifytest.NewLibrary("library", tt.book)
			if tt.setup != nil {
				tt.setup(t, lib)
			}
			ref := library.BookRef{ID: id, UUID: tt.book.Book.UUID}
			if tt.refUUID != "" {
				ref.UUID = tt.refUUID
			}
			scratch := t.TempDir()
			var log bytes.Buffer
			out, err := verify.CheckBook(context.Background(), lib, "library", ref, scratch, verify.DefaultLimits, semaphore.NewWeighted(verify.DefaultLimits.InFlight), slog.New(slog.NewJSONHandler(&log, nil)), nil)
			if tt.wantLog != nil {
				tt.wantLog(t, log.String())
			} else if log.Len() > 0 {
				t.Errorf("unexpected log: %s", log.String())
			}

			if left, _ := os.ReadDir(scratch); len(left) != 0 {
				t.Errorf("scratch holds %d entries after the check", len(left))
			}
			if tt.after != nil {
				tt.after(t, lib)
			}
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if out.State != tt.wantState || out.Reason != tt.wantReason {
				t.Fatalf("outcome %s/%q, want %s/%q", out.State, out.Reason, tt.wantState, tt.wantReason)
			}
			if out.State != verify.BookChecked {
				if out.Evidence != nil || out.Seal != "" {
					t.Errorf("a %s book carries evidence", out.State)
				}
				return
			}
			p, err := evidence.Open(out.Evidence, out.Seal)
			if err != nil {
				t.Fatalf("evidence doesn't open: %v", err)
			}
			if p.BookID != id || p.UUID != ref.UUID || p.LibraryID != "library" || p.SchemaVersion != evidence.SchemaVersion {
				t.Errorf("package identity = %d/%s/%s/v%d", p.BookID, p.UUID, p.LibraryID, p.SchemaVersion)
			}
			if !reflect.DeepEqual(p.Formats, tt.wantFormats) {
				t.Errorf("formats\n got %+v\nwant %+v", p.Formats, tt.wantFormats)
			}
		})
	}
}

func TestCheckBookFormatsInNameOrder(t *testing.T) {
	b := epubBook(7, 4, "abcd")
	b.Book.Formats = []library.Format{{Name: "PDF", Size: 3, MTime: t0}, {Name: "EPUB", Size: 4, MTime: t0}}
	b.Files["PDF"] = []byte("pdf")
	lib := verifytest.NewLibrary("library", b)
	out, err := verify.CheckBook(context.Background(), lib, "library", library.BookRef{ID: 7, UUID: b.Book.UUID}, t.TempDir(), verify.DefaultLimits, semaphore.NewWeighted(1<<30), slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	if err != nil {
		t.Fatal(err)
	}
	p, err := evidence.Open(out.Evidence, out.Seal)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Formats) != 2 || p.Formats[0].Format != "EPUB" || p.Formats[1].Format != "PDF" {
		t.Errorf("formats = %+v, want EPUB then PDF", p.Formats)
	}
}

func TestCheckBookFindings(t *testing.T) {
	for _, id := range []int{2, 5} {
		t.Run(string(rune('0'+id)), func(t *testing.T) {
			b := fileBook(id, "EPUB", tidesEPUB)
			if id == 5 {
				delete(b.Files, "EPUB")
			}
			lib := verifytest.NewLibrary("library", b)
			out, err := verify.CheckBook(context.Background(), lib, "library", library.BookRef{ID: id, UUID: b.Book.UUID}, t.TempDir(), verify.DefaultLimits, semaphore.NewWeighted(1<<30), slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
			if err != nil || out.State != verify.BookChecked {
				t.Fatalf("%+v/%v", out, err)
			}
			p, err := findings.Open(context.Background(), out.Evidence, out.Seal)
			if err != nil || p.FindingsVersion != findings.Version {
				t.Fatalf("findings not sealed: %v", err)
			}
			want := "title_mismatch"
			if id == 5 {
				want = "file_missing"
			}
			found := false
			for _, f := range p.Findings {
				if f.Code == want && f.Format == "EPUB" {
					found = true
				}
			}
			if !found {
				t.Fatalf("missing %s in %+v", want, p.Findings)
			}
		})
	}
	b := fileBook(2, "EPUB", tidesEPUB)
	lib := verifytest.NewLibrary("library", b)
	lib.MutateOnRead(2, 2, func(b *library.Book) bool { b.LastModified = b.LastModified.Add(time.Second); return false })
	out, err := verify.CheckBook(context.Background(), lib, "library", library.BookRef{ID: 2, UUID: b.Book.UUID}, t.TempDir(), verify.DefaultLimits, semaphore.NewWeighted(1<<30), slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	if err != nil || out.State != verify.BookSourceChanged || len(out.Evidence) != 0 || out.Seal != "" {
		t.Fatalf("changed book has findings: %+v/%v", out, err)
	}
}
