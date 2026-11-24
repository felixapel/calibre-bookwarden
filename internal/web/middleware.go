package web

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"runtime/debug"
	"slices"
	"strings"
	"time"
)

const contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

// hostAllowed compares the Host header, without port, brackets, case or a
// trailing dot, against the allowlist. IP addresses match in any notation.
func hostAllowed(host string, allowed []string) bool {
	h := host
	if hh, _, err := net.SplitHostPort(host); err == nil {
		h = hh
	}
	h = strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")
	h = strings.TrimSuffix(strings.ToLower(h), ".")
	if slices.Contains(allowed, h) {
		return true
	}
	ip, err := netip.ParseAddr(h)
	if err != nil {
		return false
	}
	return slices.ContainsFunc(allowed, func(a string) bool {
		entry, err := netip.ParseAddr(a)
		return err == nil && entry.Unmap() == ip.Unmap()
	})
}

// requireAllowedHost refuses requests for unknown host names, which blocks DNS
// rebinding. The probe paths are exempt (see isProbePath), so Docker can check
// 127.0.0.1 and a scraper can use the container name.
func requireAllowedHost(allowed []string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isProbePath(r.URL.Path) && !hostAllowed(r.Host, allowed) {
			http.Error(w, "This host name is not in BOOKWARDEN_ALLOWED_HOSTS.", http.StatusMisdirectedRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		// Not no-referrer: with it, browsers send "Origin: null" on form
		// posts, and over plain HTTP they send no Sec-Fetch-Site either, so
		// the cross-origin check refuses every form, the login included.
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

// limitBody caps request bodies; bookwarden's forms are tiny.
func limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// logRequests logs method, path, status and duration; never queries or bodies.
func logRequests(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		if p := r.URL.Path; p == "/healthz" || p == "/readyz" || (p == "/metrics" && sw.status < 400) {
			return
		}
		logger.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"client", clientFrom(r.Context()).IP.String())
	})
}

func recoverPanics(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			v := recover()
			if v == nil {
				return
			}
			if err, ok := v.(error); ok && errors.Is(err, http.ErrAbortHandler) {
				panic(v)
			}
			logger.Error("panic while serving a request",
				"path", r.URL.Path, "panic", fmt.Sprint(v), "stack", string(debug.Stack()))
			http.Error(w, "Something went wrong. Check the bookwarden log.", http.StatusInternalServerError)
		}()
		next.ServeHTTP(w, r)
	})
}
