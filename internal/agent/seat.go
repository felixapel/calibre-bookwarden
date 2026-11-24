package agent

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Seat is one judge on the panel.
type Seat interface {
	Name() string
	Model() string
	// Judge sends the brief and one packet and returns the answer's text.
	Judge(ctx context.Context, brief, packet string) (Reply, error)
}

// Reply is a judge's answer and what it cost. Model is the model the provider
// says answered, which may name a dated version of the one asked for;
// ReasoningTokens is the part of OutputTokens spent on reasoning, when the
// provider reports it.
type Reply struct {
	Text                      string
	InputTokens, OutputTokens int64
	ReasoningTokens           int64
	Model                     string
}

// SeatConfig configures one seat. Wire is "anthropic" (the Messages API) or
// "openai" (chat completions, as opencode-go and Gemini serve them).
//
// Family names the model's vendor family (ADR-033). AllowCIDR lists the exact
// addresses the seat may dial even though they are private or the URL is
// plain http; it is the seat's only egress allowance.
//
// Keyless allows an empty Key, for a LAN gateway that holds the provider keys
// itself (Bifrost); a Key set anyway is sent as the gateway's key.
// SessionHeader, when set, names a header that carries one random id per seat
// on every request: opencode refuses calls without a session, and Bifrost
// forwards x-bf-eh-* headers to the provider.
// NoContentLog sends x-bf-disable-content-logging: true, asking Bifrost to
// keep only metadata of the request, not the book text or the answer; it
// takes effect once Bifrost allows per-request content overrides.
type SeatConfig struct {
	Name, Wire, URL, Model, Key, Effort string
	Family                              string
	AllowCIDR                           []netip.Prefix
	Keyless                             bool
	SessionHeader                       string
	NoContentLog                        bool
}

const (
	maxBody    = 1 << 20
	maxTokens  = 16000
	maxRetries = 3
	maxWait    = time.Minute
	retryWait  = 5 * time.Second
)

// wait sleeps between retries; tests replace it.
var wait = func(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// waitFits reports whether a wait of d ends before ctx's deadline. A retry
// that can't start in time would end the call as a timeout and hide why it
// failed: a 429 must stay a quota exhaustion, recorded.
func waitFits(ctx context.Context, d time.Duration) bool {
	dl, ok := ctx.Deadline()
	return !ok || time.Until(dl) > d
}

// seatError is a failed call, named by a code without the request, the
// answer or the key.
type seatError struct {
	seat, code string
	retryAfter time.Duration // how long to wait before retrying
	retryable  bool
	// quotaRetry is the provider's Retry-After as sent, uncapped; zero if
	// none came.
	quotaRetry time.Duration
}

func (e *seatError) Error() string { return "agent: seat " + e.seat + ": " + e.code }

// quotaWords mark a 402 or 403 body as a quota problem rather than a bad key
// (ADR-033). The list is closed; matching is case-insensitive.
var quotaWords = []string{"quota", "credits", "billing", "rate limit"}

// quotaBody reports whether a JSON error body (Content-Type json) names a
// quota problem in error.message, error.type, error.code or a top-level
// message. An HTML page or plain text that happens to say "billing" doesn't
// count.
func quotaBody(contentType string, raw []byte) bool {
	if !strings.Contains(strings.ToLower(contentType), "json") {
		return false
	}
	var b struct {
		Message any `json:"message"`
		Error   struct {
			Message, Type, Code any
		} `json:"error"`
	}
	if json.Unmarshal(raw, &b) != nil {
		return false
	}
	for _, f := range []any{b.Message, b.Error.Message, b.Error.Type, b.Error.Code} {
		v, ok := f.(string)
		if !ok {
			continue
		}
		v = strings.ToLower(v)
		if slices.ContainsFunc(quotaWords, func(w string) bool { return strings.Contains(v, w) }) {
			return true
		}
	}
	return false
}

// ErrorCode names why a judge gave no verdict: timeout, quota_exhausted,
// http_4xx, http_5xx, bad_answer, refused_host or transport. Empty for nil.
func ErrorCode(err error) string {
	var se *seatError
	switch {
	case err == nil:
		return ""
	case errors.As(err, &se):
		return se.code
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, errRefusedHost):
		return "refused_host"
	case errors.Is(err, ErrBadAnswer):
		return "bad_answer"
	}
	return "transport"
}

// CallRecorder counts judge calls for the metrics endpoint: one per Judge call,
// with the final error code, empty on success.
type CallRecorder interface {
	AgentCall(seat, code string)
}

type httpSeat struct {
	cfg     SeatConfig
	hc      *http.Client
	quotas  *Quotas
	calls   CallRecorder
	session string
}

// sessionHeaderOK reports whether name is a plain "x-" header token that is
// not one the seat sets itself: no standard header can be overridden.
func sessionHeaderOK(name string) bool {
	rest, ok := strings.CutPrefix(strings.ToLower(name), "x-")
	return ok && rest != "" && strings.ToLower(name) != "x-api-key" &&
		strings.Trim(rest, "-0123456789abcdefghijklmnopqrstuvwxyz") == ""
}

// SeatOption configures NewSeat.
type SeatOption func(*httpSeat)

// WithQuotas makes the seat record its quota exhaustions in q.
func WithQuotas(q *Quotas) SeatOption { return func(s *httpSeat) { s.quotas = q } }

// WithCalls makes the seat count each Judge call in r.
func WithCalls(r CallRecorder) SeatOption { return func(s *httpSeat) { s.calls = r } }

// NewSeat returns a seat that calls cfg.URL with hc.
func NewSeat(cfg SeatConfig, hc *http.Client, opts ...SeatOption) (Seat, error) {
	u, err := url.Parse(cfg.URL)
	switch {
	case cfg.Wire != "anthropic" && cfg.Wire != "openai":
		return nil, fmt.Errorf("agent: seat %s: unknown wire format %q", cfg.Name, cfg.Wire)
	case cfg.URL == "" || err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "":
		return nil, fmt.Errorf("agent: seat %s: invalid URL", cfg.Name)
	case u != nil && u.Scheme == "http" && len(cfg.AllowCIDR) == 0:
		return nil, fmt.Errorf("agent: seat %s: plain http needs an address allowance", cfg.Name)
	case cfg.Model == "":
		return nil, fmt.Errorf("agent: seat %s: no model", cfg.Name)
	case cfg.Key == "" && !cfg.Keyless:
		return nil, fmt.Errorf("agent: seat %s: no key", cfg.Name)
	case cfg.SessionHeader != "" && !sessionHeaderOK(cfg.SessionHeader):
		return nil, fmt.Errorf("agent: seat %s: invalid session header name", cfg.Name)
	}
	s := &httpSeat{cfg: cfg, hc: hc}
	if cfg.SessionHeader != "" {
		var b [16]byte
		_, _ = rand.Read(b[:]) // crypto/rand.Read never fails
		s.session = "bookwarden-" + hex.EncodeToString(b[:])
	}
	for _, o := range opts {
		o(s)
	}
	return s, nil
}

func (s *httpSeat) Name() string  { return s.cfg.Name }
func (s *httpSeat) Model() string { return s.cfg.Model }

func (s *httpSeat) Judge(ctx context.Context, brief, packet string) (Reply, error) {
	reply, err := s.judge(ctx, brief, packet)
	// A cancelled context is a drain or an abandoned review, not a seat failure;
	// an expired deadline is a real timeout and counts.
	if s.calls != nil && !errors.Is(ctx.Err(), context.Canceled) {
		s.calls.AgentCall(s.cfg.Name, ErrorCode(err))
	}
	return reply, err
}

func (s *httpSeat) judge(ctx context.Context, brief, packet string) (Reply, error) {
	body, err := s.request(brief, packet)
	if err != nil {
		return Reply{}, err
	}
	for attempt := 0; ; attempt++ {
		reply, err := s.once(ctx, body)
		var se *seatError
		if err == nil {
			s.quotas.Clear(s.cfg.Name)
			return reply, nil
		}
		if !errors.As(err, &se) {
			return reply, err
		}
		if !se.retryable || attempt == maxRetries || se.quotaRetry > maxWait || !waitFits(ctx, se.retryAfter) {
			if se.code == "quota_exhausted" {
				s.quotas.Record(s.cfg.Name, time.Time{}, se.quotaRetry)
			}
			return reply, err
		}
		if werr := wait(ctx, se.retryAfter); werr != nil {
			return Reply{}, werr
		}
	}
}

func (s *httpSeat) request(brief, packet string) ([]byte, error) {
	var v any
	if s.cfg.Wire == "anthropic" {
		m := map[string]any{
			"model": s.cfg.Model, "max_tokens": maxTokens, "system": brief,
			"messages": []map[string]string{{"role": "user", "content": packet}},
		}
		if s.cfg.Effort != "" {
			m["output_config"] = map[string]string{"effort": s.cfg.Effort}
		}
		v = m
	} else {
		m := map[string]any{
			"model": s.cfg.Model, "max_tokens": maxTokens,
			"messages": []map[string]string{{"role": "system", "content": brief}, {"role": "user", "content": packet}},
		}
		if s.cfg.Effort != "" {
			m["reasoning_effort"] = s.cfg.Effort
		}
		v = m
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("agent: seat %s: encode request: %w", s.cfg.Name, err)
	}
	return raw, nil
}

func (s *httpSeat) once(ctx context.Context, body []byte) (Reply, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.URL, bytes.NewReader(body))
	if err != nil {
		return Reply{}, &seatError{seat: s.cfg.Name, code: "transport"}
	}
	req.Header.Set("Content-Type", "application/json")
	switch {
	case s.cfg.Wire == "anthropic":
		req.Header.Set("anthropic-version", "2023-06-01")
		if s.cfg.Key != "" {
			req.Header.Set("x-api-key", s.cfg.Key)
		}
	case s.cfg.Key != "":
		req.Header.Set("Authorization", "Bearer "+s.cfg.Key)
	}
	if s.session != "" {
		req.Header.Set(s.cfg.SessionHeader, s.session)
	}
	if s.cfg.NoContentLog {
		req.Header.Set("x-bf-disable-content-logging", "true")
	}
	resp, err := s.hc.Do(req)
	if err != nil {
		switch {
		case ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded):
			return Reply{}, &seatError{seat: s.cfg.Name, code: "timeout"}
		case errors.Is(err, errRefusedHost):
			return Reply{}, &seatError{seat: s.cfg.Name, code: "refused_host"}
		}
		return Reply{}, &seatError{seat: s.cfg.Name, code: "transport"}
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		// Retried a few times, as a rate limit often clears. judge records
		// the exhaustion when it stops: after the last retry, at once for a
		// Retry-After beyond maxWait (a usage limit, not a rate limit), or
		// when the next wait would outlast the call's deadline.
		h := resp.Header.Get("Retry-After")
		return Reply{}, &seatError{seat: s.cfg.Name, code: "quota_exhausted", retryable: true, retryAfter: retryAfter(h), quotaRetry: reportedRetry(h)}
	case (resp.StatusCode == http.StatusPaymentRequired || resp.StatusCode == http.StatusForbidden) && quotaBody(resp.Header.Get("Content-Type"), raw):
		return Reply{}, &seatError{seat: s.cfg.Name, code: "quota_exhausted", quotaRetry: reportedRetry(resp.Header.Get("Retry-After"))}
	case resp.StatusCode >= 500:
		return Reply{}, &seatError{seat: s.cfg.Name, code: "http_5xx", retryable: true, retryAfter: retryWait}
	case resp.StatusCode != http.StatusOK:
		return Reply{}, &seatError{seat: s.cfg.Name, code: "http_4xx"}
	case err != nil:
		return Reply{}, &seatError{seat: s.cfg.Name, code: "transport"}
	case len(raw) > maxBody:
		return Reply{}, &seatError{seat: s.cfg.Name, code: "bad_answer"}
	}
	reply, ok := s.parse(raw)
	if !ok {
		return Reply{}, &seatError{seat: s.cfg.Name, code: "bad_answer"}
	}
	return reply, nil
}

func (s *httpSeat) parse(raw []byte) (Reply, bool) {
	if s.cfg.Wire == "anthropic" {
		var a struct {
			Model   string                        `json:"model"`
			Content []struct{ Type, Text string } `json:"content"`
			Usage   struct {
				In  int64 `json:"input_tokens"`
				Out int64 `json:"output_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal(raw, &a) != nil {
			return Reply{}, false
		}
		var b strings.Builder
		for _, c := range a.Content {
			if c.Type == "text" {
				b.WriteString(c.Text)
			}
		}
		return Reply{Text: b.String(), InputTokens: a.Usage.In, OutputTokens: a.Usage.Out, Model: a.Model}, b.Len() > 0
	}
	var o struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			In      int64 `json:"prompt_tokens"`
			Out     int64 `json:"completion_tokens"`
			Details struct {
				Reasoning int64 `json:"reasoning_tokens"`
			} `json:"completion_tokens_details"`
		} `json:"usage"`
	}
	if json.Unmarshal(raw, &o) != nil || len(o.Choices) == 0 || o.Choices[0].Message.Content == "" {
		return Reply{}, false
	}
	return Reply{
		Text: o.Choices[0].Message.Content, InputTokens: o.Usage.In, OutputTokens: o.Usage.Out,
		ReasoningTokens: o.Usage.Details.Reasoning, Model: o.Model,
	}, true
}

// maxReportedRetry bounds a Retry-After kept for Settings.
const maxReportedRetry = 30 * 24 * time.Hour

// reportedRetry reads a Retry-After header in seconds as sent, or zero when
// it is absent or not a whole number.
func reportedRetry(v string) time.Duration {
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return min(time.Duration(min(n, int64(maxReportedRetry/time.Second)))*time.Second, maxReportedRetry)
}

// retryAfter reads a Retry-After header in seconds, capped at a minute.
func retryAfter(v string) time.Duration {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 0 {
		return retryWait
	}
	return time.Duration(min(n, int(maxWait/time.Second))) * time.Second
}
