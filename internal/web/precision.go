package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/felixapel/calibre-bookwarden/internal/fix"
	"github.com/felixapel/calibre-bookwarden/internal/report"
	"github.com/felixapel/calibre-bookwarden/internal/store"
)

// PrecisionRow is one fix type on the Precision page.
type PrecisionRow struct {
	Field, Path, Kind string
	Labels, Misses    int
	Bound             string // the one-sided 95 % lower bound on precision
	Progress          string
}

// precisionRows turns the labels per fix type into what the page shows.
func precisionRows(labels []store.TypeLabels) []PrecisionRow {
	rows := make([]PrecisionRow, 0, len(labels))
	for _, l := range labels {
		row := PrecisionRow{Field: l.Field, Path: l.Path, Kind: l.Kind, Labels: l.Labels, Misses: l.Misses, Bound: "no labels yet"}
		if b, ok := l.Lower(); ok {
			row.Bound = fmt.Sprintf("%.2f %%", b*100)
		}
		switch {
		case l.Labels == 0:
			row.Progress = "—"
		case l.Misses == 0 && l.Labels >= store.CertificationLabels:
			row.Progress = fmt.Sprintf("%d with zero misses: target reached", l.Labels)
		case l.Misses == 0:
			row.Progress = fmt.Sprintf("%d / %d with zero misses", l.Labels, store.CertificationLabels)
		default:
			row.Progress = "has a miss; the bar rises"
		}
		rows = append(rows, row)
	}
	return rows
}

// MuteRow is one fix type in the muting table of the Precision page. Muting
// counts every person's decision, unlike the labels (ADR-034).
type MuteRow struct {
	LibraryID, Field, Path, Kind string
	Fixes, Misses                int
	Bound                        string // the one-sided 95 % upper bound on precision
	State                        string
	Muted                        bool
}

// muteRows turns the muting state per fix type into what the page shows.
func muteRows(stats []store.TypeMute) []MuteRow {
	rows := make([]MuteRow, 0, len(stats))
	for _, t := range stats {
		row := MuteRow{LibraryID: t.LibraryID, Field: t.Field, Path: t.Path, Kind: t.Kind, Fixes: t.Fixes, Misses: t.Misses,
			Bound: fmt.Sprintf("%.1f %%", t.Upper*100), Muted: t.Muted}
		switch {
		case t.Muted && t.Unmute != nil:
			row.State = fmt.Sprintf("Muted %s by the %s, again after the unmute by %s on %s", t.MutedAt.UTC().Format("2006-01-02"), t.MutedBy, t.Unmute.By, t.Unmute.At.UTC().Format("2006-01-02"))
		case t.Muted:
			row.State = fmt.Sprintf("Muted %s by the %s", t.MutedAt.UTC().Format("2006-01-02"), t.MutedBy)
		case t.Held:
			row.State = fmt.Sprintf("Unmuted by %s on %s; %d of %d decisions since", t.Unmute.By, t.Unmute.At.UTC().Format("2006-01-02"), t.Since, store.UnmuteHold)
		case t.Fixes < store.MuteMinFixes:
			row.State = fmt.Sprintf("Shown; needs %d decided fixes", store.MuteMinFixes)
		default:
			row.State = "Shown"
		}
		rows = append(rows, row)
	}
	return rows
}

// precisionPage shows the labels each fix type has earned over all runs, and
// which types are muted.
func (s *Server) precisionPage(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), reportPageBudget)
	defer cancel()
	if _, err := strictQuery(r); err != nil {
		s.reportError(w, r, err)
		return
	}
	labels, err := s.opts.Fixing.Precision(ctx)
	var stats []fix.TypeMute
	if err == nil {
		stats, err = s.opts.Fixing.MuteStats(ctx)
	}
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		s.reportError(w, r, err)
		return
	}
	s.render(w, 200, "precision.html", pageData{Title: "Precision", SignedIn: true, CSRFToken: sessionFrom(ctx).CSRFToken, Precision: precisionRows(labels), Muting: muteRows(stats)})
}

// unmuteType records that the admin shows a muted fix type again (§6.3).
func (s *Server) unmuteType(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), reportPageBudget)
	defer cancel()
	form, err := strictForm(r, "", "library_id", "field", "path", "kind")
	if err != nil {
		s.reportError(w, r, err)
		return
	}
	for _, k := range []string{"library_id", "field", "path", "kind"} {
		if !form.Has(k) {
			s.reportError(w, r, report.ErrInvalidFilter)
			return
		}
	}
	t := fix.MuteType{Field: form.Get("field"), Path: form.Get("path"), Kind: form.Get("kind")}
	err = s.opts.Fixing.Unmute(ctx, form.Get("library_id"), t, actor)
	switch {
	case err == nil:
		http.Redirect(w, r, "/precision", http.StatusSeeOther)
	case errors.Is(err, fix.ErrNotMuted):
		s.refuse(w, r, http.StatusConflict, pageData{Title: "Precision", Error: "That fix type isn’t muted.", BackURL: "/precision", BackText: "Back to Precision"})
	default:
		s.serverError(w, r, err)
	}
}
