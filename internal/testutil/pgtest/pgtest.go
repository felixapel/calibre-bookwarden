// Package pgtest gives each test its own throwaway Postgres database.
package pgtest

import (
	"context"
	"crypto/rand"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// URL creates a database for the calling test and returns its URL. The
// database is dropped when the test ends. Without BOOKWARDEN_TEST_DATABASE_URL
// the test is skipped.
func URL(t *testing.T) string {
	t.Helper()
	admin := os.Getenv("BOOKWARDEN_TEST_DATABASE_URL")
	if admin == "" {
		t.Skip("BOOKWARDEN_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatalf("pgtest: connect: %v", err)
	}
	defer conn.Close(ctx)
	// Postgres folds unquoted names to lower case, and the URL must match.
	name := "bw_test_" + strings.ToLower(rand.Text())
	// template0 accepts no connections, so a session on template1 (another
	// test package's tool, say) can't make the copy fail with SQLSTATE 55006.
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+name+" TEMPLATE template0"); err != nil {
		t.Fatalf("pgtest: create database: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		c, err := pgx.Connect(ctx, admin)
		if err != nil {
			t.Logf("pgtest: drop %s: %v", name, err)
			return
		}
		defer c.Close(ctx)
		if _, err := c.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			t.Logf("pgtest: drop %s: %v", name, err)
		}
	})
	u, err := url.Parse(admin)
	if err != nil {
		t.Fatalf("pgtest: parse admin URL: %v", err)
	}
	u.Path = "/" + name
	return u.String()
}
