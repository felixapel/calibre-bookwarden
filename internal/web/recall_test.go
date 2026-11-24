package web

import (
	"context"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/eval"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/report"
	"github.com/felixapel/calibre-bookwarden/internal/store"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

// fakeRecall is an in-memory Recall with one sample of three books.
type fakeRecall struct {
	mu         sync.Mutex
	sample     *store.RecallSample
	books      []store.SampleBook
	answers    map[string]store.Confirmation
	result     eval.RecallResult
	answered   int
	next       int
	closed     bool
	confirmErr error
	reveals    []bool
	startErr   error
	seeds      []*int64
	confirmed  [][]store.Confirmation
	confirmAt  []int
}

func newFakeRecall() *fakeRecall {
	return &fakeRecall{
		next:   1,
		sample: &store.RecallSample{ID: 4, LibraryID: "library", RunID: 7, Seed: 99, Size: 3, CreatedBy: "admin", CreatedAt: time.Date(2026, 11, 8, 9, 30, 0, 0, time.UTC)},
		books: []store.SampleBook{
			{BookID: 20, UUID: "frozen", Stratum: "EPUB|eng|C|fix", Position: 1},
			{BookID: 30, UUID: "u30", Stratum: "s", Position: 2},
			{BookID: 40, UUID: "u40", Stratum: "s", Position: 3},
		},
	}
}

func (f *fakeRecall) Start(_ context.Context, seed *int64, _ string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seeds = append(f.seeds, seed)
	if f.startErr != nil {
		return 0, f.startErr
	}
	return 4, nil
}

func (f *fakeRecall) Latest(context.Context) (store.RecallSample, error) {
	if f.sample == nil {
		return store.RecallSample{}, store.ErrNoSuchSample
	}
	return *f.sample, nil
}

func (f *fakeRecall) Sample(_ context.Context, id int64) (store.RecallSample, error) {
	if f.sample == nil || f.sample.ID != id {
		return store.RecallSample{}, store.ErrNoSuchSample
	}
	return *f.sample, nil
}

func (f *fakeRecall) BookAt(_ context.Context, _ int64, pos int) (store.SampleBook, error) {
	if pos < 1 || pos > len(f.books) {
		return store.SampleBook{}, store.ErrNoSuchSample
	}
	return f.books[pos-1], nil
}

func (f *fakeRecall) Answers(context.Context, int64, int) (map[string]store.Confirmation, error) {
	return f.answers, nil
}

func (f *fakeRecall) Confirm(_ context.Context, _ int64, pos int, a []store.Confirmation, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.confirmErr != nil {
		return f.confirmErr
	}
	f.confirmed, f.confirmAt = append(f.confirmed, a), append(f.confirmAt, pos)
	return nil
}

// Status behaves as the service does: results only for a complete sample, and
// showing them closes it.
func (f *fakeRecall) Status(_ context.Context, _ int64, reveal bool) (eval.SampleStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reveals = append(f.reveals, reveal)
	st := eval.SampleStatus{Sample: *f.sample, Answered: f.answered, Next: f.next}
	if f.next != 0 {
		return st, nil
	}
	if reveal {
		f.closed = true
		f.confirmErr = store.ErrSampleClosed
	}
	if f.closed {
		at := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
		st.Sample.ClosedAt = &at
		res := f.result
		st.Result = &res
	}
	return st, nil
}

func recallEnv(t *testing.T, rec *fakeRecall, book report.Book, x *fakeExcerpts) *testEnv {
	t.Helper()
	e := newEnv(t, func(o *Options) { o.Recall = rec; o.Reports = &fakeReports{book: book}; o.Excerpts = x })
	e.setUpAdmin(t)
	e.logIn(t)
	e.runs.add(running(7))
	return e
}

// blindBook is a book with everything a blind page must never show: a tier,
// reasons, findings, fixes with roots, and an extraction with claims.
func blindBook() (report.Book, *fakeExcerpts) {
	b, x := excerptBook("Printed by Orbit Press <i>1999</i>\nISBN 978-0-00-000004-0 (ebook)", "9780000000040", false)
	b.Package.UUID = "frozen"
	b.Package.Calibre = evidence.Record{
		Title: "Binary Tides <b>", Authors: []string{"Ada Reed", "Cy Hale"}, Publisher: "Orbit", PubDate: "2001-07-01T00:00:00+00:00",
		Identifiers: map[string]string{"isbn": "9780306406157"}, Languages: []string{"eng", "deu"},
	}
	b.Package.Tier = "C"
	b.Package.TierReasons = []string{"another_work_zzreason"}
	b.Package.Findings = []evidence.Finding{{Code: "title_mismatch_zzfinding", Field: "title"}}
	b.Package.Proposals = []evidence.Proposal{{Field: "title", Current: []string{"Binary Tides"}, Proposed: []string{"ZZProposedTitle"}, Roots: []string{"zzroot"}}}
	b.Findings = b.Package.Findings
	b.Decision.Tier = "C"
	b.Decision.Reasons = []string{"another_work_zzreason"}
	b.Decision.Proposals = b.Package.Proposals
	b.Package.Formats[0].Extraction = &evidence.Extraction{
		Agreed: []evidence.AgreedFact{{Key: "title", Outcome: "zzoutcome", Values: []evidence.AgreedValue{{Value: "ZZExtractedTitle"}}}},
	}
	return b, x
}

func TestSamplePagesNeedSignIn(t *testing.T) {
	e := newEnv(t, func(o *Options) { o.Recall = newFakeRecall() })
	e.setUpAdmin(t)
	for _, path := range []string{"/sample", "/sample/4/book/1"} {
		if r := e.do(t, http.MethodGet, path, nil, nil); r.status != 303 || r.header.Get("Location") != "/login" {
			t.Errorf("GET %s signed out: %d %q", path, r.status, r.header.Get("Location"))
		}
	}
	for _, path := range []string{"/sample", "/sample/4/book/1"} {
		if r := e.do(t, http.MethodPost, path, url.Values{}, nil); r.status != http.StatusUnauthorized {
			t.Errorf("POST %s signed out: %d, want 401", path, r.status)
		}
	}
}

func TestSampleIsOffWithoutTheService(t *testing.T) {
	e := signedIn(t)
	for _, path := range []string{"/sample", "/sample/1/book/1"} {
		if r := e.do(t, http.MethodGet, path, nil, nil); r.status != 404 {
			t.Errorf("GET %s = %d, want 404", path, r.status)
		}
	}
}

func TestSamplePageStartForm(t *testing.T) {
	rec := newFakeRecall()
	rec.sample = nil
	e := recallEnv(t, rec, report.Book{}, nil)
	r := e.do(t, "GET", "/sample", nil, nil)
	for _, want := range []string{"Start a sample", `action="/sample"`, `name="csrf_token"`, `name="seed"`} {
		if r.status != 200 || !strings.Contains(r.body, want) {
			t.Fatalf("empty page %d lacks %q", r.status, want)
		}
	}
	if strings.Contains(r.body, "Recall per field") {
		t.Error("the recall table shows with no sample")
	}
	if r := e.do(t, "GET", "/sample?x=1", nil, nil); r.status != 400 {
		t.Errorf("unknown query = %d, want 400", r.status)
	}
}

func TestStartSample(t *testing.T) {
	seed := func(n int64) *int64 { return &n }
	tests := []struct {
		name     string
		form     func(tok string) url.Values
		startErr error
		want     int
		location string
		seeds    []*int64
	}{
		{"with a seed", func(tok string) url.Values { return url.Values{"csrf_token": {tok}, "seed": {"42"}} }, nil, 303, "/sample/4/book/1", []*int64{seed(42)}},
		{"no seed field", func(tok string) url.Values { return url.Values{"csrf_token": {tok}} }, nil, 303, "/sample/4/book/1", []*int64{nil}},
		{"blank seed is random", func(tok string) url.Values { return url.Values{"csrf_token": {tok}, "seed": {" "}} }, nil, 303, "/sample/4/book/1", []*int64{nil}},
		{"seed zero", func(tok string) url.Values { return url.Values{"csrf_token": {tok}, "seed": {"0"}} }, nil, 303, "/sample/4/book/1", []*int64{seed(0)}},
		{"seed not a number", func(tok string) url.Values { return url.Values{"csrf_token": {tok}, "seed": {"abc"}} }, nil, 400, "", nil},
		{"negative seed", func(tok string) url.Values { return url.Values{"csrf_token": {tok}, "seed": {"-5"}} }, nil, 400, "", nil},
		{"seed too large", func(tok string) url.Values { return url.Values{"csrf_token": {tok}, "seed": {"9223372036854775808"}} }, nil, 400, "", nil},
		{"seed twice", func(tok string) url.Values { return url.Values{"csrf_token": {tok}, "seed": {"1", "2"}} }, nil, 400, "", nil},
		{"unknown field", func(tok string) url.Values { return url.Values{"csrf_token": {tok}, "size": {"10"}} }, nil, 400, "", nil},
		{"no token", func(string) url.Values { return url.Values{"seed": {"1"}} }, nil, 403, "", nil},
		{"wrong token", func(string) url.Values { return url.Values{"csrf_token": {"nope"}} }, nil, 403, "", nil},
		{"no checked run", func(tok string) url.Values { return url.Values{"csrf_token": {tok}} }, store.ErrNoCheckedRun, 409, "", []*int64{nil}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := newFakeRecall()
			rec.startErr = tt.startErr
			e := recallEnv(t, rec, report.Book{}, nil)
			r := e.do(t, http.MethodPost, "/sample", tt.form(e.csrfToken(t)), nil)
			if r.status != tt.want || tt.location != "" && r.header.Get("Location") != tt.location {
				t.Fatalf("POST /sample = %d %q, want %d %q: %s", r.status, r.header.Get("Location"), tt.want, tt.location, r.body)
			}
			if !reflect.DeepEqual(rec.seeds, tt.seeds) {
				t.Errorf("seeds %v, want %v", rec.seeds, tt.seeds)
			}
		})
	}
}

func TestSamplePageShowsResultsOnlyWhenComplete(t *testing.T) {
	rec := newFakeRecall()
	rec.next, rec.answered = 2, 1
	rec.result = eval.RecallResult{Sample: 4, Library: 50, Unrepresented: 5, FindingsVersion: 6, DecisionVersion: 9}
	for _, f := range store.RecallFields {
		rec.result.Fields = append(rec.result.Fields, eval.FieldRecall{Field: f})
	}
	rec.result.Fields[0] = eval.FieldRecall{Field: "title", Wrong: 5, Found: 2, WrongValue: 1, Right: 2, Skipped: 1, Recall: 0.478, EffectiveN: 2.6, Lower: 0.1, Upper: 0.9, HasInterval: true, FalseAlarms: []int{6, 9}}
	e := recallEnv(t, rec, report.Book{}, nil)

	// Mid-sample: progress only. Nothing of the recall, not even its words.
	r := e.do(t, "GET", "/sample", nil, nil)
	for _, want := range []string{"Sample 4", "seed 99", "1 of 3 books answered", `href="/sample/4/book/2"`, "Continue at book 2", "not labels", "Start another sample"} {
		if r.status != 200 || !strings.Contains(r.body, want) {
			t.Errorf("progress page %d lacks %q", r.status, want)
		}
	}
	for _, hidden := range []string{"Recall per field", "<table", "47.8 %", "False alarms", "books 6, 9", "decision version", "n<sub>eff</sub></th>"} {
		if strings.Contains(r.body, hidden) {
			t.Errorf("an open sample's page shows %q", hidden)
		}
	}
	if rec.closed {
		t.Fatal("an incomplete sample was closed")
	}

	// Complete: the results show once, with the versions, and the sample is closed.
	rec.next, rec.answered = 0, 3
	r = e.do(t, "GET", "/sample", nil, nil)
	for _, want := range []string{
		"Recall per field", "47.8 %", "10.0 % to 90.0 %", "2.6", "Title: books 6, 9", "5 of 50 library books", "n<sub>eff</sub>",
		"approximate (effective sample size)", "findings version 6 and decision version 9", "This sample is closed", "n/a",
	} {
		if r.status != 200 || !strings.Contains(r.body, want) {
			t.Errorf("results page %d lacks %q", r.status, want)
		}
	}
	if !rec.closed {
		t.Error("showing the results didn't close the sample")
	}

	// Closed: further answers are refused with a clear message, and nothing is saved.
	v := url.Values{"csrf_token": {e.csrfToken(t)}}
	for _, f := range store.RecallFields {
		v.Set("answer_"+f, "calibre_wrong")
	}
	r = e.do(t, http.MethodPost, "/sample/4/book/1", v, nil)
	if r.status != 409 || !strings.Contains(r.body, "This sample is closed") || len(rec.confirmed) != 0 {
		t.Errorf("confirm after the results = %d, saved %d: %s", r.status, len(rec.confirmed), r.body)
	}
}

func TestSamplePageStaysOpenUntilEveryBookIsAnswered(t *testing.T) {
	// A fully answered sample whose answers are all skips is complete too.
	rec := newFakeRecall()
	rec.next, rec.answered = 0, 3
	e := recallEnv(t, rec, report.Book{}, nil)
	rec.result = eval.RecallResult{Fields: []eval.FieldRecall{{Field: "title", Skipped: 3}}}
	if r := e.do(t, "GET", "/sample", nil, nil); !strings.Contains(r.body, "Recall per field") {
		t.Error("a complete all-skip sample shows no results")
	}
}

func TestBlindBookPage(t *testing.T) {
	book, x := blindBook()
	rec := newFakeRecall()
	rec.answers = map[string]store.Confirmation{"title": {Field: "title", Answer: store.AnswerWrong, Typed: func() *string { s := "Typed Title"; return &s }()}}
	e := recallEnv(t, rec, book, x)
	r := e.do(t, "GET", "/sample/4/book/1", nil, nil)
	if r.status != 200 {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	for _, want := range []string{
		"Book 1 of 3",
		// Calibre's six values, escaped.
		"Binary Tides &lt;b&gt;", "Ada Reed &amp; Cy Hale", "Orbit", "2001-07-01T00:00:00&#43;00:00", "9780306406157", "eng, deu",
		// The excerpt, as plain text from the sealed evidence.
		"Printed by Orbit Press &lt;i&gt;1999&lt;/i&gt;", `class="excerpt"`,
		// Three radios per field, an optional typed value, an access key.
		`name="answer_title" value="calibre_right" accesskey="1"`, `name="answer_title" value="calibre_wrong" checked`, `name="answer_title" value="skip"`,
		`name="answer_languages" value="skip" checked`, `name="typed_isbn"`, `value="Typed Title"`, `accesskey="s"`,
		`name="csrf_token"`, `action="/sample/4/book/1"`, `href="/sample/4/book/2"`,
	} {
		if !strings.Contains(r.body, want) {
			t.Errorf("blind page lacks %q", want)
		}
	}
	if n := strings.Count(r.body, `type="radio"`); n != 3*len(store.RecallFields) {
		t.Errorf("%d radios, want %d", n, 3*len(store.RecallFields))
	}
	// The blind property: nothing Bookwarden concluded about the book is on
	// the page, whatever the evidence holds.
	// The site navigation, outside <main>, links the Findings page by name.
	main := r.body[strings.Index(r.body, "<main>"):strings.Index(r.body, "</main>")]
	for _, secret := range []string{
		"ZZProposedTitle", "zzroot", "zzreason", "another_work", "zzfinding", "title_mismatch", "ZZExtractedTitle", "zzoutcome",
		"Tier", "tier", "roots", "Roots", "proposal", "Proposal", "finding", "Finding", "Supported by", "Read from the front matter", "<mark",
	} {
		if strings.Contains(main, secret) {
			t.Errorf("blind page shows %q", secret)
		}
	}
	// The same book on the ordinary page does show them, so the checks above
	// can fail.
	if plain := e.do(t, "GET", "/runs/7/books/20", nil, nil); !strings.Contains(plain.body, "ZZProposedTitle") || !strings.Contains(plain.body, "Tier C") {
		t.Error("the ordinary book page doesn't show the proposal and tier, so the blind checks prove nothing")
	}
	// The ISBN hint is not marked in the text.
	if strings.Contains(r.body, `class="claim"`) {
		t.Error("blind page marks claims")
	}
}

func TestBlindBookPageRefusals(t *testing.T) {
	book, x := blindBook()
	rec := newFakeRecall()
	e := recallEnv(t, rec, book, x)
	for _, tt := range []struct {
		path string
		want int
	}{
		{"/sample/4/book/3", 500}, // the frozen package belongs to another book: a seal mismatch
		{"/sample/4/book/4", 404}, // past the end
		{"/sample/4/book/0", 400},
		{"/sample/4/book/x", 400},
		{"/sample/0/book/1", 400},
		{"/sample/5/book/1", 404},
		{"/sample/4/book/1?show=all", 400},
	} {
		if r := e.do(t, "GET", tt.path, nil, nil); r.status != tt.want {
			t.Errorf("GET %s = %d, want %d", tt.path, r.status, tt.want)
		}
	}
	// A book whose report can't be read is not shown with a guess.
	broken := newEnv(t, func(o *Options) { o.Recall = rec; o.Reports = &fakeReports{err: report.ErrIntegrity} })
	broken.setUpAdmin(t)
	broken.logIn(t)
	if r := broken.do(t, "GET", "/sample/4/book/1", nil, nil); r.status != 500 {
		t.Errorf("integrity failure = %d, want 500", r.status)
	}
}

func TestConfirmSample(t *testing.T) {
	full := func(tok string) url.Values {
		v := url.Values{"csrf_token": {tok}}
		for _, f := range store.RecallFields {
			v.Set("answer_"+f, "calibre_right")
			v.Set("typed_"+f, "")
		}
		v.Set("answer_title", "calibre_wrong")
		v.Set("typed_title", " Binary Tides II ")
		v.Set("answer_isbn", "skip")
		return v
	}
	with := func(mutate func(url.Values)) func(string) url.Values {
		return func(tok string) url.Values { v := full(tok); mutate(v); return v }
	}
	tests := []struct {
		name     string
		path     string
		form     func(string) url.Values
		want     int
		location string
		saved    bool
	}{
		{"saves and moves on", "/sample/4/book/1", full, 303, "/sample/4/book/2", true},
		{"the last book goes back", "/sample/4/book/3", full, 303, "/sample", true},
		{"missing answer", "/sample/4/book/1", with(func(v url.Values) { v.Del("answer_pubdate") }), 400, "", false},
		{"unknown key", "/sample/4/book/1", with(func(v url.Values) { v.Set("answer_series", "skip") }), 400, "", false},
		{"two values", "/sample/4/book/1", with(func(v url.Values) { v.Add("answer_title", "skip") }), 400, "", false},
		{"unknown extra key", "/sample/4/book/1", with(func(v url.Values) { v.Set("book", "2") }), 400, "", false},
		{"past the end", "/sample/4/book/4", full, 404, "", false},
		{"no token", "/sample/4/book/1", func(string) url.Values { return url.Values{} }, 403, "", false},
		{"wrong token", "/sample/4/book/1", func(string) url.Values { return url.Values{"csrf_token": {"x"}} }, 403, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := newFakeRecall()
			e := recallEnv(t, rec, report.Book{}, nil)
			r := e.do(t, http.MethodPost, tt.path, tt.form(e.csrfToken(t)), nil)
			if r.status != tt.want || tt.location != "" && r.header.Get("Location") != tt.location {
				t.Fatalf("POST %s = %d %q, want %d %q", tt.path, r.status, r.header.Get("Location"), tt.want, tt.location)
			}
			if tt.saved != (len(rec.confirmed) == 1) {
				t.Fatalf("saved %d times, want saved = %v", len(rec.confirmed), tt.saved)
			}
			if !tt.saved {
				return
			}
			got := rec.confirmed[0]
			if len(got) != len(store.RecallFields) {
				t.Fatalf("%d answers", len(got))
			}
			for i, f := range store.RecallFields {
				if got[i].Field != f {
					t.Errorf("answer %d is %s, want %s", i, got[i].Field, f)
				}
			}
			if got[0].Answer != store.AnswerWrong || got[0].Typed == nil || *got[0].Typed != "Binary Tides II" || got[4].Answer != store.AnswerSkip || got[1].Typed != nil {
				t.Errorf("answers %+v", got)
			}
		})
	}
}

func TestConfirmSampleRefusedAnswer(t *testing.T) {
	e := newEnv(t, func(o *Options) { o.Recall = refusingRecall{newFakeRecall()} })
	e.setUpAdmin(t)
	e.logIn(t)
	v := url.Values{"csrf_token": {e.csrfToken(t)}}
	for _, f := range store.RecallFields {
		v.Set("answer_"+f, "calibre_right")
	}
	if r := e.do(t, http.MethodPost, "/sample/4/book/1", v, nil); r.status != 400 {
		t.Errorf("a refused answer = %d, want 400", r.status)
	}
}

// refusingRecall refuses every answer, as the service does an invalid one.
type refusingRecall struct{ *fakeRecall }

func (refusingRecall) Confirm(context.Context, int64, int, []store.Confirmation, string) error {
	return eval.ErrInvalidAnswer
}

func TestSampleNeverTouchesTheDecisionTables(t *testing.T) {
	// The page's collaborators are the Recall and Reports interfaces only: a
	// confirmation has no path to the Fixing service, where labels live.
	fixing := &fakeFixing{}
	book, x := blindBook()
	rec := newFakeRecall()
	e := newEnv(t, func(o *Options) {
		o.Recall = rec
		o.Reports = &fakeReports{book: book}
		o.Excerpts = x
		o.Fixing = fixing
	})
	e.setUpAdmin(t)
	e.logIn(t)
	e.runs.add(running(7))
	v := url.Values{"csrf_token": {e.csrfToken(t)}}
	for _, f := range store.RecallFields {
		v.Set("answer_"+f, "calibre_wrong")
	}
	e.do(t, "GET", "/sample", nil, nil)
	e.do(t, "GET", "/sample/4/book/1", nil, nil)
	if r := e.do(t, http.MethodPost, "/sample/4/book/1", v, nil); r.status != 303 {
		t.Fatalf("confirm = %d", r.status)
	}
	if calls := fixing.recorded(); len(calls) != 0 {
		t.Errorf("the sample called the fixing service: %+v", calls)
	}
}

func TestBlindBookPageHintsAtLanguageCodes(t *testing.T) {
	book, x := blindBook()
	e := recallEnv(t, newFakeRecall(), book, x)
	if r := e.do(t, "GET", "/sample/4/book/1", nil, nil); !strings.Contains(r.body, "Use language codes like deu, eng.") {
		t.Error("no hint on the languages field")
	}
}

func TestBlindBookPageWithPrunedEvidence(t *testing.T) {
	for _, err := range []error{report.ErrNoSuchBook, verify.ErrNoSuchRun} {
		rec := newFakeRecall()
		e := newEnv(t, func(o *Options) { o.Recall = rec; o.Reports = &fakeReports{err: err} })
		e.setUpAdmin(t)
		e.logIn(t)
		r := e.do(t, "GET", "/sample/4/book/1", nil, nil)
		if r.status != 410 || !strings.Contains(r.body, "evidence was pruned") || !strings.Contains(r.body, `href="/sample"`) {
			t.Errorf("%v: %d %s", err, r.status, r.body)
		}
	}
}

func TestStartSampleWhileWaitingForTheSealCheck(t *testing.T) {
	rec := newFakeRecall()
	rec.startErr = store.ErrNoCheckedRun
	e := recallEnv(t, rec, report.Book{}, nil)
	r := e.do(t, http.MethodPost, "/sample", url.Values{"csrf_token": {e.csrfToken(t)}}, nil)
	if r.status != 409 || !strings.Contains(r.body, "Waiting for the nightly seal check to re-verify the latest run at the current rules (up to 25 h after an upgrade)") {
		t.Errorf("%d %s", r.status, r.body)
	}
}
