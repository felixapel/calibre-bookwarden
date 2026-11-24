package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/inspect"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
	"github.com/felixapel/calibre-bookwarden/internal/verify/verifytest"
)

const leakyPath = "/library/Hidden Author/Hidden Calibre Title (123)/Hidden Calibre Title - Hidden Author.epub"

func TestInspectCommandNeverPrintsCalibreErrors(t *testing.T) {
	for _, tc := range []struct {
		name     string
		fail     string // the format whose export fails
		err      error
		wantCode int
		want     []string // in stdout+stderr
	}{
		{
			name: "a calibre error on the last format keeps the earlier one", fail: "PDF",
			err:      &library.CalibreError{Command: "export", Message: "no such file " + leakyPath},
			wantCode: 0, want: []string{"format EPUB", "state=ok", "format PDF", "reason=calibre_error"},
		},
		{
			name: "a calibre error on the first format goes on to the next", fail: "EPUB",
			err:      fmt.Errorf("wrapped: %w", &library.CalibreError{Command: "export", Message: leakyPath}),
			wantCode: 0, want: []string{"format EPUB", "reason=calibre_error", "format PDF", "kind=pdf"},
		},
		{
			name: "unreachable", fail: "PDF", err: fmt.Errorf("GET %s: %w", leakyPath, library.ErrUnreachable),
			wantCode: 1, want: []string{"calibre_unreachable"},
		},
		{
			name: "anything else", fail: "PDF", err: errors.New("disk exploded at " + leakyPath),
			wantCode: 1, want: []string{"export_error"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scratch := t.TempDir()
			lib := verifytest.NewLibrary("library", inspectBook())
			lib.SetExportError(func(_ int, format string) error {
				if format == tc.fail {
					return tc.err
				}
				return nil
			})
			code, out, errOut := runInspectCmd(t, lib, scratch, "--book", "123")
			if code != tc.wantCode {
				t.Fatalf("exit %d, want %d:\n%s%s", code, tc.wantCode, out, errOut)
			}
			all := out + errOut
			for _, want := range tc.want {
				if !strings.Contains(all, want) {
					t.Errorf("output lacks %q:\n%s", want, all)
				}
			}
			for _, leak := range []string{"Hidden Calibre Title", "Hidden Author", "/library/", ".epub"} {
				if strings.Contains(all, leak) {
					t.Errorf("output holds %q:\n%s", leak, all)
				}
			}
			if left, _ := os.ReadDir(scratch); len(left) != 0 {
				t.Errorf("temp dir holds %d entries", len(left))
			}
		})
	}
}

func TestInspectCommandFileSizeCap(t *testing.T) {
	big := inspectBook()
	big.Book.Formats = []library.Format{{Name: "EPUB", Size: 70 << 20, MTime: big.Book.LastModified}}
	big.Files = map[string][]byte{"EPUB": []byte("short")}

	lib := verifytest.NewLibrary("library", big)
	code, out, errOut := runInspectCmd(t, lib, t.TempDir(), "--book", "123")
	if code != 0 || !strings.Contains(out, "reason=too_large") || lib.ExportCalls(123, "EPUB") != 0 {
		t.Errorf("without --large: exit %d, exports %d:\n%s%s", code, lib.ExportCalls(123, "EPUB"), out, errOut)
	}
	lib = verifytest.NewLibrary("library", big)
	code, out, errOut = runInspectCmd(t, lib, t.TempDir(), "--book", "123", "--large")
	if code != 0 || strings.Contains(out, "too_large") || lib.ExportCalls(123, "EPUB") != 1 {
		t.Errorf("with --large: exit %d, exports %d:\n%s%s", code, lib.ExportCalls(123, "EPUB"), out, errOut)
	}
}

func TestInspectCommandRefusesWithoutFreeSpace(t *testing.T) {
	book := inspectBook()
	size := book.Book.Formats[0].Size // the EPUB
	for _, tc := range []struct {
		name    string
		free    uint64
		refused bool
	}{
		{"exactly twice the size", uint64(2 * size), false},
		{"one byte short of twice the size", uint64(2*size - 1), true},
		{"none", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := freeSpace
			freeSpace = func(string) (uint64, error) { return tc.free, nil }
			t.Cleanup(func() { freeSpace = old })
			lib := verifytest.NewLibrary("library", book)
			code, out, errOut := runInspectCmd(t, lib, t.TempDir(), "--book", "123", "--format", "EPUB")
			exports := lib.ExportCalls(123, "EPUB")
			if tc.refused {
				if code != 0 || exports != 0 || !strings.Contains(out, "reason=insufficient_space") || !strings.Contains(errOut, "not enough free space") {
					t.Errorf("exit %d, exports %d:\n%s%s", code, exports, out, errOut)
				}
			} else if exports != 1 || strings.Contains(out, "insufficient_space") {
				t.Errorf("exports %d:\n%s%s", exports, out, errOut)
			}
		})
	}
	t.Run("the space check failing refuses too", func(t *testing.T) {
		old := freeSpace
		freeSpace = func(string) (uint64, error) { return 0, errors.New("statfs: " + leakyPath) }
		t.Cleanup(func() { freeSpace = old })
		lib := verifytest.NewLibrary("library", book)
		code, out, errOut := runInspectCmd(t, lib, t.TempDir(), "--book", "123", "--format", "EPUB")
		if code != 0 || lib.ExportCalls(123, "EPUB") != 0 || !strings.Contains(out, "reason=insufficient_space") || strings.Contains(out+errOut, "Hidden") {
			t.Errorf("exit %d:\n%s%s", code, out, errOut)
		}
	})
}

func TestInspectPrintsConfigurationWarnings(t *testing.T) {
	env := map[string]string{
		"BOOKWARDEN_CALIBRE_URL": "http://127.0.0.1:1", "BOOKWARDEN_CALIBRE_USER": "u", "BOOKWARDEN_CALIBRE_PASSWORD": "  pw  \n",
	}
	var out, errb bytes.Buffer
	code := run(context.Background(), []string{"inspect", "--book", "5", "--timeout", "2s"}, func(k string) string { return env[k] }, strings.NewReader(""), &out, &errb)
	if code != 1 || !strings.Contains(errb.String(), "starts or ends with a space") || !strings.Contains(errb.String(), "calibre_unreachable") || strings.Contains(errb.String(), "pw") {
		t.Errorf("exit %d: %s", code, errb.String())
	}
}

func TestNeededFreeSpace(t *testing.T) {
	for size, want := range map[int64]uint64{0: 0, 1 << 20: 2 << 20, -1: 256 << 20} {
		if got := neededFreeSpace(size); got != want {
			t.Errorf("neededFreeSpace(%d) = %d, want %d", size, got, want)
		}
	}
}

func TestInspectRemovesStaleTempDirs(t *testing.T) {
	root := t.TempDir()
	old := time.Now().Add(-2 * time.Hour)
	mk := func(name string, mtime time.Time) string {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "format"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
		return p
	}
	stale := mk("bookwarden-inspect-111", old)
	fresh := mk("bookwarden-inspect-222", time.Now().Add(-5*time.Minute))
	other := mk("bookwarden-scratch", old)           // the runner's, never ours
	lookalike := mk("not-bookwarden-inspect-1", old) // a prefix match is not enough
	if err := os.WriteFile(filepath.Join(root, "bookwarden-inspect-file"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cleanStaleInspectDirs(root, time.Now())
	for p, wantGone := range map[string]bool{stale: true, fresh: false, other: false, lookalike: false} {
		_, err := os.Stat(p)
		if gone := errors.Is(err, os.ErrNotExist); gone != wantGone {
			t.Errorf("%s: gone = %v, want %v", filepath.Base(p), gone, wantGone)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "bookwarden-inspect-file")); err != nil {
		t.Errorf("a plain file was removed: %v", err)
	}
}

func TestInspectCleansStaleDirsAtStart(t *testing.T) {
	scratch := t.TempDir()
	stale := filepath.Join(scratch, "bookwarden-inspect-killed")
	if err := os.Mkdir(stale, 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-3 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := runInspectCmd(t, verifytest.NewLibrary("library", inspectBook()), scratch, "--book", "123"); code != 0 {
		t.Fatal(errOut)
	}
	if left, _ := os.ReadDir(scratch); len(left) != 0 {
		t.Errorf("temp dir holds %d entries", len(left))
	}
}

// The timeout bounds the inspection as well as the export.
func TestInspectTimeoutBoundsTheInspector(t *testing.T) {
	old := inspectFile
	inspectFile = func(ctx context.Context, _, _ string, _ inspect.Limits) (inspect.Result, error) {
		<-ctx.Done() // a stuck inspector, freed only by the format's deadline
		return inspect.Result{State: evidence.StateNotInspected, Reason: evidence.ReasonTimeout, FailReason: inspect.FailDeadline}, nil
	}
	t.Cleanup(func() { inspectFile = old })
	type result struct {
		code     int
		out, err string
	}
	done := make(chan result, 1)
	go func() {
		code, out, errOut := runInspectCmd(t, verifytest.NewLibrary("library", inspectBook()), t.TempDir(), "--book", "123", "--format", "EPUB", "--timeout", "100ms")
		done <- result{code, out, errOut}
	}()
	select {
	case r := <-done:
		if r.code != 0 || !strings.Contains(r.out, "state=not_inspected") || !strings.Contains(r.out, "reason=timeout") {
			t.Errorf("exit %d:\n%s%s", r.code, r.out, r.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the inspector ran past --timeout: it was given the command's context, not the format's")
	}
}

// An excerpt that verify wouldn't seal is not shown either.
func TestInspectDropsAnExcerptThatWouldNotBeSealed(t *testing.T) {
	old := inspectFile
	t.Cleanup(func() { inspectFile = old })
	for _, tc := range []struct {
		name, format, method string
		state                evidence.State
		wantShown            bool
	}{
		{"EPUB with an epub_text excerpt", "EPUB", evidence.ExcerptEPUBText, evidence.StateOK, true},
		{"a PDF with an epub_text excerpt", "PDF", evidence.ExcerptEPUBText, evidence.StateOK, false},
		{"an EPUB with a text_layer excerpt", "EPUB", evidence.ExcerptTextLayer, evidence.StateOK, false},
		{"a format with no excerpt method", "MOBI", evidence.ExcerptEPUBText, evidence.StateOK, false},
		{"a file that isn't ok", "EPUB", evidence.ExcerptEPUBText, evidence.StateCorrupt, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inspectFile = func(context.Context, string, string, inspect.Limits) (inspect.Result, error) {
				return inspect.Result{State: tc.state, Detected: "epub", Excerpt: &evidence.Excerpt{Method: tc.method, Chars: 77, Complete: true}}, nil
			}
			b := inspectBook()
			b.Book.Formats = []library.Format{{Name: tc.format, Size: 5, MTime: b.Book.LastModified}}
			b.Files = map[string][]byte{tc.format: []byte("12345")}
			var out, errb bytes.Buffer
			opts := inspectOptions{Book: 123, Timeout: time.Minute}
			if code := runInspect(context.Background(), verifytest.NewLibrary("library", b), opts, verify.DefaultLimits, &out, &errb); code != 0 {
				t.Fatal(errb.String())
			}
			if shown := strings.Contains(out.String(), "excerpt: chars=77"); shown != tc.wantShown {
				t.Errorf("excerpt shown = %v, want %v:\n%s", shown, tc.wantShown, out.String())
			}
		})
	}
}
