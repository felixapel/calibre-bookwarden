package sources_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/sources"
)

// tlsClient returns a client for srv: loopback is allowed (httptest), plain
// HTTP is not.
func tlsClient(t *testing.T, srv *httptest.Server, mod func(*sources.ClientOptions)) *sources.Client {
	t.Helper()
	u, _ := url.Parse(srv.URL)
	o := sources.ClientOptions{
		UserAgent: "bookwarden/test (+https://example.invalid)", AllowHosts: []string{u.Hostname()},
		AllowPrivate: true, Pace: time.Millisecond, Budget: 5 * time.Second, Backoff: 10 * time.Millisecond,
		TLSConfig: srv.Client().Transport.(*http.Transport).TLSClientConfig.Clone(),
	}
	if mod != nil {
		mod(&o)
	}
	return sources.NewClient(o)
}

func counter(h http.HandlerFunc) (http.HandlerFunc, *atomic.Int32) {
	var n atomic.Int32
	return func(w http.ResponseWriter, r *http.Request) { n.Add(1); h(w, r) }, &n
}

func TestClientRefuses(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("plain")) }))
	defer plain.Close()
	var srv *httptest.Server
	hits := map[string]*atomic.Int32{}
	mux := http.NewServeMux()
	route := func(path string, h http.HandlerFunc) {
		c, n := counter(h)
		hits[path] = n
		mux.HandleFunc(path, c)
	}
	route("/other-host", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)+"/ok", http.StatusFound)
	})
	route("/to-http", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, plain.URL+"/x", http.StatusFound) })
	for i := range 5 {
		route("/chain"+string(rune('0'+i)), func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/chain"+string(rune('1'+i)), http.StatusFound)
		})
	}
	route("/big", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(make([]byte, 2<<20)) })
	route("/ok", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	srv = httptest.NewTLSServer(mux)
	defer srv.Close()

	c := tlsClient(t, srv, nil)
	for _, tc := range []struct{ name, url string }{
		{"host not allowed", strings.Replace(srv.URL, "127.0.0.1", "localhost", 1) + "/ok"},
		{"redirect to another host", srv.URL + "/other-host"},
		{"redirect to http", srv.URL + "/to-http"},
		{"too many redirects", srv.URL + "/chain0"},
		{"body too large", srv.URL + "/big"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := c.Get(t.Context(), tc.url, nil, 1<<20); !errors.Is(err, sources.ErrUnavailable) {
				t.Fatalf("err = %v, want ErrUnavailable", err)
			}
		})
	}
	if hits["/ok"].Load() != 0 || hits["/chain4"].Load() != 0 {
		t.Fatalf("a refused request reached the server: ok=%d chain4=%d", hits["/ok"].Load(), hits["/chain4"].Load())
	}
	t.Run("plain http", func(t *testing.T) {
		u, _ := url.Parse(plain.URL)
		pc := sources.NewClient(sources.ClientOptions{AllowHosts: []string{u.Hostname()}, AllowPrivate: true, Budget: time.Second})
		if _, _, err := pc.Get(t.Context(), plain.URL+"/x", nil, 1<<20); !errors.Is(err, sources.ErrUnavailable) {
			t.Fatalf("err = %v, want ErrUnavailable", err)
		}
	})
	t.Run("private addresses", func(t *testing.T) {
		for _, host := range []string{"127.0.0.1", "10.0.0.122", "169.254.1.1", "[::1]", "10.1.2.3", "0.0.0.0",
			// special-purpose ranges: this network, CGNAT (Tailscale), IETF, benchmarking, reserved, NAT64, 6to4
			"0.1.2.3", "100.64.0.1", "192.0.0.8", "198.18.0.1", "240.0.0.1", "[64:ff9b::7f00:1]", "[64:ff9b:1::1]", "[2002:7f00:1::1]"} {
			h := strings.Trim(host, "[]")
			pc := sources.NewClient(sources.ClientOptions{AllowHosts: []string{h}, Budget: time.Second, Backoff: time.Millisecond})
			if _, _, err := pc.Get(t.Context(), "https://"+host+"/x", nil, 1<<20); !errors.Is(err, sources.ErrUnavailable) || !strings.Contains(err.Error(), "refused address") {
				t.Fatalf("%s: err = %v, want refused address", host, err)
			}
		}
	})
}

func TestClientRetryAfter(t *testing.T) {
	var n atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/once", func(w http.ResponseWriter, _ *http.Request) {
		if n.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte("ok"))
	})
	// A long Retry-After holds its host (TestClientThrottleHoldsTheHost),
	// so it gets a server of its own.
	long, longHits := counter(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "86400")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	lsrv := httptest.NewTLSServer(long)
	defer lsrv.Close()
	down, downHits := counter(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) })
	mux.HandleFunc("/down", down)
	missing, missingHits := counter(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })
	mux.HandleFunc("/missing", missing)
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()
	c := tlsClient(t, srv, nil)

	start := time.Now()
	status, body, err := c.Get(t.Context(), srv.URL+"/once", nil, 1<<20)
	if err != nil || status != 200 || string(body) != "ok" || n.Load() != 2 || time.Since(start) < 900*time.Millisecond {
		t.Fatalf("retry-after: %d %q %v hits=%d after %v", status, body, err, n.Load(), time.Since(start))
	}
	start = time.Now()
	lc := tlsClient(t, lsrv, nil)
	if _, _, err := lc.Get(t.Context(), lsrv.URL+"/long", nil, 1<<20); !errors.Is(err, sources.ErrUnavailable) || longHits.Load() != 1 || time.Since(start) > time.Second {
		t.Fatalf("long retry-after: %v hits=%d", err, longHits.Load())
	}
	if _, _, err := c.Get(t.Context(), srv.URL+"/down", nil, 1<<20); !errors.Is(err, sources.ErrUnavailable) || downHits.Load() != 3 {
		t.Fatalf("503s: %v hits=%d", err, downHits.Load())
	}
	if status, _, err := c.Get(t.Context(), srv.URL+"/missing", nil, 1<<20); err != nil || status != 404 || missingHits.Load() != 1 {
		t.Fatalf("404: %d %v hits=%d", status, err, missingHits.Load())
	}
}

func TestClientPacing(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	defer srv.Close()
	other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	defer other.Close()
	c := tlsClient(t, srv, func(o *sources.ClientOptions) { o.Pace = 200 * time.Millisecond })
	start := time.Now()
	for range 3 {
		if _, _, err := c.Get(t.Context(), srv.URL, nil, 1<<20); err != nil {
			t.Fatal(err)
		}
	}
	if d := time.Since(start); d < 400*time.Millisecond {
		t.Fatalf("3 paced calls took %v", d)
	}
	// Hosts are paced independently: srv.URL and other.URL share 127.0.0.1
	// but differ in port, and pacing is per host:port.
	c2 := tlsClient(t, srv, func(o *sources.ClientOptions) { o.Pace = 300 * time.Millisecond })
	var wg sync.WaitGroup
	start = time.Now()
	for _, u := range []string{srv.URL, other.URL} {
		wg.Go(func() {
			cl := c2
			if u == other.URL {
				cl = tlsClient(t, other, func(o *sources.ClientOptions) { o.Pace = 300 * time.Millisecond })
			}
			_, _, _ = cl.Get(context.Background(), u, nil, 1<<20)
		})
	}
	wg.Wait()
	if d := time.Since(start); d > 250*time.Millisecond {
		t.Fatalf("independent hosts waited %v", d)
	}
}

func TestClientBudget(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
	}))
	defer srv.Close()
	c := tlsClient(t, srv, func(o *sources.ClientOptions) { o.Budget = 300 * time.Millisecond })
	start := time.Now()
	if _, _, err := c.Get(t.Context(), srv.URL, nil, 1<<20); !errors.Is(err, sources.ErrUnavailable) || time.Since(start) > time.Second {
		t.Fatalf("err = %v after %v", err, time.Since(start))
	}
}

func TestClientRequestCarriesOnlyISBN(t *testing.T) {
	type seen struct {
		method, uri string
		header      http.Header
	}
	var mu sync.Mutex
	var got []seen
	mux := http.NewServeMux()
	mux.HandleFunc("/isbn/9783551551672.json", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, seen{r.Method, r.RequestURI, r.Header.Clone()})
		mu.Unlock()
		http.Redirect(w, r, "/books/OL1M.json", http.StatusFound)
	})
	mux.HandleFunc("/books/OL1M.json", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, seen{r.Method, r.RequestURI, r.Header.Clone()})
		mu.Unlock()
		_, _ = w.Write([]byte("{}"))
	})
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()
	c := tlsClient(t, srv, nil)
	h := http.Header{"X-Goog-Api-Key": {"k"}}
	if _, _, err := c.Get(t.Context(), srv.URL+"/isbn/9783551551672.json", h, 1<<20); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].uri != "/isbn/9783551551672.json" || got[1].uri != "/books/OL1M.json" {
		t.Fatalf("requests = %+v", got)
	}
	for _, s := range got {
		if s.method != http.MethodGet || s.header.Get("User-Agent") != "bookwarden/test (+https://example.invalid)" ||
			s.header.Get("Cookie") != "" || s.header.Get("Referer") != "" {
			t.Fatalf("request %+v", s)
		}
		for k := range s.header {
			switch k {
			case "User-Agent", "Accept-Encoding", "X-Goog-Api-Key":
			default:
				t.Fatalf("unexpected header %s", k)
			}
		}
	}
}

// TestClientThrottleHoldsTheHost: a 429 with Retry-After holds its host for
// every request, not only the one that got it; another host isn't held. A
// request whose budget ends before the hold fails at once.
func TestClientThrottleHoldsTheHost(t *testing.T) {
	var n atomic.Int32
	held := http.NewServeMux()
	held.HandleFunc("/first", func(w http.ResponseWriter, _ *http.Request) {
		if n.Add(1) == 1 {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte("ok"))
	})
	held.HandleFunc("/other", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	a := httptest.NewTLSServer(held)
	defer a.Close()
	b := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	defer b.Close()
	ua, _ := url.Parse(a.URL)
	ub, _ := url.Parse(b.URL)
	c := tlsClient(t, a, func(o *sources.ClientOptions) {
		o.AllowHosts = []string{ua.Hostname(), ub.Hostname()}
		o.Budget = 4 * time.Second
	})

	first := make(chan error, 1)
	go func() {
		_, _, err := c.Get(t.Context(), a.URL+"/first", nil, 1<<20)
		first <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Until(sources.HostNext(c, ua.Host)) < time.Second {
		if time.Now().After(deadline) {
			t.Fatal("the 429 never held the host")
		}
		time.Sleep(time.Millisecond)
	}
	start := time.Now()
	if _, _, err := c.Get(t.Context(), b.URL+"/", nil, 1<<20); err != nil || time.Since(start) > 500*time.Millisecond {
		t.Errorf("another host: %v after %v, want at once", err, time.Since(start))
	}
	start = time.Now()
	if _, _, err := c.Get(t.Context(), a.URL+"/other", nil, 1<<20); err != nil || time.Since(start) < time.Second {
		t.Errorf("the held host: %v after %v, want a wait of about 2 s", err, time.Since(start))
	}
	if err := <-first; err != nil {
		t.Errorf("the throttled request: %v", err)
	}

	// A hold longer than what a request has left fails it without asking.
	short, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	sources.HoldHost(c, ua.Host, 3*time.Second)
	start = time.Now()
	if _, _, err := c.Get(short, a.URL+"/other", nil, 1<<20); !errors.Is(err, sources.ErrUnavailable) || time.Since(start) > 200*time.Millisecond {
		t.Errorf("a request that can't outlast the hold: %v after %v, want unavailable at once", err, time.Since(start))
	}
}

// TestClientHoldIsCappedAndQueued: a hold is capped at half the budget; a
// request already waiting for its turn when the hold comes doesn't go out
// during it; and the throttled request gets the first turn after it.
func TestClientHoldIsCappedAndQueued(t *testing.T) {
	var (
		mu    sync.Mutex
		order []string
		once  atomic.Bool
	)
	mux := http.NewServeMux()
	mux.HandleFunc("/first", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		order = append(order, "first")
		mu.Unlock()
		if once.CompareAndSwap(false, true) {
			w.Header().Set("Retry-After", "86400")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/other", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		order = append(order, "other")
		mu.Unlock()
		_, _ = w.Write([]byte("ok"))
	})
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	c := tlsClient(t, srv, func(o *sources.ClientOptions) { o.Budget = 6 * time.Second })

	// Retry-After 86400 is far past the budget: the throttled request gives
	// up, and the hold is capped at 3 s.
	start := time.Now()
	if _, _, err := c.Get(t.Context(), srv.URL+"/first", nil, 1<<20); !errors.Is(err, sources.ErrUnavailable) {
		t.Fatalf("throttled request: %v", err)
	}
	if held := sources.HostNext(c, u.Host).Sub(start); held > 3*time.Second+100*time.Millisecond {
		t.Errorf("held for %v, want at most half the 6 s budget", held)
	}

	// A request that took its turn before a hold arrives waits for the hold.
	slow := tlsClient(t, srv, func(o *sources.ClientOptions) { o.Pace = time.Second; o.Budget = 6 * time.Second })
	done := make(chan time.Duration, 1)
	start = time.Now()
	if _, _, err := slow.Get(t.Context(), srv.URL+"/other", nil, 1<<20); err != nil { // takes the turn at once
		t.Fatal(err)
	}
	go func() {
		_, _, err := slow.Get(t.Context(), srv.URL+"/other", nil, 1<<20) // its turn is 1 s from start
		if err != nil {
			t.Error(err)
		}
		done <- time.Since(start)
	}()
	time.Sleep(200 * time.Millisecond)
	sources.HoldHost(slow, u.Host, 2*time.Second) // until about 2.2 s from start
	if took := <-done; took < 2*time.Second {
		t.Errorf("a request that waited for its turn went out during the hold, after %v", took)
	}
}

// TestClientThrottledRequestGoesFirst: the request that got the 429 takes
// the first turn after the hold, ahead of requests that came later.
func TestClientThrottledRequestGoesFirst(t *testing.T) {
	var (
		mu    sync.Mutex
		order []string
		once  atomic.Bool
	)
	mux := http.NewServeMux()
	mux.HandleFunc("/first", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		order = append(order, "first")
		mu.Unlock()
		if once.CompareAndSwap(false, true) {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/other", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		order = append(order, "other")
		mu.Unlock()
		_, _ = w.Write([]byte("ok"))
	})
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	// Turns 500 ms apart, so who went first doesn't depend on a handshake.
	c := tlsClient(t, srv, func(o *sources.ClientOptions) { o.Budget = 6 * time.Second; o.Pace = 500 * time.Millisecond })
	first := make(chan error, 1)
	go func() {
		_, _, err := c.Get(t.Context(), srv.URL+"/first", nil, 1<<20)
		first <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Until(sources.HostNext(c, u.Host)) < time.Second {
		if time.Now().After(deadline) {
			t.Fatal("the 429 never held the host")
		}
		time.Sleep(time.Millisecond)
	}
	if _, _, err := c.Get(t.Context(), srv.URL+"/other", nil, 1<<20); err != nil {
		t.Fatal(err)
	}
	if err := <-first; err != nil {
		t.Fatalf("throttled request: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if want := []string{"first", "first", "other"}; !slices.Equal(order, want) {
		t.Errorf("requests went out as %v, want %v", order, want)
	}
}
