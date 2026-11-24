package verify_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
	"github.com/felixapel/calibre-bookwarden/internal/verify/verifytest"
)

type switchingStore struct {
	*verifytest.MemStore
	probe func(context.Context) error
}

func (s *switchingStore) ActiveRun(ctx context.Context) (verify.Run, bool, error) {
	if err := s.probe(ctx); err != nil {
		return verify.Run{}, false, err
	}
	return s.MemStore.ActiveRun(ctx)
}

func TestRecheckLibraryInterleaving(t *testing.T) {
	var switched atomic.Bool
	var wrongRead atomic.Bool
	var probesA atomic.Int64
	var exportLibrary atomic.Value
	exportLibrary.Store("")
	var readLibrary atomic.Value
	readLibrary.Store("")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ajax/library-info":
			current := "A"
			if switched.Load() {
				current = "B"
			}
			_, _ = fmt.Fprintf(w, `{"library_map":{"A":"A","B":"B"},"default_library":%q}`, current)
		case "/cdb/cmd/export/0":
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			if string(body) == `["fmt",0,"EPUB",null]` {
				_, _ = io.WriteString(w, "\x81\xa6result\xc0")
				return
			}
			lib := r.URL.Query().Get("library_id")
			exportLibrary.Store(lib)
			if lib != "A" {
				wrongRead.Store(true)
			}
			_, _ = io.WriteString(w, "\x81\xa6result\xc5\x00\x04abcd")
		case "/cdb/cmd/list/0":
			var args []json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
				t.Error(err)
				http.Error(w, "args", 500)
				return
			}
			var query string
			if err := json.Unmarshal(args[3], &query); err != nil {
				t.Error(err)
				return
			}
			if query == "id:0" {
				if r.URL.Query().Get("library_id") == "A" {
					probesA.Add(1)
				}
				_, _ = io.WriteString(w, `{"result":{"book_ids":[],"data":{"uuid":{}}}}`)
				return
			}
			lib := r.URL.Query().Get("library_id")
			readLibrary.Store(lib)
			if lib != "A" {
				wrongRead.Store(true)
			}
			_, _ = fmt.Fprintf(w, `{"result":{"book_ids":[2],"data":{"uuid":{"2":"shared-uuid"},"title":{"2":%q},"authors":{"2":["Ada Author"]},"author_sort":{"2":"Author, Ada"},"languages":{"2":["eng"]},"identifiers":{"2":{}},"formats":{"2":["EPUB"]},"last_modified":{"2":"2026-10-13T00:00:00Z"},"pubdate":{"2":null},"cover":{"2":0}}}}`, "Title from library "+lib)
		case "/ajax/books/A", "/ajax/books/B":
			if r.URL.Path != "/ajax/books/A" {
				wrongRead.Store(true)
			}
			_, _ = io.WriteString(w, `{"2":{"format_metadata":{"epub":{"size":4,"mtime":"2026-10-13T00:00:00Z"}}}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	baseURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := library.New(library.Config{BaseURL: baseURL})
	st := &switchingStore{MemStore: verifytest.NewMemStore()}
	// A successful concurrent probe replaces the cached default after Start's guard.
	st.probe = func(ctx context.Context) error {
		switched.Store(true)
		_, err := client.Check(ctx)
		return err
	}
	runner := verify.NewRunner(st, client, verify.Options{ScratchDir: t.TempDir(), Backoff: func(int) time.Duration { return 0 }, CheckEvery: time.Hour})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	run, err := runner.Start(ctx, verify.Scope{BookID: 2, ExpectedLibraryID: "A", ExpectedUUID: "shared-uuid"})
	if err != nil {
		t.Fatal(err)
	}
	if run.LibraryID != "A" || readLibrary.Load() != "A" {
		t.Fatalf("run library=%q; actual book request library=%q", run.LibraryID, readLibrary.Load())
	}
	if err := st.SetStatus(ctx, run.ID, verify.RunWaiting, "test recovery"); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- runner.Serve(ctx) }()
	waitTerminal(t, runner, run.ID)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	raw, seal, ok := st.Evidence(run.ID, 2)
	p, err := evidence.Open(raw, seal)
	if !ok || err != nil || p.LibraryID != "A" || p.Calibre.Title != "Title from library A" || wrongRead.Load() || exportLibrary.Load() != "A" {
		t.Fatalf("frozen reads were redirected: package=%+v/%v; wrong=%v; export=%v", p, err, wrongRead.Load(), exportLibrary.Load())
	}
	if probesA.Load() < 2 {
		t.Fatalf("recovery did not probe frozen library A: %d probes", probesA.Load())
	}
}
