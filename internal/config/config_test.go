package config

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func baseEnv() map[string]string {
	return map[string]string{
		"BOOKWARDEN_DATABASE_URL":     "postgres://bookwarden:db-secret@postgresql18:5432/bookwarden",
		"BOOKWARDEN_CALIBRE_URL":      "http://calibre:8081/",
		"BOOKWARDEN_CALIBRE_USER":     "bookwarden",
		"BOOKWARDEN_CALIBRE_PASSWORD": "calibre-secret",
		"BOOKWARDEN_ALLOWED_HOSTS":    "10.0.0.122, Bookwarden.LAN, [fd00::1]",
	}
}

func TestFromEnv(t *testing.T) {
	required := map[string]string{
		"BOOKWARDEN_DATABASE_URL":     "",
		"BOOKWARDEN_CALIBRE_URL":      "",
		"BOOKWARDEN_CALIBRE_USER":     "",
		"BOOKWARDEN_CALIBRE_PASSWORD": "",
		"BOOKWARDEN_ALLOWED_HOSTS":    "",
	}
	tests := []struct {
		name    string
		change  map[string]string // an empty value deletes the variable
		wantErr []string
		check   func(t *testing.T, c Config)
	}{
		{
			name: "minimal valid configuration",
			check: func(t *testing.T, c Config) {
				if c.Listen != ":8080" {
					t.Errorf("Listen = %q, want :8080", c.Listen)
				}
				if got := c.CalibreURL.String(); got != "http://calibre:8081" {
					t.Errorf("CalibreURL = %q", got)
				}
				if got := strings.Join(c.AllowedHosts, ","); got != "10.0.0.122,bookwarden.lan,fd00::1" {
					t.Errorf("AllowedHosts = %q", got)
				}
			},
		},
		{
			name:   "every required variable missing",
			change: required,
			wantErr: []string{
				"BOOKWARDEN_DATABASE_URL is required",
				"BOOKWARDEN_CALIBRE_URL is required",
				"BOOKWARDEN_CALIBRE_USER is required",
				"BOOKWARDEN_CALIBRE_PASSWORD is required",
				"BOOKWARDEN_ALLOWED_HOSTS is required",
			},
		},
		{
			name:    "database URL with the wrong scheme",
			change:  map[string]string{"BOOKWARDEN_DATABASE_URL": "mysql://u:db-secret@h/db"},
			wantErr: []string{"BOOKWARDEN_DATABASE_URL must be a postgres:// URL"},
		},
		{
			name:    "Calibre URL with credentials",
			change:  map[string]string{"BOOKWARDEN_CALIBRE_URL": "http://u:calibre-secret@calibre:8081"},
			wantErr: []string{"must not contain credentials"},
		},
		{
			name:    "Calibre URL that isn't HTTP",
			change:  map[string]string{"BOOKWARDEN_CALIBRE_URL": "ftp://calibre"},
			wantErr: []string{"must be an http(s) URL"},
		},
		{
			name:    "allowed host with a port",
			change:  map[string]string{"BOOKWARDEN_ALLOWED_HOSTS": "bookwarden.lan:8399"},
			wantErr: []string{"without a port"},
		},
		{
			name:   "trusted proxies as CIDR and address",
			change: map[string]string{"BOOKWARDEN_TRUSTED_PROXIES": "172.18.0.0/16, 10.0.0.5"},
			check: func(t *testing.T, c Config) {
				var got []string
				for _, p := range c.TrustedProxies {
					got = append(got, p.String())
				}
				if strings.Join(got, ",") != "172.18.0.0/16,10.0.0.5/32" {
					t.Errorf("TrustedProxies = %v", got)
				}
			},
		},
		// A prefix that wide would let anyone forge X-Forwarded-For and
		// switch the login backoff off.
		{name: "trusted proxies 0.0.0.0/0", change: map[string]string{"BOOKWARDEN_TRUSTED_PROXIES": "0.0.0.0/0"}, wantErr: []string{"BOOKWARDEN_TRUSTED_PROXIES entry 1 is wider than /16"}},
		{name: "trusted proxies 10.0.0.0/8", change: map[string]string{"BOOKWARDEN_TRUSTED_PROXIES": "10.0.0.5, 10.0.0.0/8"}, wantErr: []string{"BOOKWARDEN_TRUSTED_PROXIES entry 2 is wider than /16"}},
		{name: "trusted proxies IPv4-mapped /16", change: map[string]string{"BOOKWARDEN_TRUSTED_PROXIES": "::ffff:0.0.0.0/16"}, wantErr: []string{"BOOKWARDEN_TRUSTED_PROXIES entry 1 is wider than /112"}},
		{name: "trusted proxies /15", change: map[string]string{"BOOKWARDEN_TRUSTED_PROXIES": "10.0.0.0/15"}, wantErr: []string{"entry 1 is wider than /16"}},
		{name: "trusted proxies /47", change: map[string]string{"BOOKWARDEN_TRUSTED_PROXIES": "fd00::/47"}, wantErr: []string{"entry 1 is wider than /48"}},
		{name: "trusted proxies ::/0", change: map[string]string{"BOOKWARDEN_TRUSTED_PROXIES": "::/0"}, wantErr: []string{"BOOKWARDEN_TRUSTED_PROXIES entry 1 is wider than /48"}},
		{name: "trusted proxies /16, /24 and /48", change: map[string]string{"BOOKWARDEN_TRUSTED_PROXIES": "172.17.0.0/16, 192.168.1.0/24, fd00::/48"}, check: func(t *testing.T, c Config) {
			if len(c.TrustedProxies) != 3 {
				t.Errorf("TrustedProxies = %v", c.TrustedProxies)
			}
		}},
		{name: "metrics token padded to 32 with spaces", change: map[string]string{"BOOKWARDEN_METRICS_TOKEN": "  " + strings.Repeat("m", 30) + "  "}, wantErr: []string{"at least 32 characters"}},
		{name: "metrics token of 31 characters", change: map[string]string{"BOOKWARDEN_METRICS_TOKEN": strings.Repeat("m", 31)}, wantErr: []string{"BOOKWARDEN_METRICS_TOKEN must be at least 32 characters"}},
		{name: "metrics token of 32 characters", change: map[string]string{"BOOKWARDEN_METRICS_TOKEN": strings.Repeat("m", 32)}, check: func(t *testing.T, c Config) {
			if c.MetricsToken != strings.Repeat("m", 32) {
				t.Errorf("MetricsToken has %d characters", len(c.MetricsToken))
			}
		}},
		{name: "no metrics token", change: map[string]string{"BOOKWARDEN_METRICS_TOKEN": ""}, check: func(t *testing.T, c Config) {
			if c.MetricsToken != "" {
				t.Error("MetricsToken set")
			}
		}},
		{
			name:    "trusted proxy that isn't an address",
			change:  map[string]string{"BOOKWARDEN_TRUSTED_PROXIES": "proxy.lan"},
			wantErr: []string{"BOOKWARDEN_TRUSTED_PROXIES entry 1 is not an IP address or CIDR"},
		},
		{
			name: "log level and stop grace default",
			check: func(t *testing.T, c Config) {
				if c.LogLevel != slog.LevelInfo || c.StopGrace != 25*time.Second {
					t.Errorf("LogLevel = %v, StopGrace = %v, want info and 25s", c.LogLevel, c.StopGrace)
				}
			},
		},
		{
			name:   "log level debug",
			change: map[string]string{"BOOKWARDEN_LOG_LEVEL": " Debug "},
			check: func(t *testing.T, c Config) {
				if c.LogLevel != slog.LevelDebug {
					t.Errorf("LogLevel = %v, want debug", c.LogLevel)
				}
			},
		},
		{
			name:   "log level warn and error",
			change: map[string]string{"BOOKWARDEN_LOG_LEVEL": "warn"},
			check: func(t *testing.T, c Config) {
				if c.LogLevel != slog.LevelWarn {
					t.Errorf("LogLevel = %v, want warn", c.LogLevel)
				}
			},
		},
		{
			name:    "log level that isn't one",
			change:  map[string]string{"BOOKWARDEN_LOG_LEVEL": "trace"},
			wantErr: []string{"BOOKWARDEN_LOG_LEVEL must be debug, info, warn or error"},
		},
		{
			name:   "stop grace",
			change: map[string]string{"BOOKWARDEN_STOP_GRACE": "1m30s"},
			check: func(t *testing.T, c Config) {
				if c.StopGrace != 90*time.Second {
					t.Errorf("StopGrace = %v, want 1m30s", c.StopGrace)
				}
			},
		},
		{
			name:    "stop grace that isn't a duration",
			change:  map[string]string{"BOOKWARDEN_STOP_GRACE": "25"},
			wantErr: []string{"BOOKWARDEN_STOP_GRACE must be a duration such as 25s"},
		},
		{
			name:    "stop grace of zero",
			change:  map[string]string{"BOOKWARDEN_STOP_GRACE": "0s"},
			wantErr: []string{"BOOKWARDEN_STOP_GRACE must be a duration such as 25s"},
		},
		{
			name:    "listen address without a colon",
			change:  map[string]string{"BOOKWARDEN_LISTEN": "8080"},
			wantErr: []string{"BOOKWARDEN_LISTEN must be host:port or :port"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := baseEnv()
			for k, v := range tc.change {
				if v == "" {
					delete(env, k)
				} else {
					env[k] = v
				}
			}
			cfg, err := FromEnv(func(k string) string { return env[k] })
			if len(tc.wantErr) > 0 {
				if err == nil {
					t.Fatal("FromEnv succeeded, want an error")
				}
				for _, want := range tc.wantErr {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error %q does not mention %q", err, want)
					}
				}
				for _, secret := range []string{"db-secret", "calibre-secret"} {
					if strings.Contains(err.Error(), secret) {
						t.Fatalf("error leaks a secret: %v", err)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("FromEnv: %v", err)
			}
			if tc.check != nil {
				tc.check(t, cfg)
			}
		})
	}
}

func TestLogValueRedactsSecrets(t *testing.T) {
	env := baseEnv()
	env["BOOKWARDEN_GOOGLE_BOOKS_KEY"] = "google-secret"
	env["BOOKWARDEN_HARDCOVER_TOKEN"] = "hardcover-secret"
	env["BOOKWARDEN_METRICS_TOKEN"] = "metrics-secret-of-at-least-32-characters"
	cfg, err := FromEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HardcoverToken != "hardcover-secret" {
		t.Fatalf("HardcoverToken = %q", cfg.HardcoverToken)
	}
	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("starting", "config", cfg)
	for _, secret := range []string{"db-secret", "calibre-secret", "google-secret", "hardcover-secret", "metrics-secret"} {
		if strings.Contains(buf.String(), secret) {
			t.Fatalf("log line leaks %q: %s", secret, buf.String())
		}
	}
	for _, want := range []string{`"google_books_key_set":true`, `"hardcover_token_set":true`, `"metrics_enabled":true`, `"calibre_url":"http://calibre:8081"`} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("log line lacks %s: %s", want, buf.String())
		}
	}
}

func TestAgentConfig(t *testing.T) {
	keys := map[string]string{
		"BOOKWARDEN_AGENT_ANTHROPIC_KEY": "ant-secret",
		"BOOKWARDEN_AGENT_OPENCODE_KEY":  "oc-secret",
		"BOOKWARDEN_AGENT_GEMINI_KEY":    "gem-secret",
	}
	for _, tc := range []struct {
		name    string
		change  map[string]string
		wantErr string
		check   func(t *testing.T, a AgentConfig)
	}{
		{"no keys", nil, "", func(t *testing.T, a AgentConfig) {
			if len(a.Seats) != 0 || strings.Join(a.Missing, ",") != "haiku,deepseek,gemini" || a.MaxTokensPerRun != 20_000_000 {
				t.Fatalf("agent = %+v", a)
			}
		}},
		{"three keys", keys, "", func(t *testing.T, a AgentConfig) {
			if len(a.Seats) != 3 || len(a.Missing) != 0 {
				t.Fatalf("agent = %+v", a)
			}
			want := []struct{ name, wire, url, model, effort, key string }{
				{"haiku", "anthropic", "https://api.anthropic.com/v1/messages", "claude-haiku-5-5", "max", "ant-secret"},
				{"deepseek", "openai", "https://opencode.ai/zen/go/v1/chat/completions", "deepseek-v4.1-flash", "max", "oc-secret"},
				{"gemini", "openai", "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions", "gemini-3.8-flash", "high", "gem-secret"},
			}
			for i, w := range want {
				s := a.Seats[i]
				if s.Name != w.name || s.Wire != w.wire || s.URL != w.url || s.Model != w.model || s.Effort != w.effort || s.Key != w.key {
					t.Errorf("seat %d = %+v", i, s)
				}
			}
		}},
		{"one key", map[string]string{"BOOKWARDEN_AGENT_GEMINI_KEY": "gem-secret"}, "", func(t *testing.T, a AgentConfig) {
			if len(a.Seats) != 1 || a.Seats[0].Name != "gemini" || strings.Join(a.Missing, ",") != "haiku,deepseek" {
				t.Fatalf("agent = %+v", a)
			}
		}},
		{"overrides", map[string]string{"BOOKWARDEN_AGENT_ANTHROPIC_KEY": "k", "BOOKWARDEN_AGENT_HAIKU_MODEL": "claude-haiku-5-6",
			"BOOKWARDEN_AGENT_HAIKU_URL": "https://proxy.example/v1/messages", "BOOKWARDEN_AGENT_HAIKU_EFFORT": "high",
			"BOOKWARDEN_AGENT_MAX_TOKENS_PER_RUN": "1000"}, "", func(t *testing.T, a AgentConfig) {
			s := a.Seats[0]
			if s.Model != "claude-haiku-5-6" || s.URL != "https://proxy.example/v1/messages" || s.Effort != "high" || a.MaxTokensPerRun != 1000 {
				t.Fatalf("agent = %+v", a)
			}
		}},
		{"http URL", map[string]string{"BOOKWARDEN_AGENT_GEMINI_URL": "http://gemini.example/v1"}, "BOOKWARDEN_AGENT_GEMINI_URL", nil},
		{"bad tokens", map[string]string{"BOOKWARDEN_AGENT_MAX_TOKENS_PER_RUN": "-1"}, "BOOKWARDEN_AGENT_MAX_TOKENS_PER_RUN", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := baseEnv()
			for k, v := range tc.change {
				env[k] = v
			}
			cfg, err := FromEnv(func(k string) string { return env[k] })
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want one naming %s", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			tc.check(t, cfg.Agent)
		})
	}
	env := baseEnv()
	for k, v := range keys {
		env[k] = v
	}
	cfg, _ := FromEnv(func(k string) string { return env[k] })
	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("starting", "config", cfg)
	for _, secret := range []string{"ant-secret", "oc-secret", "gem-secret"} {
		if strings.Contains(buf.String(), secret) {
			t.Fatalf("log line leaks %q: %s", secret, buf.String())
		}
	}
	if !strings.Contains(buf.String(), `"agent_seats":"haiku,deepseek,gemini"`) {
		t.Errorf("log line lacks the configured seats: %s", buf.String())
	}
}

func TestExtractorConfig(t *testing.T) {
	keys := map[string]string{"BOOKWARDEN_EXTRACTOR_KEY": "oc-secret"}
	bifrost := map[string]string{
		"BOOKWARDEN_EXTRACTOR_BIFROST_URL": "http://10.0.0.122:8082",
		"BOOKWARDEN_EXTRACTOR_ALLOW_CIDR":  "10.0.0.122/32",
	}
	with := func(base map[string]string, kv ...string) map[string]string {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}
	names := func(x ExtractorConfig) string {
		var n []string
		for _, e := range x.Extractors {
			n = append(n, e.Name)
		}
		return strings.Join(n, ",")
	}
	for _, tc := range []struct {
		name    string
		change  map[string]string
		wantErr string
		check   func(t *testing.T, x ExtractorConfig)
	}{
		{"nothing set", nil, "", func(t *testing.T, x ExtractorConfig) {
			if len(x.Extractors) != 0 || strings.Join(x.Missing, ",") != "luna,deepseek,glm" {
				t.Fatalf("extractors = %+v", x)
			}
		}},
		{"opencode-go directly", keys, "", func(t *testing.T, x ExtractorConfig) {
			// luna and glm are the benchmark's choice (2026-11-03); deepseek is
			// Felix's, provisional until measured.
			want := []struct {
				name, model, family, effort string
				calls                       int
			}{
				{"luna", "gpt-6-luna", "openai", "low", 5000},
				{"deepseek", "deepseek-v4.1-flash", "deepseek", "none", 5000},
				{"glm", "glm-5.3-flash", "zhipu", "low", 15000},
			}
			if len(x.Extractors) != 3 {
				t.Fatalf("extractors = %+v", x)
			}
			for i, w := range want {
				e := x.Extractors[i]
				if e.Name != w.name || e.Wire != "openai" || e.URL != "https://opencode.ai/zen/go/v1/chat/completions" ||
					e.Model != w.model || e.Family != w.family || e.Effort != w.effort || e.Key != "oc-secret" || e.Keyless ||
					e.SessionHeader != "x-opencode-session" || e.NoContentLog || len(e.AllowCIDR) != 0 {
					t.Errorf("extractor %d = %+v", i, e)
				}
				if x.MaxCalls[w.name] != w.calls {
					t.Errorf("%s max calls = %d", w.name, x.MaxCalls[w.name])
				}
			}
		}},
		{"through Bifrost", bifrost, "", func(t *testing.T, x ExtractorConfig) {
			if names(x) != "luna,deepseek,glm" {
				t.Fatalf("extractors = %+v", x)
			}
			e := x.Extractors[2]
			if e.URL != "http://10.0.0.122:8082/v1/chat/completions" || e.Model != "opencode-go/glm-5.3-flash" || !e.Keyless ||
				e.Key != "" || e.SessionHeader != "x-bf-eh-x-opencode-session" || !e.NoContentLog || len(e.AllowCIDR) != 1 {
				t.Errorf("glm = %+v", e)
			}
		}},
		{"Bifrost with a virtual key", with(bifrost, "BOOKWARDEN_EXTRACTOR_KEY", "vk"), "", func(t *testing.T, x ExtractorConfig) {
			if e := x.Extractors[0]; e.Key != "vk" || !e.Keyless || e.Model != "opencode-go/gpt-6-luna" {
				t.Errorf("luna = %+v", e)
			}
		}},
		{"an explicit model is sent as given", with(bifrost, "BOOKWARDEN_EXTRACTOR_GLM_MODEL", "opencode-go/glm-5.4-flash"), "", func(t *testing.T, x ExtractorConfig) {
			if x.Extractors[2].Model != "opencode-go/glm-5.4-flash" {
				t.Errorf("glm = %+v", x.Extractors[2])
			}
		}},
		{"overrides", with(keys, "BOOKWARDEN_EXTRACTOR_GLM_EFFORT", "high", "BOOKWARDEN_EXTRACTOR_GLM_MAX_CALLS", "250",
			"BOOKWARDEN_EXTRACTOR_LUNA_MODEL", "gpt-6.1-luna"), "", func(t *testing.T, x ExtractorConfig) {
			if x.Extractors[0].Model != "gpt-6.1-luna" || x.Extractors[2].Effort != "high" || x.MaxCalls["glm"] != 250 {
				t.Fatalf("extractors = %+v", x)
			}
		}},
		{"Bifrost over plain http needs an allowance", map[string]string{"BOOKWARDEN_EXTRACTOR_BIFROST_URL": "http://10.0.0.122:8082"},
			"BOOKWARDEN_EXTRACTOR_BIFROST_URL must be an https URL, or http with BOOKWARDEN_EXTRACTOR_ALLOW_CIDR set", nil},
		{"an extractor's own allowance is checked", with(bifrost, "BOOKWARDEN_EXTRACTOR_GLM_ALLOW_CIDR", "10.0.0.1/32"), "BOOKWARDEN_EXTRACTOR_GLM_URL host is not inside", nil},
		// The virtual key, the session header and the model prefix belong to
		// Bifrost: an extractor can't be sent elsewhere with them.
		{"no own URL through Bifrost", with(bifrost, "BOOKWARDEN_EXTRACTOR_LUNA_URL", "https://api.example.com/v1/chat/completions"),
			"BOOKWARDEN_EXTRACTOR_LUNA_URL can't be set with BOOKWARDEN_EXTRACTOR_BIFROST_URL", nil},
		{"Bifrost URL outside the allowance", with(bifrost, "BOOKWARDEN_EXTRACTOR_ALLOW_CIDR", "10.0.0.123/32"), "not inside", nil},
		{"Bifrost URL with a path", with(bifrost, "BOOKWARDEN_EXTRACTOR_BIFROST_URL", "http://10.0.0.122:8082/v1"), "BOOKWARDEN_EXTRACTOR_BIFROST_URL", nil},
		{"Bifrost URL with credentials", with(bifrost, "BOOKWARDEN_EXTRACTOR_BIFROST_URL", "http://u:p@10.0.0.122:8082"), "BOOKWARDEN_EXTRACTOR_BIFROST_URL", nil},
		{"one family twice", with(keys, "BOOKWARDEN_EXTRACTOR_GLM_FAMILY", "deepseek"), "share family", nil},
		{"bad cap", with(keys, "BOOKWARDEN_EXTRACTOR_DEEPSEEK_MAX_CALLS", "0"), "BOOKWARDEN_EXTRACTOR_DEEPSEEK_MAX_CALLS", nil},
		{"unknown family", with(keys, "BOOKWARDEN_EXTRACTOR_GLM_FAMILY", "acme"), "BOOKWARDEN_EXTRACTOR_GLM_FAMILY", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x, err := ExtractorsFromEnv(func(k string) string { return tc.change[k] })
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want one naming %s", err, tc.wantErr)
				}
				// A gateway error is reported once, under the variables set.
				if strings.Count(err.Error(), "\n") > 0 && !strings.Contains(tc.name, "family") {
					t.Errorf("err = %v; want one error", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			tc.check(t, x)
		})
	}
	// FromEnv carries them, and never logs a key.
	env := baseEnv()
	for k, v := range keys {
		env[k] = v
	}
	cfg, err := FromEnv(func(k string) string { return env[k] })
	if err != nil || len(cfg.Extract.Extractors) != 3 {
		t.Fatalf("FromEnv: %v, %+v", err, cfg.Extract)
	}
	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("starting", "config", cfg)
	if strings.Contains(buf.String(), "oc-secret") {
		t.Fatalf("log line leaks a key: %s", buf.String())
	}
	if !strings.Contains(buf.String(), `"extractors":"luna,deepseek,glm"`) {
		t.Errorf("log line lacks the extractors: %s", buf.String())
	}
}

// CalibreFromEnv is what the read-only commands need: the content server and
// nothing else, so no database URL, allowed hosts or agent keys.
func TestCalibreFromEnv(t *testing.T) {
	only := func(drop ...string) func(string) string {
		env := map[string]string{
			"BOOKWARDEN_CALIBRE_URL":      "http://calibre:8081/",
			"BOOKWARDEN_CALIBRE_USER":     "bookwarden",
			"BOOKWARDEN_CALIBRE_PASSWORD": "calibre-secret\n",
			"BOOKWARDEN_CALIBRE_LIBRARY":  "Books",
		}
		for _, k := range drop {
			delete(env, k)
		}
		return func(k string) string { return env[k] }
	}
	c, err := CalibreFromEnv(only())
	if err != nil {
		t.Fatal(err)
	}
	if c.URL.String() != "http://calibre:8081" || c.User != "bookwarden" || c.Password != "calibre-secret" || c.Library != "Books" {
		t.Errorf("got %+v", c)
	}
	_, err = CalibreFromEnv(only("BOOKWARDEN_CALIBRE_URL", "BOOKWARDEN_CALIBRE_USER", "BOOKWARDEN_CALIBRE_PASSWORD"))
	for _, want := range []string{"BOOKWARDEN_CALIBRE_URL is required", "BOOKWARDEN_CALIBRE_USER is required", "BOOKWARDEN_CALIBRE_PASSWORD is required"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("error %v lacks %q", err, want)
		}
	}
	if _, err := CalibreFromEnv(func(k string) string {
		if k == "BOOKWARDEN_CALIBRE_URL" {
			return "http://u:p@calibre:8081"
		}
		return only()(k)
	}); err == nil || strings.Contains(err.Error(), "p@") {
		t.Errorf("credentials in the URL: error %v", err)
	}
}

// The password keeps its spaces, loses only the trailing CR/LF of a secrets
// file, and the space is warned about (ADR-033) by both entry points.
func TestCalibrePasswordSpaces(t *testing.T) {
	get := func(k string) string {
		if k == "BOOKWARDEN_CALIBRE_PASSWORD" {
			return "  pw  \r\n"
		}
		return baseEnv()[k]
	}
	const warning = "BOOKWARDEN_CALIBRE_PASSWORD starts or ends with a space; it is used as written"
	c, err := CalibreFromEnv(get)
	if err != nil {
		t.Fatal(err)
	}
	if c.Password != "  pw  " || len(c.Warnings) != 1 || c.Warnings[0] != warning {
		t.Errorf("CalibreFromEnv: password %q, warnings %v", c.Password, c.Warnings)
	}
	full, err := FromEnv(get)
	if err != nil {
		t.Fatal(err)
	}
	if full.CalibrePassword != "  pw  " || len(full.Warnings) != 1 || full.Warnings[0] != warning {
		t.Errorf("FromEnv: password %q, warnings %v", full.CalibrePassword, full.Warnings)
	}
	if c, _ := CalibreFromEnv(func(k string) string { return baseEnv()[k] }); len(c.Warnings) != 0 {
		t.Errorf("a plain password warns: %v", c.Warnings)
	}
}

func TestLogLevelFrom(t *testing.T) {
	tests := []struct {
		in      string
		want    slog.Level
		wantErr bool
	}{
		{"", slog.LevelInfo, false},
		{"debug", slog.LevelDebug, false},
		{"INFO", slog.LevelInfo, false},
		{"warn", slog.LevelWarn, false},
		{"error", slog.LevelError, false},
		{"warning", slog.LevelInfo, true},
		{"trace", slog.LevelInfo, true},
	}
	for _, tc := range tests {
		got, err := LogLevelFrom(func(string) string { return tc.in })
		if got != tc.want || (err != nil) != tc.wantErr {
			t.Errorf("LogLevelFrom(%q) = %v, %v; want %v, error %v", tc.in, got, err, tc.want, tc.wantErr)
		}
	}
}
