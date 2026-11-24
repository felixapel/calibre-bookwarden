package library_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/library"
)

func TestExportFormat(t *testing.T) {
	var gotBody, gotAccept, gotLibrary string
	c := fakeCalibre(t, map[string]http.HandlerFunc{
		"POST /cdb/cmd/export/0": func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			if string(b) == `["fmt",0,"EPUB",null]` { // Check's probe
				msgpackAnswer("\x81\xa6result\xc0")(w, r)
				return
			}
			gotBody, gotAccept, gotLibrary = string(b), r.Header.Get("Accept"), r.URL.Query().Get("library_id")
			msgpackAnswer("\x81\xa6result\xc5\x00\x04abcd")(w, r)
		},
	})
	var w bytes.Buffer
	got, err := c.ExportFormat(context.Background(), 3, "EPUB", &w, 4)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != library.ExportOK || got.Bytes != 4 || w.String() != "abcd" {
		t.Errorf("ExportFormat() = %+v, wrote %q", got, w.String())
	}
	if gotBody != `["fmt",3,"EPUB",null]` || gotAccept != "application/x-msgpack" || gotLibrary != "library" {
		t.Errorf("request: body %s, Accept %q, library_id %q", gotBody, gotAccept, gotLibrary)
	}
}

func TestExportFormatErrors(t *testing.T) {
	probeOr := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			if string(b) == `["fmt",0,"EPUB",null]` {
				msgpackAnswer("\x81\xa6result\xc0")(w, r)
				return
			}
			h(w, r)
		}
	}
	tests := []struct {
		name    string
		handler http.HandlerFunc
		want    error
	}{
		{
			name:    "version mismatch",
			handler: status(404, strings.Replace(versionText, "list", "export", 1)),
			want:    library.ErrIncompatible,
		},
		{
			// Calibre restarting mid-transfer cuts the stream short: the run
			// must wait for Calibre, not fail the book.
			name: "connection cut mid-file",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Length", "20")
				_, _ = io.WriteString(w, "\x81\xa6result\xc5\x00\x0aab")
			},
			want: library.ErrUnreachable,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := fakeCalibre(t, map[string]http.HandlerFunc{"POST /cdb/cmd/export/0": probeOr(tt.handler)})
			_, err := c.ExportFormat(context.Background(), 3, "EPUB", io.Discard, 10)
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

func coverServer(t *testing.T, answer string, gotBody *string) *library.Client {
	t.Helper()
	return fakeCalibre(t, map[string]http.HandlerFunc{
		"POST /cdb/cmd/export/0": func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			if string(b) == `["fmt",0,"EPUB",null]` {
				msgpackAnswer("\x81\xa6result\xc0")(w, r)
				return
			}
			if gotBody != nil {
				*gotBody = string(b)
			}
			msgpackAnswer(answer)(w, r)
		},
	})
}

func TestExportCover(t *testing.T) {
	var body string
	c := coverServer(t, "\x81\xa6result\xc5\x00\x04abcd", &body)
	var w bytes.Buffer
	got, err := c.ExportCoverInLibrary(context.Background(), "library", 2, &w, library.CoverCap)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != library.ExportOK || got.Bytes != 4 || w.String() != "abcd" || got.SHA256 != "88d4266fd4e6338d13b845fcf289579d209c897823b9217da3e161936f031589" {
		t.Errorf("ExportCoverInLibrary() = %+v, wrote %q", got, w.String())
	}
	if body != `["cover",2]` {
		t.Errorf("request body %s", body)
	}
}

func TestExportCoverNil(t *testing.T) {
	c := coverServer(t, "\x81\xa6result\xc0", nil)
	var w bytes.Buffer
	got, err := c.ExportCoverInLibrary(context.Background(), "library", 2, &w, library.CoverCap)
	if err != nil || got.Status != library.ExportNotInCalibre || w.Len() != 0 {
		t.Fatalf("no cover = %+v, %v", got, err)
	}
}

func TestExportCoverCap(t *testing.T) {
	const size = 21 << 20
	answer := "\x81\xa6result\xc6\x01\x50\x00\x00" + strings.Repeat("x", size)
	c := coverServer(t, answer, nil)
	var w bytes.Buffer
	got, err := c.ExportCoverInLibrary(context.Background(), "library", 2, &w, library.CoverCap)
	if err != nil || got.Status != library.ExportTooLarge || w.Len() != 0 {
		t.Fatalf("21 MiB cover = %+v, %v, wrote %d", got, err, w.Len())
	}
	small := coverServer(t, "\x81\xa6result\xc5\x00\x04abcd", nil)
	if got, err := small.ExportCoverInLibrary(context.Background(), "library", 2, &w, 3); err != nil || got.Status != library.ExportTooLarge || w.Len() != 0 {
		t.Fatalf("4 bytes over a cap of 3 = %+v, %v", got, err)
	}
}

func TestExportCoverErrors(t *testing.T) {
	var ce *library.CalibreError
	c := coverServer(t, "\x81\xa3err\xa3bad\xa2tb\xa0", nil)
	if _, err := c.ExportCoverInLibrary(context.Background(), "library", 2, io.Discard, library.CoverCap); !errors.As(err, &ce) {
		t.Errorf("err answer = %v", err)
	}
	cut := fakeCalibre(t, map[string]http.HandlerFunc{
		"POST /cdb/cmd/export/0": func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			if string(b) == `["fmt",0,"EPUB",null]` {
				msgpackAnswer("\x81\xa6result\xc0")(w, r)
				return
			}
			w.Header().Set("Content-Length", "20")
			_, _ = io.WriteString(w, "\x81\xa6result\xc5\x00\x0aab")
		},
	})
	if _, err := cut.ExportCoverInLibrary(context.Background(), "library", 2, io.Discard, library.CoverCap); !errors.Is(err, library.ErrUnreachable) {
		t.Errorf("cut stream = %v", err)
	}
	if _, err := c.ExportCoverInLibrary(context.Background(), "", 2, io.Discard, library.CoverCap); !errors.Is(err, library.ErrNoLibrary) {
		t.Errorf("no library = %v", err)
	}
}
