package agent

import (
	"context"
	"io"
	"net/http"
	"slices"
	"sync"
	"testing"
)

type callRecorder struct {
	mu  sync.Mutex
	got []string
}

func (r *callRecorder) AgentCall(seat, code string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, seat+"/"+code)
}

// TestSeatDoesNotCountACallCutShortByTheContext: a drain or a cancelled
// review is not a seat failure and must not show in agent_calls_total.
func TestSeatDoesNotCountACallCutShortByTheContext(t *testing.T) {
	noWait(t)
	rec := &callRecorder{}
	cfg, hc := testServer(t, "openai", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	})
	s, err := NewSeat(cfg, hc, WithCalls(rec))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Judge(ctx, "b", "p"); err == nil {
		t.Fatal("Judge with a cancelled context succeeded")
	}
	if len(rec.got) != 0 {
		t.Errorf("counted %v for a cancelled call", rec.got)
	}
}

// TestSeatCountsEachJudgeCallOnce: one count per Judge call, by seat and the
// final error code, however many retries the call took.
func TestSeatCountsEachJudgeCallOnce(t *testing.T) {
	noWait(t)
	rec := &callRecorder{}
	calls := 0
	cfg, hc := testServer(t, "openai", func(w http.ResponseWriter, _ *http.Request) {
		calls++
		switch calls {
		case 1:
			_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
		case 2:
			// Two server errors, then success: one call, no code.
			http.Error(w, "boom", http.StatusInternalServerError)
		case 3:
			_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
		default:
			http.Error(w, "nope", http.StatusBadRequest)
		}
	})
	cfg.Name = "deepseek"
	s, err := NewSeat(cfg, hc, WithCalls(rec))
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		_, _ = s.Judge(context.Background(), "b", "p")
	}
	want := []string{"deepseek/", "deepseek/", "deepseek/http_4xx"}
	if !slices.Equal(rec.got, want) {
		t.Errorf("counted %v, want %v", rec.got, want)
	}
}
