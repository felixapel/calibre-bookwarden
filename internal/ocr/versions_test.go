package ocr

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// fakeTool writes an executable that prints out to stderr (as pdftotext -v
// does) or stdout.
func fakeTool(t *testing.T, dir, name, out string, toStderr bool) string {
	t.Helper()
	redirect := ""
	if toStderr {
		redirect = " >&2"
	}
	path := filepath.Join(dir, name)
	body := "#!/bin/sh\necho '" + out + "'" + redirect + "\necho 'second line'" + redirect + "\n"
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil { //nolint:gosec // a test script
		t.Fatal(err)
	}
	return path
}

func TestToolVersions(t *testing.T) {
	dir := t.TempDir()
	tess := filepath.Join(dir, "tessdata")
	if err := os.Mkdir(tess, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tess, "COMMIT"), []byte("abc123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tools := Tools{
		PDFToText:   fakeTool(t, dir, "pdftotext", "pdftotext version 24.08.0", true),
		Tesseract:   fakeTool(t, dir, "tesseract", "tesseract 5.3.4", false),
		TessdataDir: tess,
	}
	got := ToolVersions(context.Background(), tools)
	want := "pdftotext=pdftotext version 24.08.0;tesseract=tesseract 5.3.4;tessdata=abc123"
	if got != want {
		t.Errorf("ToolVersions() = %q, want %q", got, want)
	}

	t.Run("a tool that can't run is unknown, not an error", func(t *testing.T) {
		got := ToolVersions(context.Background(), Tools{PDFToText: filepath.Join(dir, "missing"), Tesseract: tools.Tesseract, TessdataDir: dir})
		want := "pdftotext=unknown;tesseract=tesseract 5.3.4;tessdata=unknown"
		if got != want {
			t.Errorf("ToolVersions() = %q, want %q", got, want)
		}
	})
}
