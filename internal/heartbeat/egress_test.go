package heartbeat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type redirector struct {
	mu sync.Mutex
	to string
	n  int
}

func (r *redirector) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	r.n++
	r.mu.Unlock()
	http.Redirect(w, req, r.to, http.StatusFound)
}

func TestRedirectIsNotFollowed(t *testing.T) {
	target := &recorder{codes: []int{200}}
	b := httptest.NewServer(target)
	defer b.Close()
	a := &redirector{to: b.URL + "/other"}
	srv := httptest.NewServer(a)
	defer srv.Close()
	s, logs := newSender(t, srv.URL+"/api/push/"+token, loopback)
	s.Send(context.Background(), Beat{Up: true, Msg: "m"})
	if a.n != 2 || len(target.got) != 0 {
		t.Errorf("redirecting host got %d requests, target %d; want 2 and 0", a.n, len(target.got))
	}
	out := logs.String()
	if !strings.Contains(out, "status 302") || strings.Contains(out, "/api/push") || strings.Contains(out, token) {
		t.Errorf("warning: %s", out)
	}
}

func TestNameResolvingToAPrivateAddressIsRefusedAtDial(t *testing.T) {
	rec := &recorder{codes: []int{200}}
	srv := httptest.NewServer(rec)
	defer srv.Close()
	port := strings.TrimPrefix(srv.URL, "http://127.0.0.1:")
	s, logs := newSender(t, "https://localhost:"+port+"/api/push/"+token, nil)
	s.Send(context.Background(), Beat{Up: true, Msg: "m"})
	if len(rec.got) != 0 {
		t.Errorf("%d requests reached loopback", len(rec.got))
	}
	if out := logs.String(); !strings.Contains(out, "refused host") || strings.Contains(out, token) {
		t.Errorf("log = %s", out)
	}
}
