package auth

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	// SessionIdleTimeout ends a session that has not been used for this long.
	SessionIdleTimeout = 12 * time.Hour
	// SessionAbsoluteTimeout ends every session this long after sign-in.
	SessionAbsoluteTimeout = 7 * 24 * time.Hour
	// MinPasswordLength is the shortest accepted admin password.
	MinPasswordLength = 12
	// touchInterval limits last-seen updates to one write per minute.
	touchInterval = time.Minute
)

// Service is the sign-in logic the web layer uses.
type Service struct {
	repo    Repo
	limiter *LoginLimiter
	params  Params
	now     func() time.Time
	dummy   func() string
}

// NewService builds a Service. now may be nil, meaning time.Now.
func NewService(repo Repo, params Params, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	s := &Service{repo: repo, limiter: NewLoginLimiter(), params: params, now: now}
	// A hash to check against when the username doesn't exist, so the response
	// time doesn't reveal which usernames are real.
	s.dummy = sync.OnceValue(func() string {
		h, _ := HashPassword("bookwarden-timing-equaliser", params)
		return h
	})
	return s
}

// SetupNeeded reports whether no admin exists yet.
func (s *Service) SetupNeeded(ctx context.Context) (bool, error) {
	exists, err := s.repo.AdminExists(ctx)
	if err != nil {
		return false, fmt.Errorf("auth: check admin: %w", err)
	}
	return !exists, nil
}

// PrepareSetup creates a new one-time setup code while no admin exists,
// replacing any earlier code. ok is false once setup is done.
func (s *Service) PrepareSetup(ctx context.Context) (code string, ok bool, err error) {
	needed, err := s.SetupNeeded(ctx)
	if err != nil || !needed {
		return "", false, err
	}
	code, hash, err := newSetupCode()
	if err != nil {
		return "", false, err
	}
	if err := s.repo.ReplaceSetupCode(ctx, hash); err != nil {
		return "", false, fmt.Errorf("auth: store setup code: %w", err)
	}
	return code, true, nil
}

// CompleteSetup creates the admin when code matches. Wrong codes count against
// the client IP like failed logins.
func (s *Service) CompleteSetup(ctx context.Context, ip netip.Addr, code, username, password string) error {
	ip = ip.Unmap()
	now := s.now()
	if wait := s.limiter.Check(ip, now); wait > 0 {
		return &RateLimitedError{RetryAfter: wait}
	}
	// Refuse before hashing: once the admin exists, nobody gets argon2id
	// work out of this endpoint. repo.CompleteSetup re-checks atomically.
	needed, err := s.SetupNeeded(ctx)
	if err != nil {
		return err
	}
	if !needed {
		return ErrAdminExists
	}
	username = strings.TrimSpace(username)
	if username == "" {
		return ErrUsernameRequired
	}
	if utf8.RuneCountInString(password) < MinPasswordLength {
		return ErrWeakPassword
	}
	hash, err := HashPassword(password, s.params)
	if err != nil {
		return err
	}
	err = s.repo.CompleteSetup(ctx, hashSetupCode(code), username, hash)
	if errors.Is(err, ErrSetupCodeInvalid) {
		s.limiter.Fail(ip, now)
	}
	return err
}

// LoginDelay is how long a setup or login attempt waits before it is
// checked: the limiter's global budget (2 s after 50 failures within an
// hour, from any clients), else zero. The web layer waits it out before the
// attempt takes a hashing slot, so waiting attempts hold no shared resource.
func (s *Service) LoginDelay() time.Duration {
	return s.limiter.Delay(s.now())
}

// Login checks the credentials and returns the raw session token for the cookie.
func (s *Service) Login(ctx context.Context, ip netip.Addr, username, password string) (string, Session, error) {
	ip = ip.Unmap()
	now := s.now()
	if wait := s.limiter.Check(ip, now); wait > 0 {
		return "", Session{}, &RateLimitedError{RetryAfter: wait}
	}
	user, err := s.repo.UserByName(ctx, strings.TrimSpace(username))
	switch {
	case errors.Is(err, ErrInvalidCredentials):
		_, _ = VerifyPassword(s.dummy(), password)
		s.limiter.Fail(ip, now)
		return "", Session{}, ErrInvalidCredentials
	case err != nil:
		return "", Session{}, fmt.Errorf("auth: load user: %w", err)
	}
	ok, err := VerifyPassword(user.PasswordHash, password)
	if err != nil {
		return "", Session{}, err
	}
	if !ok {
		s.limiter.Fail(ip, now)
		return "", Session{}, ErrInvalidCredentials
	}
	s.limiter.Succeed(ip)
	raw, hash, err := newToken()
	if err != nil {
		return "", Session{}, err
	}
	csrf, _, err := newToken()
	if err != nil {
		return "", Session{}, err
	}
	sess := Session{
		TokenHash:  hash,
		UserID:     user.ID,
		CSRFToken:  csrf,
		CreatedAt:  now,
		LastSeenAt: now,
		ExpiresAt:  now.Add(SessionAbsoluteTimeout),
	}
	if err := s.repo.CreateSession(ctx, sess); err != nil {
		return "", Session{}, fmt.Errorf("auth: create session: %w", err)
	}
	return raw, sess, nil
}

// Authenticate resolves a raw session token, enforcing both timeouts.
func (s *Service) Authenticate(ctx context.Context, raw string) (Session, error) {
	if raw == "" {
		return Session{}, ErrNoSession
	}
	hash := hashToken(raw)
	sess, err := s.repo.SessionByTokenHash(ctx, hash)
	if err != nil {
		return Session{}, err
	}
	now := s.now()
	if !now.Before(sess.ExpiresAt) || now.Sub(sess.LastSeenAt) >= SessionIdleTimeout {
		if err := s.repo.DeleteSession(ctx, hash); err != nil {
			return Session{}, fmt.Errorf("auth: delete expired session: %w", err)
		}
		return Session{}, ErrNoSession
	}
	if now.Sub(sess.LastSeenAt) >= touchInterval {
		if err := s.repo.TouchSession(ctx, hash, now); err != nil {
			return Session{}, fmt.Errorf("auth: touch session: %w", err)
		}
		sess.LastSeenAt = now
	}
	return sess, nil
}

// Logout ends the session for raw. Unknown tokens are not an error.
func (s *Service) Logout(ctx context.Context, raw string) error {
	if raw == "" {
		return nil
	}
	if err := s.repo.DeleteSession(ctx, hashToken(raw)); err != nil {
		return fmt.Errorf("auth: delete session: %w", err)
	}
	return nil
}

// PurgeExpiredSessions deletes every session past either timeout.
func (s *Service) PurgeExpiredSessions(ctx context.Context) (int64, error) {
	now := s.now()
	n, err := s.repo.DeleteSessionsBefore(ctx, now, now.Add(-SessionIdleTimeout))
	if err != nil {
		return 0, fmt.Errorf("auth: purge sessions: %w", err)
	}
	return n, nil
}
