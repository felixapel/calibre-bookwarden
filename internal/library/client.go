// Package library is the only code that talks to Calibre. It reads book
// records and format bytes through the content server, never the library
// folder (docs/calibre-protocol.md).
package library

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/icholy/digest"
)

// Errors a caller can act on. Everything else Calibre reports is a
// *CalibreError.
var (
	ErrUnreachable  = errors.New("calibre is unreachable")
	ErrIncompatible = errors.New("incompatible calibre")
	ErrAuth         = errors.New("calibre refused the credentials")
	ErrForbidden    = errors.New("the calibre account may not use this endpoint")
	ErrNoLibrary    = errors.New("calibre has no such library")
	ErrBookNotFound = errors.New("book not found in calibre")
)

// CalibreError is an error Calibre reported in its answer. Message is
// Calibre's one-line message; the traceback it sends alongside is dropped.
type CalibreError struct {
	Command, Message string
}

func (e *CalibreError) Error() string {
	return "library: calibre " + e.Command + ": " + e.Message
}

// Config says how to reach the content server. An empty LibraryID means the
// server's default library.
type Config struct {
	BaseURL        *url.URL
	User, Password string
	LibraryID      string
	// MetaTimeout bounds every request that is not a file or cover export
	// or a set-fields write (the caller bounds those); zero means
	// DefaultMetaTimeout.
	MetaTimeout time.Duration
}

// DefaultMetaTimeout is how long a metadata request may take before Calibre
// counts as unreachable.
const DefaultMetaTimeout = 60 * time.Second

// Client reads one Calibre library. It is safe for concurrent use.
type Client struct {
	cfg  Config
	http *http.Client

	mu        sync.Mutex
	libraryID string // last successful Check; only convenience reads use it
}

// maxJSON bounds a JSON answer. Listing only uuids stays far below it even
// for very large libraries.
const maxJSON = 64 << 20

// New returns a client. It makes no request until it is used.
func New(cfg Config) *Client {
	if cfg.MetaTimeout <= 0 {
		cfg.MetaTimeout = DefaultMetaTimeout
	}
	return &Client{
		cfg: cfg,
		// No client timeout: an export lasts as long as the file needs, and
		// the caller's context bounds it; every other request gets
		// MetaTimeout (withMetaTimeout). A redirect is never followed, so the
		// credentials never answer another host's challenge; the 3xx itself
		// then counts as unreachable.
		http: &http.Client{
			Transport:     &digest.Transport{Username: cfg.User, Password: cfg.Password},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// Check resolves the library and probes the list and export modules, so an
// incompatible Calibre is reported before any book is read. It returns the
// library id.
func (c *Client) Check(ctx context.Context) (string, error) {
	id, err := c.resolveLibrary(ctx)
	if err != nil {
		return "", err
	}
	if err := c.checkLibrary(ctx, id); err != nil {
		return "", err
	}
	c.mu.Lock()
	c.libraryID = id
	c.mu.Unlock()
	return id, nil
}

// CheckInLibrary probes a frozen library without changing convenience reads.
func (c *Client) CheckInLibrary(ctx context.Context, libraryID string) (string, error) {
	if libraryID == "" {
		return "", ErrNoLibrary
	}
	id, err := c.resolveLibraryID(ctx, libraryID)
	if err != nil {
		return "", err
	}
	if err := c.checkLibrary(ctx, id); err != nil {
		return "", err
	}
	return id, nil
}

func (c *Client) checkLibrary(ctx context.Context, id string) error {
	var probe listing
	if err := c.cdb(ctx, id, "list", []any{[]string{"uuid"}, "id", true, "id:0", 1}, &probe); err != nil {
		return err
	}
	return c.withMetaTimeout(ctx, "export", func(ctx context.Context) error {
		resp, err := c.do(ctx, "export", http.MethodPost, cdbURL(c.cfg.BaseURL, id, "export"), []byte(`["fmt",0,"EPUB",null]`), "application/x-msgpack")
		if err != nil {
			return err
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))
		_ = resp.Body.Close()
		return nil
	})
}

// withMetaTimeout runs f under the client's MetaTimeout. If that deadline
// passes while the caller's context is still alive, Calibre counts as
// unreachable, so a run waits for it instead of failing the book or hanging.
func (c *Client) withMetaTimeout(ctx context.Context, command string, f func(context.Context) error) error {
	mctx, cancel := context.WithTimeout(ctx, c.cfg.MetaTimeout)
	defer cancel()
	err := f(mctx)
	if err != nil && ctx.Err() == nil && errors.Is(mctx.Err(), context.DeadlineExceeded) && errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("library: %s: %w: no answer within %s", command, ErrUnreachable, c.cfg.MetaTimeout)
	}
	return err
}

// library returns the resolved library id, running Check if no check has
// succeeded yet.
func (c *Client) library(ctx context.Context) (string, error) {
	c.mu.Lock()
	id := c.libraryID
	c.mu.Unlock()
	if id != "" {
		return id, nil
	}
	return c.Check(ctx)
}

func (c *Client) resolveLibrary(ctx context.Context) (string, error) {
	return c.resolveLibraryID(ctx, c.cfg.LibraryID)
}

func (c *Client) resolveLibraryID(ctx context.Context, requested string) (string, error) {
	var info struct {
		LibraryMap     map[string]string `json:"library_map"`
		DefaultLibrary string            `json:"default_library"`
	}
	if err := c.fetchJSON(ctx, "library-info", http.MethodGet, c.cfg.BaseURL.JoinPath("ajax", "library-info"), nil, &info); err != nil {
		return "", err
	}
	id := requested
	if id == "" {
		id = info.DefaultLibrary
	}
	if _, ok := info.LibraryMap[id]; !ok || id == "" {
		return "", fmt.Errorf("library: %w: %q", ErrNoLibrary, id)
	}
	return id, nil
}

// cdb runs a cdb command and decodes its result into out. Calibre answers
// HTTP 200 with {"err": …} for failures, and with a bare string result for
// some refusals ("Unknown fields: …"); both become a *CalibreError.
func (c *Client) cdb(ctx context.Context, libraryID, command string, args, out any) error {
	body, err := json.Marshal(args)
	if err != nil {
		return fmt.Errorf("library: encode %s arguments: %w", command, err)
	}
	var env struct {
		Result json.RawMessage `json:"result"`
		Err    *string         `json:"err"`
	}
	if err := c.fetchJSON(ctx, command, http.MethodPost, cdbURL(c.cfg.BaseURL, libraryID, command), body, &env); err != nil {
		return err
	}
	if env.Err != nil {
		return &CalibreError{Command: command, Message: firstLine(*env.Err)}
	}
	var msg string
	if json.Unmarshal(env.Result, &msg) == nil {
		return &CalibreError{Command: command, Message: firstLine(msg)}
	}
	if len(env.Result) == 0 || string(env.Result) == "null" {
		return &CalibreError{Command: command, Message: "empty answer"}
	}
	if err := json.Unmarshal(env.Result, out); err != nil {
		return fmt.Errorf("library: decode %s result: %w", command, err)
	}
	return nil
}

func cdbURL(base *url.URL, libraryID, command string) *url.URL {
	u := base.JoinPath("cdb", "cmd", command, "0")
	u.RawQuery = url.Values{"library_id": {libraryID}}.Encode()
	return u
}

// fetchJSON sends one request and decodes the JSON answer into out, within
// MetaTimeout.
func (c *Client) fetchJSON(ctx context.Context, command, method string, u *url.URL, body []byte, out any) error {
	return c.withMetaTimeout(ctx, command, func(ctx context.Context) error {
		return c.fetchJSONOnce(ctx, command, method, u, body, out)
	})
}

func (c *Client) fetchJSONOnce(ctx context.Context, command, method string, u *url.URL, body []byte, out any) error {
	resp, err := c.do(ctx, command, method, u, body, "application/json")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxJSON+1))
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("library: read %s answer: %w", command, ctx.Err())
		}
		return fmt.Errorf("library: read %s answer: %w: %w", command, ErrUnreachable, err)
	}
	if len(data) > maxJSON {
		return fmt.Errorf("library: %s answer is larger than %d bytes", command, maxJSON)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("library: decode %s answer: %w", command, err)
	}
	return nil
}

// do sends one request and maps transport and status failures to the
// package's errors. On success the caller owns the response body.
func (c *Client) do(ctx context.Context, command, method string, u *url.URL, body []byte, accept string) (*http.Response, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), r)
	if err != nil {
		return nil, fmt.Errorf("library: %s: %w", command, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", accept)
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("library: %s: %w", command, ctx.Err())
		}
		return nil, fmt.Errorf("library: %s: %w: %w", command, ErrUnreachable, err)
	}
	if resp.StatusCode == http.StatusOK {
		return resp, nil
	}
	text, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<10))
	_ = resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, fmt.Errorf("library: %s: %w", command, ErrAuth)
	case resp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("library: %s: %w", command, ErrForbidden)
	case resp.StatusCode == http.StatusNotFound && bytes.Contains(text, []byte("is not available in version")):
		return nil, fmt.Errorf("library: %s: %w: %s", command, ErrIncompatible, firstLine(string(text)))
	case resp.StatusCode >= 500:
		return nil, fmt.Errorf("library: %s: %w: HTTP %d", command, ErrUnreachable, resp.StatusCode)
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		// Redirects are never followed (New); one means the URL points at
		// something other than the content server.
		return nil, fmt.Errorf("library: %s: %w: HTTP %d redirect refused", command, ErrUnreachable, resp.StatusCode)
	default:
		return nil, fmt.Errorf("library: %s: unexpected HTTP %d", command, resp.StatusCode)
	}
}

// firstLine keeps Calibre's messages to one bounded line.
func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
