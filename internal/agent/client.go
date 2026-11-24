package agent

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"syscall"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/netguard"
)

// callTimeout bounds one judge call; reasoning models can think for minutes.
const callTimeout = 10 * time.Minute

// errRefusedHost: the request went to a host that isn't a configured seat,
// or to a private address.
var errRefusedHost = errors.New("agent: refused host")

type allowList struct {
	hosts []string
	next  http.RoundTripper
}

func (a allowList) RoundTrip(r *http.Request) (*http.Response, error) {
	if !slices.Contains(a.hosts, hostPort(r.URL.Scheme, r.URL.Host)) {
		return nil, fmt.Errorf("%w: %s", errRefusedHost, r.URL.Hostname())
	}
	return a.next.RoundTrip(r)
}

func hostPort(scheme, host string) string {
	if _, _, err := net.SplitHostPort(host); err == nil {
		return host
	}
	if scheme == "http" {
		return net.JoinHostPort(host, "80")
	}
	return net.JoinHostPort(host, "443")
}

// NewHTTPClient returns a client that sends requests only to allowHosts
// (host or host:port, https by default), never to private or special-purpose
// addresses unless allowPrivate (tests only), and never follows redirects.
func NewHTTPClient(allowHosts []string, allowPrivate bool) *http.Client {
	hosts := make([]string, 0, len(allowHosts))
	for _, h := range allowHosts {
		hosts = append(hosts, hostPort("https", h))
	}
	return newClient(hosts, nil, allowPrivate)
}

// seatHost is the host:port a seat's URL names.
func seatHost(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" {
		return "", errors.New("agent: invalid seat URL")
	}
	return hostPort(u.Scheme, u.Host), nil
}

// NewSeatClient returns the client for one seat (ADR-033). It talks only to
// the host:port of rawURL and never follows redirects. Without allow it
// refuses private and special-purpose addresses. With allow, every address it
// dials must be inside allow, and nothing else is reachable.
func NewSeatClient(rawURL string, allow []netip.Prefix) (*http.Client, error) {
	host, err := seatHost(rawURL)
	if err != nil {
		return nil, err
	}
	if u, _ := url.Parse(rawURL); u.Scheme == "http" && len(allow) == 0 {
		return nil, errors.New("agent: plain http needs an address allowance")
	}
	return newClient([]string{host}, allow, false), nil
}

// dialControl returns the dial-time check, which sees the resolved address of
// every connection, so a name that resolves somewhere forbidden is refused.
func dialControl(allow []netip.Prefix, allowPrivate bool) func(network, address string, c syscall.RawConn) error {
	return netguard.DialControl(allow, allowPrivate, errRefusedHost)
}

func newClient(hosts []string, allow []netip.Prefix, allowPrivate bool) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, Control: dialControl(allow, allowPrivate)}
	transport := &http.Transport{
		DialContext:           dialer.DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: callTimeout,
		MaxIdleConnsPerHost:   4,
		ForceAttemptHTTP2:     true,
	}
	return &http.Client{
		Transport: allowList{hosts: hosts, next: transport},
		Timeout:   callTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}
