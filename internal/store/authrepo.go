package store

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/felixapel/calibre-bookwarden/internal/auth"
)

// AuthRepo implements auth.Repo on Postgres.
type AuthRepo struct {
	pool *pgxpool.Pool
}

// NewAuthRepo returns the sign-in repository for s.
func NewAuthRepo(s *Store) *AuthRepo { return &AuthRepo{pool: s.Pool} }

func (r *AuthRepo) AdminExists(ctx context.Context) (bool, error) {
	var exists bool
	if err := r.pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM users)").Scan(&exists); err != nil {
		return false, fmt.Errorf("store: check admin: %w", err)
	}
	return exists, nil
}

func (r *AuthRepo) ReplaceSetupCode(ctx context.Context, codeHash []byte) error {
	_, err := r.pool.Exec(ctx, `INSERT INTO setup (id, code_hash, created_at) VALUES (true, $1, now())
		ON CONFLICT (id) DO UPDATE SET code_hash = EXCLUDED.code_hash, created_at = EXCLUDED.created_at`, codeHash)
	if err != nil {
		return fmt.Errorf("store: replace setup code: %w", err)
	}
	return nil
}

func (r *AuthRepo) CompleteSetup(ctx context.Context, codeHash []byte, username, passwordHash string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: begin setup: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Lock the setup row so concurrent attempts run one after another.
	var stored []byte
	err = tx.QueryRow(ctx, "SELECT code_hash FROM setup WHERE id FOR UPDATE").Scan(&stored)
	if errors.Is(err, pgx.ErrNoRows) {
		var exists bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM users)").Scan(&exists); err != nil {
			return fmt.Errorf("store: check admin: %w", err)
		}
		if exists {
			return auth.ErrAdminExists
		}
		return auth.ErrSetupCodeInvalid
	}
	if err != nil {
		return fmt.Errorf("store: read setup code: %w", err)
	}
	if subtle.ConstantTimeCompare(stored, codeHash) != 1 {
		return auth.ErrSetupCodeInvalid
	}
	if _, err := tx.Exec(ctx, "INSERT INTO users (username, password_hash) VALUES ($1, $2)", username, passwordHash); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation
			return auth.ErrAdminExists
		}
		return fmt.Errorf("store: create admin: %w", err)
	}
	if _, err := tx.Exec(ctx, "DELETE FROM setup WHERE id"); err != nil {
		return fmt.Errorf("store: consume setup code: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: commit setup: %w", err)
	}
	return nil
}

func (r *AuthRepo) UserByName(ctx context.Context, username string) (auth.User, error) {
	var u auth.User
	err := r.pool.QueryRow(ctx, "SELECT id, username, password_hash FROM users WHERE username = $1", username).
		Scan(&u.ID, &u.Username, &u.PasswordHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.User{}, auth.ErrInvalidCredentials
	}
	if err != nil {
		return auth.User{}, fmt.Errorf("store: load user: %w", err)
	}
	return u, nil
}

func (r *AuthRepo) CreateSession(ctx context.Context, s auth.Session) error {
	_, err := r.pool.Exec(ctx, `INSERT INTO sessions
		(token_hash, user_id, csrf_token, created_at, last_seen_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		s.TokenHash, s.UserID, s.CSRFToken, s.CreatedAt, s.LastSeenAt, s.ExpiresAt)
	if err != nil {
		return fmt.Errorf("store: create session: %w", err)
	}
	return nil
}

func (r *AuthRepo) SessionByTokenHash(ctx context.Context, tokenHash []byte) (auth.Session, error) {
	var s auth.Session
	err := r.pool.QueryRow(ctx, `SELECT token_hash, user_id, csrf_token, created_at, last_seen_at, expires_at
		FROM sessions WHERE token_hash = $1`, tokenHash).
		Scan(&s.TokenHash, &s.UserID, &s.CSRFToken, &s.CreatedAt, &s.LastSeenAt, &s.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.Session{}, auth.ErrNoSession
	}
	if err != nil {
		return auth.Session{}, fmt.Errorf("store: load session: %w", err)
	}
	return s, nil
}

func (r *AuthRepo) TouchSession(ctx context.Context, tokenHash []byte, at time.Time) error {
	if _, err := r.pool.Exec(ctx, "UPDATE sessions SET last_seen_at = $2 WHERE token_hash = $1", tokenHash, at); err != nil {
		return fmt.Errorf("store: touch session: %w", err)
	}
	return nil
}

func (r *AuthRepo) DeleteSession(ctx context.Context, tokenHash []byte) error {
	if _, err := r.pool.Exec(ctx, "DELETE FROM sessions WHERE token_hash = $1", tokenHash); err != nil {
		return fmt.Errorf("store: delete session: %w", err)
	}
	return nil
}

func (r *AuthRepo) DeleteSessionsBefore(ctx context.Context, expiredAt, idleSince time.Time) (int64, error) {
	tag, err := r.pool.Exec(ctx, "DELETE FROM sessions WHERE expires_at <= $1 OR last_seen_at <= $2", expiredAt, idleSince)
	if err != nil {
		return 0, fmt.Errorf("store: purge sessions: %w", err)
	}
	return tag.RowsAffected(), nil
}
