package sources_test

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"sync"

	"github.com/felixapel/calibre-bookwarden/internal/sources"
)

// fakeFetcher answers by URL path (and query, when routes name one) and
// records every request. A POST is routed like a GET and also records its
// body.
type fakeFetcher struct {
	mu      sync.Mutex
	routes  map[string]answer // key: path, or path?query
	urls    []string
	headers []http.Header
	methods []string
	bodies  [][]byte
}

type answer struct {
	status int
	body   []byte
	err    error
}

func file(name string) answer {
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		panic(err)
	}
	return answer{status: 200, body: b}
}

func (f *fakeFetcher) Get(_ context.Context, raw string, h http.Header, maxBody int64) (int, []byte, error) {
	return f.serve(http.MethodGet, raw, h, nil, maxBody)
}

func (f *fakeFetcher) Post(_ context.Context, raw string, h http.Header, body []byte, maxBody int64) (int, []byte, error) {
	return f.serve(http.MethodPost, raw, h, body, maxBody)
}

func (f *fakeFetcher) serve(method, raw string, h http.Header, body []byte, maxBody int64) (int, []byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.urls = append(f.urls, raw)
	f.headers = append(f.headers, h)
	f.methods = append(f.methods, method)
	f.bodies = append(f.bodies, body)
	u, _ := url.Parse(raw)
	a, ok := f.routes[u.Path+"?"+u.RawQuery]
	if !ok {
		a, ok = f.routes[u.Path]
	}
	if !ok {
		return 404, nil, nil
	}
	if a.err != nil {
		return 0, nil, a.err
	}
	if int64(len(a.body)) > maxBody {
		return 0, nil, sources.ErrUnavailable
	}
	return a.status, a.body, nil
}

var _ sources.Fetcher = (*fakeFetcher)(nil)
