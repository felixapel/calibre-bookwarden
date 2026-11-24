package ocr

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ToolVersions describes the tools whose behaviour feeds sealed hints and
// excerpts: the first line of pdftotext's and tesseract's version output and
// the commit of the tessdata models (the image writes it to COMMIT next to
// them). Incremental runs reuse file evidence only from packages made under
// the same text (ADR-039). A tool that can't be asked reads as "unknown".
//
// It runs the two tools with their version flag, outside the sandbox, once at
// start; they read no book.
func ToolVersions(ctx context.Context, t Tools) string {
	commit := "unknown"
	if b, err := os.ReadFile(filepath.Join(t.TessdataDir, "COMMIT")); err == nil {
		if s := strings.TrimSpace(string(b)); s != "" {
			commit = s
		}
	}
	return "pdftotext=" + firstLine(ctx, t.PDFToText, "-v") +
		";tesseract=" + firstLine(ctx, t.Tesseract, "--version") +
		";tessdata=" + commit
}

// firstLine is the first line of a tool's output (pdftotext prints its version
// to stderr), or "unknown".
func firstLine(ctx context.Context, path string, arg string) string {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, path, arg) //nolint:gosec // our own tool, a fixed argument
	cmd.Stdout, cmd.Stderr = &out, &out
	_ = cmd.Run() // some tools exit non-zero after printing their version
	line, _, _ := strings.Cut(strings.TrimSpace(out.String()), "\n")
	if line = strings.TrimSpace(line); line != "" && !strings.Contains(line, "no such file") {
		return line
	}
	return "unknown"
}
