package web

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/felixapel/calibre-bookwarden/internal/fix"
	"github.com/felixapel/calibre-bookwarden/internal/report"
)

// settingsLogShown is how many switch changes Settings lists.
const settingsLogShown = 20

func (s *Server) incidentsPage(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), reportPageBudget)
	defer cancel()
	r = r.WithContext(ctx)
	if _, err := strictQuery(r); err != nil {
		s.reportError(w, r, err)
		return
	}
	incidents, err := s.opts.Fixing.Incidents(ctx)
	data := pageData{Title: "Incidents", SignedIn: true, CSRFToken: sessionFrom(ctx).CSRFToken}
	if err == nil {
		data.Paused, err = s.opts.Fixing.Paused(ctx)
	}
	if err == nil {
		data.WritesOn, err = s.opts.Fixing.WritesEnabled(ctx)
	}
	if err != nil {
		s.reportError(w, r, err)
		return
	}
	// Open ones first; each group keeps its newest-first order.
	slices.SortStableFunc(incidents, func(a, b fix.Incident) int {
		return cmp.Compare(boolRank(a.AcknowledgedAt != nil), boolRank(b.AcknowledgedAt != nil))
	})
	data.Incidents = incidents
	s.render(w, 200, "incidents.html", data)
}

func boolRank(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s *Server) acknowledgeIncident(w http.ResponseWriter, r *http.Request) {
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
	err = s.opts.Fixing.Acknowledge(ctx, id, actor)
	switch {
	case err == nil:
		http.Redirect(w, r, "/incidents", http.StatusSeeOther)
	case errors.Is(err, fix.ErrNoIncident):
		s.refuse(w, r, http.StatusConflict, pageData{Title: "Incidents", Error: fmt.Sprintf("Incident %d is still being restored or already acknowledged.", id), BackURL: "/incidents", BackText: "Back to the incidents"})
	default:
		s.serverError(w, r, err)
	}
}

// retryIncident queues an immediate retry of the writer's recovery, for an
// incident whose operation is still restoring. Which incident doesn't
// matter: recovery finishes every unfinished operation.
func (s *Server) retryIncident(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), reportPageBudget)
	defer cancel()
	if _, err := strictForm(r, ""); err != nil {
		s.reportError(w, r, err)
		return
	}
	if _, err := positiveID(r.PathValue("id")); err != nil {
		s.reportError(w, r, report.ErrInvalidFilter)
		return
	}
	refuse := func(msg string) {
		s.refuse(w, r, http.StatusConflict, pageData{Title: "Incidents", Error: msg, BackURL: "/incidents", BackText: "Back to the incidents"})
	}
	err := s.opts.Fixing.RetryRecovery(ctx)
	switch {
	case err == nil:
		http.Redirect(w, r, "/incidents", http.StatusSeeOther)
	case errors.Is(err, fix.ErrBusy):
		refuse("The restore can’t be retried while an apply is running. Try again when it has finished.")
	case errors.Is(err, fix.ErrWritesOff):
		refuse("Writes are off. Restoring continues once writes are on.")
	default:
		s.serverError(w, r, err)
	}
}

func (s *Server) settingsPage(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), reportPageBudget)
	defer cancel()
	r = r.WithContext(ctx)
	if _, err := strictQuery(r); err != nil {
		s.reportError(w, r, err)
		return
	}
	data := pageData{Title: "Settings", SignedIn: true, CSRFToken: sessionFrom(ctx).CSRFToken, Sources: s.sources()}
	var err error
	if data.WritesOn, err = s.opts.Fixing.WritesEnabled(ctx); err == nil {
		data.SettingChanges, err = s.opts.Fixing.SettingChanges(ctx, settingsLogShown)
	}
	if err == nil {
		data.Paused, err = s.opts.Fixing.Paused(ctx)
	}
	if err == nil {
		data.WritesOn, err = s.opts.Fixing.WritesEnabled(ctx)
	}
	if err == nil {
		err = s.agentData(ctx, &data)
	}
	data.RetainRuns = s.opts.RetainRuns
	if err == nil && s.opts.Schedule != nil {
		data.Schedule, err = s.opts.Schedule.Status(ctx)
	}
	if err != nil {
		s.reportError(w, r, err)
		return
	}
	s.render(w, 200, "settings.html", data)
}

// setWrites records a change of the "Allow changes to Calibre" switch.
func (s *Server) setWrites(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), reportPageBudget)
	defer cancel()
	form, err := strictForm(r, "", "value")
	if err != nil {
		s.reportError(w, r, err)
		return
	}
	var on bool
	switch form.Get("value") {
	case "on":
		on = true
	case "off":
	default:
		s.reportError(w, r, report.ErrInvalidFilter)
		return
	}
	if err := s.opts.Fixing.SetWritesEnabled(ctx, on, actor); err != nil {
		s.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

// sources is Settings' source list, with each source's note appended.
func (s *Server) sources() []SourceStatus {
	if s.opts.SourceNotes == nil {
		return s.opts.Sources
	}
	notes := s.opts.SourceNotes()
	out := make([]SourceStatus, len(s.opts.Sources))
	for i, src := range s.opts.Sources {
		out[i] = src
		if n := notes[src.Name]; n != "" {
			out[i].State += "; " + n
		}
	}
	return out
}
