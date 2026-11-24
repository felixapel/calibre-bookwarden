package web

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/felixapel/calibre-bookwarden/internal/duplicates"
	"github.com/felixapel/calibre-bookwarden/internal/report"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

// duplicateKind names a kind of duplicate group on the pages.
func duplicateKind(kind string) string {
	switch kind {
	case duplicates.KindISBN:
		return "Same ISBN"
	case duplicates.KindFile:
		return "Same file"
	case duplicates.KindTitleAuthor:
		return "Same title and authors"
	case duplicates.KindCover:
		return "Similar cover, a hint only"
	}
	return kind
}

// duplicatesUnavailable says why a run has no duplicates to show.
func duplicatesUnavailable(reason string) string {
	switch reason {
	case report.DuplicatesSingleBook:
		return "Duplicates are compared within a whole-library check."
	case report.DuplicatesPredate:
		return "This check predates duplicate detection. Start a new check to compare the library."
	case report.DuplicatesUnknownVersion:
		return "These duplicates were found under rules this version doesn’t know."
	}
	return ""
}

func (s *Server) duplicatesLatest(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), reportPageBudget)
	defer cancel()
	if _, err := strictQuery(r); err != nil {
		s.reportError(w, r, err)
		return
	}
	runs, err := s.opts.Runs.List(ctx, runsShown)
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		s.reportError(w, r, err)
		return
	}
	if chosen := duplicatesRunID(runs); chosen != 0 {
		http.Redirect(w, r, fmt.Sprintf("/runs/%d/duplicates", chosen), http.StatusSeeOther)
		return
	}
	notice := "No checks yet."
	if len(runs) > 0 {
		notice = "No whole-library checks yet."
	}
	s.render(w, 200, "duplicates.html", pageData{Title: "Duplicates", SignedIn: true, CSRFToken: sessionFrom(ctx).CSRFToken, Notice: notice})
}

// duplicatesRunID picks the run Duplicates opens: the latest completed
// whole-library run, else the latest ended one. One-book rechecks have no
// duplicates to show.
func duplicatesRunID(runs []verify.RunView) int64 {
	for _, v := range runs {
		if v.Run.Status == verify.RunCompleted && v.Run.Scope.BookID == 0 {
			return v.Run.ID
		}
	}
	for _, v := range runs {
		if v.Run.Status.Terminal() && v.Run.Scope.BookID == 0 {
			return v.Run.ID
		}
	}
	return 0
}

func duplicatesURL(runID int64, kind string, page int) string {
	q := url.Values{}
	if kind != "" {
		q.Set("kind", kind)
	}
	q.Set("page", strconv.Itoa(page))
	return fmt.Sprintf("/runs/%d/duplicates?%s", runID, q.Encode())
}

func (s *Server) duplicatesPage(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), reportPageBudget)
	defer cancel()
	r = r.WithContext(ctx)
	id, err := positiveID(r.PathValue("id"))
	if err != nil {
		s.reportError(w, r, err)
		return
	}
	q, err := strictQuery(r, "kind", "page")
	if err != nil {
		s.reportError(w, r, err)
		return
	}
	f := report.DuplicateFilter{Kind: q.Get("kind"), Page: 1}
	if raw := q.Get("page"); raw != "" {
		n, err := positiveID(raw)
		if err != nil || n > 10000 {
			s.reportError(w, r, report.ErrInvalidFilter)
			return
		}
		f.Page = int(n)
	}
	if err := report.ValidateDuplicateFilter(f); err != nil {
		s.reportError(w, r, err)
		return
	}
	v, err := s.opts.Runs.View(ctx, id)
	if err != nil {
		s.reportError(w, r, err)
		return
	}
	p, err := s.opts.Reports.Duplicates(ctx, id, f)
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		s.reportError(w, r, err)
		return
	}
	data := pageData{Title: fmt.Sprintf("Run %d duplicates", id), SignedIn: true, CSRFToken: sessionFrom(ctx).CSRFToken, Run: v, Duplicates: p, DuplicateKind: f.Kind}
	data.Notice = duplicatesUnavailable(p.Unavailable)
	if p.Partial {
		data.Notice = "This check didn’t finish; only the books it checked were compared."
	}
	if p.More {
		data.NextURL = duplicatesURL(id, f.Kind, f.Page+1)
	}
	if f.Page > 1 {
		data.PrevURL = duplicatesURL(id, f.Kind, f.Page-1)
	}
	s.render(w, 200, "duplicates.html", data)
}
