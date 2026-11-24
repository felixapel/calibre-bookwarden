// Package config reads bookwarden's settings from environment variables.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/felixapel/calibre-bookwarden/internal/agent"
	"github.com/felixapel/calibre-bookwarden/internal/netguard"
	"github.com/felixapel/calibre-bookwarden/internal/schedule"
)

// Config is bookwarden's runtime configuration (spec section 4).
type Config struct {
	DatabaseURL     string
	CalibreURL      *url.URL
	CalibreUser     string
	CalibrePassword string
	CalibreLibrary  string
	AllowedHosts    []string
	GoogleBooksKey  string
	HardcoverToken  string
	TrustedProxies  []netip.Prefix
	MetricsToken    string
	Listen          string
	LogLevel        slog.Level
	// StopGrace bounds how long a stop waits for the book in hand and the
	// writer's current operation before cancelling hard.
	StopGrace time.Duration
	Agent     AgentConfig
	// Extract is M4's extractors.
	Extract ExtractorConfig
	// RetainRuns is how many of the newest runs retention keeps; 0 keeps
	// every run (ADR-039).
	RetainRuns int
	// Schedule is the scheduled runs, off unless set (ADR-039).
	Schedule schedule.Config
	// Heartbeat is the Uptime Kuma push monitor each ended run reports to.
	Heartbeat HeartbeatConfig
	// Warnings are startup notices; they never carry a secret value.
	Warnings []string
}

// HeartbeatConfig is where the heartbeat goes (ADR-041). URL holds a secret
// token and is never logged or shown; empty means off. AllowCIDR is the
// exact addresses it may dial.
type HeartbeatConfig struct {
	URL       string
	AllowCIDR []netip.Prefix
}

// Host returns scheme://host of the URL, the only part that may be shown.
func (h HeartbeatConfig) Host() string {
	u, err := url.Parse(h.URL)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// heartbeatFromEnv applies the same egress rules as a seat: plain http or a
// private literal address needs the exact allowance, and a literal outside it
// is refused. Errors never carry the URL.
func heartbeatFromEnv(get func(string) string) (HeartbeatConfig, []error) {
	const urlVar, allowVar = "BOOKWARDEN_HEARTBEAT_URL", "BOOKWARDEN_HEARTBEAT_ALLOW_CIDR"
	var h HeartbeatConfig
	var errs []error
	if v := get(allowVar); v != "" {
		cidrs, err := parseExactCIDRs(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s %w", allowVar, err))
		}
		h.AllowCIDR = cidrs
	}
	h.URL = get(urlVar)
	if h.URL == "" {
		return h, errs
	}
	cerrs := checkURL(urlVar, allowVar, agent.SeatConfig{URL: h.URL, AllowCIDR: h.AllowCIDR})
	if u, err := url.Parse(h.URL); len(cerrs) == 0 && err == nil && len(h.AllowCIDR) == 0 {
		if a, err := netip.ParseAddr(u.Hostname()); err == nil && netguard.Refused(a) {
			cerrs = append(cerrs, fmt.Errorf("%s names a private address, so %s must allow it", urlVar, allowVar))
		}
	}
	return h, append(errs, cerrs...)
}

// AgentConfig is agent fixing's panel and limits (ADR-029). Seats holds the
// seats that have a key, in panel order; Missing names the others.
type AgentConfig struct {
	Seats           []agent.SeatConfig
	Missing         []string
	MaxTokensPerRun int64
}

// agentSeats are the panel's seats and their defaults; an endpoint, model or
// effort can be overridden per seat.
var agentSeats = []struct{ name, keyVar, wire, url, model, effort, family string }{
	{"haiku", "BOOKWARDEN_AGENT_ANTHROPIC_KEY", "anthropic", "https://api.anthropic.com/v1/messages", "claude-haiku-5-5", "max", "anthropic"},
	{"deepseek", "BOOKWARDEN_AGENT_OPENCODE_KEY", "openai", "https://opencode.ai/zen/go/v1/chat/completions", "deepseek-v4.1-flash", "max", "deepseek"},
	{"gemini", "BOOKWARDEN_AGENT_GEMINI_KEY", "openai", "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions", "gemini-3.8-flash", "high", "google"},
}

// agentFamilies are the model families a seat may name (ADR-033).
var agentFamilies = []string{"anthropic", "deepseek", "google", "openai", "qwen", "moonshot", "minimax", "zhipu", "tencent"}

// ExtractorConfig is M4's extractors (design section 4): the configured ones,
// in order, and the per-run call cap of each. Missing names the others.
type ExtractorConfig struct {
	Extractors []agent.SeatConfig
	Missing    []string
	MaxCalls   map[string]int
	// Gateway is where the extractors' calls go; extract-bench builds its
	// candidates on it.
	Gateway ExtractorGateway
}

// extractors are M4's extractors, their default models, efforts and per-run
// call caps (design section 4a). luna and glm are the extract-bench choice of
// 2026-11-03, the cheapest per grounded fact; each cap was sized as about
// 80 % of a 5-hour quota window at the measured cost per call, though run 12
// showed opencode-go's window is per account, so a cold run can end at the
// shared limit first (section 4d). deepseek replaced the benchmark's hy3 at
// Felix's choice the same day and was measured once Bifrost's rules were
// scoped: effort none recalls the most and reasoning adds nothing. glm moved
// from none to low on 2026-11-04, when its upstream began refusing to turn
// thinking off (section 4e). The three extractors' families differ, as
// agreement needs independent ones.
var extractors = []struct {
	name, model, family, effort string
	maxCalls                    int
}{
	{"luna", "gpt-6-luna", "openai", "low", 5000},
	{"deepseek", "deepseek-v4.1-flash", "deepseek", "none", 5000},
	{"glm", "glm-5.3-flash", "zhipu", "low", 15000},
}

const (
	opencodeGoURL   = "https://opencode.ai/zen/go/v1/chat/completions"
	opencodeSession = "x-opencode-session"
	bifrostSession  = "x-bf-eh-" + opencodeSession // Bifrost forwards x-bf-eh-* headers
	bifrostProvider = "opencode-go/"               // Bifrost's name for opencode-go's models
)

// ExtractorGateway is where extractor calls go: opencode-go directly with
// BOOKWARDEN_EXTRACTOR_KEY, or the Bifrost gateway at
// BOOKWARDEN_EXTRACTOR_BIFROST_URL, which holds the provider keys itself (a
// BOOKWARDEN_EXTRACTOR_KEY set anyway is Bifrost's virtual key). Through
// Bifrost the default models are opencode-go's.
type ExtractorGateway struct {
	// Seat holds what every extractor shares: wire, URL, key, session header
	// and address allowance.
	Seat agent.SeatConfig
	// ModelPrefix names the provider in a gateway's model ids ("opencode-go/").
	ModelPrefix string
}

// Configured reports whether extractors can be called at all.
func (g ExtractorGateway) Configured() bool { return g.Seat.Key != "" || g.Seat.Keyless }

// Provider returns the configuration of an extractor named name that calls
// model, a default model id the gateway's prefix is added to.
func (g ExtractorGateway) Provider(name, model, family string) agent.SeatConfig {
	c := g.Seat
	c.Name, c.Model, c.Family = name, g.ModelPrefix+model, family
	return c
}

func extractorGatewayFromEnv(get func(string) string) (ExtractorGateway, []error) {
	const prefix = "BOOKWARDEN_EXTRACTOR_"
	var errs []error
	g := ExtractorGateway{Seat: agent.SeatConfig{Wire: "openai", URL: opencodeGoURL, Key: get(prefix + "KEY"), SessionHeader: opencodeSession}}
	if v := get(prefix + "ALLOW_CIDR"); v != "" {
		cidrs, err := parseExactCIDRs(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("%sALLOW_CIDR %w", prefix, err))
		}
		g.Seat.AllowCIDR = cidrs
	}
	base := get(prefix + "BIFROST_URL")
	if base == "" {
		if g.Configured() {
			errs = append(errs, checkURL(opencodeGoURL, prefix+"ALLOW_CIDR", g.Seat)...)
		}
		return g, errs
	}
	u, err := url.Parse(base)
	if err != nil || u.Hostname() == "" || u.User != nil || strings.Trim(u.Path, "/") != "" || u.RawQuery != "" || u.Fragment != "" {
		return g, append(errs, fmt.Errorf("%sBIFROST_URL must be the gateway's base URL, as http://host:port", prefix))
	}
	g.Seat.URL = strings.TrimRight(base, "/") + "/v1/chat/completions"
	g.Seat.Keyless = true
	g.Seat.SessionHeader = bifrostSession
	g.Seat.NoContentLog = true // excerpts don't belong in Bifrost's log store (§10)
	g.ModelPrefix = bifrostProvider
	return g, append(errs, checkURL(prefix+"BIFROST_URL", prefix+"ALLOW_CIDR", g.Seat)...)
}

// ExtractorsFromEnv reads the extractors' configuration. A command that only
// extracts (extract-bench) needs nothing else. Each extractor's URL, model,
// effort, family and address allowance can be overridden as
// BOOKWARDEN_EXTRACTOR_<NAME>_*; an overriding model id is sent as given.
func ExtractorsFromEnv(get func(string) string) (ExtractorConfig, error) {
	x := ExtractorConfig{MaxCalls: map[string]int{}}
	g, errs := extractorGatewayFromEnv(get)
	x.Gateway = g
	for _, d := range extractors {
		prefix := "BOOKWARDEN_EXTRACTOR_" + strings.ToUpper(d.name) + "_"
		// The gateway is checked once, above; an extractor's own URL or
		// allowance is checked here.
		t := g.Provider(d.name, d.model, d.family)
		t.Effort = d.effort
		t.Key, t.Keyless = "", false
		c, perrs := providerFromEnv(prefix, t, get)
		errs = append(errs, perrs...)
		if g.Seat.Keyless && get(prefix+"URL") != "" {
			errs = append(errs, fmt.Errorf("%sURL can't be set with BOOKWARDEN_EXTRACTOR_BIFROST_URL", prefix))
		}
		c.Key, c.Keyless = g.Seat.Key, g.Seat.Keyless
		if get(prefix+"URL") == "" && get(prefix+"ALLOW_CIDR") != "" && g.Configured() {
			errs = append(errs, checkURL(prefix+"URL", prefix+"ALLOW_CIDR", c)...)
		}
		x.MaxCalls[d.name] = d.maxCalls
		if v := get(prefix + "MAX_CALLS"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 {
				errs = append(errs, fmt.Errorf("%sMAX_CALLS must be a whole number of at least 1", prefix))
			}
			x.MaxCalls[d.name] = n
		}
		if !g.Configured() {
			x.Missing = append(x.Missing, d.name)
			continue
		}
		x.Extractors = append(x.Extractors, c)
	}
	errs = append(errs, distinctFamilies("extractors", x.Extractors, "agreement needs independent families")...)
	return x, errors.Join(errs...)
}

const defaultAgentMaxTokens = 20_000_000

// providerFromEnv applies a provider's overrides, read from prefix+"URL",
// "MODEL", "EFFORT", "FAMILY" and "ALLOW_CIDR", to its defaults c (ADR-033).
func providerFromEnv(prefix string, c agent.SeatConfig, get func(string) string) (agent.SeatConfig, []error) {
	var errs []error
	if v := get(prefix + "ALLOW_CIDR"); v != "" {
		cidrs, err := parseExactCIDRs(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("%sALLOW_CIDR %w", prefix, err))
		}
		c.AllowCIDR = cidrs
	}
	if v := get(prefix + "URL"); v != "" {
		c.URL = v
	}
	if c.Key != "" || c.Keyless || get(prefix+"URL") != "" {
		errs = append(errs, checkURL(prefix+"URL", prefix+"ALLOW_CIDR", c)...)
	}
	if v := get(prefix + "FAMILY"); v != "" {
		if !slices.Contains(agentFamilies, v) {
			errs = append(errs, fmt.Errorf("%sFAMILY must be one of %s", prefix, strings.Join(agentFamilies, ", ")))
		}
		c.Family = v
	}
	if v := get(prefix + "MODEL"); v != "" {
		c.Model = v
	}
	if v := get(prefix + "EFFORT"); v != "" {
		c.Effort = v
	}
	return c, errs
}

// checkURL reports, as errors naming urlVar and allowVar, a URL c may not
// call: not http(s), with credentials or a zone, plain http without an
// allowance, or a literal address outside it.
func checkURL(urlVar, allowVar string, c agent.SeatConfig) []error {
	u, err := url.Parse(c.URL)
	switch {
	case err != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http"):
		return []error{fmt.Errorf("%s must be an https URL", urlVar)}
	case strings.Contains(u.Hostname(), "%"):
		return []error{fmt.Errorf("%s must not name an IPv6 zone", urlVar)}
	case u.Scheme == "http" && len(c.AllowCIDR) == 0:
		return []error{fmt.Errorf("%s must be an https URL, or http with %s set", urlVar, allowVar)}
	case len(c.AllowCIDR) > 0 && literalOutside(u.Hostname(), c.AllowCIDR):
		return []error{fmt.Errorf("%s host is not inside %s", urlVar, allowVar)}
	}
	return nil
}

// distinctFamilies reports, as errors, providers of one list that share a
// family: independence is the point of having several (ADR-033).
func distinctFamilies(kind string, cs []agent.SeatConfig, why string) []error {
	var errs []error
	seen := map[string]string{}
	for _, c := range cs {
		if other, ok := seen[c.Family]; ok {
			errs = append(errs, fmt.Errorf("%s %s and %s share family %s; %s", kind, other, c.Name, c.Family, why))
		}
		seen[c.Family] = c.Name
	}
	return errs
}

func agentFromEnv(get func(string) string) (AgentConfig, []error) {
	var errs []error
	a := AgentConfig{MaxTokensPerRun: defaultAgentMaxTokens}
	for _, d := range agentSeats {
		prefix := "BOOKWARDEN_AGENT_" + strings.ToUpper(d.name) + "_"
		c, perrs := providerFromEnv(prefix, agent.SeatConfig{Name: d.name, Wire: d.wire, URL: d.url, Model: d.model, Effort: d.effort, Family: d.family, Key: get(d.keyVar)}, get)
		errs = append(errs, perrs...)
		if c.Key == "" {
			a.Missing = append(a.Missing, d.name)
			continue
		}
		a.Seats = append(a.Seats, c)
	}
	// The panel's value is three independent vendors (ADR-033).
	errs = append(errs, distinctFamilies("seats", a.Seats, "the panel needs independent vendors")...)
	if v := get("BOOKWARDEN_AGENT_MAX_TOKENS_PER_RUN"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1 {
			errs = append(errs, errors.New("BOOKWARDEN_AGENT_MAX_TOKENS_PER_RUN must be a whole number of at least 1"))
		}
		a.MaxTokensPerRun = n
	}
	return a, errs
}

const (
	defaultListen    = ":8080"
	defaultStopGrace = 25 * time.Second
	// defaultRetainRuns is the spec's retention: the last 10 runs.
	defaultRetainRuns = 10
)

// scheduleFromEnv reads the scheduled runs and the time zone they run in.
// The zone is the container's: TZ, which Unraid sets, else the process's
// local zone. A TZ this image can't resolve is an error only when a schedule
// is on, so that it can't stop a deployment that doesn't use one.
func scheduleFromEnv(get func(string) string) (schedule.Config, error) {
	inc, full := get(schedule.EnvIncremental), get(schedule.EnvFull)
	loc := time.Local
	if inc != "" || full != "" {
		if tz := strings.TrimPrefix(get("TZ"), ":"); tz != "" {
			l, err := time.LoadLocation(tz)
			if err != nil {
				return schedule.Config{}, fmt.Errorf("TZ %q is not a time zone: the schedule needs a name such as Europe/Berlin", tz)
			}
			loc = l
		}
	}
	return schedule.Parse(inc, full, loc)
}

// LogLevelFrom reads BOOKWARDEN_LOG_LEVEL: debug, info, warn or error, any
// case, info when unset. On an invalid value it returns info and an error, so
// a caller that must log before the configuration is checked can still do so.
func LogLevelFrom(getenv func(string) string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(getenv("BOOKWARDEN_LOG_LEVEL"))) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return slog.LevelInfo, errors.New("BOOKWARDEN_LOG_LEVEL must be debug, info, warn or error")
}

// CalibreConfig is what a command that only reads the library needs: the
// content server's address and account. Library is "" for its default.
type CalibreConfig struct {
	URL      *url.URL
	User     string
	Password string
	Library  string
	// Warnings are startup notices; they never carry a secret value.
	Warnings []string
}

// CalibreFromEnv reads only the BOOKWARDEN_CALIBRE_* variables, with the same
// rules as FromEnv, so a diagnostic can run without the database. It never
// puts a secret value into an error.
func CalibreFromEnv(getenv func(string) string) (CalibreConfig, error) {
	var errs []error
	c := CalibreConfig{
		User:     strings.TrimSpace(getenv("BOOKWARDEN_CALIBRE_USER")),
		Password: strings.TrimRight(getenv("BOOKWARDEN_CALIBRE_PASSWORD"), "\r\n"),
		Library:  strings.TrimSpace(getenv("BOOKWARDEN_CALIBRE_LIBRARY")),
	}
	if c.User == "" {
		errs = append(errs, errors.New("BOOKWARDEN_CALIBRE_USER is required"))
	}
	if strings.TrimSpace(c.Password) == "" {
		c.Password = ""
		errs = append(errs, errors.New("BOOKWARDEN_CALIBRE_PASSWORD is required"))
	} else if c.Password != strings.TrimSpace(c.Password) {
		c.Warnings = append(c.Warnings, "BOOKWARDEN_CALIBRE_PASSWORD starts or ends with a space; it is used as written")
	}
	if raw := strings.TrimSpace(getenv("BOOKWARDEN_CALIBRE_URL")); raw == "" {
		errs = append(errs, errors.New("BOOKWARDEN_CALIBRE_URL is required"))
	} else if u, err := parseCalibreURL(raw); err != nil {
		errs = append(errs, err)
	} else {
		c.URL = u
	}
	if len(errs) > 0 {
		return CalibreConfig{}, errors.Join(errs...)
	}
	return c, nil
}

// minMetricsToken is the shortest BOOKWARDEN_METRICS_TOKEN accepted.
const minMetricsToken = 32

// FromEnv builds a Config from getenv (os.Getenv in production). It reports
// every problem at once and never puts a secret value into an error.
func FromEnv(getenv func(string) string) (Config, error) {
	var errs []error
	get := func(name string) string { return strings.TrimSpace(getenv(name)) }
	require := func(name string) string {
		v := get(name)
		if v == "" {
			errs = append(errs, fmt.Errorf("%s is required", name))
		}
		return v
	}

	cfg := Config{
		DatabaseURL:    require("BOOKWARDEN_DATABASE_URL"),
		GoogleBooksKey: get("BOOKWARDEN_GOOGLE_BOOKS_KEY"),
		HardcoverToken: get("BOOKWARDEN_HARDCOVER_TOKEN"),
		MetricsToken:   get("BOOKWARDEN_METRICS_TOKEN"),
		Listen:         get("BOOKWARDEN_LISTEN"),
	}
	// /metrics has no attempt limit, so its token must not be guessable.
	if n := utf8.RuneCountInString(cfg.MetricsToken); n > 0 && n < minMetricsToken {
		errs = append(errs, fmt.Errorf("BOOKWARDEN_METRICS_TOKEN must be at least %d characters", minMetricsToken))
	}
	// The content server's settings are read by CalibreFromEnv, so that the
	// read-only commands and serve can't drift apart. The password keeps its
	// spaces, but a trailing CR or LF from a secrets file is not part of it.
	calibre, err := CalibreFromEnv(getenv)
	if err != nil {
		errs = append(errs, err)
	}
	cfg.CalibreURL, cfg.CalibreUser, cfg.CalibrePassword, cfg.CalibreLibrary = calibre.URL, calibre.User, calibre.Password, calibre.Library
	cfg.Warnings = append(cfg.Warnings, calibre.Warnings...)
	if cfg.Listen == "" {
		cfg.Listen = defaultListen
	}
	var levelErr error
	if cfg.LogLevel, levelErr = LogLevelFrom(getenv); levelErr != nil {
		errs = append(errs, levelErr)
	}
	cfg.StopGrace = defaultStopGrace
	if v := get("BOOKWARDEN_STOP_GRACE"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			errs = append(errs, errors.New("BOOKWARDEN_STOP_GRACE must be a duration such as 25s"))
		} else {
			cfg.StopGrace = d
		}
	}
	cfg.RetainRuns = defaultRetainRuns
	if v := get("BOOKWARDEN_RETAIN_RUNS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			errs = append(errs, errors.New("BOOKWARDEN_RETAIN_RUNS must be a whole number of 0 or more (0 keeps every run)"))
		} else {
			cfg.RetainRuns = n
		}
	}
	var schedErr error
	if cfg.Schedule, schedErr = scheduleFromEnv(get); schedErr != nil {
		errs = append(errs, schedErr)
	}
	var hbErrs []error
	cfg.Heartbeat, hbErrs = heartbeatFromEnv(get)
	errs = append(errs, hbErrs...)
	var agentErrs []error
	cfg.Agent, agentErrs = agentFromEnv(get)
	errs = append(errs, agentErrs...)
	var extractErr error
	if cfg.Extract, extractErr = ExtractorsFromEnv(get); extractErr != nil {
		errs = append(errs, extractErr)
	}
	if cfg.DatabaseURL != "" {
		if err := checkDatabaseURL(cfg.DatabaseURL); err != nil {
			errs = append(errs, err)
		}
	}
	if raw := require("BOOKWARDEN_ALLOWED_HOSTS"); raw != "" {
		hosts, err := parseAllowedHosts(raw)
		if err != nil {
			errs = append(errs, err)
		}
		cfg.AllowedHosts = hosts
	}
	if raw := get("BOOKWARDEN_TRUSTED_PROXIES"); raw != "" {
		proxies, err := parseTrustedProxies(raw)
		if err != nil {
			errs = append(errs, err)
		}
		cfg.TrustedProxies = proxies
	}
	if _, _, err := net.SplitHostPort(cfg.Listen); err != nil {
		errs = append(errs, errors.New("BOOKWARDEN_LISTEN must be host:port or :port"))
	}
	if len(errs) > 0 {
		return Config{}, errors.Join(errs...)
	}
	return cfg, nil
}

// LogValue keeps secrets out of logs: only whether they are set is logged.
func (c Config) LogValue() slog.Value {
	calibre := ""
	if c.CalibreURL != nil {
		calibre = c.CalibreURL.String()
	}
	return slog.GroupValue(
		slog.String("calibre_url", calibre),
		slog.String("calibre_user", c.CalibreUser),
		slog.String("calibre_library", c.CalibreLibrary),
		slog.Any("allowed_hosts", c.AllowedHosts),
		slog.Int("trusted_proxies", len(c.TrustedProxies)),
		slog.Bool("google_books_key_set", c.GoogleBooksKey != ""),
		slog.Bool("hardcover_token_set", c.HardcoverToken != ""),
		slog.Bool("metrics_enabled", c.MetricsToken != ""),
		slog.String("listen", c.Listen),
		slog.String("log_level", c.LogLevel.String()),
		slog.Duration("stop_grace", c.StopGrace),
		slog.Int("retain_runs", c.RetainRuns),
		slog.String("schedule_incremental", c.Schedule.IncrementalText()),
		slog.String("schedule_full", c.Schedule.FullText()),
		slog.String("agent_seats", strings.Join(seatNames(c.Agent.Seats), ",")),
		slog.Int64("agent_max_tokens_per_run", c.Agent.MaxTokensPerRun),
		slog.String("extractors", strings.Join(seatNames(c.Extract.Extractors), ",")),
		slog.String("heartbeat", c.Heartbeat.Host()),
	)
}

func seatNames(seats []agent.SeatConfig) []string {
	names := make([]string, len(seats))
	for i, s := range seats {
		names[i] = s.Name
	}
	return names
}

// literalOutside reports whether host is an IP address that no prefix holds.
// A host name is left to the dial-time check.
func literalOutside(host string, allow []netip.Prefix) bool {
	a, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	a = a.Unmap()
	return !slices.ContainsFunc(allow, func(p netip.Prefix) bool { return p.Contains(a) })
}

// parseExactCIDRs reads a seat's egress allowance: single addresses only, so
// an allowance can't grow into a network (ADR-033).
func parseExactCIDRs(raw string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, part := range strings.Split(raw, ",") {
		s := strings.TrimSpace(part)
		if s == "" {
			continue
		}
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return nil, fmt.Errorf("entry %q is not an address prefix such as 192.0.2.10/32", s)
		}
		if p.Addr().Is4In6() && p.Bits() == 128 {
			p = netip.PrefixFrom(p.Addr().Unmap(), 32)
		}
		if p.Bits() != p.Addr().BitLen() {
			return nil, fmt.Errorf("entry %q must be one address: /32 for IPv4, /128 for IPv6", s)
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil, errors.New("has no entries")
	}
	return out, nil
}

func checkDatabaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Host == "" {
		// Never echo the value: it holds the database password.
		return errors.New("BOOKWARDEN_DATABASE_URL must be a postgres:// URL with a host")
	}
	return nil
}

func parseCalibreURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	switch {
	case err != nil, u.Host == "", u.Scheme != "http" && u.Scheme != "https":
		return nil, errors.New("BOOKWARDEN_CALIBRE_URL must be an http(s) URL such as http://calibre:8081")
	case u.User != nil:
		return nil, errors.New("BOOKWARDEN_CALIBRE_URL must not contain credentials; use BOOKWARDEN_CALIBRE_USER and BOOKWARDEN_CALIBRE_PASSWORD")
	case u.RawQuery != "" || u.Fragment != "":
		return nil, errors.New("BOOKWARDEN_CALIBRE_URL must not have a query or fragment")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return u, nil
}

func parseAllowedHosts(raw string) ([]string, error) {
	var hosts []string
	for _, part := range strings.Split(raw, ",") {
		h := strings.ToLower(strings.TrimSpace(part))
		if h == "" {
			continue
		}
		h = strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")
		if strings.Contains(h, "/") || (strings.Contains(h, ":") && !isIP(h)) {
			return nil, fmt.Errorf("BOOKWARDEN_ALLOWED_HOSTS entry %q must be a host name or IP address without a port", strings.TrimSpace(part))
		}
		hosts = append(hosts, strings.TrimSuffix(h, "."))
	}
	if len(hosts) == 0 {
		return nil, errors.New("BOOKWARDEN_ALLOWED_HOSTS has no entries")
	}
	return hosts, nil
}

func isIP(s string) bool {
	_, err := netip.ParseAddr(s)
	return err == nil
}

// minProxyBits is the narrowest trusted-proxy prefix accepted: /16 for
// IPv4, /48 for IPv6.
func minProxyBits(a netip.Addr) int {
	switch {
	case a.Is4In6():
		return 96 + 16 // an IPv4-mapped prefix counts its bits out of 128
	case a.Is6():
		return 48
	}
	return 16
}

func parseTrustedProxies(raw string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for i, part := range strings.Split(raw, ",") {
		s := strings.TrimSpace(part)
		if s == "" {
			continue
		}
		if p, err := netip.ParsePrefix(s); err == nil {
			// A wide prefix trusts X-Forwarded-For from almost anyone, and
			// then a forged header picks the address the login backoff
			// counts. The entry is named by position: it may be a secret
			// network layout, and it is a config value either way.
			if floor := minProxyBits(p.Addr()); p.Bits() < floor {
				return nil, fmt.Errorf("BOOKWARDEN_TRUSTED_PROXIES entry %d is wider than /%d", i+1, floor)
			}
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(s)
		if err != nil {
			return nil, fmt.Errorf("BOOKWARDEN_TRUSTED_PROXIES entry %d is not an IP address or CIDR", i+1)
		}
		a = a.Unmap()
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}
