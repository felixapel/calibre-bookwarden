// Package heartbeat tells an Uptime Kuma push monitor how each run ended
// (ADR-041). Kuma alerts when no push arrives within its interval, or when a
// push says down. The push URL holds a secret token, so nothing here logs or
// returns it: only scheme://host is ever shown.
package heartbeat

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/netguard"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

const (
	requestTimeout = 10 * time.Second
	retryWait      = 5 * time.Second
	maxMessage     = 200
)

var errRefusedHost = errors.New("heartbeat: refused host")

// Beat is one push: Up false is Kuma's down.
type Beat struct {
	Up  bool
	Msg string
}

// Sender pushes beats to one URL.
type Sender struct {
	target    *url.URL
	host      string // scheme://host, the only part that may be logged
	client    *http.Client
	logger    *slog.Logger
	retryWait time.Duration
}

// New returns a Sender for rawURL. It dials only the addresses in allow; with
// no allowance it refuses private and special-purpose addresses, like a seat.
func New(rawURL string, allow []netip.Prefix, logger *slog.Logger) (*Sender, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, errors.New("heartbeat: invalid URL") // never echo it: it holds the token
	}
	if u.Scheme == "http" && len(allow) == 0 {
		return nil, errors.New("heartbeat: plain http needs an address allowance")
	}
	dialer := &net.Dialer{Timeout: requestTimeout, Control: netguard.DialControl(allow, false, errRefusedHost)}
	return &Sender{
		target:    u,
		host:      u.Scheme + "://" + u.Host,
		logger:    logger,
		retryWait: retryWait,
		client: &http.Client{
			Timeout:   requestTimeout,
			Transport: &http.Transport{DialContext: dialer.DialContext, TLSHandshakeTimeout: requestTimeout, DisableKeepAlives: true},
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

// Send pushes b: one retry after five seconds, then a warning (host only) and
// give up. It returns early when ctx ends, so a shutdown never waits for it.
func (s *Sender) Send(ctx context.Context, b Beat) {
	err := s.once(ctx, b)
	if err != nil && ctx.Err() == nil {
		select {
		case <-ctx.Done():
		case <-time.After(s.retryWait):
			err = s.once(ctx, b)
		}
	}
	if err != nil && ctx.Err() == nil {
		s.logger.Warn("heartbeat not delivered", "host", s.host, "error", err)
	}
}

func (s *Sender) once(ctx context.Context, b Beat) error {
	status := "up"
	if !b.Up {
		status = "down"
	}
	u := *s.target
	q := u.Query()
	q.Set("status", status)
	q.Set("msg", b.Msg)
	q.Set("ping", "")
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return errors.New("building the request failed")
	}
	resp, err := s.client.Do(req)
	if err != nil {
		// A *url.Error carries the full URL, token included; keep the cause.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

// Result is what a run's beat is decided from. Broken and SealsChecked come
// from the run's seal check; Calls and Failed from its sealed packages (Calls
// 0 means the run made no extractor calls).
type Result struct {
	RunID         int64
	Status        verify.RunStatus
	SealsChecked  bool
	Books, Broken int
	Calls, Failed int
}

// minRateCalls is how many extractor calls a run needs before its failure
// rate can turn the beat down (ADR-041 amendment).
const minRateCalls = 20

// Beat decides the push. It is down when the run failed or was cancelled, when
// the seal check found broken seals or could not run, or when more than 5 % of
// the run's extractor entries were not answered. The rate counts only from
// minRateCalls calls, so one unanswered call of a small run isn't an outage;
// below that it is still reported. The message never carries book text.
func (r Result) Beat() Beat {
	switch r.Status {
	case verify.RunFailed, verify.RunCancelled:
		return Beat{Msg: fmt.Sprintf("run %d %s", r.RunID, r.Status)}
	}
	msg := fmt.Sprintf("run %d %s: ", r.RunID, r.Status)
	var up bool
	if r.SealsChecked {
		msg += fmt.Sprintf("%d books, %d broken seals", r.Books, r.Broken)
		up = r.Broken == 0
	} else {
		msg += "seals not checked"
	}
	if r.Calls > 0 {
		msg += fmt.Sprintf(", extractor failures %.2f %% (%d/%d not answered)", 100*float64(r.Failed)/float64(r.Calls), r.Failed, r.Calls)
		if r.Calls >= minRateCalls {
			up = up && r.Failed*20 <= r.Calls
		}
	}
	if len(msg) > maxMessage {
		msg = msg[:maxMessage]
	}
	return Beat{Up: up, Msg: msg}
}
