package agent

import (
	"strconv"
	"strings"

	"github.com/felixapel/calibre-bookwarden/internal/store"
)

// SeatResult is one seat's newest verdict on a proposal: an answer, or the
// error code of the call that gave none.
type SeatResult struct{ Seat, Answer, Err string }

// Combine applies the decision rule (ADR-029): every seat approves →
// approved; every seat rejects → rejected; anything else (a split, an
// unsure, a failure, a seat that didn't answer) is left for a person, with
// the reason.
func Combine(rs []SeatResult, seats []string) (outcome, reason string) {
	if len(seats) == 0 {
		return store.OutcomeLeft, "no judges"
	}
	by := map[string]SeatResult{}
	for _, r := range rs {
		by[r.Seat] = r
	}
	counts := map[string]int{}
	for _, s := range seats {
		r, ok := by[s]
		switch {
		case !ok:
			return store.OutcomeLeft, s + " didn't answer"
		case r.Err != "":
			return store.OutcomeLeft, s + " failed: " + r.Err
		}
		counts[r.Answer]++
	}
	all := "all " + count(len(seats)) + " judges "
	switch len(seats) {
	case counts[Approve]:
		return store.OutcomeApproved, all + "approve"
	case counts[Reject]:
		return store.OutcomeRejected, all + "reject"
	case counts[Unsure]:
		return store.OutcomeLeft, "agents unsure"
	}
	return store.OutcomeLeft, "agents disagree"
}

// NotUnanimous reports whether a reason Combine gave left a proposal because
// the verdicts weren't unanimous: a split, an unsure, a failed or a silent
// seat. Shadow reasons, and the ceiling, pause and refusal reasons old reviews
// recorded, are not.
func NotUnanimous(reason string) bool {
	if reason == "agents disagree" || reason == "agents unsure" {
		return true
	}
	if seat, _, ok := strings.Cut(reason, " failed: "); ok {
		return !strings.Contains(seat, " ")
	}
	seat, ok := strings.CutSuffix(reason, " didn't answer")
	return ok && !strings.Contains(seat, " ")
}

func count(n int) string {
	if n == 3 {
		return "three"
	}
	return strconv.Itoa(n)
}
