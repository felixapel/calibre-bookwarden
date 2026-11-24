package store

import (
	"context"
	"fmt"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

func TestExtractorCalls(t *testing.T) {
	ctx := context.Background()
	// An ok call (and an invalid_output one) is sealed with the hash of its
	// raw answer, which the outcome must carry.
	seq := 0
	bodies := map[string]string{} // hash -> body
	call := func(state string, cached bool) evidence.ExtractorRun {
		c := evidence.ExtractorRun{Name: "x", Family: "fam", Model: "m", State: state, Cached: cached}
		if state == evidence.ExtractOK || state == evidence.ExtractInvalidOutput {
			seq++
			body := fmt.Sprintf(`{"n":%d}`, seq)
			c.RawSHA256 = rawOf(body, "").SHA256
			bodies[c.RawSHA256] = body
		}
		return c
	}
	// withRuns is the n-th book of a run (finishedRun numbers them by
	// position), its one format carrying an extraction with these calls.
	withRuns := func(t *testing.T, n int, runs ...evidence.ExtractorRun) verify.Outcome {
		out, _ := excerptOutcomeWith(t, refs(n)[n-1], func(p *evidence.Package) {
			for i := range runs {
				runs[i].Name = fmt.Sprintf("ex%d", i)
			}
			p.Formats[0].Extraction = &evidence.Extraction{PromptVersion: "extract-2", ExtractVersion: 2, ExcerptSHA256: p.Formats[0].Excerpt.SHA256, Extractors: runs}
		}, "extraction excerpt text")
		for _, c := range runs {
			if c.RawSHA256 != "" {
				out.Raws = append(out.Raws, rawOf(bodies[c.RawSHA256], ""))
			}
		}
		return out
	}
	tests := []struct {
		name          string
		outcomes      func(t *testing.T) []verify.Outcome
		calls, failed int
	}{
		{name: "no extraction at all", outcomes: func(t *testing.T) []verify.Outcome {
			out, _ := excerptOutcome(t, refs(1)[0], "plain")
			return []verify.Outcome{out}
		}},
		{name: "cached answers are not calls", outcomes: func(t *testing.T) []verify.Outcome {
			return []verify.Outcome{withRuns(t, 1, call(evidence.ExtractOK, true), call(evidence.ExtractTimeout, true))}
		}},
		// A quota pause or a per-run cap seals unavailable without any call
		// made, and the sealed data can't tell it from a provider failure.
		// It counts as not answered: down is right after a quota hit.
		{name: "unavailable from a quota pause counts as not answered", outcomes: func(t *testing.T) []verify.Outcome {
			return []verify.Outcome{withRuns(t, 1, call(evidence.ExtractUnavailable, false), call(evidence.ExtractQuotaExhausted, false), call(evidence.ExtractOK, false))}
		}, calls: 3, failed: 2},
		{name: "counts fresh calls and the ones that did not end ok", outcomes: func(t *testing.T) []verify.Outcome {
			return []verify.Outcome{
				withRuns(t, 1, call(evidence.ExtractOK, false), call(evidence.ExtractUnavailable, false), call(evidence.ExtractOK, true), call(evidence.ExtractInvalidOutput, false)),
				withRuns(t, 2, call(evidence.ExtractOK, false)),
				sealed(t, 30, "uuid-3"),
			}
		}, calls: 4, failed: 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := runRepo(t)
			// Another run's calls must not leak into this run's count.
			finishedRun(t, r, verify.RunCompleted, withRuns(t, 1, call(evidence.ExtractUnavailable, false)))
			id := finishedRun(t, r, verify.RunCompleted, tc.outcomes(t)...)
			calls, failed, err := r.ExtractorCalls(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if calls != tc.calls || failed != tc.failed {
				t.Errorf("calls, failed = %d, %d; want %d, %d", calls, failed, tc.calls, tc.failed)
			}
		})
	}
}
