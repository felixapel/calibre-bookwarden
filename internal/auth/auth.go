// Package auth implements bookwarden's single-admin sign-in: a one-time setup
// code, argon2id passwords, server-side sessions and per-IP backoff.
package auth

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var (
	// ErrAdminExists means setup already happened.
	ErrAdminExists = errors.New("auth: an admin account already exists")
	// ErrSetupCodeInvalid means the code is wrong or was replaced by a restart.
	ErrSetupCodeInvalid = errors.New("auth: setup code is wrong")
	// ErrInvalidCredentials hides whether the username or the password was wrong.
	ErrInvalidCredentials = errors.New("auth: invalid username or password")
	// ErrNoSession means the session token is unknown or expired.
	ErrNoSession = errors.New("auth: no valid session")
	// ErrWeakPassword rejects passwords shorter than MinPasswordLength.
	ErrWeakPassword = fmt.Errorf("auth: password must have at least %d characters", MinPasswordLength)
	// ErrUsernameRequired rejects an empty username.
	ErrUsernameRequired = errors.New("auth: username is required")
)

// RateLimitedError reports a request refused because of earlier failures.
type RateLimitedError struct {
	RetryAfter time.Duration
}

func (e *RateLimitedError) Error() string {
	return fmt.Sprintf("auth: too many failed attempts; retry in %s", e.RetryAfter.Round(time.Second))
}

// User is the admin account.
type User struct {
	ID           int64
	Username     string
	PasswordHash string
}

// Session is a signed-in browser. Only the SHA-256 of its token is stored.
type Session struct {
	TokenHash  []byte
	UserID     int64
	CSRFToken  string
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
}

// Repo is the storage Service needs. internal/store implements it on Postgres
// and internal/auth/authtest in memory.
type Repo interface {
	AdminExists(ctx context.Context) (bool, error)
	ReplaceSetupCode(ctx context.Context, codeHash []byte) error
	// CompleteSetup atomically checks the code hash, consumes it and creates
	// the admin. It returns ErrSetupCodeInvalid or ErrAdminExists.
	CompleteSetup(ctx context.Context, codeHash []byte, username, passwordHash string) error
	// UserByName returns ErrInvalidCredentials when no such user exists.
	UserByName(ctx context.Context, username string) (User, error)
	CreateSession(ctx context.Context, s Session) error
	// SessionByTokenHash returns ErrNoSession when the hash is unknown.
	SessionByTokenHash(ctx context.Context, tokenHash []byte) (Session, error)
	TouchSession(ctx context.Context, tokenHash []byte, at time.Time) error
	DeleteSession(ctx context.Context, tokenHash []byte) error
	// DeleteSessionsBefore removes sessions that expired at or before
	// expiredAt or were last seen at or before idleSince.
	DeleteSessionsBefore(ctx context.Context, expiredAt, idleSince time.Time) (int64, error)
}
