package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/agent"
	"github.com/felixapel/calibre-bookwarden/internal/config"
)

type noCache struct{}

func (noCache) CachedRaw(context.Context, string) (string, string, bool, error) {
	return "", "", false, nil
}

func TestExtractionService(t *testing.T) {
	seat := func(name, family string) agent.SeatConfig {
		return agent.SeatConfig{Name: name, Wire: "openai", URL: "https://opencode.ai/zen/go/v1/chat/completions", Model: "m-" + name, Key: "k", Family: family}
	}
	for _, tc := range []struct {
		name     string
		cfg      config.ExtractorConfig
		on       bool
		logWants string
	}{
		{"none configured", config.ExtractorConfig{Missing: []string{"luna", "deepseek", "glm"}}, false, "extraction=off"},
		{"one is not enough", config.ExtractorConfig{Extractors: []agent.SeatConfig{seat("luna", "openai")}, MaxCalls: map[string]int{"luna": 5}}, false, "needs at least two"},
		{"two", config.ExtractorConfig{Extractors: []agent.SeatConfig{seat("luna", "openai"), seat("glm", "zhipu")}, MaxCalls: map[string]int{"luna": 5, "glm": 5}}, true, "extraction=on"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			svc, err := extractionService(tc.cfg, noCache{}, nil, slog.New(slog.NewTextHandler(&buf, nil)))
			if err != nil {
				t.Fatal(err)
			}
			if (svc != nil) != tc.on || !strings.Contains(buf.String(), tc.logWants) {
				t.Fatalf("service %v, log %q", svc != nil, buf.String())
			}
			if strings.Contains(buf.String(), "k ") || strings.Contains(buf.String(), "=k") {
				t.Errorf("the log carries a key: %s", buf.String())
			}
		})
	}
}
