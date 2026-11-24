package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/felixapel/calibre-bookwarden/internal/agent"
	"github.com/felixapel/calibre-bookwarden/internal/fix"
	"github.com/felixapel/calibre-bookwarden/internal/report"
	"github.com/felixapel/calibre-bookwarden/internal/store"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

// Agents is agent fixing behind Settings, Fixes and the run page (ADR-029);
// *agent.Service implements it.
type Agents interface {
	Mode(ctx context.Context) (string, error)
	SetMode(ctx context.Context, mode, by string) error
	Seats() []agent.SeatStatus
	MaxTokens() int64
	Request(ctx context.Context, runID int64, by string) error
	RunSummary(ctx context.Context, runID int64) (agent.RunSummary, error)
	Verdicts(ctx context.Context, runID int64) (map[agent.Key][]store.Verdict, error)
	Outcomes(ctx context.Context, runID int64) (map[agent.Key]store.Outcome, error)
}

var _ Agents = (*agent.Service)(nil)

// agentData adds agent fixing's state to the Settings page.
func (s *Server) agentData(ctx context.Context, data *pageData) error {
	if s.opts.Agents == nil {
		return nil
	}
	mode, err := s.opts.Agents.Mode(ctx)
	if err != nil {
		return err
	}
	data.AgentsOn, data.AgentMode, data.AgentSeats = true, mode, s.opts.Agents.Seats()
	data.AgentMaxTokens = s.opts.Agents.MaxTokens()
	return nil
}

// agentRefusal is the message for a refused agent request, or "".
func agentRefusal(err error) string {
	switch {
	case errors.Is(err, agent.ErrSeatsMissing):
		_, names, _ := strings.Cut(err.Error(), ": a judge isn't configured: ")
		return "A judge isn’t configured: " + names + ". Set its key, then restart bookwarden."
	case errors.Is(err, agent.ErrOff):
		return "Agent fixing is off. Set it to shadow in Settings first."
	case errors.Is(err, agent.ErrQueued):
		return "A review of this run is already waiting."
	}
	return ""
}

// setAgentMode records a change of the agent fixing mode.
func (s *Server) setAgentMode(w http.ResponseWriter, r *http.Request) {
	if s.opts.Agents == nil {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), reportPageBudget)
	defer cancel()
	form, err := strictForm(r, "", "value")
	if err != nil {
		s.reportError(w, r, err)
		return
	}
	mode := form.Get("value")
	if mode == "on" {
		s.refuse(w, r, http.StatusBadRequest, pageData{Title: "Settings", Error: "Agent fixing can no longer be turned on: the judges only give a second opinion, and nothing is changed without your decision. Choose off or shadow.", BackURL: "/settings", BackText: "Back to Settings"})
		return
	}
	if mode != store.ModeOff && mode != store.ModeShadow {
		s.reportError(w, r, report.ErrInvalidFilter)
		return
	}
	err = s.opts.Agents.SetMode(ctx, mode, actor)
	if msg := agentRefusal(err); msg != "" {
		s.refuse(w, r, http.StatusConflict, pageData{Title: "Settings", Error: msg, BackURL: "/settings", BackText: "Back to Settings"})
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

// reviewRun queues a review of a finished run by the agents.
func (s *Server) reviewRun(w http.ResponseWriter, r *http.Request) {
	if s.opts.Agents == nil {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), reportPageBudget)
	defer cancel()
	if _, err := strictForm(r, ""); err != nil {
		s.reportError(w, r, err)
		return
	}
	id, err := positiveID(r.PathValue("id"))
	if err != nil {
		s.reportError(w, r, report.ErrInvalidFilter)
		return
	}
	v, err := s.opts.Runs.View(ctx, id)
	if err != nil {
		s.reportError(w, r, err)
		return
	}
	back := fmt.Sprintf("/runs/%d/fixes", id)
	if !v.Run.Status.Terminal() {
		s.refuse(w, r, http.StatusConflict, pageData{Title: "Fixes", Error: "The check is still running. Review it with agents once it has finished.", BackURL: back, BackText: "Back to the fixes"})
		return
	}
	err = s.opts.Agents.Request(ctx, id, actor)
	if msg := agentRefusal(err); msg != "" {
		s.refuse(w, r, http.StatusConflict, pageData{Title: "Fixes", Error: msg, BackURL: back, BackText: "Back to the fixes"})
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// agentSummary adds what the agents did with a run to its page.
func (s *Server) agentSummary(ctx context.Context, v verify.RunView, data *pageData) error {
	if s.opts.Agents == nil {
		return nil
	}
	sum, err := s.opts.Agents.RunSummary(ctx, v.Run.ID)
	data.AgentSummary = sum
	return err
}

// withVerdicts adds the agents' verdicts and notes to a page of fixes: to
// every row when reveal is set, otherwise only to the decided ones.
func (s *Server) withVerdicts(ctx context.Context, runID int64, views []FixView, reveal bool) ([]FixView, error) {
	verdicts, err := s.opts.Agents.Verdicts(ctx, runID)
	if err != nil {
		return nil, err
	}
	outcomes, err := s.opts.Agents.Outcomes(ctx, runID)
	if err != nil {
		return nil, err
	}
	for i, v := range views {
		if !reveal && v.Status == "" {
			continue // still undecided: the verdicts stay hidden (ADR-034)
		}
		k := agent.Key{BookID: v.BookID, Field: v.Proposal.Field}
		views[i].Verdicts = verdicts[k]
		if o, ok := outcomes[k]; ok && o.Outcome == store.OutcomeLeft {
			views[i].AgentNote = o.Reason
		}
	}
	return views, nil
}

// filterDecided keeps the rows a Decided filter asks for.
func filterDecided(views []FixView, decided string) []FixView {
	if decided == "" {
		return views
	}
	out := make([]FixView, 0, len(views))
	for _, v := range views {
		var keep bool
		switch decided {
		case report.DecidedAgent:
			keep = v.DecidedBy == store.AgentActor
		case report.DecidedPerson:
			keep = v.DecidedBy != "" && v.DecidedBy != store.AgentActor
		case report.DecidedDisagree:
			keep = v.Status == "" && agent.NotUnanimous(v.AgentNote)
		}
		if keep {
			out = append(out, v)
		}
	}
	return out
}

// agentDecided returns who decided each field written by an operation, when
// the agent decided any of them; otherwise none, and the page is as before.
func (s *Server) agentDecided(ctx context.Context, runID, opID int64) ([]fix.ProposalRow, error) {
	rows, err := s.opts.Fixing.RunProposals(ctx, runID)
	if err != nil {
		return nil, err
	}
	var out []fix.ProposalRow
	agentDecided := false
	for _, p := range rows {
		if p.OperationID != nil && *p.OperationID == opID {
			out = append(out, p)
			agentDecided = agentDecided || p.DecidedBy == store.AgentActor
		}
	}
	if !agentDecided {
		return nil, nil
	}
	return out, nil
}
