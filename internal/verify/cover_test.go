package verify_test

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"golang.org/x/sync/semaphore"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/findings"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
	"github.com/felixapel/calibre-bookwarden/internal/verify/verifytest"
)

func coverPNG(t testing.TB, w, h int) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetGray(x, y, color.Gray{Y: uint8(x * 255 / max(w-1, 1))})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func coverBook(id int, cover []byte) verifytest.FakeBook {
	b := fileBook(id, "EPUB", tidesEPUB)
	b.Book.HasCover = cover != nil
	b.Cover = cover
	return b
}

func checkCover(t *testing.T, lib *verifytest.FakeLibrary, id int, uuid string) (verify.Outcome, evidence.Package, error) {
	t.Helper()
	out, err := verify.CheckBook(context.Background(), lib, "library", library.BookRef{ID: id, UUID: uuid}, t.TempDir(), verify.DefaultLimits,
		semaphore.NewWeighted(verify.DefaultLimits.InFlight), slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	if err != nil || out.State != verify.BookChecked {
		return out, evidence.Package{}, err
	}
	p, err := findings.Open(context.Background(), out.Evidence, out.Seal)
	return out, p, err
}

func hasFinding(p evidence.Package, code, reason string) bool {
	for _, f := range p.Findings {
		if f.Code == code && f.Reason == reason {
			return true
		}
	}
	return false
}

func TestCheckCover(t *testing.T) {
	good := coverPNG(t, 800, 1200)
	for _, tt := range []struct {
		name   string
		book   verifytest.FakeBook
		setup  func(*verifytest.FakeLibrary)
		want   evidence.CoverEvidence
		code   string
		reason string
		// timeout is the per-file deadline; decoding under -race on a busy
		// runner can take longer than the timeout case's 200 ms.
		timeout time.Duration
	}{
		{name: "decoded", book: coverBook(2, good),
			want: evidence.CoverEvidence{State: evidence.StateOK, Bytes: int64(len(good)), SHA256: sha(good), Detected: "png", Width: 800, Height: 1200, DHash: "0000000000000000"}},
		{name: "none", book: coverBook(2, nil), want: evidence.CoverEvidence{State: evidence.StateMissing, Flatness: -1}, code: "cover_missing"},
		{name: "listed, not sent", book: func() verifytest.FakeBook {
			b := coverBook(2, good)
			b.CoverStatus = library.ExportNotInCalibre
			return b
		}(), want: evidence.CoverEvidence{State: evidence.StateMissing, Reason: evidence.ReasonMissingOnDisk, Flatness: -1}, code: "cover_missing", reason: "missing_on_disk"},
		{name: "too large", book: func() verifytest.FakeBook {
			b := coverBook(2, good)
			b.CoverStatus = library.ExportTooLarge
			return b
		}(), want: evidence.CoverEvidence{State: evidence.StateNotInspected, Reason: evidence.ReasonTooLarge, Flatness: -1}, code: "cover_uninspected", reason: "too_large"},
		{name: "corrupt", book: coverBook(2, []byte("not an image")),
			want: evidence.CoverEvidence{State: evidence.StateCorrupt, Bytes: 12, SHA256: sha([]byte("not an image")), Flatness: -1}, code: "cover_corrupt"},
		{name: "timeout", book: coverBook(2, good), setup: func(l *verifytest.FakeLibrary) {
			l.SetCoverDelay(time.Second)
		}, timeout: 200 * time.Millisecond, want: evidence.CoverEvidence{State: evidence.StateNotInspected, Reason: evidence.ReasonTimeout, Flatness: -1}, code: "cover_uninspected", reason: "timeout"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			timeout := tt.timeout
			if timeout == 0 {
				timeout = 30 * time.Second
			}
			verify.SetFormatTimeout(t, timeout)
			lib := verifytest.NewLibrary("library", tt.book)
			if tt.setup != nil {
				tt.setup(lib)
			}
			_, p, err := checkCover(t, lib, 2, tt.book.Book.UUID)
			if err != nil || p.Cover == nil {
				t.Fatalf("check: %v, cover %+v", err, p.Cover)
			}
			got := *p.Cover
			if tt.want.State == evidence.StateOK {
				if got.Flatness < 0 {
					t.Errorf("no flatness: %+v", got)
				}
				got.Flatness = 0
			}
			if got != tt.want {
				t.Errorf("cover\n got %+v\nwant %+v", got, tt.want)
			}
			if tt.code != "" && !hasFinding(p, tt.code, tt.reason) {
				t.Errorf("no %s/%s in %+v", tt.code, tt.reason, p.Findings)
			}
			if !tt.book.Book.HasCover && lib.ExportCalls(2, "cover") != 0 {
				t.Error("a book without a cover was asked for one")
			}
		})
	}
}

func TestCoverUnreachable(t *testing.T) {
	b := coverBook(2, coverPNG(t, 10, 10))
	lib := verifytest.NewLibrary("library", b)
	lib.SetExportError(func(_ int, format string) error {
		if format == "cover" {
			return library.ErrUnreachable
		}
		return nil
	})
	if _, _, err := checkCover(t, lib, 2, b.Book.UUID); !errors.Is(err, library.ErrUnreachable) {
		t.Fatalf("unreachable during the cover = %v", err)
	}
}

// A cover replaced while it is exported moves last_modified, which the
// check's second read must see: the cover is exported between the reads.
func TestCoverChangedMidCheck(t *testing.T) {
	b := coverBook(2, coverPNG(t, 800, 1200))
	lib := verifytest.NewLibrary("library", b)
	lib.OnCoverExport(2, func(b *library.Book) { b.LastModified = b.LastModified.Add(time.Second) })
	out, _, err := checkCover(t, lib, 2, b.Book.UUID)
	if err != nil || out.State != verify.BookSourceChanged || out.Seal != "" {
		t.Fatalf("a cover changed mid-check was sealed: state %s, %v", out.State, err)
	}
}

func TestCoverExportCountsAgainstBudget(t *testing.T) {
	const mib = 1 << 20
	a, b := coverBook(2, coverPNG(t, 20, 20)), coverBook(3, coverPNG(t, 20, 20))
	b.Book.UUID = "uuid-other"
	lib := verifytest.NewLibrary("library", a, b)
	lib.SetCoverDelay(50 * time.Millisecond)
	lim := verify.DefaultLimits
	lim.FileSize, lim.InFlight = mib, mib
	budget := semaphore.NewWeighted(mib)
	var wg sync.WaitGroup
	for _, fb := range []verifytest.FakeBook{a, b} {
		wg.Go(func() {
			out, err := verify.CheckBook(context.Background(), lib, "library", library.BookRef{ID: fb.Book.ID, UUID: fb.Book.UUID}, t.TempDir(), lim, budget,
				slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
			if err != nil || out.State != verify.BookChecked {
				t.Errorf("book %d: %+v, %v", fb.Book.ID, out, err)
			}
		})
	}
	wg.Wait()
	if got := lib.MaxConcurrentExportBytes(); got > mib {
		t.Fatalf("%d bytes in flight, budget %d", got, mib)
	}
}
