package agent

import (
	"context"
	"encoding/json"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/store"
)

// Decisions reads a run's decided proposals; *store.FixRepo implements it.
type Decisions interface {
	RunProposals(ctx context.Context, runID int64) ([]store.ProposalRow, error)
}

// Key names one proposal of a run.
type Key struct {
	BookID int
	Field  string
}

// RunSummary is what the agents did with a run, as its page shows it.
type RunSummary struct {
	Reviewed bool // a review of the run exists
	// Approved and Rejected count the agent's decisions; Applied, the
	// approvals already written. ForYou counts proposals the panel left
	// that nobody has decided since.
	Approved, Applied, Rejected, ForYou int
	Tokens                              map[string]int64 // per seat
}

// RunSummary sums up the agents' work on a run.
func (s *Service) RunSummary(ctx context.Context, runID int64) (RunSummary, error) {
	var sum RunSummary
	reviews, err := s.o.Store.Reviews(ctx, runID)
	if err != nil || len(reviews) == 0 {
		return sum, err
	}
	sum.Reviewed = true
	rows, err := s.o.Decisions.RunProposals(ctx, runID)
	if err != nil {
		return sum, err
	}
	decided := map[Key]bool{}
	for _, r := range rows {
		decided[Key{r.BookID, r.Proposal.Field}] = true
		if r.DecidedBy != store.AgentActor {
			continue
		}
		switch r.Status {
		case store.ProposalRejected:
			sum.Rejected++
		case store.ProposalApplied:
			sum.Approved++
			sum.Applied++
		case store.ProposalApproved, store.ProposalStale, store.ProposalBlocked, store.ProposalFailed:
			sum.Approved++
		}
	}
	outcomes, err := s.o.Store.Outcomes(ctx, runID)
	if err != nil {
		return sum, err
	}
	for _, o := range outcomes {
		// A book-level outcome (no field) names no proposal, so it isn't counted.
		if o.Outcome == store.OutcomeLeft && o.Field != "" && !decided[Key{o.BookID, o.Field}] {
			sum.ForYou++
		}
	}
	sum.Tokens, err = s.o.Store.Tokens(ctx, runID)
	return sum, err
}

// sealedHashes returns the ProposalHash of each proposal in a book's stored
// package, by field. It reads values only to filter what the page shows; the
// reviewer opens and verifies the package itself. nil when the package can't
// be read.
func (s *Service) sealedHashes(ctx context.Context, runID int64, bookID int) map[string]string {
	if s.o.Reviewer == nil {
		return nil
	}
	stored, _, err := s.o.Reviewer.o.Evidence.Evidence(ctx, runID, bookID)
	if err != nil {
		return nil
	}
	var doc struct {
		Proposals []evidence.Proposal `json:"proposals"`
	}
	if json.Unmarshal(stored, &doc) != nil {
		return nil
	}
	return proposalHashes(doc.Proposals)
}

// Verdicts returns each proposal's newest verdict per seat, in panel order.
// Only verdicts on the proposal as it is sealed now are shown, the ones the
// reviewer would use.
func (s *Service) Verdicts(ctx context.Context, runID int64) (map[Key][]store.Verdict, error) {
	all, err := s.o.Store.Verdicts(ctx, runID, PromptVersion)
	if err != nil {
		return nil, err
	}
	newest := map[Key]map[string]store.Verdict{}
	current := map[int]map[string]string{} // book → field → proposal hash
	for _, v := range all {
		// Show only verdicts the reviewer would use: those on the
		// proposal as sealed now. Unreadable evidence shows none.
		hashes, ok := current[v.BookID]
		if !ok {
			hashes = s.sealedHashes(ctx, runID, v.BookID)
			current[v.BookID] = hashes
		}
		if want := hashes[v.Field]; want == "" || v.ProposalHash != want {
			continue
		}
		k := Key{v.BookID, v.Field}
		if newest[k] == nil {
			newest[k] = map[string]store.Verdict{}
		}
		newest[k][v.Seat] = v
	}
	out := map[Key][]store.Verdict{}
	for k, bySeat := range newest {
		for _, st := range s.o.Seats {
			if v, ok := bySeat[st.Name]; ok {
				out[k] = append(out[k], v)
			}
		}
	}
	return out, nil
}

// Outcomes returns what the newest review did with each proposal.
func (s *Service) Outcomes(ctx context.Context, runID int64) (map[Key]store.Outcome, error) {
	all, err := s.o.Store.Outcomes(ctx, runID)
	if err != nil {
		return nil, err
	}
	out := make(map[Key]store.Outcome, len(all))
	for _, o := range all {
		out[Key{o.BookID, o.Field}] = o
	}
	return out, nil
}
