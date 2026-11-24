package extractor

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/agent"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/extract"
)

// scriptSeat answers each excerpt from a function of its text.
type scriptSeat struct {
	answer func(packet string) (string, error)
}

func (s scriptSeat) Name() string  { return "x" }
func (s scriptSeat) Model() string { return "m" }
func (s scriptSeat) Judge(_ context.Context, _, packet string) (agent.Reply, error) {
	text, err := s.answer(packet)
	return agent.Reply{Text: text, InputTokens: 100, OutputTokens: 20, ReasoningTokens: 5, Model: "m"}, err
}

func TestRunCellScoresSyntheticCases(t *testing.T) {
	c := Synthetic(3, 4)[3] // injected case
	// One right title, one trap title (printed, so it grounds), one wrong role.
	answer := `{"claims":[` +
		`{"field":"title","value":"` + c.Truth[0].Value + `","quote":"` + c.Truth[0].Quote + `"},` +
		`{"field":"title","value":"Pwned Volume","quote":"Pwned Volume"},` +
		`{"field":"subtitle","value":"Nothing","quote":"Nothing"}]}`
	e := Extractor{Name: "deepseek", Model: "m", seat: scriptSeat{func(string) (string, error) { return answer, nil }}}
	st := RunCell(context.Background(), e, []Sample{SyntheticSample(c)}, RunOptions{Concurrency: 2, Budget: NewBudget(10)})
	if st.Calls != 1 || st.TP != 1 || st.FP != 1 || st.TrapHits != 1 || st.FN != len(c.Truth)-1 {
		t.Fatalf("stats = %+v", st)
	}
	if st.Status[extract.StatusNotFound] != 1 || st.Claims != 3 || st.Grounded != 2 {
		t.Errorf("statuses = %v, claims %d, grounded %d", st.Status, st.Claims, st.Grounded)
	}
	if st.InputTokens != 100 || st.ReasoningTokens != 5 || len(st.Latencies) != 1 {
		t.Errorf("cost = %+v", st)
	}
	if p := st.Precision(); p != 0.5 {
		t.Errorf("precision = %v", p)
	}
}

func TestRunCellRealSamplesAgreeWithParsers(t *testing.T) {
	b := evidence.NewExcerptBuilder(evidence.ExcerptEPUBText)
	b.Add(1, evidence.LandmarkCopyright, "© 2019 Harbor House\nISBN 978-1-4028-9462-6 (ebook)")
	x := extract.NewExcerpt(*b.Excerpt())
	answer := `{"claims":[{"field":"year","value":"2019","quote":"© 2019"},{"field":"isbn","value":"9780306406157","quote":"ISBN 978-0-306-40615-7"}]}`
	e := Extractor{Name: "glm", Model: "m", seat: scriptSeat{func(string) (string, error) { return answer, nil }}}
	st := RunCell(context.Background(), e, []Sample{RealSample("book-1", x)}, RunOptions{Concurrency: 1, Budget: NewBudget(10)})
	// The year agrees with the parser; the ISBN isn't printed, so it is no voice.
	if st.ParserPatterned != 1 || st.ParserAgree != 1 || st.ParserTotal != 2 || st.ParserCovered != 1 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestRunCellErrorsAndBudget(t *testing.T) {
	samples := []Sample{SyntheticSample(Synthetic(1, 1)[0]), SyntheticSample(Synthetic(2, 1)[0]), SyntheticSample(Synthetic(3, 1)[0])}
	bad := Extractor{Name: "gemini", Model: "m", seat: scriptSeat{func(p string) (string, error) {
		if strings.Contains(p, "Copyright") {
			return "not json", nil
		}
		return "", errors.New("boom")
	}}}
	st := RunCell(context.Background(), bad, samples, RunOptions{Concurrency: 1, Budget: NewBudget(2)})
	if st.Calls != 2 || st.Skipped != 1 {
		t.Fatalf("budget: calls %d, skipped %d", st.Calls, st.Skipped)
	}
	if st.Errors["invalid_output"] != 2 {
		t.Errorf("errors = %v", st.Errors)
	}
}

func TestAgreement(t *testing.T) {
	a := CellStats{grounded: map[string]map[string]bool{"s1": {"k1": true, "k2": true}, "s2": {"k3": true}}}
	b := CellStats{grounded: map[string]map[string]bool{"s1": {"k1": true}, "s2": {"k3": true, "k4": true}}}
	if got := Agreement(a, b); got != 0.5 { // 2 shared of 4 distinct
		t.Errorf("agreement = %v", got)
	}
}

func TestSuggestEffort(t *testing.T) {
	cell := func(name, effort string, tp, fp, claims, grounded int) CellStats {
		return CellStats{Extractor: name, Effort: effort, Calls: 10, TP: tp, FP: fp, Claims: claims, Grounded: grounded}
	}
	cells := []CellStats{
		cell("deepseek", "low", 90, 1, 100, 90),    // lower precision
		cell("deepseek", "medium", 95, 0, 100, 91), // best precision, grounded within 2 pts
		cell("deepseek", "high", 96, 0, 100, 92),   // best precision too, more costly
		cell("glm", "none", 50, 0, 100, 60),        // best precision, grounded 30 pts lower
		cell("glm", "high", 80, 0, 100, 90),
		cell("gemini", "default", 70, 0, 100, 80),
		cell("gemini", "low", 70, 0, 100, 80),
		{Extractor: "gemini", Effort: "max", Calls: 10, Errors: map[string]int{"http_4xx": 10}}, // refused
	}
	got := SuggestEfforts(cells)
	want := map[string]string{"deepseek": "medium", "glm": "high", "gemini": "low"}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s: suggested %q, want %q (all: %v)", k, got[k], w, got)
		}
	}
}

func TestRunCellCountsAnotherModel(t *testing.T) {
	e := Extractor{Name: "deepseek", Model: "opencode-go/deepseek-v4.1-flash", seat: scriptSeat{func(string) (string, error) { return `{"claims":[]}`, nil }}}
	st := RunCell(context.Background(), e, []Sample{SyntheticSample(Synthetic(1, 1)[0])}, RunOptions{Concurrency: 1, Budget: NewBudget(5)})
	if st.Errors["model_mismatch"] != 1 || st.Models["m"] != 1 || st.Usable() {
		t.Fatalf("stats = %+v", st)
	}
}

func TestSuggestEffortsPicksAmongTheMostPrecise(t *testing.T) {
	cell := func(effort string, tp, fp, claims, grounded, calls, errs int) CellStats {
		return CellStats{Extractor: "x", Effort: effort, TP: tp, FP: fp, Claims: claims, Grounded: grounded, Calls: calls, Errors: map[string]int{"http_5xx": errs}}
	}
	for _, tc := range []struct {
		name  string
		cells []CellStats
		want  string
	}{
		// The most precise cell grounds less than a less precise one: it is
		// still the pick, not nothing.
		{"precision first", []CellStats{cell("none", 100, 0, 100, 80, 30, 0), cell("high", 99, 1, 100, 90, 30, 0)}, "none"},
		// A cell that mostly failed is not a candidate, however precise.
		{"mostly failed", []CellStats{cell("none", 995, 5, 1000, 950, 100, 0), cell("high", 1, 0, 1, 1, 100, 99)}, "none"},
		{"cheapest of the equals", []CellStats{cell("medium", 10, 0, 10, 10, 30, 0), cell("low", 10, 0, 10, 10, 30, 1)}, "low"},
	} {
		if got := SuggestEfforts(tc.cells)["x"]; got != tc.want {
			t.Errorf("%s: suggested %q, want %q", tc.name, got, tc.want)
		}
	}
}
