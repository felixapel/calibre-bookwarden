package library_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/library"
)

// stallingCalibre is a fake Calibre whose answers never come: handlers block
// until the test ends. slowBody sends the headers at once and stalls the body.
func stallingCalibre(t *testing.T, slowBody bool) *library.Client {
	t.Helper()
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if slowBody {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"library_map": {"library": `)
			w.(http.Flusher).Flush()
		}
		select {
		case <-done:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(done); srv.Close() })
	base, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return library.New(library.Config{BaseURL: base, User: "reader", Password: testPassword, LibraryID: "library", MetaTimeout: 50 * time.Millisecond})
}

// within fails the test if f takes longer than d, and returns its error.
func within(t *testing.T, d time.Duration, f func() error) error {
	t.Helper()
	ch := make(chan error, 1)
	go func() { ch <- f() }()
	select {
	case err := <-ch:
		return err
	case <-time.After(d):
		t.Fatalf("call didn't return within %v", d)
		return nil
	}
}

// TestMetadataCallsTimeOutAsUnreachable: a Calibre that accepts the
// connection and never answers makes metadata calls fail as unreachable, so
// a run waits for Calibre instead of hanging.
func TestMetadataCallsTimeOutAsUnreachable(t *testing.T) {
	for _, slow := range []bool{false, true} {
		c := stallingCalibre(t, slow)
		calls := map[string]func() error{
			"Check":          func() error { _, err := c.Check(t.Context()); return err },
			"CheckInLibrary": func() error { _, err := c.CheckInLibrary(t.Context(), "library"); return err },
			"BookInLibrary":  func() error { _, err := c.BookInLibrary(t.Context(), "library", 1); return err },
			"ListIDsInLibrary": func() error {
				_, err := c.ListIDsInLibrary(t.Context(), "library")
				return err
			},
		}
		for name, call := range calls {
			err := within(t, 2*time.Second, call)
			if !errors.Is(err, library.ErrUnreachable) {
				t.Errorf("slow body %v, %s: %v, want ErrUnreachable", slow, name, err)
			}
		}
	}
}

// TestACancelledCallerIsNotUnreachable: when the caller gives up first, the
// error is the caller's, not a Calibre outage.
func TestACancelledCallerIsNotUnreachable(t *testing.T) {
	c := stallingCalibre(t, false)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	_, err := c.CheckInLibrary(ctx, "library")
	if errors.Is(err, library.ErrUnreachable) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the caller's deadline and not ErrUnreachable", err)
	}
}

// TestRedirectsAreNotFollowed: a redirect is never followed, so the digest
// credentials never answer another host's challenge.
func TestRedirectsAreNotFollowed(t *testing.T) {
	var other atomic.Int32
	b := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { other.Add(1) }))
	defer b.Close()
	c := fakeCalibre(t, map[string]http.HandlerFunc{
		"GET /ajax/library-info": func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, b.URL+"/ajax/library-info", http.StatusFound)
		},
	})
	_, err := c.Check(t.Context())
	if !errors.Is(err, library.ErrUnreachable) || other.Load() != 0 {
		t.Fatalf("err = %v, requests to the other host = %d; want ErrUnreachable and 0", err, other.Load())
	}
}

// shortTimeout is fakeCalibre with a 50 ms MetaTimeout.
func shortTimeout(t *testing.T, routes map[string]http.HandlerFunc) *library.Client {
	t.Helper()
	_, srv := fakeCalibreConfig(t, routes, "")
	base, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return library.New(library.Config{BaseURL: base, User: "reader", Password: testPassword, MetaTimeout: 50 * time.Millisecond})
}

func slow(d time.Duration, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(d)
		h(w, r)
	}
}

// TestTheExportProbeTimesOut: Check's export probe is a metadata call too.
func TestTheExportProbeTimesOut(t *testing.T) {
	done := make(chan struct{})
	defer close(done)
	c := shortTimeout(t, map[string]http.HandlerFunc{
		"POST /cdb/cmd/export/0": func(_ http.ResponseWriter, r *http.Request) {
			select {
			case <-done:
			case <-r.Context().Done():
			}
		},
	})
	if err := within(t, 2*time.Second, func() error { _, err := c.CheckInLibrary(t.Context(), "library"); return err }); !errors.Is(err, library.ErrUnreachable) {
		t.Fatalf("err = %v, want ErrUnreachable", err)
	}
}

// TestExportsAndWritesAreNotBoundedByMetaTimeout: a file export lasts as
// long as the file needs, and a write is bounded by its caller.
func TestExportsAndWritesAreNotBoundedByMetaTimeout(t *testing.T) {
	c := shortTimeout(t, map[string]http.HandlerFunc{
		"POST /cdb/cmd/export/0": slow(150*time.Millisecond, msgpackAnswer("\x81\xa6result\xc4\x03abc")),
		setFieldsRoute:           slow(150*time.Millisecond, jsonAnswer(book7Answer)),
	})
	var buf bytes.Buffer
	res, err := c.ExportFormatInLibrary(t.Context(), "library", 7, "EPUB", &buf, 3)
	if err != nil || buf.String() != "abc" {
		t.Fatalf("slow export: %+v, %q, %v", res, buf.String(), err)
	}
	if _, err := c.SetFields(t.Context(), "lib one", 7, []library.Change{change("title", `"T"`)}); err != nil {
		t.Fatalf("slow write: %v", err)
	}
}
