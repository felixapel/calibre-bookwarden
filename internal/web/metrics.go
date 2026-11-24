package web

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/felixapel/calibre-bookwarden/internal/metrics"
)

// serveMetrics serves the Prometheus text format to a caller with the bearer
// token. The route exists only when a token is configured.
func (s *Server) serveMetrics(w http.ResponseWriter, r *http.Request) {
	if !bearerMatches(r.Header.Get("Authorization"), s.opts.MetricsToken) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="bookwarden metrics"`)
		http.Error(w, "A bearer token is required.", http.StatusUnauthorized)
		return
	}
	// Written to a buffer first, so a failure sends no partial scrape.
	var buf bytes.Buffer
	mw := metrics.NewWriter(&buf)
	if err := s.opts.Metrics(r.Context(), mw); err != nil {
		s.opts.Logger.Error("metrics scrape failed", "error", err)
		http.Error(w, "Metrics unavailable.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = buf.WriteTo(w)
}

// bearerMatches reports whether header is "Bearer <token>". The scheme is
// case-insensitive; the token is compared in constant time, over digests so
// that its length doesn't leak either.
func bearerMatches(header, token string) bool {
	scheme, got, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" {
		return false
	}
	a, b := sha256.Sum256([]byte(got)), sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}
