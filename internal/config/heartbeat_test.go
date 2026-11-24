package config

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestHeartbeatHostShowsNoSecrets(t *testing.T) {
	h := HeartbeatConfig{URL: "https://user:pw@kuma.example.org:3001/api/push/tok?status=up"}
	if got := h.Host(); got != "https://kuma.example.org:3001" {
		t.Errorf("Host() = %q", got)
	}
}

func TestHeartbeatFromEnv(t *testing.T) {
	const token = "tok3n-secret"
	url := "http://10.0.0.122:3001/api/push/" + token
	tests := []struct {
		name    string
		env     map[string]string
		wantErr string // a substring; empty means valid
		wantURL bool
	}{
		{name: "unset is off"},
		{name: "private http without an allowance", env: map[string]string{"BOOKWARDEN_HEARTBEAT_URL": url}, wantErr: "BOOKWARDEN_HEARTBEAT_URL must be an https URL, or http with BOOKWARDEN_HEARTBEAT_ALLOW_CIDR set"},
		{name: "private http with the exact address", env: map[string]string{"BOOKWARDEN_HEARTBEAT_URL": url, "BOOKWARDEN_HEARTBEAT_ALLOW_CIDR": "10.0.0.122/32"}, wantURL: true},
		{name: "host outside the allowance", env: map[string]string{"BOOKWARDEN_HEARTBEAT_URL": url, "BOOKWARDEN_HEARTBEAT_ALLOW_CIDR": "10.0.0.5/32"}, wantErr: "BOOKWARDEN_HEARTBEAT_URL host is not inside BOOKWARDEN_HEARTBEAT_ALLOW_CIDR"},
		{name: "a network is not an exact address", env: map[string]string{"BOOKWARDEN_HEARTBEAT_URL": url, "BOOKWARDEN_HEARTBEAT_ALLOW_CIDR": "10.0.0.0/24"}, wantErr: "BOOKWARDEN_HEARTBEAT_ALLOW_CIDR"},
		{name: "private https without an allowance", env: map[string]string{"BOOKWARDEN_HEARTBEAT_URL": "https://10.0.0.5/api/push/" + token}, wantErr: "BOOKWARDEN_HEARTBEAT_ALLOW_CIDR"},
		{name: "public https", env: map[string]string{"BOOKWARDEN_HEARTBEAT_URL": "https://kuma.example.org/api/push/" + token}, wantURL: true},
		{name: "not a URL scheme", env: map[string]string{"BOOKWARDEN_HEARTBEAT_URL": "ftp://kuma.example.org/x"}, wantErr: "BOOKWARDEN_HEARTBEAT_URL must be an https URL"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := baseEnv()
			for k, v := range tc.env {
				env[k] = v
			}
			cfg, err := FromEnv(func(k string) string { return env[k] })
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				if strings.Contains(err.Error(), token) {
					t.Fatalf("error leaks the token: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.Heartbeat.URL != ""; got != tc.wantURL {
				t.Fatalf("Heartbeat.URL set = %v, want %v", got, tc.wantURL)
			}
			var buf bytes.Buffer
			slog.New(slog.NewJSONHandler(&buf, nil)).Info("starting", "config", cfg)
			if strings.Contains(buf.String(), token) || strings.Contains(buf.String(), "/api/push") {
				t.Fatalf("log line leaks the push URL: %s", buf.String())
			}
		})
	}
}
