// Package authtest provides an in-memory auth.Repo for tests.
package authtest

import (
	"bytes"
	"context"
	"sync"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/auth"
)

// MemRepo is a goroutine-safe in-memory auth.Repo.
type MemRepo struct {
	mu       sync.Mutex
	setup    []byte
	users    map[string]auth.User
	nextID   int64
	sessions map[string]auth.Session
}

// NewMemRepo returns an empty repository.
func NewMemRepo() *MemRepo {
	return &MemRepo{users: map[string]auth.User{}, sessions: map[string]auth.Session{}}
}

func (m *MemRepo) AdminExists(context.Context) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.users) > 0, nil
}

func (m *MemRepo) ReplaceSetupCode(_ context.Context, codeHash []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.setup = bytes.Clone(codeHash)
	return nil
}

func (m *MemRepo) CompleteSetup(_ context.Context, codeHash []byte, username, passwordHash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.users) > 0 {
		return auth.ErrAdminExists
	}
	if m.setup == nil || !bytes.Equal(m.setup, codeHash) {
		return auth.ErrSetupCodeInvalid
	}
	m.nextID++
	m.users[username] = auth.User{ID: m.nextID, Username: username, PasswordHash: passwordHash}
	m.setup = nil
	return nil
}

func (m *MemRepo) UserByName(_ context.Context, username string) (auth.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[username]
	if !ok {
		return auth.User{}, auth.ErrInvalidCredentials
	}
	return u, nil
}

func (m *MemRepo) CreateSession(_ context.Context, s auth.Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[string(s.TokenHash)] = s
	return nil
}

func (m *MemRepo) SessionByTokenHash(_ context.Context, tokenHash []byte) (auth.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[string(tokenHash)]
	if !ok {
		return auth.Session{}, auth.ErrNoSession
	}
	return s, nil
}

func (m *MemRepo) TouchSession(_ context.Context, tokenHash []byte, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[string(tokenHash)]; ok {
		s.LastSeenAt = at
		m.sessions[string(tokenHash)] = s
	}
	return nil
}

func (m *MemRepo) DeleteSession(_ context.Context, tokenHash []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, string(tokenHash))
	return nil
}

func (m *MemRepo) DeleteSessionsBefore(_ context.Context, expiredAt, idleSince time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for k, s := range m.sessions {
		if !s.ExpiresAt.After(expiredAt) || !s.LastSeenAt.After(idleSince) {
			delete(m.sessions, k)
			n++
		}
	}
	return n, nil
}
