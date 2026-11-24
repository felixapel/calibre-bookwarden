package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/report"
	"github.com/felixapel/calibre-bookwarden/internal/store"
	"github.com/felixapel/calibre-bookwarden/internal/testutil/pgtest"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

func TestRun(t *testing.T) {
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("ok\n"))
	}))
	defer healthy.Close()
	unhealthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer unhealthy.Close()
	listenOf := func(s *httptest.Server) string { return strings.TrimPrefix(s.URL, "http://") }

	tests := []struct {
		name       string
		args       []string
		env        map[string]string
		stdin      string
		wantCode   int
		wantStdout string
		wantStderr string
		notStderr  string
	}{
		{name: "version", args: []string{"version"}, wantCode: 0, wantStdout: "bookwarden dev\n"},
		{name: "unknown command", args: []string{"frobnicate"}, wantCode: 2, wantStderr: `unknown command "frobnicate" (want: serve, healthcheck, version, ocr-check, parity, agent-review, verify-seals, eval, inspect, extract-bench)`},
		{name: "parity without a run", args: []string{"parity", "--certificate-a", "x.jsonl"}, wantCode: 2, wantStderr: "usage: bookwarden parity --run N --certificate-a FILE"},
		{name: "parity without a file", args: []string{"parity", "--run", "7"}, wantCode: 2, wantStderr: "usage: bookwarden parity --run N --certificate-a FILE"},
		{name: "parity reads the export from stdin", args: []string{"parity", "--run", "7", "--certificate-a", "-"}, stdin: "{nope\n", wantCode: 1, wantStderr: "export line 1"},
		{name: "parity with a missing file", args: []string{"parity", "--run", "7", "--certificate-a", "/nonexistent/x.jsonl"}, env: map[string]string{"BOOKWARDEN_DATABASE_URL": "postgres://u:p@127.0.0.1:1/db"}, wantCode: 1, wantStderr: "parity: "},
		{name: "agent-review without a run", args: []string{"agent-review", "--calibrate"}, wantCode: 2, wantStderr: "usage: bookwarden agent-review --run N --calibrate"},
		{name: "agent-review without --calibrate", args: []string{"agent-review", "--run", "4"}, wantCode: 2, wantStderr: "usage: bookwarden agent-review --run N --calibrate"},
		{name: "agent-review without a database", args: []string{"agent-review", "--run", "4", "--calibrate"}, env: map[string]string{"BOOKWARDEN_DATABASE_URL": "postgres://u:p@127.0.0.1:1/db"}, wantCode: 1, wantStderr: "agent-review: "},
		{name: "healthcheck against a healthy server", args: []string{"healthcheck"}, env: map[string]string{"BOOKWARDEN_LISTEN": listenOf(healthy)}, wantCode: 0},
		{name: "healthcheck against an unhealthy server", args: []string{"healthcheck"}, env: map[string]string{"BOOKWARDEN_LISTEN": listenOf(unhealthy)}, wantCode: 1, wantStderr: "HTTP 503"},
		{name: "healthcheck with nothing listening", args: []string{"healthcheck"}, env: map[string]string{"BOOKWARDEN_LISTEN": "127.0.0.1:1"}, wantCode: 1},
		{
			name:       "serve with invalid settings",
			args:       []string{"serve"},
			env:        map[string]string{"BOOKWARDEN_DATABASE_URL": "mysql://u:db-secret@h/db"},
			wantCode:   1,
			wantStderr: "BOOKWARDEN_DATABASE_URL must be a postgres:// URL",
			notStderr:  "db-secret",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			getenv := func(k string) string { return tc.env[k] }
			code := run(context.Background(), tc.args, getenv, strings.NewReader(tc.stdin), &stdout, &stderr)
			if code != tc.wantCode {
				t.Fatalf("exit code = %d, want %d (stderr %q)", code, tc.wantCode, stderr.String())
			}
			if tc.wantStdout != "" && stdout.String() != tc.wantStdout {
				t.Fatalf("stdout = %q, want %q", stdout.String(), tc.wantStdout)
			}
			if tc.wantStderr != "" && !strings.Contains(stderr.String(), tc.wantStderr) {
				t.Fatalf("stderr = %q, want it to contain %q", stderr.String(), tc.wantStderr)
			}
			if tc.notStderr != "" && strings.Contains(stderr.String(), tc.notStderr) {
				t.Fatalf("stderr leaks %q: %s", tc.notStderr, stderr.String())
			}
		})
	}
}

func TestNewLoggerLevel(t *testing.T) {
	tests := []struct {
		level     string
		wantDebug bool
		wantWarn  bool
	}{
		{"", false, true},
		{"info", false, true},
		{"debug", true, true},
		{"error", false, false},
		{"bogus", false, true}, // serve reports the bad value as a config error
	}
	for _, tc := range tests {
		t.Run("level "+tc.level, func(t *testing.T) {
			var buf bytes.Buffer
			logger := newLogger(&buf, func(string) string { return tc.level })
			logger.Debug("a-debug-line")
			logger.Warn("a-warn-line")
			if got := strings.Contains(buf.String(), "a-debug-line"); got != tc.wantDebug {
				t.Errorf("debug line logged = %v, want %v", got, tc.wantDebug)
			}
			if got := strings.Contains(buf.String(), "a-warn-line"); got != tc.wantWarn {
				t.Errorf("warn line logged = %v, want %v", got, tc.wantWarn)
			}
		})
	}
}

func TestServeRejectsAnInvalidLogLevel(t *testing.T) {
	env := map[string]string{"BOOKWARDEN_LOG_LEVEL": "loud"}
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"serve"}, func(k string) string { return env[k] }, strings.NewReader(""), &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "BOOKWARDEN_LOG_LEVEL must be debug, info, warn or error") {
		t.Fatalf("code %d, stderr %q", code, stderr.String())
	}
}

func TestAgentReviewCommand(t *testing.T) {
	ctx := context.Background()
	url := pgtest.URL(t)
	st, err := store.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	runs := store.NewRunRepo(st)
	r, err := runs.CreateRun(ctx, verify.Scope{}, "library", "v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := runs.SetStatus(ctx, r.ID, verify.RunCompleted, ""); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"BOOKWARDEN_DATABASE_URL": url}
	for i, want := range []int{0, 1} {
		var stdout, stderr bytes.Buffer
		code := run(ctx, []string{"agent-review", "--run", fmt.Sprint(r.ID), "--calibrate"}, func(k string) string { return env[k] }, strings.NewReader(""), &stdout, &stderr)
		if code != want {
			t.Fatalf("call %d: code %d, stderr %q", i, code, stderr.String())
		}
	}
	rs, err := store.NewAgentRepo(st).Reviews(ctx, r.ID)
	if err != nil || len(rs) != 1 || rs[0].Kind != store.ReviewCalibration || rs[0].Mode != store.ModeShadow || rs[0].State != store.ReviewQueued {
		t.Fatalf("reviews = %+v, %v", rs, err)
	}
	var stdout, stderr bytes.Buffer
	if code := run(ctx, []string{"agent-review", "--run", "999", "--calibrate"}, func(k string) string { return env[k] }, strings.NewReader(""), &stdout, &stderr); code != 1 {
		t.Fatalf("a run that doesn't exist: code %d", code)
	}
}

func TestVerifySealsCommand(t *testing.T) {
	ctx := context.Background()
	url := pgtest.URL(t)
	st, err := store.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	runs := store.NewRunRepo(st)
	finished := func(books ...library.BookRef) int64 {
		r, err := runs.CreateRun(ctx, verify.Scope{}, "library", "v1", books)
		if err != nil {
			t.Fatal(err)
		}
		for _, b := range books {
			p := evidence.Package{
				SchemaVersion: evidence.SchemaVersion, LibraryID: "library", BookID: b.ID, UUID: b.UUID,
				Calibre: evidence.Record{Title: "Binary Tides", Authors: []string{"Ada Reed"}, Languages: []string{"eng"}, Identifiers: map[string]string{}},
				Formats: []evidence.FormatEvidence{},
			}
			raw, seal, err := decide.Seal(ctx, p)
			if err != nil {
				t.Fatal(err)
			}
			if err := runs.FinishBook(ctx, r.ID, b, verify.Outcome{State: verify.BookChecked, Evidence: raw, Seal: seal}); err != nil {
				t.Fatal(err)
			}
		}
		if err := runs.SetStatus(ctx, r.ID, verify.RunCompleted, ""); err != nil {
			t.Fatal(err)
		}
		return r.ID
	}
	good := finished(library.BookRef{ID: 1, UUID: "u1"}, library.BookRef{ID: 2, UUID: "u2"})
	bad := finished(library.BookRef{ID: 1, UUID: "u1"}, library.BookRef{ID: 2, UUID: "u2"})
	// A run that is still going is never listed, and can't be checked.
	if _, err := runs.CreateRun(ctx, verify.Scope{}, "library", "v1", []library.BookRef{{ID: 1, UUID: "u1"}}); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"BOOKWARDEN_DATABASE_URL": url}
	call := func(args ...string) (int, string, string) {
		var stdout, stderr bytes.Buffer
		code := run(ctx, append([]string{"verify-seals"}, args...), func(k string) string { return env[k] }, strings.NewReader(""), &stdout, &stderr)
		return code, stdout.String(), stderr.String()
	}
	if code, out, errOut := call("--run", fmt.Sprint(good)); code != 0 || out != fmt.Sprintf("run %d: ok, 2 books\n", good) {
		t.Fatalf("a clean run: code %d, stdout %q, stderr %q", code, out, errOut)
	}
	if _, err := st.Pool.Exec(ctx, `UPDATE evidence SET package=jsonb_set(package,'{calibre_record,title}','"Edited"') WHERE run_id=$1 AND book_id=2`, bad); err != nil {
		t.Fatal(err)
	}
	if code, out, _ := call("--run", fmt.Sprint(bad)); code != 1 || out != fmt.Sprintf("run %d: DAMAGED, 1 of 2 books broken, first book 2\n", bad) {
		t.Fatalf("a damaged run: code %d, stdout %q", code, out)
	}
	// Every finished run: one line each, and the damaged one decides the code.
	code, out, errOut := call()
	if code != 1 || !strings.Contains(out, fmt.Sprintf("run %d: ok", good)) || !strings.Contains(out, fmt.Sprintf("run %d: DAMAGED", bad)) || errOut != "" {
		t.Fatalf("all runs: code %d, stdout %q, stderr %q", code, out, errOut)
	}
	if code, _, errOut := call("--run", "9999"); code != 1 || !strings.Contains(errOut, "verify-seals: run 9999") {
		t.Fatalf("a missing run: code %d, stderr %q", code, errOut)
	}
	for _, args := range [][]string{{"--run", "-1"}, {"--run", "x"}, {"extra"}} {
		if code, _, errOut := call(args...); code != 2 || !strings.Contains(errOut, "usage: bookwarden verify-seals [--run N]") {
			t.Fatalf("%v: code %d, stderr %q", args, code, errOut)
		}
	}
	// Once the damage is gone, all runs pass.
	if _, err := st.Pool.Exec(ctx, `DELETE FROM runs WHERE id=$1`, bad); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := call(); code != 0 {
		t.Fatalf("all runs clean: code %d, stderr %q", code, errOut)
	}
}

// TestSealOnFinish: a run that ends with a terminal status gets a seal check
// in the background, the way serve wires it; a run still going doesn't.
func TestSealOnFinish(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, pgtest.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	runs := store.NewRunRepo(st)
	reports := report.New(store.NewReportRepo(st))
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	var wg sync.WaitGroup
	finished := sealOnFinish(ctx, &wg, reports, logger, nil)

	checks := func() int {
		var n int
		if err := st.Pool.QueryRow(ctx, `SELECT count(*) FROM seal_checks`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	book := library.BookRef{ID: 1, UUID: "u1"}
	r, err := runs.CreateRun(ctx, verify.Scope{}, "library", "v1", []library.BookRef{book})
	if err != nil {
		t.Fatal(err)
	}
	// Still running: the hook is only for runs that end.
	finished(ctx, r.ID, verify.RunWaiting)
	wg.Wait()
	if n := checks(); n != 0 {
		t.Fatalf("%d checks of a run that hasn't ended", n)
	}
	if err := runs.SetStatus(ctx, r.ID, verify.RunCompleted, ""); err != nil {
		t.Fatal(err)
	}
	finished(ctx, r.ID, verify.RunCompleted)
	wg.Wait()
	if n := checks(); n != 1 {
		t.Fatalf("%d checks after a run ended, want 1 (logs: %s)", n, logs.String())
	}
	// A run pruned meanwhile is not a warning.
	logs.Reset()
	finished(ctx, 99999, verify.RunFailed)
	wg.Wait()
	if strings.Contains(logs.String(), "level=WARN") {
		t.Fatalf("a gone run was warned about: %s", logs.String())
	}
}

// TestVersionOutput: the version line carries the first 12 characters of the
// revision when one was stamped in, so a deploy can check a release image by
// its commit (the tag alone is shorter than a deploy label).
func TestVersionOutput(t *testing.T) {
	tests := []struct{ version, revision, want string }{
		{"v2.0.0", "abc1234def5678", "bookwarden v2.0.0 (abc1234def56)"},
		{"v2.0.0", "abc1234", "bookwarden v2.0.0 (abc1234)"},
		{"dev", "unknown", "bookwarden dev"},
		{"dev", "", "bookwarden dev"},
	}
	for _, tt := range tests {
		if got := versionLine(tt.version, tt.revision); got != tt.want {
			t.Errorf("versionLine(%q, %q) = %q, want %q", tt.version, tt.revision, got, tt.want)
		}
	}
}
