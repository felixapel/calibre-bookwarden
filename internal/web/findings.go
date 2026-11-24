package web

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/findings"
	"github.com/felixapel/calibre-bookwarden/internal/report"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

type Reports interface {
	Book(context.Context, int64, int) (report.Book, error)
	Books(context.Context, int64, int) (report.BooksPage, error)
	Findings(context.Context, int64, report.Filter, *report.Cursor) (report.Page, error)
	CSV(context.Context, int64, report.Filter, io.Writer, report.CSVLimits) error
	Fixes(context.Context, int64, report.FixFilter, *report.Cursor) (report.FixPage, error)
	Duplicates(context.Context, int64, report.DuplicateFilter) (report.DuplicatePage, error)
	BookDuplicates(context.Context, int64, int) (report.DuplicatePage, error)
}

const reportPageBudget = 15 * time.Second

func positiveID(raw string) (int64, error) {
	if raw == "" {
		return 0, report.ErrInvalidFilter
	}
	for _, c := range raw {
		if c < '0' || c > '9' {
			return 0, report.ErrInvalidFilter
		}
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, report.ErrInvalidFilter
	}
	return id, nil
}
func strictQuery(r *http.Request, allowed ...string) (url.Values, error) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, report.ErrInvalidFilter
	}
	for key, values := range q {
		if len(values) != 1 {
			return nil, report.ErrInvalidFilter
		}
		found := false
		for _, a := range allowed {
			if key == a {
				found = true
				break
			}
		}
		if !found {
			return nil, report.ErrInvalidFilter
		}
	}
	return q, nil
}
func encodeCursor(c report.Cursor) string {
	raw, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(raw)
}
func decodeCursor(token string, runID int64) (*report.Cursor, error) {
	if token == "" || len(token) > 128 {
		return nil, report.ErrInvalidCursor
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil {
		return nil, report.ErrInvalidCursor
	}
	var c report.Cursor
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return nil, report.ErrInvalidCursor
	}
	if c.RunID != runID || c.BookID <= 0 || c.Ordinal < 0 || encodeCursor(c) != token {
		return nil, report.ErrInvalidCursor
	}
	return &c, nil
}
func filterQuery(r *http.Request, runID int64, withCursor bool) (report.Filter, *report.Cursor, error) {
	allowed := []string{"code", "format", "book_id"}
	if withCursor {
		allowed = append(allowed, "cursor")
	}
	q, err := strictQuery(r, allowed...)
	if err != nil {
		return report.Filter{}, nil, err
	}
	f := report.Filter{Code: q.Get("code"), Format: q.Get("format")}
	if raw := q.Get("book_id"); raw != "" {
		id, err := positiveID(raw)
		if err != nil || id > 2147483647 {
			return f, nil, report.ErrInvalidFilter
		}
		f.BookID = int(id)
	}
	if err := report.ValidateFilter(f); err != nil {
		return f, nil, err
	}
	var c *report.Cursor
	if q.Has("cursor") {
		c, err = decodeCursor(q.Get("cursor"), runID)
		if err != nil {
			return f, nil, err
		}
		if f.BookID != 0 && f.BookID != c.BookID {
			return f, nil, report.ErrInvalidCursor
		}
	}
	return f, c, nil
}
func filterValues(f report.Filter) url.Values {
	q := url.Values{}
	if f.Code != "" {
		q.Set("code", f.Code)
	}
	if f.Format != "" {
		q.Set("format", f.Format)
	}
	if f.BookID != 0 {
		q.Set("book_id", strconv.Itoa(f.BookID))
	}
	return q
}

// reportError answers a failed report call. The expected errors get their own
// status; anything else is logged with its cause, the method and the path
// (never the query or a form value) before the generic page goes out.
func (s *Server) reportError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, verify.ErrNoSuchRun), errors.Is(err, report.ErrNoSuchBook):
		http.Error(w, "Run or book not found.", http.StatusNotFound)
	case errors.Is(err, report.ErrInvalidCursor), errors.Is(err, report.ErrInvalidFilter):
		http.Error(w, "Invalid report selection.", http.StatusBadRequest)
	case errors.Is(err, context.DeadlineExceeded):
		http.Error(w, "The report timed out. Narrow the selection and try again.", http.StatusGatewayTimeout)
	case errors.Is(err, context.Canceled):
		http.Error(w, "The report was cancelled.", http.StatusRequestTimeout)
	default:
		s.opts.Logger.Error("report unavailable", "method", r.Method, "path", r.URL.Path, "status", http.StatusInternalServerError, "error", err)
		http.Error(w, "Report unavailable. Try again later.", http.StatusInternalServerError)
	}
}
func (s *Server) reportRun(r *http.Request) (verify.RunView, error) {
	id, err := positiveID(r.PathValue("id"))
	if err != nil {
		return verify.RunView{}, err
	}
	return s.opts.Runs.View(r.Context(), id)
}

// reportRunID picks the run Findings and Fixes open: the latest completed
// whole-library run, else the latest ended run, else 0.
func (s *Server) reportRunID(ctx context.Context) (int64, error) {
	runs, err := s.opts.Runs.List(ctx, runsShown)
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		return 0, err
	}
	for _, v := range runs {
		if v.Run.Status == verify.RunCompleted && v.Run.Scope.BookID == 0 {
			return v.Run.ID, nil
		}
	}
	for _, v := range runs {
		if v.Run.Status.Terminal() {
			return v.Run.ID, nil
		}
	}
	return 0, nil
}

func (s *Server) findingsLatest(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), reportPageBudget)
	defer cancel()
	r = r.WithContext(ctx)
	if _, err := strictQuery(r); err != nil {
		s.reportError(w, r, err)
		return
	}
	chosen, err := s.reportRunID(ctx)
	if err != nil {
		s.reportError(w, r, err)
		return
	}
	if chosen != 0 {
		http.Redirect(w, r, fmt.Sprintf("/runs/%d/findings", chosen), http.StatusSeeOther)
		return
	}
	s.render(w, 200, "findings.html", pageData{Title: "Findings", SignedIn: true, CSRFToken: sessionFrom(ctx).CSRFToken, Notice: "No checks yet."})
}
func (s *Server) findingsPage(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), reportPageBudget)
	defer cancel()
	r = r.WithContext(ctx)
	id, err := positiveID(r.PathValue("id"))
	if err != nil {
		s.reportError(w, r, err)
		return
	}
	f, c, err := filterQuery(r, id, true)
	if err != nil {
		s.reportError(w, r, err)
		return
	}
	v, err := s.opts.Runs.View(ctx, id)
	if err != nil {
		s.reportError(w, r, err)
		return
	}
	p, err := s.opts.Reports.Findings(ctx, id, f, c)
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		s.reportError(w, r, err)
		return
	}
	data := pageData{Title: fmt.Sprintf("Run %d findings", id), SignedIn: true, CSRFToken: sessionFrom(ctx).CSRFToken, Run: v, FindingPage: p, Filter: f, Catalog: findings.Catalog()}
	data.CSVURL = fmt.Sprintf("/runs/%d/findings.csv?%s", id, filterValues(f).Encode())
	if !v.Run.Status.Terminal() {
		data.Notice = "This run is active; these results are partial."
	}
	if p.Next != nil {
		q := filterValues(f)
		q.Set("cursor", encodeCursor(*p.Next))
		data.NextURL = fmt.Sprintf("/runs/%d/findings?%s", id, q.Encode())
	}
	s.render(w, 200, "findings.html", data)
}
func (s *Server) bookPage(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), reportPageBudget)
	defer cancel()
	r = r.WithContext(ctx)
	if _, err := strictQuery(r); err != nil {
		s.reportError(w, r, err)
		return
	}
	v, err := s.reportRun(r)
	if err != nil {
		s.reportError(w, r, err)
		return
	}
	id, err := positiveID(r.PathValue("book"))
	if err != nil || id > 2147483647 {
		s.reportError(w, r, report.ErrInvalidFilter)
		return
	}
	b, err := s.opts.Reports.Book(ctx, v.Run.ID, int(id))
	var dups report.DuplicatePage
	if err == nil {
		dups, err = s.opts.Reports.BookDuplicates(ctx, v.Run.ID, int(id))
	}
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		s.reportError(w, r, err)
		return
	}
	s.render(w, 200, "book.html", pageData{Title: fmt.Sprintf("Book %d · Run %d", id, v.Run.ID), SignedIn: true, CSRFToken: sessionFrom(ctx).CSRFToken, Run: v, Book: b, Duplicates: dups, Excerpts: s.bookExcerpts(ctx, b.Package)})
}
