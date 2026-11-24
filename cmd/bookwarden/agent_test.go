package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/agent"
)

func TestWarnPlainHTTP(t *testing.T) {
	for _, tc := range []struct {
		name, url string
		warns     bool
	}{
		{"plain http", "http://10.0.0.122:8766/v1/chat/completions", true},
		{"https", "https://api.anthropic.com/v1/messages", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&buf, nil))
			warnPlainHTTP(logger, agent.SeatConfig{Name: "gemini", URL: tc.url, Key: "super-secret-key"})
			out := buf.String()
			if got := strings.Contains(out, "level=WARN") && strings.Contains(out, "seat=gemini"); got != tc.warns {
				t.Fatalf("warned = %v, want %v: %s", got, tc.warns, out)
			}
			if strings.Contains(out, "super-secret-key") {
				t.Fatalf("log line carries the key: %s", out)
			}
		})
	}
}
