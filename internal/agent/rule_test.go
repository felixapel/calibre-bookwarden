package agent

import "testing"

func TestCombine(t *testing.T) {
	seats := []string{"haiku", "deepseek", "gemini"}
	r := func(seat, answer string) SeatResult { return SeatResult{Seat: seat, Answer: answer} }
	for _, tc := range []struct {
		name            string
		rs              []SeatResult
		outcome, reason string
	}{
		{"all approve", []SeatResult{r("haiku", Approve), r("deepseek", Approve), r("gemini", Approve)}, "approved", "all three judges approve"},
		{"all reject", []SeatResult{r("haiku", Reject), r("deepseek", Reject), r("gemini", Reject)}, "rejected", "all three judges reject"},
		{"one unsure", []SeatResult{r("haiku", Approve), r("deepseek", Approve), r("gemini", Unsure)}, "left", "agents disagree"},
		{"split", []SeatResult{r("haiku", Approve), r("deepseek", Reject), r("gemini", Approve)}, "left", "agents disagree"},
		{"all unsure", []SeatResult{r("haiku", Unsure), r("deepseek", Unsure), r("gemini", Unsure)}, "left", "agents unsure"},
		{"a failure", []SeatResult{r("haiku", Approve), r("deepseek", Approve), {Seat: "gemini", Err: "timeout"}}, "left", "gemini failed: timeout"},
		{"a missing seat", []SeatResult{r("haiku", Approve), r("gemini", Approve)}, "left", "deepseek didn't answer"},
		{"a seat off the panel", []SeatResult{r("haiku", Approve), r("deepseek", Approve), r("gemini", Approve), r("sonnet", Reject)}, "approved", "all three judges approve"},
		{"no seats", nil, "left", "haiku didn't answer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outcome, reason := Combine(tc.rs, seats)
			if outcome != tc.outcome || reason != tc.reason {
				t.Fatalf("Combine = %q, %q; want %q, %q", outcome, reason, tc.outcome, tc.reason)
			}
		})
	}
	if outcome, _ := Combine(nil, nil); outcome != "left" {
		t.Fatalf("an empty panel decided %q", outcome)
	}
}

func TestNotUnanimous(t *testing.T) {
	for reason, want := range map[string]bool{
		"agents disagree": true, "agents unsure": true, "gemini failed: timeout": true, "deepseek didn't answer": true,
		"shadow: approved": false, "shadow: rejected": false, "agent ceiling reached": false, "writes paused by an incident": false,
		"approve refused: it failed: x": false, "decided by a person": false, "superseded by a newer check": false, "all three judges approve": false, "": false,
	} {
		if got := NotUnanimous(reason); got != want {
			t.Errorf("NotUnanimous(%q) = %v, want %v", reason, got, want)
		}
	}
}
