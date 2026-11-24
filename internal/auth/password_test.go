package auth_test

import (
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/auth"
)

var cheap = auth.Params{Memory: 64, Time: 1, Threads: 1, KeyLen: 32, SaltLen: 16}

func TestVerifyPassword(t *testing.T) {
	hash, err := auth.HashPassword("correct horse battery", cheap)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		encoded  string
		password string
		want     bool
		wantErr  bool
	}{
		{"right password", hash, "correct horse battery", true, false},
		{"wrong password", hash, "correct horse batterY", false, false},
		{"not argon2id", "$2a$10$abcdefghijklmnopqrstuv", "x", false, true},
		{"truncated", "$argon2id$v=19$m=64,t=1,p=1$c2FsdA", "x", false, true},
		{"zero lanes", "$argon2id$v=19$m=64,t=1,p=0$c2FsdHNhbHRzYWx0c2FsdA$aGFzaGhhc2hoYXNoaGFzaA", "x", false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := auth.VerifyPassword(tc.encoded, tc.password)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("VerifyPassword = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestHashPasswordUsesAFreshSalt(t *testing.T) {
	a, _ := auth.HashPassword("same password here", cheap)
	b, _ := auth.HashPassword("same password here", cheap)
	if a == b {
		t.Fatal("two hashes of the same password are identical")
	}
}
