package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/fix"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/report"
)

// historyShown is how many operations one History page lists.
const historyShown = 50

func (s *Server) historyPage(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), reportPageBudget)
	defer cancel()
	r = r.WithContext(ctx)
	q, err := strictQuery(r, "before")
	if err != nil {
		s.reportError(w, r, err)
		return
	}
	var before int64
	if q.Has("before") {
		if before, err = positiveID(q.Get("before")); err != nil {
			s.reportError(w, r, report.ErrInvalidFilter)
			return
		}
	}
	ops, err := s.opts.Fixing.Operations(ctx, before, historyShown+1)
	if err != nil {
		s.reportError(w, r, err)
		return
	}
	data := pageData{Title: "History", SignedIn: true, CSRFToken: sessionFrom(ctx).CSRFToken, Apply: s.opts.Fixing.ApplyStatus()}
	if len(ops) > historyShown {
		ops = ops[:historyShown]
		data.NextURL = fmt.Sprintf("/history?before=%d", ops[historyShown-1].ID)
	}
	data.Operations = ops
	if data.Paused, err = s.opts.Fixing.Paused(ctx); err != nil {
		s.reportError(w, r, err)
		return
	}
	s.render(w, 200, "history.html", data)
}

func (s *Server) operationPage(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), reportPageBudget)
	defer cancel()
	r = r.WithContext(ctx)
	if _, err := strictQuery(r); err != nil {
		s.reportError(w, r, err)
		return
	}
	id, err := positiveID(r.PathValue("id"))
	if err != nil {
		s.reportError(w, r, report.ErrInvalidFilter)
		return
	}
	op, err := s.opts.Fixing.Operation(ctx, id)
	if errors.Is(err, fix.ErrNoOperation) {
		http.Error(w, "Operation not found.", http.StatusNotFound)
		return
	}
	if err != nil {
		s.reportError(w, r, err)
		return
	}
	data := pageData{Title: fmt.Sprintf("Operation %d", id), SignedIn: true, CSRFToken: sessionFrom(ctx).CSRFToken, Operation: op}
	if data.Events, err = s.opts.Fixing.Events(ctx, id); err == nil {
		data.Undoable, err = s.opts.Fixing.Undoable(ctx, id)
	}
	var incidents []fix.Incident
	if err == nil {
		incidents, err = s.opts.Fixing.Incidents(ctx)
	}
	if err == nil {
		data.Paused, err = s.opts.Fixing.Paused(ctx)
	}
	if err == nil && op.RunID != nil && s.opts.Agents != nil {
		data.FieldDecisions, err = s.agentDecided(ctx, *op.RunID, id)
	}
	if err != nil {
		s.reportError(w, r, err)
		return
	}
	for _, in := range incidents {
		if in.OperationID == id {
			data.Incidents = append(data.Incidents, in)
		}
	}
	s.render(w, 200, "operation.html", data)
}

// undoOperation runs Undo for one book and waits for it (spec §8.4).
func (s *Server) undoOperation(w http.ResponseWriter, r *http.Request) {
	if _, err := strictForm(r, ""); err != nil {
		s.reportError(w, r, err)
		return
	}
	id, err := positiveID(r.PathValue("id"))
	if err != nil {
		s.reportError(w, r, report.ErrInvalidFilter)
		return
	}
	undo, err := s.opts.Fixing.Undo(r.Context(), id, actor)
	back := fmt.Sprintf("/history/%d", id)
	refuse := func(status int, msg, url, text string) {
		s.refuse(w, r, status, pageData{Title: "Undo", Error: msg, BackURL: url, BackText: text})
	}
	switch {
	case err == nil:
		http.Redirect(w, r, fmt.Sprintf("/history/%d", undo), http.StatusSeeOther)
	case errors.Is(err, fix.ErrChanged):
		refuse(http.StatusConflict, "The book changed in Calibre since this operation, or is gone, so undoing it could overwrite a newer edit.", back, "Back to the operation")
	case errors.Is(err, fix.ErrNotUndoable):
		refuse(http.StatusConflict, "This operation can’t be undone: it didn’t complete, or it is undone already.", back, "Back to the operation")
	case errors.Is(err, fix.ErrBlocked):
		refuse(http.StatusConflict, "Calibre can’t store the old values exactly, or writing them would rename a name other books share.", back, "Back to the operation")
	case errors.Is(err, fix.ErrPaused) && undo != 0:
		refuse(http.StatusConflict, "The undo didn’t complete. Writes are paused until its incident is acknowledged.", "/incidents", "See the incidents")
	case errors.Is(err, fix.ErrPaused):
		refuse(http.StatusConflict, "Writes are paused until an incident is acknowledged.", "/incidents", "See the incidents")
	case errors.Is(err, fix.ErrWritesOff):
		refuse(http.StatusConflict, "Changes to Calibre are switched off.", "/settings", "Turn them on in Settings")
	case errors.Is(err, fix.ErrBusy):
		refuse(http.StatusConflict, "Another write is running. Try again when it has finished.", back, "Back to the operation")
	case errors.Is(err, fix.ErrRefused):
		refuse(http.StatusConflict, "Calibre refused the write. The Calibre account needs write access (see the README, “Fixing”). Nothing was changed.", back, "Back to the operation")
	case errors.Is(err, library.ErrUnreachable), errors.Is(err, context.DeadlineExceeded):
		refuse(http.StatusServiceUnavailable, "Calibre couldn’t be reached. Try again later.", back, "Back to the operation")
	default:
		s.serverError(w, r, err)
	}
}

// valueText shows a value as written to set-fields: a string, a list, a map
// of identifiers or null. Each piece is capped by report.Display.
func valueText(raw json.RawMessage) string {
	if strings.TrimSpace(string(raw)) == "null" {
		return "(unset)"
	}
	var str string
	if json.Unmarshal(raw, &str) == nil {
		return orEmptyText(report.Display(str))
	}
	var list []string
	if json.Unmarshal(raw, &list) == nil {
		return listText(list)
	}
	var m map[string]string
	if json.Unmarshal(raw, &m) == nil {
		return mapText(m)
	}
	return "(unreadable value)"
}

func orEmptyText(s string) string {
	if s == "" {
		return "(empty)"
	}
	return s
}

func listText(list []string) string {
	parts := make([]string, 0, len(list))
	for _, v := range list {
		parts = append(parts, report.Display(v))
	}
	return orEmptyText(strings.Join(parts, "; "))
}

func mapText(m map[string]string) string {
	var parts []string
	for _, k := range slices.Sorted(maps.Keys(m)) {
		parts = append(parts, report.Display(k)+": "+report.Display(m[k]))
	}
	return orEmptyText(strings.Join(parts, "; "))
}

// recordField shows a record's value of a set-fields field.
func recordField(r evidence.Record, field string) string {
	switch field {
	case "title":
		return orEmptyText(report.Display(r.Title))
	case "sort":
		if r.TitleSort == nil {
			return "(not recorded)"
		}
		return orEmptyText(report.Display(*r.TitleSort))
	case "authors":
		return listText(r.Authors)
	case "author_sort":
		return orEmptyText(report.Display(r.AuthorSort))
	case "languages":
		return listText(r.Languages)
	case "identifiers":
		return mapText(r.Identifiers)
	case "publisher":
		return orEmptyText(report.Display(r.Publisher))
	case "pubdate":
		if r.PubDate == "" {
			return "(unset)"
		}
		return report.Display(r.PubDate)
	}
	return ""
}

// changeFields lists an operation's fields.
func changeFields(changes []library.Change) string {
	fields := make([]string, 0, len(changes))
	for _, c := range changes {
		fields = append(fields, c.Field)
	}
	return strings.Join(fields, ", ")
}
