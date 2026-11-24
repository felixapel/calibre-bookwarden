package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/report"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

const exportBudget = 20 * time.Second

func (s *Server) findingsCSV(w http.ResponseWriter, r *http.Request) {
	id, err := positiveID(r.PathValue("id"))
	if err != nil {
		s.reportError(w, r, err)
		return
	}
	filter, _, err := filterQuery(r, id, false)
	if err != nil {
		s.reportError(w, r, err)
		return
	}
	select {
	case s.exportSlots <- struct{}{}:
		defer func() { <-s.exportSlots }()
	default:
		w.Header().Set("Retry-After", "1")
		http.Error(w, "Another export is in progress. Try again shortly.", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), exportBudget)
	defer cancel()
	dir, err := os.MkdirTemp(os.TempDir(), "bookwarden-export-*")
	if err != nil {
		s.reportError(w, r, err)
		return
	}
	defer func() { _ = os.RemoveAll(dir) }()
	path := filepath.Join(dir, "findings.csv")
	file, err := s.createExportFile(path)
	if err != nil {
		s.reportError(w, r, err)
		return
	}
	err = s.opts.Reports.CSV(ctx, id, filter, file, report.DefaultCSVLimits())
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		if errors.Is(err, report.ErrExportTooLarge) {
			http.Error(w, "Export too large. Narrow the filters and try again.", http.StatusUnprocessableEntity)
		} else {
			s.reportError(w, r, err)
		}
		return
	}
	ready, err := os.Open(path) //nolint:gosec // same server-created 0700 directory and constant filename
	if err != nil {
		s.reportError(w, r, err)
		return
	}
	defer func() { _ = ready.Close() }()
	stat, err := ready.Stat()
	if err != nil {
		s.reportError(w, r, err)
		return
	}
	if err := ctx.Err(); err != nil {
		s.reportError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="bookwarden-run-%d-findings.csv"`, id))
	w.Header().Set("Content-Length", strconv.FormatInt(stat.Size(), 10))
	w.Header().Set("Cache-Control", "no-store")
	// Staging establishes complete verified content before this point; a later
	// client disconnect remains a transport failure.
	_, _ = io.Copy(w, ready)
}
func (s *Server) recheckBook(w http.ResponseWriter, r *http.Request) {
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
	if err != nil {
		s.reportError(w, r, err)
		return
	}
	if err := ctx.Err(); err != nil {
		s.reportError(w, r, err)
		return
	}
	run, err := s.opts.Runs.Start(ctx, verify.Scope{BookID: b.Ref.ID, ExpectedLibraryID: v.Run.LibraryID, ExpectedUUID: b.Ref.UUID})
	switch {
	case err == nil:
		http.Redirect(w, r, fmt.Sprintf("/runs/%d", run.ID), http.StatusSeeOther)
	case errors.Is(err, verify.ErrRunActive), errors.Is(err, verify.ErrBookChanged):
		http.Error(w, "A check is active or this book’s identity has changed. Check the current library and try again.", http.StatusConflict)
	case errors.Is(err, library.ErrBookNotFound):
		http.Error(w, "This book is no longer in Calibre.", http.StatusNotFound)
	case errors.Is(err, verify.ErrInvalidScope):
		http.Error(w, "Incomplete historical identity.", http.StatusBadRequest)
	case errors.Is(err, verify.ErrCalibreNotReady):
		http.Error(w, "Calibre is not ready. Try again later.", http.StatusServiceUnavailable)
	default:
		s.reportError(w, r, err)
	}
}
