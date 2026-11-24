package extraction

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/felixapel/calibre-bookwarden/internal/agent"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/extractor"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

// fakeSeat answers every call with text as model, or fails with err.
type fakeSeat struct {
	text, model string
	err         error
	calls       atomic.Int64
	block       bool // wait for the context
}

func (f *fakeSeat) Name() string  { return "fake" }
func (f *fakeSeat) Model() string { return f.model }
func (f *fakeSeat) Judge(ctx context.Context, _, _ string) (agent.Reply, error) {
	f.calls.Add(1)
	if f.block {
		<-ctx.Done()
		return agent.Reply{}, ctx.Err()
	}
	return agent.Reply{Text: f.text, Model: f.model, InputTokens: 100, OutputTokens: 10}, f.err
}

const body = "THE LANTERN YEARS\nby Jane Doe\n© 2019 Jane Doe\nISBN 978-0-306-40615-7"

func excerpt() evidence.Excerpt {
	return evidence.Excerpt{
		Method: evidence.ExcerptEPUBText, SHA256: hash(body), Chars: len([]rune(body)), Complete: true,
		Units: []evidence.ExcerptUnit{{Pos: 1, Landmark: evidence.LandmarkTitle, Start: 0, End: 29}, {Pos: 2, Landmark: evidence.LandmarkCopyright, Start: 30, End: len([]rune(body))}},
	}
}

func hash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

const titleAnswer = `{"claims":[{"field":"title","value":"THE LANTERN YEARS","quote":"THE LANTERN YEARS"}]}`

func ext(name, family string, s *fakeSeat) extractor.Extractor {
	return extractor.NewWithSeat(name, family, s.model, "", s)
}

type mapCache map[string][2]string

func (m mapCache) CachedRaw(_ context.Context, key string) (string, string, bool, error) {
	v, ok := m[key]
	return v[0], v[1], ok, nil
}

func service(t *testing.T, cache Cache, caps map[string]int, es ...extractor.Extractor) *Service {
	t.Helper()
	hosts := map[string]string{}
	for _, e := range es {
		hosts[e.Name] = "bifrost:8080"
	}
	s := New(Config{Extractors: es, Hosts: hosts, MaxCalls: caps, Cache: cache, Quotas: agent.NewQuotas(), Timeout: time.Second})
	s.BeginRun(1)
	return s
}

func TestExtractAgrees(t *testing.T) {
	a := &fakeSeat{text: titleAnswer, model: "m-a"}
	b := &fakeSeat{text: titleAnswer, model: "m-b"}
	s := service(t, mapCache{}, map[string]int{"a": 10, "b": 10}, ext("a", "fa", a), ext("b", "fb", b))
	x, raws, err := s.Extract(context.Background(), excerpt(), body)
	if err != nil {
		t.Fatal(err)
	}
	if x.ExcerptSHA256 != excerpt().SHA256 || x.PromptVersion != extractor.PromptVersion || len(x.Extractors) != 2 {
		t.Fatalf("extraction = %+v", x)
	}
	for _, r := range x.Extractors {
		if r.State != evidence.ExtractOK || r.RawSHA256 != hash(titleAnswer) || r.Cached {
			t.Errorf("run = %+v", r)
		}
	}
	var title *evidence.AgreedFact
	for i := range x.Agreed {
		if x.Agreed[i].Key == "title" {
			title = &x.Agreed[i]
		}
	}
	if title == nil || title.Outcome != evidence.AgreeAgreed || title.Values[0].Value != "THE LANTERN YEARS" {
		t.Fatalf("agreed = %+v", x.Agreed)
	}
	// The parsers vote too: the ISBN and the year are their claims.
	parser := 0
	for _, c := range x.Claims {
		if c.Source == evidence.ExtractParser {
			parser++
		}
	}
	if parser == 0 {
		t.Error("no parser claims")
	}
	if len(raws) != 2 || raws[0].Key == "" || raws[0].Key == raws[1].Key || raws[0].Body != titleAnswer {
		t.Errorf("raws = %+v", raws)
	}
	// The block seals.
	p := evidence.Package{SchemaVersion: evidence.SchemaVersion, Formats: []evidence.FormatEvidence{{Format: "EPUB", State: evidence.StateOK, Excerpt: ptr(excerpt()), Extraction: x}}}
	raw, seal, err := evidence.Seal(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := evidence.Open(raw, seal); err != nil {
		t.Fatalf("Open: %v", err)
	}
}

func ptr[T any](v T) *T { return &v }

// quotaExtractor is a real seat whose provider answers that the credits are
// spent.
func quotaExtractor(t *testing.T, name string, q *agent.Quotas) extractor.Extractor {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = io.WriteString(w, `{"error":{"message":"Insufficient credits"}}`)
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	cfg := agent.SeatConfig{Name: name, Wire: "openai", URL: srv.URL, Model: "m", Key: "k", Family: "f1", AllowCIDR: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}}
	e, err := extractor.New(cfg, agent.NewHTTPClient([]string{u.Host}, true), agent.WithQuotas(q))
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestExtractionAllFail(t *testing.T) {
	q := agent.NewQuotas()
	invalid := &fakeSeat{model: "m", text: "sorry"}
	other := &fakeSeat{model: "other", text: titleAnswer}
	es := []extractor.Extractor{quotaExtractor(t, "q", q), ext("i", "f2", invalid), extractor.NewWithSeat("o", "f3", "wanted", "", other)}
	s := New(Config{Extractors: es, Hosts: map[string]string{"q": "h", "i": "h", "o": "h"}, MaxCalls: map[string]int{"q": 5, "i": 5, "o": 5}, Cache: mapCache{}, Quotas: q, Timeout: 5 * time.Second})
	s.BeginRun(1)
	x, raws, err := s.Extract(context.Background(), excerpt(), body)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"q": evidence.ExtractQuotaExhausted, "i": evidence.ExtractInvalidOutput, "o": evidence.ExtractModelMismatch}
	for _, r := range x.Extractors {
		if r.State != want[r.Name] {
			t.Errorf("%s state %s, want %s", r.Name, r.State, want[r.Name])
		}
	}
	for _, c := range x.Claims {
		if c.Source != evidence.ExtractParser {
			t.Errorf("claim of a failed extractor: %+v", c)
		}
	}
	// The invalid answer is kept for re-verification but never cached.
	if len(raws) != 1 || raws[0].Key != "" || raws[0].Body != "sorry" {
		t.Errorf("raws = %+v", raws)
	}
	// The exhausted extractor is paused for the rest of the run.
	again, _, _ := s.Extract(context.Background(), excerpt(), body)
	if again.Extractors[0].State != evidence.ExtractUnavailable {
		t.Errorf("after exhaustion: %+v", again.Extractors[0])
	}
}

// Run 12: opencode-go's usage limit answers 429 with a Retry-After of 25
// minutes. The extractor must seal quota_exhausted at once and rest, not wait
// out its timeout on every format.
func TestUsageLimitPausesAtOnce(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "1512")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"type":"GoUsageLimitError","message":"Go usage limit exceeded"}}`)
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	q := agent.NewQuotas()
	cfg := agent.SeatConfig{Name: "q", Wire: "openai", URL: srv.URL, Model: "m", Key: "k", Family: "f1", AllowCIDR: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}}
	e, err := extractor.New(cfg, agent.NewHTTPClient([]string{u.Host}, true), agent.WithQuotas(q))
	if err != nil {
		t.Fatal(err)
	}
	s := New(Config{Extractors: []extractor.Extractor{e}, Hosts: map[string]string{"q": "h"}, MaxCalls: map[string]int{"q": 5}, Cache: mapCache{}, Quotas: q})
	s.BeginRun(1)
	start := time.Now()
	x, _, err := s.Extract(context.Background(), excerpt(), body)
	if err != nil {
		t.Fatal(err)
	}
	if x.Extractors[0].State != evidence.ExtractQuotaExhausted || time.Since(start) > 10*time.Second {
		t.Fatalf("state %s after %v", x.Extractors[0].State, time.Since(start))
	}
	again, _, _ := s.Extract(context.Background(), excerpt(), body)
	if again.Extractors[0].State != evidence.ExtractUnavailable || calls.Load() != 1 {
		t.Fatalf("after the limit: state %s, calls %d", again.Extractors[0].State, calls.Load())
	}
}

func TestCacheHitMakesNoCall(t *testing.T) {
	a := &fakeSeat{text: titleAnswer, model: "m-a"}
	b := &fakeSeat{text: titleAnswer, model: "m-b"}
	cache := mapCache{}
	s := service(t, cache, map[string]int{"a": 10, "b": 10}, ext("a", "fa", a), ext("b", "fb", b))
	_, raws, err := s.Extract(context.Background(), excerpt(), body)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range raws {
		cache[r.Key] = [2]string{r.SHA256, r.Body}
	}
	x, again, err := s.Extract(context.Background(), excerpt(), body)
	if err != nil {
		t.Fatal(err)
	}
	if a.calls.Load() != 1 || b.calls.Load() != 1 {
		t.Fatalf("calls %d, %d; want one each", a.calls.Load(), b.calls.Load())
	}
	for _, r := range x.Extractors {
		if !r.Cached || r.State != evidence.ExtractOK {
			t.Errorf("run = %+v", r)
		}
	}
	if len(again) != 2 {
		t.Errorf("a cached answer is still handed back for its package: %+v", again)
	}
}

func TestCapsAreExact(t *testing.T) {
	a := &fakeSeat{text: titleAnswer, model: "m-a"}
	b := &fakeSeat{text: titleAnswer, model: "m-b"}
	s := service(t, mapCache{}, map[string]int{"a": 5, "b": 50}, ext("a", "fa", a), ext("b", "fb", b))
	var wg sync.WaitGroup
	var unavailable atomic.Int64
	for range 20 {
		wg.Go(func() {
			x, _, err := s.Extract(context.Background(), excerpt(), body)
			if err != nil {
				t.Error(err)
				return
			}
			if x.Extractors[0].State == evidence.ExtractUnavailable {
				unavailable.Add(1)
			}
		})
	}
	wg.Wait()
	if a.calls.Load() != 5 || unavailable.Load() != 15 || b.calls.Load() != 20 {
		t.Fatalf("a calls %d, unavailable %d, b calls %d", a.calls.Load(), unavailable.Load(), b.calls.Load())
	}
	// A new run has a new budget.
	s.BeginRun(2)
	if _, _, err := s.Extract(context.Background(), excerpt(), body); err != nil || a.calls.Load() != 6 {
		t.Fatalf("after BeginRun: calls %d, %v", a.calls.Load(), err)
	}
}

func TestQuotaPauses(t *testing.T) {
	a := &fakeSeat{text: titleAnswer, model: "m-a"}
	q := agent.NewQuotas()
	s := New(Config{Extractors: []extractor.Extractor{ext("a", "fa", a)}, Hosts: map[string]string{"a": "h"}, MaxCalls: map[string]int{"a": 10}, Cache: mapCache{}, Quotas: q, Timeout: time.Second})
	s.BeginRun(1)
	q.Record("a", time.Now(), time.Hour) // as the seat records an exhaustion
	x, _, err := s.Extract(context.Background(), excerpt(), body)
	if err != nil || a.calls.Load() != 0 || x.Extractors[0].State != evidence.ExtractUnavailable {
		t.Fatalf("paused extractor: calls %d, %+v, %v", a.calls.Load(), x.Extractors, err)
	}
	q.Record("a", time.Now().Add(-2*time.Hour), time.Hour) // the window is over
	if x, _, _ := s.Extract(context.Background(), excerpt(), body); a.calls.Load() != 1 || x.Extractors[0].State != evidence.ExtractOK {
		t.Fatalf("after the window: calls %d, %+v", a.calls.Load(), x.Extractors)
	}
}

func TestExtractCancelled(t *testing.T) {
	a := &fakeSeat{model: "m-a", block: true}
	s := service(t, mapCache{}, map[string]int{"a": 5}, ext("a", "fa", a))
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	if _, _, err := s.Extract(ctx, excerpt(), body); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestExtractTimeout(t *testing.T) {
	a := &fakeSeat{model: "m-a", block: true}
	s := New(Config{Extractors: []extractor.Extractor{ext("a", "fa", a)}, Hosts: map[string]string{"a": "h"}, MaxCalls: map[string]int{"a": 5}, Cache: mapCache{}, Timeout: 10 * time.Millisecond})
	s.BeginRun(1)
	x, _, err := s.Extract(context.Background(), excerpt(), body)
	if err != nil || x.Extractors[0].State != evidence.ExtractTimeout {
		t.Fatalf("%+v, %v", x, err)
	}
}

func TestCacheKey(t *testing.T) {
	k := cacheKey("ab", "extract-2", "host", "model", "low")
	for _, other := range []string{cacheKey("ac", "extract-2", "host", "model", "low"), cacheKey("ab", "extract-3", "host", "model", "low"),
		cacheKey("ab", "extract-2", "other", "model", "low"), cacheKey("ab", "extract-2", "host", "m2", "low"), cacheKey("ab", "extract-2", "host", "model", "")} {
		if other == k {
			t.Fatal("a cache key ignores one of its parts")
		}
	}
	if !evidence.ValidExcerptHash(k) {
		t.Errorf("key %q is not 64 hex", k)
	}
}

func TestUnstorableAnswerIsUnavailable(t *testing.T) {
	for name, text := range map[string]string{"a NUL": titleAnswer + "\x00", "invalid UTF-8": "{\"claims\":[]}\xff"} {
		t.Run(name, func(t *testing.T) {
			a := &fakeSeat{text: text, model: "m-a"}
			s := service(t, mapCache{}, map[string]int{"a": 5}, ext("a", "fa", a))
			x, raws, err := s.Extract(context.Background(), excerpt(), body)
			if err != nil || x.Extractors[0].State != evidence.ExtractUnavailable || x.Extractors[0].RawSHA256 != "" || len(raws) != 0 {
				t.Fatalf("%+v, raws %d, %v; want unavailable and nothing to store", x.Extractors, len(raws), err)
			}
		})
	}
}

func TestCapSurvivesReexecute(t *testing.T) {
	a := &fakeSeat{text: titleAnswer, model: "m-a"}
	s := service(t, mapCache{}, map[string]int{"a": 1}, ext("a", "fa", a))
	for range 2 {
		s.BeginRun(1) // the same run executed again after an interruption
		if _, _, err := s.Extract(context.Background(), excerpt(), body); err != nil {
			t.Fatal(err)
		}
	}
	if a.calls.Load() != 1 {
		t.Fatalf("%d calls; a run executed again keeps its budget", a.calls.Load())
	}
}

func TestReportedModelIsBounded(t *testing.T) {
	a := &fakeSeat{text: titleAnswer, model: "line one\nline two"}
	s := service(t, mapCache{}, map[string]int{"a": 5}, extractor.NewWithSeat("a", "fa", "wanted", "", a))
	x, _, err := s.Extract(context.Background(), excerpt(), body)
	if err != nil || x.Extractors[0].State != evidence.ExtractModelMismatch || x.Extractors[0].ModelReported != "" {
		t.Fatalf("%+v, %v", x.Extractors, err)
	}
}

// FuzzExtractSeals: whatever the models answer, the block Extract returns
// seals and opens, so an extraction can never make a book unsealable.
func FuzzExtractSeals(f *testing.F) {
	f.Add("THE LANTERN YEARS\nby Jane Doe", `{"claims":[{"field":"title","value":"THE LANTERN YEARS","quote":"THE LANTERN YEARS"}]}`, `{"claims":[]}`, 10)
	f.Add("THE LANTERN YEARS\nTranslated from the German by Jo Ray\nBook 2 of the Salt Road Cycle", `{"claims":[{"field":"contributor","value":"Jo Ray","quote":"Translated from the German by Jo Ray","role":"translator"},{"field":"series","value":"Salt Road Cycle","quote":"Book 2 of the Salt Road Cycle"},{"field":"series_index","value":"2","quote":"Book 2 of the Salt Road Cycle"}]}`, `{"claims":[{"field":"title","value":"THE LANTERN YEARS","quote":"THE LANTERN YEARS"},{"field":"contributor","value":"Jo Ray","quote":"Translated from the German by Jo Ray","role":"translator"}]}`, 18)
	f.Add("© 2019 Jane Doe\nISBN 978-0-306-40615-7", `{"claims":[{"field":"year","value":"2019","quote":"© 2019","label":"copyright"},{"field":"contributor","value":"Jane Doe","quote":"© 2019 Jane Doe","role":"author"}]}`, `{"claims":[{"field":"isbn","value":"9780306406157","quote":"ISBN 978-0-306-40615-7","label":"ebook"}]}`, 5)
	unsealable = func(err error) { panic(fmt.Sprintf("an extraction had to be emptied: %v", err)) }
	f.Fuzz(func(t *testing.T, text, answerA, answerB string, split int) {
		if !utf8.ValidString(text) || text == "" || strings.ContainsRune(text, 0) {
			return
		}
		b := evidence.NewExcerptBuilder(evidence.ExcerptEPUBText)
		r := []rune(text)
		cut := min(max(split, 0), len(r))
		b.Add(1, evidence.LandmarkTitle, string(r[:cut]))
		b.Add(2, evidence.LandmarkCopyright, string(r[cut:]))
		ex := b.Excerpt()
		if ex == nil || ex.Chars == 0 {
			return
		}
		s := New(Config{Extractors: []extractor.Extractor{ext("a", "fa", &fakeSeat{text: answerA, model: "m-a"}), ext("b", "fb", &fakeSeat{text: answerB, model: "m-b"})},
			Hosts: map[string]string{"a": "h", "b": "h"}, MaxCalls: map[string]int{"a": 1, "b": 1}})
		s.BeginRun(1)
		body := ex.Text
		sealedEx := *ex
		sealedEx.Text = ""
		x, _, err := s.Extract(context.Background(), sealedEx, body)
		if err != nil {
			t.Fatal(err)
		}
		p := evidence.Package{SchemaVersion: evidence.SchemaVersion, Formats: []evidence.FormatEvidence{{Format: "EPUB", State: evidence.StateOK, Excerpt: &sealedEx, Extraction: x}}}
		raw, seal, err := evidence.Seal(p)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := evidence.Open(raw, seal); err != nil {
			t.Fatalf("an extraction does not open: %v", err)
		}
	})
}

// TestExtractorCountsForTheRun: the run's counts say how many calls asked a
// model, how many answers came from the cache, and how many calls gave no
// usable answer; a take starts again from zero.
func TestExtractorCountsForTheRun(t *testing.T) {
	a := &fakeSeat{text: titleAnswer, model: "m-a"}
	b := &fakeSeat{model: "m-b", err: errors.New("fake: down")}
	cache := mapCache{}
	s := service(t, cache, map[string]int{"a": 10, "b": 10}, ext("a", "fa", a), ext("b", "fb", b))
	_, raws, err := s.Extract(context.Background(), excerpt(), body)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range raws {
		cache[r.Key] = [2]string{r.SHA256, r.Body}
	}
	if _, _, err := s.Extract(context.Background(), excerpt(), body); err != nil {
		t.Fatal(err)
	}
	if got, want := s.TakeExtractorCounts(), (verify.ExtractorCounts{Calls: 3, Cached: 1, Failed: 2}); got != want {
		t.Errorf("counts = %+v, want %+v", got, want)
	}
	if got := s.TakeExtractorCounts(); got != (verify.ExtractorCounts{}) {
		t.Errorf("a second take = %+v, want zero", got)
	}
}
