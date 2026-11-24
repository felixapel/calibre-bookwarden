package agent

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"testing"
)

func prefixes(t *testing.T, ss ...string) []netip.Prefix {
	t.Helper()
	var out []netip.Prefix
	for _, s := range ss {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}

func TestDialControl(t *testing.T) {
	lan := prefixes(t, "10.0.0.122/32", "fd00::5/128")
	for _, tc := range []struct {
		name    string
		allow   []netip.Prefix
		private bool
		address string
		ok      bool
	}{
		{"public, no allowance", nil, false, "8.8.8.8:443", true},
		{"public v6, no allowance", nil, false, "[2606:4700::1111]:443", true},
		{"loopback", nil, false, "127.0.0.1:443", false},
		{"RFC 1918", nil, false, "10.0.0.122:8766", false},
		{"link local", nil, false, "169.254.169.254:80", false},
		{"ULA", nil, false, "[fd00::5]:80", false},
		{"CGNAT", nil, false, "100.64.0.1:80", false},
		{"benchmarking", nil, false, "198.18.0.1:80", false},
		{"this network", nil, false, "0.0.0.0:80", false},
		{"reserved", nil, false, "240.0.0.1:80", false},
		{"IETF assignments", nil, false, "192.0.0.9:80", false},
		{"documentation", nil, false, "203.0.113.5:80", false},
		{"documentation v6", nil, false, "[2001:db8::1]:80", false},
		{"NAT64 with private v4", nil, false, "[64:ff9b::a00:1]:80", false},
		{"v4-mapped private", nil, false, "[::ffff:10.0.0.1]:80", false},
		{"zoned 6to4", nil, false, "[2002:c0a8:7a::1%eth0]:80", false},
		{"zoned NAT64", nil, false, "[64:ff9b::c0a8:7a%eth0]:80", false},
		{"zoned documentation", nil, false, "[2001:db8::1%eth0]:80", false},
		{"zoned public", nil, false, "[2606:4700::1111%eth0]:80", false},
		{"Teredo", nil, false, "[2001:0:4136:e378:8000:63bf:3fff:fdd2]:80", false},
		{"IPv4-compatible", nil, false, "[::808:808]:80", false},
		{"site-local", nil, false, "[fec0::1]:80", false},
		{"not an address", nil, false, "proxy.lan:80", false},
		{"no port", nil, false, "8.8.8.8", false},
		{"tests allow private", nil, true, "127.0.0.1:1", true},
		{"exact v4 allowed", lan, false, "10.0.0.122:8766", true},
		{"exact v6 allowed", lan, false, "[fd00::5]:8766", true},
		{"v4-mapped form of an allowed v4", lan, false, "[::ffff:10.0.0.122]:8766", true},
		{"neighbour not allowed", lan, false, "10.0.0.123:8766", false},
		{"other private not allowed", lan, false, "10.0.0.1:8766", false},
		{"public not allowed once pinned", lan, false, "8.8.8.8:443", false},
		{"allowed special range", prefixes(t, "100.64.0.7/32"), false, "100.64.0.7:80", true},
		{"allowed loopback", prefixes(t, "127.0.0.1/32"), false, "127.0.0.1:80", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := dialControl(tc.allow, tc.private)("tcp", tc.address, nil)
			if (err == nil) != tc.ok {
				t.Fatalf("control(%s) = %v, want ok=%v", tc.address, err, tc.ok)
			}
			if err != nil && !errors.Is(err, errRefusedHost) {
				t.Fatalf("error %v is not errRefusedHost", err)
			}
		})
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSeatClientHostAllowance(t *testing.T) {
	for _, tc := range []struct {
		name, seatURL, requestURL string
		wantErr                   bool
	}{
		{"https default port", "https://api.example.com/v1", "https://api.example.com/v1/x", false},
		{"http default port", "http://proxy.lan/v1", "http://proxy.lan/v1/x", false},
		{"explicit port", "http://10.0.0.122:8766/v1", "http://10.0.0.122:8766/v1/x", false},
		{"other host", "https://api.example.com/v1", "https://other.example.com/v1", true},
		{"other port", "http://10.0.0.122:8766/v1", "http://10.0.0.122:9999/v1", true},
		{"downgrade to http", "https://api.example.com/v1", "http://api.example.com/v1", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host, err := seatHost(tc.seatURL)
			if err != nil {
				t.Fatal(err)
			}
			rt := allowList{hosts: []string{host}, next: roundTripperFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
			})}
			req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, tc.requestURL, nil)
			resp, err := rt.RoundTrip(req)
			if resp != nil {
				_ = resp.Body.Close()
			}
			if tc.wantErr != errors.Is(err, errRefusedHost) || (!tc.wantErr && err != nil) {
				t.Fatalf("RoundTrip = %v, want refusal %v", err, tc.wantErr)
			}
		})
	}
}

// Two seats' clients keep their allowances apart: the one without the
// allowance still refuses the address the other may dial.
func TestSeatClientsDoNotShareAllowance(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "{}") }))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	// httptest listens on loopback, so loopback stands in for the LAN.
	with, err := NewSeatClient(srv.URL, prefixes(t, "127.0.0.1/32"))
	if err != nil {
		t.Fatal(err)
	}
	// NewSeatClient refuses plain http without an allowance, so build the
	// no-allowance client the way it would be for an https seat.
	without := newClient([]string{u.Host}, nil, false)
	other, err := NewSeatClient(srv.URL, prefixes(t, "10.0.0.122/32"))
	if err != nil {
		t.Fatal(err)
	}
	get := func(hc *http.Client) error {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
		resp, err := hc.Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
		return err
	}
	if err := get(with); err != nil {
		t.Fatalf("seat with the allowance: %v", err)
	}
	if err := get(without); !errors.Is(err, errRefusedHost) {
		t.Fatalf("seat without it: %v", err)
	}
	if err := get(other); !errors.Is(err, errRefusedHost) {
		t.Fatalf("seat with another address: %v", err)
	}
}

func TestNewSeatClientRejects(t *testing.T) {
	for _, u := range []string{"", "://x", "ftp://x/y", "https://", "http://10.0.0.122:8766/v1", "http://api.example.com/v1"} {
		if _, err := NewSeatClient(u, nil); err == nil {
			t.Errorf("NewSeatClient(%q) accepted it", u)
		}
	}
}
