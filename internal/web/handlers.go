package web

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/auth"
)

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := s.opts.Health(ctx); err != nil {
		s.opts.Logger.Warn("health check failed", "error", err)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, "unhealthy\n")
		return
	}
	_, _ = io.WriteString(w, "ok\n")
}

func (s *Server) setupForm(w http.ResponseWriter, r *http.Request) {
	needed, err := s.opts.Auth.SetupNeeded(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if !needed {
		http.NotFound(w, r)
		return
	}
	s.render(w, http.StatusOK, "setup.html", pageData{Title: "Set up"})
}

func (s *Server) setupSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "The form could not be read.", http.StatusBadRequest)
		return
	}
	username := r.PostForm.Get("username")
	data := pageData{Title: "Set up", Username: username}
	if r.PostForm.Get("password") != r.PostForm.Get("confirm") {
		data.Error = "The passwords don't match."
		s.render(w, http.StatusBadRequest, "setup.html", data)
		return
	}
	err := s.opts.Auth.CompleteSetup(r.Context(), clientFrom(r.Context()).IP,
		r.PostForm.Get("code"), username, r.PostForm.Get("password"))
	var limited *auth.RateLimitedError
	switch {
	case err == nil:
		s.setupDone.Store(true)
		http.Redirect(w, r, "/login?created=1", http.StatusSeeOther)
	case errors.Is(err, auth.ErrAdminExists):
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	case errors.As(err, &limited):
		setRetryAfter(w, limited.RetryAfter)
		data.Error = "Too many wrong codes. Wait a moment and try again."
		s.render(w, http.StatusTooManyRequests, "setup.html", data)
	case errors.Is(err, auth.ErrSetupCodeInvalid):
		data.Error = "That setup code is wrong. Use the newest code from the container log."
		s.render(w, http.StatusBadRequest, "setup.html", data)
	case errors.Is(err, auth.ErrWeakPassword):
		data.Error = "The password needs at least 12 characters."
		s.render(w, http.StatusBadRequest, "setup.html", data)
	case errors.Is(err, auth.ErrUsernameRequired):
		data.Error = "Choose a username."
		s.render(w, http.StatusBadRequest, "setup.html", data)
	default:
		s.serverError(w, r, err)
	}
}

func (s *Server) loginForm(w http.ResponseWriter, r *http.Request) {
	data := pageData{Title: "Sign in"}
	if r.URL.Query().Get("created") == "1" {
		data.Notice = "The admin account is ready. Sign in."
	}
	s.render(w, http.StatusOK, "login.html", data)
}

func (s *Server) loginSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "The form could not be read.", http.StatusBadRequest)
		return
	}
	username := r.PostForm.Get("username")
	raw, sess, err := s.opts.Auth.Login(r.Context(), clientFrom(r.Context()).IP, username, r.PostForm.Get("password"))
	data := pageData{Title: "Sign in", Username: username}
	var limited *auth.RateLimitedError
	switch {
	case err == nil:
		s.setSessionCookie(w, r, raw, sess.ExpiresAt)
		http.Redirect(w, r, "/", http.StatusSeeOther)
	case errors.As(err, &limited):
		setRetryAfter(w, limited.RetryAfter)
		data.Error = "Too many failed sign-ins. Wait a moment and try again."
		s.render(w, http.StatusTooManyRequests, "login.html", data)
	case errors.Is(err, auth.ErrInvalidCredentials):
		data.Error = "Wrong username or password."
		s.render(w, http.StatusUnauthorized, "login.html", data)
	default:
		s.serverError(w, r, err)
	}
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		if err := s.opts.Auth.Logout(r.Context(), c.Value); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	s.clearSessionCookie(w, r)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	runs, err := s.opts.Runs.List(r.Context(), 1)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, http.StatusOK, "overview.html", pageData{
		Title: "Library", SignedIn: true, CSRFToken: sessionFrom(r.Context()).CSRFToken,
		Calibre: s.opts.Runs.Calibre(), Runs: runs,
	})
}

func setRetryAfter(w http.ResponseWriter, d time.Duration) {
	secs := max(int(math.Ceil(d.Seconds())), 1)
	w.Header().Set("Retry-After", strconv.Itoa(secs))
}
