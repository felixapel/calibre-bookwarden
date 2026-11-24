package fix_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/auth"
	"github.com/felixapel/calibre-bookwarden/internal/auth/authtest"
	"github.com/felixapel/calibre-bookwarden/internal/fix"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/report"
	"github.com/felixapel/calibre-bookwarden/internal/store"
	"github.com/felixapel/calibre-bookwarden/internal/testutil/pgtest"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
	"github.com/felixapel/calibre-bookwarden/internal/web"
)

// readOnlyLibrary gives the runner the fake's reads; the end-to-end test
// never starts a check.
type readOnlyLibrary struct{ *fix.FakeCalibre }

func (readOnlyLibrary) Check(context.Context) (string, error) { return "library", nil }

func (readOnlyLibrary) CheckInLibrary(context.Context, string) (string, error) { return "library", nil }

func (readOnlyLibrary) ListIDsInLibrary(context.Context, string) ([]library.BookRef, error) {
	return nil, nil
}

func (readOnlyLibrary) ExportCoverInLibrary(context.Context, string, int, io.Writer, int64) (library.ExportResult, error) {
	return library.ExportResult{Status: library.ExportNotInCalibre}, nil
}

func (readOnlyLibrary) ExportFormatInLibrary(context.Context, string, int, string, io.Writer, int64) (library.ExportResult, error) {
	return library.ExportResult{}, library.ErrBookNotFound
}

type browser struct {
	t      *testing.T
	base   string
	client *http.Client
	token  string
}

func (b *browser) do(method, path string, form url.Values) (int, string, string) {
	b.t.Helper()
	var body io.Reader
	if form != nil {
		form.Set("csrf_token", b.token)
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(context.Background(), method, b.base+path, body)
	if err != nil {
		b.t.Fatal(err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := b.client.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		b.t.Fatal(err)
	}
	return resp.StatusCode, resp.Header.Get("Location"), string(raw)
}

// post submits a form and wants a 303 to a location matching want.
func (b *browser) post(path string, form url.Values, want string) string {
	b.t.Helper()
	status, loc, body := b.do("POST", path, form)
	if status != http.StatusSeeOther || !regexp.MustCompile("^"+want+"$").MatchString(loc) {
		b.t.Fatalf("POST %s = %d → %q: %s", path, status, loc, body)
	}
	return loc
}

func (b *browser) get(path string) string {
	b.t.Helper()
	status, _, body := b.do("GET", path, nil)
	if status != http.StatusOK {
		b.t.Fatalf("GET %s = %d: %s", path, status, body)
	}
	return body
}

func TestFixingPagesEndToEnd(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, pgtest.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	cal := fix.NewFakeCalibre(fix.Book2())
	before := cal.Book(2)
	run := fix.SealBook2(t, st, before)

	fixer := fix.New(store.NewFixRepo(st), store.NewRunRepo(st), cal, nil)
	serveCtx, stop := context.WithCancel(ctx)
	served := make(chan struct{})
	go func() {
		_ = fixer.Serve(serveCtx)
		close(served)
	}()
	t.Cleanup(func() { stop(); <-served })

	logger := slog.New(slog.DiscardHandler)
	authSvc := auth.NewService(authtest.NewMemRepo(), auth.Params{Memory: 64, Time: 1, Threads: 1, KeyLen: 32, SaltLen: 16}, time.Now)
	srv, err := web.New(web.Options{
		AllowedHosts: []string{"127.0.0.1"},
		Auth:         authSvc,
		Health:       func(context.Context) error { return nil },
		Logger:       logger,
		Runs:         verify.NewRunner(store.NewRunRepo(st), readOnlyLibrary{cal}, verify.Options{Logger: logger}),
		Reports:      report.New(store.NewReportRepo(st)),
		Fixing:       fixer,
		Sources:      []web.SourceStatus{{Name: "DNB", State: "on"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	b := &browser{t: t, base: ts.URL, client: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}

	// 1. Set the admin up, sign in and read the CSRF token.
	code, ok, err := authSvc.PrepareSetup(ctx)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	const password = "correct horse battery"
	b.post("/setup", url.Values{"code": {code}, "username": {"admin"}, "password": {password}, "confirm": {password}}, ".*")
	b.post("/login", url.Values{"username": {"admin"}, "password": {password}}, ".*")
	m := regexp.MustCompile(`name="csrf_token" value="([^"]+)"`).FindStringSubmatch(b.get("/settings"))
	if m == nil {
		t.Fatal("no CSRF token on Settings")
	}
	b.token = m[1]

	// 2. Turn writes on.
	b.post("/settings/writes", url.Values{"value": {"on"}}, "/settings")

	// 3. Tick every fix (none is pre-ticked) and approve them as a batch.
	fixesPath := "/runs/" + itoa(run) + "/fixes"
	var picks []string
	for _, m := range regexp.MustCompile(`<input type="checkbox" name="pick" value="([^"]+)"[^>]*>`).FindAllStringSubmatch(b.get(fixesPath), -1) {
		picks = append(picks, m[1])
	}
	if want := []string{"2:title", "2:authors", "2:languages", "2:publisher", "2:pubdate"}; !sameSet(picks, want) {
		t.Fatalf("picks %v, want %v", picks, want)
	}
	b.post(fixesPath+"/decide", url.Values{"action": {"approve"}, "pick": picks}, fixesPath)
	if !strings.Contains(b.get(fixesPath), "5 approved, waiting") {
		t.Fatal("the Fixes page doesn't count the approvals")
	}
	decided, err := fixer.RunProposals(ctx, run)
	if err != nil || len(decided) != 5 {
		t.Fatalf("RunProposals = %d, %v", len(decided), err)
	}
	for _, d := range decided {
		if d.DecisionMode != store.DecisionBulk || d.VerdictsShown {
			t.Errorf("%s decided as %q (verdicts shown %v), want a blind bulk decision", d.Proposal.Field, d.DecisionMode, d.VerdictsShown)
		}
	}

	// 4. Apply, and wait for the background apply.
	b.post("/fixes/apply", url.Values{}, "/history")
	deadline := time.Now().Add(10 * time.Second)
	for st := fixer.ApplyStatus(); st.Running || st.Finished.IsZero(); st = fixer.ApplyStatus() {
		if time.Now().After(deadline) {
			t.Fatalf("apply didn't finish: %+v", st)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if st := fixer.ApplyStatus(); st.Summary != (fix.Summary{Applied: 5}) || st.Err != "" {
		t.Fatalf("apply = %+v", st)
	}
	got := cal.Book(2)
	if got.Title != "Lighthouse Signals" || !slices.Equal(got.Authors, []string{"Bea Sample"}) || !slices.Equal(got.Languages, []string{"eng"}) ||
		got.Publisher != "Harbour Press" || !strings.HasPrefix(got.PubDate, "2001-07-26") {
		t.Fatalf("after apply: %+v", got)
	}

	// 5. Find the operation on History.
	history := b.get("/history")
	if !strings.Contains(history, "Last apply: 5 applied, 0 stale, 0 blocked, 0 failed") {
		t.Fatal("History doesn't show the last apply")
	}
	m = regexp.MustCompile(`<a href="/history/([0-9]+)">`).FindStringSubmatch(history)
	if m == nil {
		t.Fatal("no operation on History")
	}
	opPath := "/history/" + m[1]
	if page := b.get(opPath); !strings.Contains(page, "Incorrect Catalogue Title") || !strings.Contains(page, opPath+"/undo") {
		t.Fatal("the operation page lacks the before value or Undo")
	}

	// 6. Undo it.
	undo := b.post(opPath+"/undo", url.Values{}, "/history/[0-9]+")
	if page := b.get(undo); !strings.Contains(page, "Undoes") {
		t.Fatal("the undo's page doesn't say what it undoes")
	}
	got = cal.Book(2)
	if got.Title != before.Title || !slices.Equal(got.Authors, before.Authors) || !slices.Equal(got.Languages, before.Languages) ||
		got.Publisher != before.Publisher || got.PubDate != before.PubDate {
		t.Fatalf("after undo: %+v, want %+v", got, before)
	}
	if page := b.get("/settings"); !strings.Contains(page, "<tr><td>On</td><td>admin</td>") {
		t.Fatal("Settings' log lacks the switch change")
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func sameSet(a, b []string) bool {
	return slices.Equal(slices.Sorted(slices.Values(a)), slices.Sorted(slices.Values(b)))
}
