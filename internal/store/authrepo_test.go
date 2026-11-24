package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/auth"
)

var _ auth.Repo = (*AuthRepo)(nil)

func migratedRepo(t *testing.T) (*AuthRepo, *Store) {
	t.Helper()
	s := openTest(t)
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return NewAuthRepo(s), s
}

func digest(s string) []byte {
	sum := sha256.Sum256([]byte(s))
	return sum[:]
}

func TestAuthRepoSetupFlow(t *testing.T) {
	ctx := context.Background()
	r, _ := migratedRepo(t)
	if exists, err := r.AdminExists(ctx); err != nil || exists {
		t.Fatalf("AdminExists on an empty database = %v, %v", exists, err)
	}
	if err := r.ReplaceSetupCode(ctx, digest("first")); err != nil {
		t.Fatal(err)
	}
	if err := r.ReplaceSetupCode(ctx, digest("second")); err != nil {
		t.Fatal(err)
	}
	steps := []struct {
		name string
		code string
		want error
	}{
		{"a replaced code no longer works", "first", auth.ErrSetupCodeInvalid},
		{"the current code works", "second", nil},
		{"setup cannot run twice", "second", auth.ErrAdminExists},
	}
	for _, st := range steps {
		if err := r.CompleteSetup(ctx, digest(st.code), "admin", "hash"); !errors.Is(err, st.want) {
			t.Fatalf("%s: CompleteSetup = %v, want %v", st.name, err, st.want)
		}
	}
	u, err := r.UserByName(ctx, "admin")
	if err != nil || u.PasswordHash != "hash" {
		t.Fatalf("UserByName = %+v, %v", u, err)
	}
	if _, err := r.UserByName(ctx, "mallory"); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("UserByName(unknown) = %v, want ErrInvalidCredentials", err)
	}
}

func TestAuthRepoConcurrentSetupCreatesOneAdmin(t *testing.T) {
	ctx := context.Background()
	r, s := migratedRepo(t)
	if err := r.ReplaceSetupCode(ctx, digest("code")); err != nil {
		t.Fatal(err)
	}
	const attempts = 8
	errs := make([]error, attempts)
	var wg sync.WaitGroup
	for i := range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = r.CompleteSetup(ctx, digest("code"), fmt.Sprintf("admin%d", i), "hash")
		}()
	}
	wg.Wait()
	succeeded := 0
	for _, err := range errs {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, auth.ErrAdminExists), errors.Is(err, auth.ErrSetupCodeInvalid):
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	var users int
	if err := s.Pool.QueryRow(ctx, "SELECT count(*) FROM users").Scan(&users); err != nil {
		t.Fatal(err)
	}
	if succeeded != 1 || users != 1 {
		t.Fatalf("%d setups succeeded and %d users exist, want exactly 1 of each", succeeded, users)
	}
}

func TestAuthRepoSessions(t *testing.T) {
	ctx := context.Background()
	r, _ := migratedRepo(t)
	if err := r.ReplaceSetupCode(ctx, digest("code")); err != nil {
		t.Fatal(err)
	}
	if err := r.CompleteSetup(ctx, digest("code"), "admin", "hash"); err != nil {
		t.Fatal(err)
	}
	u, err := r.UserByName(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	live := auth.Session{TokenHash: digest("live"), UserID: u.ID, CSRFToken: "c1", CreatedAt: t0, LastSeenAt: t0, ExpiresAt: t0.Add(7 * 24 * time.Hour)}
	idle := auth.Session{TokenHash: digest("idle"), UserID: u.ID, CSRFToken: "c2", CreatedAt: t0, LastSeenAt: t0.Add(-13 * time.Hour), ExpiresAt: t0.Add(24 * time.Hour)}
	expired := auth.Session{TokenHash: digest("expired"), UserID: u.ID, CSRFToken: "c3", CreatedAt: t0, LastSeenAt: t0, ExpiresAt: t0}
	for _, sess := range []auth.Session{live, idle, expired} {
		if err := r.CreateSession(ctx, sess); err != nil {
			t.Fatal(err)
		}
	}
	got, err := r.SessionByTokenHash(ctx, live.TokenHash)
	if err != nil || got.CSRFToken != "c1" || !got.ExpiresAt.Equal(live.ExpiresAt) || !bytes.Equal(got.TokenHash, live.TokenHash) {
		t.Fatalf("SessionByTokenHash = %+v, %v", got, err)
	}
	if err := r.TouchSession(ctx, live.TokenHash, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.SessionByTokenHash(ctx, live.TokenHash); !got.LastSeenAt.Equal(t0.Add(time.Hour)) {
		t.Fatalf("LastSeenAt after touch = %s", got.LastSeenAt)
	}
	n, err := r.DeleteSessionsBefore(ctx, t0, t0.Add(-12*time.Hour))
	if err != nil || n != 2 {
		t.Fatalf("DeleteSessionsBefore = %d, %v; want 2, nil", n, err)
	}
	if err := r.DeleteSession(ctx, live.TokenHash); err != nil {
		t.Fatal(err)
	}
	if _, err := r.SessionByTokenHash(ctx, live.TokenHash); !errors.Is(err, auth.ErrNoSession) {
		t.Fatalf("SessionByTokenHash after delete = %v, want ErrNoSession", err)
	}
}

func TestServiceOnPostgres(t *testing.T) {
	ctx := context.Background()
	r, _ := migratedRepo(t)
	svc := auth.NewService(r, auth.Params{Memory: 64, Time: 1, Threads: 1, KeyLen: 32, SaltLen: 16}, nil)
	code, ok, err := svc.PrepareSetup(ctx)
	if err != nil || !ok {
		t.Fatalf("PrepareSetup: %v, %v", ok, err)
	}
	ip := netip.MustParseAddr("192.0.2.50")
	if err := svc.CompleteSetup(ctx, ip, code, "admin", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	token, _, err := svc.Login(ctx, ip, "admin", "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(ctx, token); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
}
