package web

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/auth"
)

const sessionCookie = "bookwarden_session"

type sessionKey struct{}

// sessionCookieFor builds the session cookie. Secure follows the request: it's
// set over HTTPS, directly or through a trusted proxy, and left off on a
// plain-HTTP LAN port, where the browser would never send a Secure cookie back
// (spec section 12). .golangci.yml exempts this file from gosec's G124 for that
// reason.
func sessionCookieFor(r *http.Request, value string) *http.Cookie {
	return &http.Cookie{
		Name:     sessionCookie,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   clientFrom(r.Context()).HTTPS,
	}
}

func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, raw string, expires time.Time) {
	c := sessionCookieFor(r, raw)
	c.Expires = expires
	http.SetCookie(w, c)
}

func (s *Server) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	c := sessionCookieFor(r, "")
	c.MaxAge = -1
	http.SetCookie(w, c)
}

// requireSession lets signed-in requests through. State-changing requests
// must also carry the session's CSRF token in the csrf_token form field.
func (s *Server) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var sess auth.Session
		c, err := r.Cookie(sessionCookie)
		if err == nil {
			sess, err = s.opts.Auth.Authenticate(r.Context(), c.Value)
		}
		if err != nil {
			if !errors.Is(err, http.ErrNoCookie) && !errors.Is(err, auth.ErrNoSession) {
				s.serverError(w, r, err)
				return
			}
			s.clearSessionCookie(w, r)
			if r.Header.Get("HX-Request") == "true" {
				// An htmx poll swaps the answer into the page; this header
				// makes htmx load the sign-in page instead.
				w.Header().Set("HX-Redirect", "/login")
				w.WriteHeader(http.StatusOK)
				return
			}
			if r.Method == http.MethodGet || r.Method == http.MethodHead {
				http.Redirect(w, r, "/login", http.StatusSeeOther)
				return
			}
			http.Error(w, "Sign in first.", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			got := r.PostFormValue("csrf_token")
			if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(sess.CSRFToken)) != 1 {
				http.Error(w, "The form token is missing or stale. Reload the page and try again.", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), sessionKey{}, sess)))
	})
}

func sessionFrom(ctx context.Context) auth.Session {
	sess, _ := ctx.Value(sessionKey{}).(auth.Session)
	return sess
}
