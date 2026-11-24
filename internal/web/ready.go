package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// ReadyCheck is one condition of /readyz. Check returns nil when it holds;
// its error is logged and never sent to the caller.
type ReadyCheck struct {
	Name  string
	Check func(context.Context) error
}

// defaultReadyTimeout bounds each readiness check.
const defaultReadyTimeout = 2 * time.Second

// isProbePath reports whether path is a probe or scrape endpoint: those are
// exempt from the allowed-host check (a monitor may use any address of the
// container) and from the setup redirect, and aren't logged per request.
// /healthz and /readyz need no credentials; /metrics has its own bearer
// token, which no browser sends by itself, so rebinding can't use it.
func isProbePath(path string) bool {
	return path == "/healthz" || path == "/readyz" || path == "/metrics"
}

// readyz answers 200 when every readiness check holds and 503 with the names
// of the failing ones otherwise. /healthz stays liveness only.
func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	timeout := s.opts.ReadyTimeout
	if timeout <= 0 {
		timeout = defaultReadyTimeout
	}
	// The checks run at once, each against the same deadline. One that ignores
	// its context is given up on at the deadline, not waited for.
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	results := make([]chan error, len(s.opts.Ready))
	for i, c := range s.opts.Ready {
		results[i] = make(chan error, 1)
		go func() {
			defer func() {
				if v := recover(); v != nil {
					results[i] <- fmt.Errorf("panic: %v", v)
				}
			}()
			results[i] <- c.Check(ctx)
		}()
	}
	errs := make([]error, len(results))
	for i, ch := range results {
		select {
		case errs[i] = <-ch:
		case <-ctx.Done():
			errs[i] = fmt.Errorf("check timed out: %w", ctx.Err())
		}
	}

	body := struct {
		Status  string   `json:"status"`
		Failing []string `json:"failing,omitempty"`
	}{Status: "ok"}
	for i, err := range errs {
		if err != nil {
			body.Failing = append(body.Failing, s.opts.Ready[i].Name)
			s.opts.Logger.Warn("readiness check failed", "check", s.opts.Ready[i].Name, "error", err)
		}
	}
	status := http.StatusOK
	if len(body.Failing) > 0 {
		body.Status, status = "unavailable", http.StatusServiceUnavailable
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
