package sources_test

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/sources"
)

func TestClientPost(t *testing.T) {
	var mu sync.Mutex
	var gotBody, gotType, gotUA, gotMethod string
	mux := http.NewServeMux()
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotBody, gotType, gotUA, gotMethod = string(b), r.Header.Get("Content-Type"), r.Header.Get("User-Agent"), r.Method
		mu.Unlock()
		_, _ = w.Write([]byte("pong"))
	})
	redirect, redirectHits := counter(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/echo", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("/redirect", redirect)
	limited, limitedHits := counter(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTooManyRequests) })
	mux.HandleFunc("/limited", limited)
	denied, deniedHits := counter(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) })
	mux.HandleFunc("/denied", denied)
	mux.HandleFunc("/big", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(make([]byte, 2<<20)) })
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()
	c := tlsClient(t, srv, nil)

	h := http.Header{"Content-Type": {"application/json"}}
	status, body, err := c.Post(t.Context(), srv.URL+"/echo", h, []byte(`{"q":1}`), 1<<20)
	mu.Lock()
	ok := err == nil && status == 200 && string(body) == "pong" && gotBody == `{"q":1}` && gotType == "application/json" &&
		gotMethod == http.MethodPost && gotUA == "bookwarden/test (+https://example.invalid)"
	mu.Unlock()
	if !ok {
		t.Fatalf("post: %d %q %v; server saw %s %q %q %q", status, body, err, gotMethod, gotBody, gotType, gotUA)
	}
	for _, tc := range []struct {
		name, url    string
		hits         *atomic.Int32
		want         int32
		unauthorized bool
	}{
		{"no redirect", srv.URL + "/redirect", redirectHits, 1, false},
		{"429 is retried then unavailable", srv.URL + "/limited", limitedHits, 3, false},
		{"401 is not retried", srv.URL + "/denied", deniedHits, 1, true},
		{"body too large", srv.URL + "/big", nil, 0, false},
		{"host not allowed", strings.Replace(srv.URL, "127.0.0.1", "localhost", 1) + "/echo", nil, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := c.Post(t.Context(), tc.url, h, []byte("x"), 1<<20)
			if !errors.Is(err, sources.ErrUnavailable) {
				t.Fatalf("err = %v, want ErrUnavailable", err)
			}
			if tc.hits != nil && tc.hits.Load() != tc.want {
				t.Fatalf("hits = %d, want %d", tc.hits.Load(), tc.want)
			}
			if errors.Is(err, sources.ErrUnauthorized) != tc.unauthorized {
				t.Fatalf("ErrUnauthorized = %v, want %v (%v)", !tc.unauthorized, tc.unauthorized, err)
			}
		})
	}
}

func TestClientHostPace(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	c := tlsClient(t, srv, func(o *sources.ClientOptions) {
		o.Pace = time.Millisecond
		o.HostPace = map[string]time.Duration{u.Hostname(): 300 * time.Millisecond}
	})
	start := time.Now()
	for range 3 {
		if _, _, err := c.Post(t.Context(), srv.URL, nil, nil, 1<<20); err != nil {
			t.Fatal(err)
		}
	}
	if d := time.Since(start); d < 550*time.Millisecond {
		t.Fatalf("3 calls on a 300 ms host took %v", d)
	}
}
