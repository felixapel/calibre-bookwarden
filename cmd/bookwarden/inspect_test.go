package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/inspect/inspecttest"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
	"github.com/felixapel/calibre-bookwarden/internal/verify/verifytest"
)

const (
	inspectTitle = "Hidden Calibre Title"
	inspectProse = "Secret prose sentence"
)

func inspectXHTML(body string) string {
	return `<?xml version="1.0" encoding="utf-8"?>
<html xmlns="http://www.w3.org/1999/xhtml"><head><title>T</title></head><body>` + body + `</body></html>`
}

// inspectBook is book 123 with an EPUB of three spine documents (the second
// a copyright page holding an ISBN), a second EPUB-named format that is not
// there, and a PDF.
func inspectBook(extra ...string) verifytest.FakeBook {
	nav := `<?xml version="1.0" encoding="utf-8"?>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops"><head><title>Nav</title></head><body>
<nav epub:type="landmarks"><ol><li><a epub:type="copyright-page" href="text/extra0.xhtml">x</a></li></ol></nav></body></html>`
	epub := inspecttest.EPUB(inspecttest.EPUBOptions{
		Title: "Embedded Title Zyx", EPUB3: true, Nav: nav,
		ContentDoc: inspectXHTML(`<p>` + inspectProse + `</p>`),
		Extra:      append([]string{inspectXHTML(`<p>Imprint. ISBN 978-0-00-000004-0</p>`), inspectXHTML(`<p>End</p>`)}, extra...),
	})
	pdf := inspecttest.PDF(inspecttest.PDFOptions{Info: map[string]string{"Title": "Paper Trail"}, Text: "A tiny generated book."})
	t0 := time.Date(2026, 10, 12, 21, 5, 55, 0, time.UTC)
	return verifytest.FakeBook{
		Book: library.Book{
			ID: 123, UUID: "uuid-123", Title: inspectTitle, Authors: []string{"Hidden Author"}, LastModified: t0,
			Identifiers: map[string]string{"isbn": "9780000000040"},
			Formats: []library.Format{
				{Name: "EPUB", Size: int64(len(epub)), MTime: t0},
				{Name: "MOBI", Size: 10, MTime: t0, Missing: true},
				{Name: "PDF", Size: int64(len(pdf)), MTime: t0},
			},
		},
		Files: map[string][]byte{"EPUB": epub, "PDF": pdf},
	}
}

func runInspectCmd(t *testing.T, lib *verifytest.FakeLibrary, scratch string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	t.Setenv("TMPDIR", scratch)
	var out, errb bytes.Buffer
	opts, err := parseInspectArgs(args)
	if err != nil {
		t.Fatalf("args %v: %v", args, err)
	}
	code = runInspect(context.Background(), lib, opts, verify.DefaultLimits, &out, &errb)
	return code, out.String(), errb.String()
}

func TestInspectCommandSummary(t *testing.T) {
	scratch := t.TempDir()
	lib := verifytest.NewLibrary("library", inspectBook())
	code, out, errOut := runInspectCmd(t, lib, scratch, "--book", "123")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	for _, want := range []string{
		"book 123", "format EPUB", "kind=epub", "state=ok",
		"hints: method=epub_text state=ok", "isbns=1",
		"read 3 documents", "1:front", "2:copyright", "3:front",
		"excerpt: chars=", "truncated=false", "complete=true",
		"timings: export=", "inspect=",
		"format MOBI", "state=missing",
		"format PDF", "kind=pdf",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	for _, secret := range []string{inspectProse, inspectTitle, "Embedded Title", "Hidden Author", "Paper Trail", "9780000000040", "978-0-00-000004-0", "A tiny generated"} {
		if strings.Contains(out, secret) {
			t.Errorf("output holds %q:\n%s", secret, out)
		}
	}
	if left, _ := os.ReadDir(scratch); len(left) != 0 {
		t.Errorf("temp dir holds %d entries after the command", len(left))
	}
	if lib.ExportCalls(123, "MOBI") != 0 {
		t.Error("a format Calibre can't find was requested")
	}
}

func TestInspectCommandJSON(t *testing.T) {
	scratch := t.TempDir()
	lib := verifytest.NewLibrary("library", inspectBook())
	code, out, errOut := runInspectCmd(t, lib, scratch, "--book", "123", "--format", "epub", "--json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	var rep struct {
		Book    int `json:"book"`
		Formats []struct {
			Format string `json:"format"`
			Kind   string `json:"kind"`
			State  string `json:"state"`
			Hints  *struct {
				Method    string `json:"method"`
				State     string `json:"state"`
				Reason    string `json:"reason"`
				ISBNCount int    `json:"isbn_count"`
				Pages     []int  `json:"pages"`
			} `json:"hints"`
			Docs []struct {
				Pos      int    `json:"pos"`
				Landmark string `json:"landmark"`
				Whole    bool   `json:"whole"`
			} `json:"docs"`
			Excerpt *struct {
				Chars     int  `json:"chars"`
				Truncated bool `json:"truncated"`
				Complete  bool `json:"complete"`
			} `json:"excerpt"`
			ExportMS  *int64 `json:"export_ms"`
			InspectMS *int64 `json:"inspect_ms"`
		} `json:"formats"`
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("%v:\n%s", err, out)
	}
	if rep.Book != 123 || len(rep.Formats) != 1 {
		t.Fatalf("report %+v", rep)
	}
	f := rep.Formats[0]
	if f.Format != "EPUB" || f.Kind != "epub" || f.State != "ok" || f.Hints == nil || f.Hints.ISBNCount != 1 || f.Hints.State != "ok" ||
		len(f.Docs) != 3 || f.Docs[1].Landmark != "copyright" || !f.Docs[1].Whole ||
		f.Excerpt == nil || f.Excerpt.Chars == 0 || !f.Excerpt.Complete || f.Excerpt.Truncated || f.ExportMS == nil || f.InspectMS == nil {
		t.Fatalf("format %+v hints %+v excerpt %+v", f, f.Hints, f.Excerpt)
	}
	for _, secret := range []string{inspectProse, inspectTitle, "Embedded Title", "Hidden Author", "9780000000040", "978-0-00-000004-0"} {
		if strings.Contains(out, secret) {
			t.Errorf("JSON holds %q", secret)
		}
	}
	if lib.ExportCalls(123, "PDF") != 0 {
		t.Error("--format EPUB exported the PDF")
	}
	if left, _ := os.ReadDir(scratch); len(left) != 0 {
		t.Errorf("temp dir holds %d entries", len(left))
	}
}

func TestInspectCommandShowsAWindowedDocument(t *testing.T) {
	// A fourth document longer than the per-document window is read to its
	// first 32 KiB: not a failure, and not "partial".
	scratch := t.TempDir()
	long := inspectXHTML(`<p>` + strings.Repeat("lorem ipsum ", 6_000) + `</p>`)
	lib := verifytest.NewLibrary("library", inspectBook(long))
	code, out, errOut := runInspectCmd(t, lib, scratch, "--book", "123", "--format", "EPUB")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	for _, want := range []string{"state=ok", "4:front(window)"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	for _, not := range []string{"reason=", "(partial)"} {
		if strings.Contains(out, not) {
			t.Errorf("output has %q:\n%s", not, out)
		}
	}
	code, out, errOut = runInspectCmd(t, lib, scratch, "--book", "123", "--format", "EPUB", "--json")
	if code != 0 || !strings.Contains(out, `"reason": "doc_window"`) {
		t.Fatalf("exit %d, JSON lacks the window reason: %s %s", code, out, errOut)
	}
}

func TestInspectCommandShowsTheFailureReason(t *testing.T) {
	// A fourth spine document that doesn't parse: with no body landmark it is
	// still a front document, and the scan is failed with xml_error.
	scratch := t.TempDir()
	lib := verifytest.NewLibrary("library", inspectBook(`<html><body><p>cut <p class="x`))
	code, out, errOut := runInspectCmd(t, lib, scratch, "--book", "123", "--format", "EPUB")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	for _, want := range []string{"state=failed", "reason=xml_error", "4:front(partial)"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if left, _ := os.ReadDir(scratch); len(left) != 0 {
		t.Errorf("temp dir holds %d entries", len(left))
	}
}

func TestInspectCommandFailures(t *testing.T) {
	t.Run("no such book", func(t *testing.T) {
		scratch := t.TempDir()
		code, _, errOut := runInspectCmd(t, verifytest.NewLibrary("library", inspectBook()), scratch, "--book", "999")
		if code != 1 || !strings.Contains(errOut, "999") {
			t.Errorf("exit %d: %s", code, errOut)
		}
	})
	t.Run("no such format", func(t *testing.T) {
		scratch := t.TempDir()
		code, _, errOut := runInspectCmd(t, verifytest.NewLibrary("library", inspectBook()), scratch, "--book", "123", "--format", "AZW3")
		if code != 1 || !strings.Contains(errOut, "AZW3") {
			t.Errorf("exit %d: %s", code, errOut)
		}
	})
	t.Run("the timeout ends a slow export", func(t *testing.T) {
		scratch := t.TempDir()
		lib := verifytest.NewLibrary("library", inspectBook())
		lib.SetExportDelay(time.Second)
		code, out, errOut := runInspectCmd(t, lib, scratch, "--book", "123", "--format", "EPUB", "--timeout", "30ms")
		if code != 0 || !strings.Contains(out, "state=not_inspected") || !strings.Contains(out, "reason=timeout") {
			t.Errorf("exit %d: %s%s", code, out, errOut)
		}
		if left, _ := os.ReadDir(scratch); len(left) != 0 {
			t.Errorf("temp dir holds %d entries", len(left))
		}
	})
}

func TestInspectArgs(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		ok   bool
	}{
		{"book only", []string{"--book", "5"}, true},
		{"all flags", []string{"--book", "5", "--format", "epub", "--json", "--timeout", "10s"}, true},
		{"no book", nil, false},
		{"zero book", []string{"--book", "0"}, false},
		{"bad timeout", []string{"--book", "5", "--timeout", "0s"}, false},
		{"stray argument", []string{"--book", "5", "x"}, false},
		{"unknown flag", []string{"--book", "5", "--all"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseInspectArgs(tc.args); (err == nil) != tc.ok {
				t.Errorf("err = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}

// The command needs the content server and nothing else: no database URL.
func TestInspectCommandNeedsNoDatabase(t *testing.T) {
	var out, errb bytes.Buffer
	env := map[string]string{} // no BOOKWARDEN_DATABASE_URL, no allowed hosts
	code := run(context.Background(), []string{"inspect", "--book", "5"}, func(k string) string { return env[k] }, strings.NewReader(""), &out, &errb)
	if code != 1 || strings.Contains(errb.String(), "DATABASE_URL") || !strings.Contains(errb.String(), "BOOKWARDEN_CALIBRE_URL is required") {
		t.Errorf("exit %d: %s", code, errb.String())
	}
	code = run(context.Background(), []string{"inspect"}, func(string) string { return "" }, strings.NewReader(""), &out, &errb)
	if code != 2 {
		t.Errorf("no --book: exit %d, want 2", code)
	}
}
