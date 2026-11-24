package pgtest_test

import (
	"context"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/felixapel/calibre-bookwarden/internal/testutil/pgtest"
)

// TestURLWhileTemplate1IsBusy: parallel test packages connect to Postgres
// at once, and a session on template1 must not stop a test from getting its
// database (SQLSTATE 55006 in CI).
func TestURLWhileTemplate1IsBusy(t *testing.T) {
	admin := os.Getenv("BOOKWARDEN_TEST_DATABASE_URL")
	if admin == "" {
		t.Skip("BOOKWARDEN_TEST_DATABASE_URL is not set")
	}
	u, err := url.Parse(admin)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/template1"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	busy, err := pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close(ctx)

	conn, err := pgx.Connect(ctx, pgtest.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
}
