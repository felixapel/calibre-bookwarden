package agent

import (
	"context"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestQuotaRecorded(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		header     string
		body       string
		wantRecord bool
		wantRetry  time.Duration
	}{
		{"429 with Retry-After, uncapped", 429, "600", "", true, 10 * time.Minute},
		{"429 with a Retry-After a retry covers", 429, "60", "", true, time.Minute},
		{"429 without header", 429, "", "", true, 0},
		{"429 with a junk header", 429, "soon", "", true, 0},
		{"402 credits", 402, "", `{"error":{"message":"no credits left"}}`, true, 0},
		{"403 billing with header", 403, "90", `{"message":"billing issue"}`, true, 90 * time.Second},
		{"403 forbidden", 403, "", `{"message":"forbidden"}`, false, 0},
		{"401", 401, "", `{"message":"quota"}`, false, 0},
		{"500", 500, "", `{"message":"quota"}`, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			noWait(t)
			cfg, hc := testServer(t, "openai", func(w http.ResponseWriter, _ *http.Request) {
				if tc.header != "" {
					w.Header().Set("Retry-After", tc.header)
				}
				w.Header().Set("Content-Type", bodyType(tc.body))
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			})
			now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
			q := NewQuotas()
			q.now = func() time.Time { return now }
			s, err := NewSeat(cfg, hc, WithQuotas(q))
			if err != nil {
				t.Fatal(err)
			}
			if _, st := q.State("s"); st {
				t.Fatal("state before any call")
			}
			_, _ = s.Judge(context.Background(), "B", "P")
			got, ok := q.State("s")
			if ok != tc.wantRecord {
				t.Fatalf("recorded = %v, want %v", ok, tc.wantRecord)
			}
			if ok && (!got.At.Equal(now) || got.RetryAfter != tc.wantRetry) {
				t.Fatalf("state = %+v, want at %v retry %v", got, now, tc.wantRetry)
			}
			if _, other := q.State("other"); other {
				t.Fatal("state leaked to another seat")
			}
		})
	}
}

func TestQuotaWithoutTracker(t *testing.T) {
	noWait(t)
	cfg, hc := testServer(t, "openai", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(402)
		_, _ = io.WriteString(w, `{"message":"credits"}`)
	})
	s, _ := NewSeat(cfg, hc)
	if _, err := s.Judge(context.Background(), "B", "P"); ErrorCode(err) != "quota_exhausted" {
		t.Fatalf("code = %q", ErrorCode(err))
	}
}

func TestServiceSeatsShowQuota(t *testing.T) {
	q := NewQuotas()
	at := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	q.Record("haiku", at, time.Minute)
	svc := NewService(ServiceOptions{Seats: []SeatStatus{{Name: "haiku", Model: "m", Family: "anthropic", Configured: true}, {Name: "gemini"}}, Quotas: q})
	got := svc.Seats()
	if !got[0].QuotaAt.Equal(at) || got[0].QuotaRetryAfter != time.Minute || got[0].Family != "anthropic" || !got[1].QuotaAt.IsZero() {
		t.Fatalf("seats = %+v", got)
	}
}

// A seat that works again stops showing as out of quota.
func TestQuotaClearedBySuccess(t *testing.T) {
	noWait(t)
	var fail atomic.Bool
	fail.Store(true)
	cfg, hc := testServer(t, "openai", func(w http.ResponseWriter, _ *http.Request) {
		if fail.Load() {
			w.WriteHeader(429)
			return
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	})
	q := NewQuotas()
	s, _ := NewSeat(cfg, hc, WithQuotas(q))
	_, _ = s.Judge(context.Background(), "B", "P")
	if _, ok := q.State("s"); !ok {
		t.Fatal("quota not recorded")
	}
	fail.Store(false)
	if _, err := s.Judge(context.Background(), "B", "P"); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.State("s"); ok {
		t.Fatal("quota state survived a successful call")
	}
}

// A wait that would outlast the call's deadline ends the call as an exhausted
// quota, recorded, not as a timeout (run 12: a 25-minute usage limit made
// every extractor call wait out its two minutes and record nothing).
//
// Without a Retry-After the retry wait is 5 s and the recorded Retry-After is
// zero, which the extraction service rests as its default pause: a 429 near
// the deadline errs toward resting the provider, at the cost of coverage.
func TestQuotaRecordedWhenTheWaitOutlastsTheDeadline(t *testing.T) {
	for _, tc := range []struct {
		name, header string
		deadline     time.Duration
		wantRetry    time.Duration
	}{
		{"Retry-After longer than the time left", "45", 30 * time.Second, 45 * time.Second},
		{"no Retry-After, under the retry wait left", "", 3 * time.Second, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			noWait(t)
			var calls atomic.Int32
			cfg, hc := testServer(t, "openai", func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				if tc.header != "" {
					w.Header().Set("Retry-After", tc.header)
				}
				w.WriteHeader(429)
			})
			q := NewQuotas()
			s, _ := NewSeat(cfg, hc, WithQuotas(q))
			ctx, cancel := context.WithTimeout(context.Background(), tc.deadline)
			defer cancel()
			if _, err := s.Judge(ctx, "B", "P"); ErrorCode(err) != "quota_exhausted" {
				t.Fatalf("code = %q", ErrorCode(err))
			}
			if got, ok := q.State("s"); !ok || got.RetryAfter != tc.wantRetry {
				t.Fatalf("state = %+v, %v", got, ok)
			}
			if calls.Load() != 1 {
				t.Fatalf("calls = %d, want 1", calls.Load())
			}
		})
	}
}
