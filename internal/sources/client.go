// Package sources looks books up by ISBN in DNB, BNE, Open Library and Google
// Books (spec section 9, ADR-019). Every request goes through Client, which
// enforces the outbound rules; adapters turn answers into validated records.
package sources

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/netguard"
)

// ErrUnavailable is any failure to get a usable answer: a refused host,
// address or redirect, a network error, an HTTP error after retries, or a body
// over its cap. Its text never carries the URL.
var ErrUnavailable = errors.New("sources: unavailable")

// ErrUnauthorized marks an ErrUnavailable caused by a 401 answer: the source
// refused our credentials, which retrying can't fix.
var ErrUnauthorized = errors.New("sources: unauthorized")

// Fetcher performs one paced, retried request: a GET, or a POST for the
// sources that take a query in the body (Wikidata SPARQL, Hardcover GraphQL).
// Both go through the same allowlist, pacing, budget and address rules; a
// POST never follows a redirect.
type Fetcher interface {
	Get(ctx context.Context, rawURL string, header http.Header, maxBody int64) (status int, body []byte, err error)
	Post(ctx context.Context, rawURL string, header http.Header, body []byte, maxBody int64) (status int, resp []byte, err error)
}

// Hosts are the only hosts the production client may reach, one per
// adapter in result order (ADR-019, ADR-035).
var Hosts = []string{"services.dnb.de", "catalogo.bne.es", "openlibrary.org", "www.googleapis.com",
	"query.wikidata.org", "api.hardcover.app"}

// HostPace slows the production client down for hosts with a documented rate
// limit. Hardcover allows 60 requests a minute with a burst of 10; 1.1 s
// between requests stays under it (ADR-035).
var HostPace = map[string]time.Duration{"api.hardcover.app": 1100 * time.Millisecond}

// UserAgent is the User-Agent every source request carries.
func UserAgent(version string) string {
	return "bookwarden/" + version + " (+https://github.com/felixapel/calibre-bookwarden)"
}

// ClientOptions configure a Client. Zero durations take the defaults.
type ClientOptions struct {
	UserAgent  string
	AllowHosts []string
	// AllowPrivate permits loopback and private addresses. Tests only.
	AllowPrivate bool
	// AllowHTTP permits plain http. Tests only.
	AllowHTTP bool
	Pace      time.Duration // between requests to one host; default 1 s
	// HostPace overrides Pace for a hostname (no port), for sources with a
	// stricter published rate limit.
	HostPace  map[string]time.Duration
	Budget    time.Duration // per Get, retries included; default 30 s
	Backoff   time.Duration // first retry wait; default 1 s
	TLSConfig *tls.Config   // tests only
}

const (
	attempts     = 3
	maxRedirects = 3
)

// Client implements Fetcher under the spec's client rules.
type Client struct {
	opts  ClientOptions
	http  *http.Client
	post  *http.Client // never follows a redirect
	mu    sync.Mutex
	next  map[string]time.Time // per host:port, the earliest next request
	held  map[string]time.Time // per host:port, until when a Retry-After holds it
	hosts map[string]bool
}

var _ Fetcher = (*Client)(nil)

// refusal is a non-retryable failure with a static reason.
type refusal string

func (r refusal) Error() string { return string(r) }

// NewClient returns a client with its own transport.
func NewClient(o ClientOptions) *Client {
	if o.Pace == 0 {
		o.Pace = time.Second
	}
	if o.Budget == 0 {
		o.Budget = 30 * time.Second
	}
	if o.Backoff == 0 {
		o.Backoff = time.Second
	}
	c := &Client{opts: o, next: map[string]time.Time{}, held: map[string]time.Time{}, hosts: map[string]bool{}}
	for _, h := range o.AllowHosts {
		c.hosts[strings.ToLower(h)] = true
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second, Control: c.checkAddress}
	c.http = &http.Client{
		Transport: &http.Transport{
			Proxy:                 nil,
			DialContext:           dialer.DialContext,
			TLSClientConfig:       o.TLSConfig,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 20 * time.Second,
			MaxIdleConnsPerHost:   2,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > maxRedirects {
				return refusal("refused redirect: too many")
			}
			if req.URL.Host != via[0].URL.Host || c.checkURL(req.URL) != nil {
				return refusal("refused redirect")
			}
			req.Header.Del("Referer")
			return nil
		},
	}
	post := *c.http
	post.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	c.post = &post
	return c
}

func (c *Client) checkURL(u *url.URL) error {
	switch {
	case u.Scheme != "https" && (!c.opts.AllowHTTP || u.Scheme != "http"):
		return refusal("refused scheme")
	case !c.hosts[strings.ToLower(u.Hostname())]:
		return refusal("refused host")
	case u.User != nil:
		return refusal("refused URL")
	}
	return nil
}

// checkAddress runs on the resolved address of every connection, so a name
// that resolves to a private address is refused too.
func (c *Client) checkAddress(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return refusal("refused address")
	}
	a := ap.Addr().Unmap()
	if !c.opts.AllowPrivate && netguard.Refused(a) {
		return refusal("refused address")
	}
	return nil
}

// pace waits until host may be asked again. A request whose context ends
// before then (a throttled host, see hold) fails at once instead of
// sleeping out its budget, and one whose turn came during a hold that
// started while it waited takes a new turn after the hold.
func (c *Client) pace(ctx context.Context, u *url.URL) error {
	host, pace := u.Host, c.opts.Pace
	if p, ok := c.opts.HostPace[strings.ToLower(u.Hostname())]; ok {
		pace = p
	}
	for {
		c.mu.Lock()
		now := time.Now()
		at := c.next[host]
		if at.Before(now) {
			at = now
		}
		if dl, ok := ctx.Deadline(); ok && dl.Before(at) {
			c.mu.Unlock()
			return context.DeadlineExceeded
		}
		c.next[host] = at.Add(pace)
		c.mu.Unlock()
		if err := sleep(ctx, at.Sub(now)); err != nil {
			return err
		}
		c.mu.Lock()
		held := time.Now().Before(c.held[host])
		c.mu.Unlock()
		if !held {
			return nil
		}
	}
}

func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func unavailable(reason string) error { return fmt.Errorf("%w: %s", ErrUnavailable, reason) }

// Get fetches rawURL. It returns the status for 2xx and 404; anything else,
// after up to 3 attempts for network errors, 429 and 5xx, is ErrUnavailable.
func (c *Client) Get(ctx context.Context, rawURL string, header http.Header, maxBody int64) (int, []byte, error) {
	return c.do(ctx, http.MethodGet, rawURL, header, nil, maxBody)
}

// Post sends body to rawURL under Get's rules, except that it follows no
// redirect: a 3xx answer is ErrUnavailable. The caller sets Content-Type.
func (c *Client) Post(ctx context.Context, rawURL string, header http.Header, body []byte, maxBody int64) (int, []byte, error) {
	return c.do(ctx, http.MethodPost, rawURL, header, body, maxBody)
}

func (c *Client) do(ctx context.Context, method, rawURL string, header http.Header, reqBody []byte, maxBody int64) (int, []byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return 0, nil, unavailable("bad URL")
	}
	if err := c.checkURL(u); err != nil {
		return 0, nil, unavailable(err.Error())
	}
	ctx, cancel := context.WithTimeout(ctx, c.opts.Budget)
	defer cancel()
	var reason string
	for attempt := range attempts {
		if err := c.pace(ctx, u); err != nil {
			return 0, nil, unavailable("out of time")
		}
		status, body, retryAfter, err := c.once(ctx, method, u, header, reqBody, maxBody)
		// A throttled host is left alone by every request for as long as
		// it asked, not only by this one.
		held := err == nil && (status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable) && retryAfter > 0
		if held {
			c.hold(u.Host, retryAfter)
		}
		var ref refusal
		switch {
		case errors.As(err, &ref):
			return 0, nil, unavailable(ref.Error())
		case err == nil && (status/100 == 2 || status == http.StatusNotFound):
			return status, body, nil
		case err == nil && status != http.StatusTooManyRequests && status < 500:
			if status == http.StatusUnauthorized {
				return 0, nil, fmt.Errorf("%w: status 401: %w", ErrUnavailable, ErrUnauthorized)
			}
			return 0, nil, unavailable("status " + strconv.Itoa(status))
		case err != nil:
			reason = "network error"
		default:
			reason = "status " + strconv.Itoa(status)
		}
		if attempt == attempts-1 {
			break
		}
		wait := c.opts.Backoff << attempt
		wait += time.Duration((rand.Float64()*0.4 - 0.2) * float64(wait)) //nolint:gosec // jitter, not security
		if retryAfter > 0 {
			wait = retryAfter
		}
		if dl, ok := ctx.Deadline(); ok && time.Until(dl) < wait {
			return 0, nil, unavailable(reason + ": retry would exceed the budget")
		}
		if held {
			// pace waits the hold out and gives this request the first
			// turn after it, ahead of requests that came later.
			continue
		}
		if err := sleep(ctx, wait); err != nil {
			return 0, nil, unavailable("out of time")
		}
	}
	return 0, nil, unavailable(reason)
}

func (c *Client) once(ctx context.Context, method string, u *url.URL, header http.Header, reqBody []byte, maxBody int64) (int, []byte, time.Duration, error) {
	var rd io.Reader
	if reqBody != nil {
		rd = bytes.NewReader(reqBody)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rd)
	if err != nil {
		return 0, nil, 0, refusal("bad request")
	}
	for k, v := range header {
		req.Header[k] = slices.Clone(v)
	}
	req.Header.Set("User-Agent", c.opts.UserAgent)
	hc := c.http
	if method == http.MethodPost {
		hc = c.post
	}
	resp, err := hc.Do(req)
	if err != nil {
		var ref refusal
		if errors.As(err, &ref) {
			return 0, nil, 0, ref
		}
		return 0, nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return resp.StatusCode, nil, retryAfter(resp.Header.Get("Retry-After")), nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return 0, nil, 0, err
	}
	if int64(len(body)) > maxBody {
		return 0, nil, 0, refusal("body too large")
	}
	return resp.StatusCode, body, 0, nil
}

// retryAfter parses seconds or an HTTP date; 0 means absent or unusable.
func retryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if s, err := strconv.Atoi(v); err == nil && s >= 0 {
		return time.Duration(s) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(time.Until(t), 0)
	}
	return 0
}

// hold keeps every request from host for d from now, capped at half the
// per-Get budget, so a request that waits the hold out still has time to be
// answered.
func (c *Client) hold(host string, d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	until := time.Now().Add(min(d, c.opts.Budget/2))
	if until.After(c.held[host]) {
		c.held[host] = until
	}
	if until.After(c.next[host]) {
		c.next[host] = until
	}
}
