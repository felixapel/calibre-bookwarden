// Package extraction runs M4's shadow extraction of one format's excerpt
// (design sections 4, 7 and 8): the parsers and the extractors propose
// claims, extract.Verify keeps what the book's own text derives, and
// extract.Agree says what the voices agree on. It answers with the sealable
// block and the raw answers, which are stored privately; nothing here logs
// or returns excerpt text.
package extraction

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/felixapel/calibre-bookwarden/internal/agent"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/extract"
	"github.com/felixapel/calibre-bookwarden/internal/extractor"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

// Cache finds a raw answer by its cache key; a corrupt row is a miss.
type Cache interface {
	CachedRaw(ctx context.Context, key string) (sha, body string, ok bool, err error)
}

// Config is a Service's extractors and limits.
type Config struct {
	Extractors []extractor.Extractor
	Hosts      map[string]string // extractor name → its URL's host, part of the cache key
	MaxCalls   map[string]int    // calls per run; cache hits don't count
	Cache      Cache
	// Quotas is where the extractors' seats record quota exhaustion; an
	// extractor is not called while its pause lasts.
	Quotas  *agent.Quotas
	Timeout time.Duration // per call; default defaultTimeout
	Now     func() time.Time
}

const (
	defaultTimeout = 2 * time.Minute
	// quotaPause is how long an exhausted extractor rests when the provider
	// gave no Retry-After.
	quotaPause = 15 * time.Minute
)

// Service extracts excerpts for a run. It is safe for concurrent use.
type Service struct {
	c      Config
	mu     sync.Mutex
	run    int64
	calls  map[string]int
	counts verify.ExtractorCounts // since the last take
}

// New returns a Service for c.
func New(c Config) *Service {
	if c.Timeout <= 0 {
		c.Timeout = defaultTimeout
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return &Service{c: c, calls: map[string]int{}}
}

// BeginRun starts a run's call budget. The same run executed again, after
// an interruption, keeps what it has spent.
func (s *Service) BeginRun(runID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if runID != s.run {
		s.run, s.calls = runID, map[string]int{}
	}
}

// take spends one of name's calls for this run, if any is left.
func (s *Service) take(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.calls[name] >= s.c.MaxCalls[name] {
		return false
	}
	s.calls[name]++
	return true
}

// TakeExtractorCounts returns the calls counted since it was last called and
// starts again from zero; the runner adds them to the run's stats (B5).
func (s *Service) TakeExtractorCounts() verify.ExtractorCounts {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.counts
	s.counts = verify.ExtractorCounts{}
	return out
}

// tally counts one answer: from the cache, or a call that did or didn't
// give a usable answer.
func (s *Service) tally(cached, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case cached:
		s.counts.Cached++
	case ok:
		s.counts.Calls++
	default:
		s.counts.Calls++
		s.counts.Failed++
	}
}

func (s *Service) paused(name string) bool {
	st, ok := s.c.Quotas.State(name)
	if !ok {
		return false
	}
	return s.c.Now().Before(st.At.Add(cmp.Or(st.RetryAfter, quotaPause)))
}

// answer is one extractor's outcome for the excerpt.
type answer struct {
	run    evidence.ExtractorRun
	claims []extract.Claim
	raw    *verify.RawOutput
}

// Extract runs the parsers and the extractors over the excerpt whose text is
// body. Every provider failure becomes the extractor's state; the error is
// only ever ctx's.
func (s *Service) Extract(ctx context.Context, ex evidence.Excerpt, body string) (*evidence.Extraction, []verify.RawOutput, error) {
	ex.Text = body
	x := extract.NewExcerpt(ex).Prepared()
	answers := make([]answer, len(s.c.Extractors))
	var wg sync.WaitGroup
	for i, e := range s.c.Extractors {
		wg.Go(func() { answers[i] = s.one(ctx, e, ex.SHA256, x) })
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	out := &evidence.Extraction{PromptVersion: extractor.PromptVersion, ExtractVersion: extract.Version, ExcerptSHA256: ex.SHA256}
	byName := map[string][]extract.Claim{}
	var raws []verify.RawOutput
	for _, a := range answers {
		out.Extractors = append(out.Extractors, a.run)
		if a.run.State == evidence.ExtractOK {
			byName[a.run.Name] = a.claims
		}
		if a.raw != nil {
			raws = append(raws, *a.raw)
		}
	}
	out.Claims, out.Agreed = judge(x, byName, names(out.Extractors))
	// What can't be sealed must not make the book unsealable: keep the
	// states and drop what they found (FuzzExtractSeals keeps this unused).
	if err := out.Check(&ex); err != nil {
		unsealable(err)
		out.Claims, out.Agreed = nil, nil
	}
	return out, raws, nil
}

func (s *Service) one(ctx context.Context, e extractor.Extractor, excerptSHA string, x extract.Excerpt) answer {
	run := evidence.ExtractorRun{Name: e.Name, Family: e.Family, Model: e.Model, Effort: e.Effort}
	key := cacheKey(excerptSHA, extractor.PromptVersion, s.c.Hosts[e.Name], e.Model, e.Effort)
	if s.c.Cache != nil {
		if sha, body, ok, err := s.c.Cache.CachedRaw(ctx, key); err == nil && ok {
			if claims, err := extractor.ParseOutput(body); err == nil {
				s.tally(true, true)
				run.State, run.RawSHA256, run.Cached = evidence.ExtractOK, sha, true
				return answer{run: run, claims: withSource(claims, e.Name), raw: &verify.RawOutput{Key: key, SHA256: sha, Body: body}}
			}
		}
	}
	if s.paused(e.Name) || !s.take(e.Name) {
		run.State = evidence.ExtractUnavailable
		return answer{run: run}
	}
	cctx, cancel := context.WithTimeout(ctx, s.c.Timeout)
	defer cancel()
	r, err := e.Extract(cctx, x)
	s.tally(false, err == nil && storable(r.Raw))
	if modelID(r.Reply.Model) {
		run.ModelReported = r.Reply.Model
	}
	switch {
	case !storable(r.Raw):
		// Postgres text holds no NUL and no invalid UTF-8: an answer that
		// can't be stored can't be re-verified, so it is not used.
		run.State = evidence.ExtractUnavailable
	case err == nil:
		run.State, run.RawSHA256 = evidence.ExtractOK, hashOf(r.Raw)
		return answer{run: run, claims: r.Claims, raw: &verify.RawOutput{Key: key, SHA256: run.RawSHA256, Body: r.Raw}}
	case errors.Is(err, extractor.ErrInvalidOutput):
		// Kept to re-verify the package, never cached: the next run asks again.
		run.State, run.RawSHA256 = evidence.ExtractInvalidOutput, hashOf(r.Raw)
		return answer{run: run, raw: &verify.RawOutput{SHA256: run.RawSHA256, Body: r.Raw}}
	case errors.Is(err, extractor.ErrModelMismatch):
		run.State = evidence.ExtractModelMismatch
	case agent.ErrorCode(err) == "quota_exhausted":
		run.State = evidence.ExtractQuotaExhausted
	case agent.ErrorCode(err) == "timeout":
		run.State = evidence.ExtractTimeout
	default:
		run.State = evidence.ExtractUnavailable
	}
	return answer{run: run}
}

// judge verifies the parsers' claims and each answering extractor's, and
// agrees them. Claims are ordered by where their quote is, so the same
// answers always seal the same block.
func judge(x extract.Excerpt, byName map[string][]extract.Claim, extractors []string) ([]evidence.ExtractedClaim, []evidence.AgreedFact) {
	claims, agreed, _ := judgeVerified(x, byName, extractors)
	return claims, agreed
}

// judgeVerified is judge that also hands over the verifier's verdicts, which
// keep what each claim asked for (its role and label) when it is not grounded.
func judgeVerified(x extract.Excerpt, byName map[string][]extract.Claim, extractors []string) ([]evidence.ExtractedClaim, []evidence.AgreedFact, []extract.Verified) {
	claims := extract.Parse(x)
	for _, name := range extractors {
		claims = append(claims, byName[name]...)
	}
	vs := make([]extract.Verified, 0, len(claims))
	for _, c := range claims {
		vs = append(vs, extract.Verify(x, c))
	}
	sealed := make([]evidence.ExtractedClaim, 0, len(vs))
	for _, v := range vs {
		s := v.Sealable()
		sealed = append(sealed, evidence.ExtractedClaim{Field: s.Field, Value: s.Value, Role: s.Role, Label: s.Label, Source: s.Source, Status: s.Status, Unit: s.Unit, Start: s.Start, End: s.End})
	}
	slices.SortStableFunc(sealed, func(a, b evidence.ExtractedClaim) int {
		return cmp.Or(cmp.Compare(a.Unit, b.Unit), cmp.Compare(a.Start, b.Start), cmp.Compare(a.End, b.End),
			cmp.Compare(a.Source, b.Source), cmp.Compare(a.Field, b.Field), cmp.Compare(a.Status, b.Status),
			cmp.Compare(a.Value, b.Value), cmp.Compare(a.Role, b.Role), cmp.Compare(a.Label, b.Label))
	})
	var agreed []evidence.AgreedFact
	for _, f := range extract.Agree(vs, extractors) {
		a := evidence.AgreedFact{Key: f.Key, Outcome: f.Outcome, Voices: f.Voices}
		for _, v := range f.Values {
			a.Values = append(a.Values, evidence.AgreedValue{Value: v.Value, Role: v.Role, Label: v.Label})
		}
		agreed = append(agreed, a)
	}
	return nilIfEmpty(sealed), agreed, vs
}

func nilIfEmpty[T any](s []T) []T {
	if len(s) == 0 {
		return nil
	}
	return s
}

func names(runs []evidence.ExtractorRun) []string {
	out := make([]string, len(runs))
	for i, r := range runs {
		out[i] = r.Name
	}
	return out
}

func withSource(claims []extract.Claim, name string) []extract.Claim {
	for i := range claims {
		claims[i].Source = name
	}
	return claims
}

// cacheKey is design section 4's key: the excerpt, the prompt, the
// provider, the model and the effort.
func cacheKey(excerptSHA, prompt, host, model, effort string) string {
	return hashOf(excerptSHA + "\x00" + prompt + "\x00" + host + "\x00" + model + "\x00" + effort)
}

// unsealable hears of a block that had to be emptied; FuzzExtractSeals
// fails on it.
var unsealable = func(error) {}

// storable reports whether a raw answer fits a Postgres text column.
func storable(s string) bool { return utf8.ValidString(s) && !strings.ContainsRune(s, 0) }

// modelID reports whether a reported model may be sealed: short, with no
// space or control character (evidence checks the same).
func modelID(s string) bool {
	return utf8.RuneCountInString(s) <= 200 && !strings.ContainsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) })
}

func hashOf(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
