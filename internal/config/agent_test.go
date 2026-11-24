package config

import (
	"strings"
	"testing"
)

func TestAgentFamilyAndEgress(t *testing.T) {
	keys := map[string]string{
		"BOOKWARDEN_AGENT_ANTHROPIC_KEY": "ant-secret",
		"BOOKWARDEN_AGENT_OPENCODE_KEY":  "oc-secret",
		"BOOKWARDEN_AGENT_GEMINI_KEY":    "gem-secret",
	}
	lan := map[string]string{
		"BOOKWARDEN_AGENT_GEMINI_URL":        "http://10.0.0.122:8766/v1/chat/completions",
		"BOOKWARDEN_AGENT_GEMINI_ALLOW_CIDR": "10.0.0.122/32",
		"BOOKWARDEN_AGENT_GEMINI_FAMILY":     "google",
	}
	merge := func(ms ...map[string]string) map[string]string {
		out := map[string]string{}
		for _, m := range ms {
			for k, v := range m {
				out[k] = v
			}
		}
		return out
	}
	for _, tc := range []struct {
		name    string
		env     map[string]string
		wantErr string
		check   func(t *testing.T, a AgentConfig)
	}{
		{"default families", keys, "", func(t *testing.T, a AgentConfig) {
			for i, want := range []string{"anthropic", "deepseek", "google"} {
				if a.Seats[i].Family != want || len(a.Seats[i].AllowCIDR) != 0 {
					t.Errorf("seat %d = %+v", i, a.Seats[i])
				}
			}
		}},
		{"LAN proxy with exact CIDR", merge(keys, lan), "", func(t *testing.T, a AgentConfig) {
			s := a.Seats[2]
			if s.URL != lan["BOOKWARDEN_AGENT_GEMINI_URL"] || len(s.AllowCIDR) != 1 || s.AllowCIDR[0].String() != "10.0.0.122/32" {
				t.Fatalf("seat = %+v", s)
			}
			if len(a.Seats[0].AllowCIDR) != 0 || len(a.Seats[1].AllowCIDR) != 0 {
				t.Fatal("the allowance leaked to another seat")
			}
		}},
		{"several prefixes, v6 and spaces", merge(keys, lan, map[string]string{
			"BOOKWARDEN_AGENT_GEMINI_ALLOW_CIDR": " 10.0.0.122/32 , fd00::5/128 ,::ffff:10.0.0.9/128"}), "", func(t *testing.T, a AgentConfig) {
			var got []string
			for _, p := range a.Seats[2].AllowCIDR {
				got = append(got, p.String())
			}
			if strings.Join(got, ",") != "10.0.0.122/32,fd00::5/128,10.0.0.9/32" {
				t.Fatalf("prefixes = %v", got)
			}
		}},
		{"family override", merge(keys, map[string]string{"BOOKWARDEN_AGENT_DEEPSEEK_FAMILY": "qwen"}), "", func(t *testing.T, a AgentConfig) {
			if a.Seats[1].Family != "qwen" {
				t.Fatalf("family = %q", a.Seats[1].Family)
			}
		}},
		{"http without CIDR", map[string]string{"BOOKWARDEN_AGENT_GEMINI_URL": "http://10.0.0.122:8766/v1"}, "BOOKWARDEN_AGENT_GEMINI_URL", nil},
		{"CIDR too wide", merge(keys, lan, map[string]string{"BOOKWARDEN_AGENT_GEMINI_ALLOW_CIDR": "10.0.0.0/24"}), "BOOKWARDEN_AGENT_GEMINI_ALLOW_CIDR", nil},
		{"v6 CIDR too wide", merge(keys, lan, map[string]string{"BOOKWARDEN_AGENT_GEMINI_ALLOW_CIDR": "fd00::/64"}), "BOOKWARDEN_AGENT_GEMINI_ALLOW_CIDR", nil},
		{"CIDR is a bare address", merge(keys, lan, map[string]string{"BOOKWARDEN_AGENT_GEMINI_ALLOW_CIDR": "10.0.0.122"}), "BOOKWARDEN_AGENT_GEMINI_ALLOW_CIDR", nil},
		{"CIDR garbage", merge(keys, lan, map[string]string{"BOOKWARDEN_AGENT_GEMINI_ALLOW_CIDR": "lan"}), "BOOKWARDEN_AGENT_GEMINI_ALLOW_CIDR", nil},
		{"unknown family", merge(keys, map[string]string{"BOOKWARDEN_AGENT_GEMINI_FAMILY": "mistral"}), "BOOKWARDEN_AGENT_GEMINI_FAMILY", nil},
		{"same family twice", merge(keys, map[string]string{"BOOKWARDEN_AGENT_GEMINI_FAMILY": "anthropic"}), "family anthropic", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := merge(baseEnv(), tc.env)
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
}

func TestSecrets(t *testing.T) {
	for _, tc := range []struct {
		name, password string
		want           string
		warns          bool
	}{
		{"plain", "pass", "pass", false},
		{"CRLF", "pass\r\n", "pass", false},
		{"LF", "pass\n", "pass", false},
		{"inner spaces", "pass word", "pass word", false},
		{"trailing space kept, warned", "pass \n", "pass ", true},
		{"leading space kept, warned", "  pass", "  pass", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := baseEnv()
			env["BOOKWARDEN_CALIBRE_PASSWORD"] = tc.password
			cfg, err := FromEnv(func(k string) string { return env[k] })
			if err != nil {
				t.Fatal(err)
			}
			if cfg.CalibrePassword != tc.want {
				t.Fatalf("password = %q, want %q", cfg.CalibrePassword, tc.want)
			}
			if tc.warns != (len(cfg.Warnings) == 1) {
				t.Fatalf("warnings = %v, want a warning: %v", cfg.Warnings, tc.warns)
			}
			for _, w := range cfg.Warnings {
				if !strings.Contains(w, "BOOKWARDEN_CALIBRE_PASSWORD") || strings.Contains(w, "pass") {
					t.Fatalf("warning %q must name the variable and not the value", w)
				}
			}
		})
	}

	env := baseEnv()
	env["BOOKWARDEN_AGENT_GEMINI_KEY"] = " key\r\n"
	env["BOOKWARDEN_GOOGLE_BOOKS_KEY"] = " gb "
	env["BOOKWARDEN_CALIBRE_USER"] = " bookwarden "
	cfg, err := FromEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.Seats[0].Key != "key" || cfg.GoogleBooksKey != "gb" || cfg.CalibreUser != "bookwarden" {
		t.Fatalf("keys are trimmed: %q %q %q", cfg.Agent.Seats[0].Key, cfg.GoogleBooksKey, cfg.CalibreUser)
	}
	env["BOOKWARDEN_CALIBRE_PASSWORD"] = "   "
	if _, err := FromEnv(func(k string) string { return env[k] }); err == nil || !strings.Contains(err.Error(), "BOOKWARDEN_CALIBRE_PASSWORD is required") {
		t.Fatalf("blank password: %v", err)
	}
}

func TestAgentURLChecks(t *testing.T) {
	for _, tc := range []struct {
		name, url, cidr string
		wantErr         string
	}{
		{"no host", "http://:8766/", "10.0.0.122/32", "BOOKWARDEN_AGENT_GEMINI_URL"},
		{"https no host", "https://:443/v1", "", "BOOKWARDEN_AGENT_GEMINI_URL"},
		{"IP outside the CIDR", "http://10.0.0.123:8766", "10.0.0.122/32", "not inside"},
		{"v6 IP outside the CIDR", "http://[fd00::6]:8766", "fd00::5/128", "not inside"},
		{"IP inside the CIDR", "http://10.0.0.122:8766", "10.0.0.122/32", ""},
		{"name with a CIDR", "http://proxy.lan:8766", "10.0.0.122/32", ""},
		{"zoned 6to4", "https://[2002:c0a8:7a::1%25lo]:8443/v1", "", "zone"},
		{"zoned with a CIDR", "http://[fe80::1%25eth0]:8766/v1", "fe80::1/128", "zone"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := baseEnv()
			env["BOOKWARDEN_AGENT_GEMINI_KEY"] = "k"
			env["BOOKWARDEN_AGENT_GEMINI_URL"] = tc.url
			if tc.cidr != "" {
				env["BOOKWARDEN_AGENT_GEMINI_ALLOW_CIDR"] = tc.cidr
			}
			_, err := FromEnv(func(k string) string { return env[k] })
			if tc.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want one containing %q", err, tc.wantErr)
			}
		})
	}
}
