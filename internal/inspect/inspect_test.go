package inspect_test

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/inspect"
)

func palmDB(kind string) []byte {
	b := make([]byte, 78)
	copy(b[60:], kind)
	return b
}

func epubZip(t testing.TB) []byte {
	return zipOf(t,
		entry{name: "mimetype", data: []byte("application/epub+zip"), method: zip.Store},
		entry{name: "META-INF/container.xml", data: []byte("<container/>"), method: zip.Deflate})
}

func TestDetect(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want string
	}{
		{"pdf", []byte("%PDF-1.7\n%\xe2\xe3\xcf\xd3\n"), "pdf"},
		{"pdf after junk", append(bytes.Repeat([]byte{'j'}, 300), "%PDF-1.4\n"...), "pdf"},
		{"pdf too late", append(bytes.Repeat([]byte{'j'}, 1100), "%PDF-1.4\n"...), "unknown"},
		{"epub", epubZip(t), "epub"},
		{"zip", zipOf(t, entry{name: "page1.png", data: []byte("x"), method: zip.Store}), "zip"},
		{"empty zip", eocd(0, 0, 0), "zip"},
		{"mobi", palmDB("BOOKMOBI"), "mobi"},
		{"palmdoc", palmDB("TEXtREAd"), "palmdoc"},
		{"rar", []byte("Rar!\x1a\x07\x01\x00"), "rar"},
		{"jpeg", []byte("\xff\xd8\xff\xe0\x00\x10JFIF"), "jpeg"},
		{"png", []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\x0dIHDR"), "png"},
		{"gif87", []byte("GIF87a"), "gif"},
		{"gif89", []byte("GIF89a"), "gif"},
		{"webp", []byte("RIFF\x24\x00\x00\x00WEBPVP8L"), "webp"},
		{"riff but not webp", []byte("RIFF\x24\x00\x00\x00WAVEfmt "), "unknown"},
		{"text", []byte("abcd"), "unknown"},
		{"empty", nil, "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := inspect.Detect(bytes.NewReader(tt.data), int64(len(tt.data))); got != tt.want {
				t.Errorf("Detect = %q, want %q", got, tt.want)
			}
		})
	}
}

// fakes registers an inspector per kind that reports its own kind as reason.
func fakes(t *testing.T) {
	for _, kind := range []string{"epub", "cbz", "mobi", "pdf", "jpeg", "png", "gif", "webp"} {
		inspect.SetInspector(t, kind, func(context.Context, io.ReaderAt, int64, inspect.Limits) inspect.Result {
			return inspect.Result{State: evidence.StateOK, Reason: kind}
		})
	}
}

func TestInspectDispatch(t *testing.T) {
	fakes(t)
	plainZip := zipOf(t, entry{name: "page1.png", data: []byte("x"), method: zip.Store})
	tests := []struct {
		name     string
		declared string
		data     []byte
		state    evidence.State
		reason   string
		detected string
	}{
		{"a PDF stored as EPUB", "EPUB", []byte("%PDF-1.7\n"), evidence.StateOK, "pdf", "pdf"},
		{"unrecognisable bytes go to the declared format", "EPUB", []byte("abcd"), evidence.StateOK, "epub", "unknown"},
		{"an EPUB", "EPUB", epubZip(t), evidence.StateOK, "epub", "epub"},
		{"a KEPUB", "KEPUB", epubZip(t), evidence.StateOK, "epub", "epub"},
		{"an EPUB without its mimetype entry", "EPUB", plainZip, evidence.StateOK, "epub", "zip"},
		{"Calibre's ORIGINAL_EPUB", "ORIGINAL_EPUB", plainZip, evidence.StateOK, "epub", "zip"},
		{"a CBZ", "CBZ", plainZip, evidence.StateOK, "cbz", "zip"},
		{"a DOCX", "DOCX", plainZip, evidence.StateNotInspected, evidence.ReasonNoInspector, "zip"},
		{"a CBR", "CBR", []byte("Rar!\x1a\x07\x01\x00"), evidence.StateNotInspected, evidence.ReasonNoInspector, "rar"},
		{"a RAR stored as CBZ", "CBZ", []byte("Rar!\x1a\x07\x01\x00"), evidence.StateNotInspected, evidence.ReasonNoInspector, "rar"},
		{"an AZW3", "AZW3", palmDB("BOOKMOBI"), evidence.StateOK, "mobi", "mobi"},
		{"a PalmDOC", "PDB", palmDB("TEXtREAd"), evidence.StateOK, "mobi", "palmdoc"},
		{"a truncated MOBI", "MOBI", []byte("BOOK"), evidence.StateOK, "mobi", "unknown"},
		{"a PNG stored as JPG", "JPG", []byte("\x89PNG\r\n\x1a\n"), evidence.StateOK, "png", "png"},
		{"a TXT", "TXT", []byte("abcd"), evidence.StateNotInspected, evidence.ReasonNoInspector, "unknown"},
		{"lower-case declared names", "epub", []byte("abcd"), evidence.StateOK, "epub", "unknown"},
		{"empty", "EPUB", nil, evidence.StateEmpty, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := inspect.Inspect(context.Background(), tt.declared, bytes.NewReader(tt.data), int64(len(tt.data)), inspect.Limits{})
			if got.State != tt.state || got.Reason != tt.reason || got.Detected != tt.detected {
				t.Errorf("got %s/%s detected %q, want %s/%s detected %q", got.State, got.Reason, got.Detected, tt.state, tt.reason, tt.detected)
			}
		})
	}
}

func TestInspectRecoversPanics(t *testing.T) {
	inspect.SetInspector(t, "pdf", func(context.Context, io.ReaderAt, int64, inspect.Limits) inspect.Result {
		panic("boom")
	})
	data := []byte("%PDF-1.7\n")
	got := inspect.Inspect(context.Background(), "PDF", bytes.NewReader(data), int64(len(data)), inspect.Limits{})
	if got.State != evidence.StateNotInspected || got.Reason != evidence.ReasonInspectorError || got.Detected != "pdf" {
		t.Errorf("got %s/%s detected %q, want not_inspected/inspector_error detected pdf", got.State, got.Reason, got.Detected)
	}
	if !strings.HasPrefix(got.Panic, "boom\n") || !strings.Contains(got.Panic, "goroutine ") {
		t.Errorf("Panic = %q, want the value and a stack", got.Panic)
	}
}

func TestInspectHonoursTheDeadline(t *testing.T) {
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	inspect.SetInspector(t, "pdf", func(ctx context.Context, _ io.ReaderAt, _ int64, _ inspect.Limits) inspect.Result {
		if ctx.Err() != nil {
			return inspect.Result{State: evidence.StateCorrupt}
		}
		return inspect.Result{State: evidence.StateOK}
	})
	pdf := []byte("%PDF-1.7\n")
	if got := inspect.Inspect(expired, "PDF", bytes.NewReader(pdf), int64(len(pdf)), inspect.Limits{}); got.State != evidence.StateNotInspected || got.Reason != evidence.ReasonTimeout {
		t.Errorf("an inspector that stopped at the deadline: got %s/%s, want not_inspected/timeout", got.State, got.Reason)
	}

	inspect.SetInspector(t, "cbz", inspect.VerifyZip)
	archive := manyEntries(t, 1000)
	if got := inspect.Inspect(expired, "CBZ", bytes.NewReader(archive), int64(len(archive)), inspect.Limits{}); got.State != evidence.StateNotInspected || got.Reason != evidence.ReasonTimeout {
		t.Errorf("the zip guard past the deadline: got %s/%s, want not_inspected/timeout", got.State, got.Reason)
	}
}

func TestFile(t *testing.T) {
	fakes(t)
	path := filepath.Join(t.TempDir(), "format")
	data := []byte("%PDF-1.7\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := inspect.File(context.Background(), "PDF", path, inspect.Limits{})
	want := inspect.Inspect(context.Background(), "PDF", bytes.NewReader(data), int64(len(data)), inspect.Limits{})
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("File = %+v, %v; want %+v", got, err, want)
	}
	if _, err := inspect.File(context.Background(), "PDF", filepath.Join(t.TempDir(), "gone"), inspect.Limits{}); err == nil {
		t.Error("File on a missing path: no error")
	}
}

type panicReader struct{}

func (panicReader) ReadAt([]byte, int64) (int, error) { panic("reader blew up") }

// Detect reads the file's head before any inspector runs; a panic there must
// come back as a Result, as Inspect documents.
func TestInspectRecoversFromAPanicInDetect(t *testing.T) {
	res := inspect.Inspect(context.Background(), "EPUB", panicReader{}, 100, inspect.Limits{})
	if res.State != evidence.StateNotInspected || res.Reason != evidence.ReasonInspectorError || !strings.Contains(res.Panic, "reader blew up") {
		t.Errorf("got %s/%s panic %q, want not_inspected/inspector_error", res.State, res.Reason, res.Panic)
	}
}
