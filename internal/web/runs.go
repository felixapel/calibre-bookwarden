package web

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/duplicates"
	"github.com/felixapel/calibre-bookwarden/internal/findings"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/report"
	"github.com/felixapel/calibre-bookwarden/internal/schedule"
	"github.com/felixapel/calibre-bookwarden/internal/store"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

// Runs starts, cancels and lists library checks; *verify.Runner implements it.
type Runs interface {
	Start(ctx context.Context, scope verify.Scope) (verify.Run, error)
	Cancel(ctx context.Context, runID int64) error
	// SetPinned keeps a run whatever retention says, or stops keeping it.
	SetPinned(ctx context.Context, runID int64, pinned bool) error
	List(ctx context.Context, limit int) ([]verify.RunView, error)
	View(ctx context.Context, runID int64) (verify.RunView, error)
	Calibre() verify.CalibreStatus
}

// Ledger is the run ledger (migration 0024); *store.RunRepo implements it.
type Ledger interface {
	BurnIn(ctx context.Context) (store.BurnIn, error)
}

// Schedule is what the Settings page needs from the scheduler (ADR-039).
type Schedule interface {
	Status(ctx context.Context) (schedule.Status, error)
}

// runsShown is how many runs the Runs page lists.
const runsShown = 10

var templateFuncs = map[string]any{
	"display":               report.Display,
	"duplicateKind":         duplicateKind,
	"duplicateKinds":        duplicates.Kinds,
	"duplicatesUnavailable": duplicatesUnavailable,
	"tierReason":            decide.ReasonLabel,
	"tiers":                 func() []string { return []string{"A", "B", "C"} },
	"decidedFilters":        func() []string { return []string{report.DecidedAgent, report.DecidedPerson, report.DecidedDisagree} },
	"fixSorts":              func() []string { return []string{report.SortField, report.SortTier, report.SortDecided} },
	"fixStatus":             fixStatus,
	"valueText":             valueText,
	"recordField":           recordField,
	"changeFields":          changeFields,
	"opKind": func(kind string) string {
		if kind == "undo" {
			return "Undo"
		}
		return "Apply"
	},
	"reasonText": reasonText,
	"anyPickable": func(vs []FixView) bool {
		for _, v := range vs {
			if v.Pickable {
				return true
			}
		}
		return false
	},
	// displayPtr shows an optional value; nil and empty are both "".
	"displayPtr": func(s *string) string {
		if s == nil {
			return ""
		}
		return report.Display(*s)
	},
	"findingReason": findings.ReasonLabel,
	"findingLabel": func(code string) string {
		for _, d := range findings.Catalog() {
			if d.Code == code {
				return d.Label
			}
		}
		return code
	},
	"statusText": func(s verify.RunStatus) string {
		switch s {
		case verify.RunRunning:
			return "Running"
		case verify.RunWaiting:
			return "Waiting for Calibre"
		case verify.RunCompleted:
			return "Completed"
		case verify.RunCancelled:
			return "Cancelled"
		case verify.RunFailed:
			return "Failed"
		}
		return string(s)
	},
	"calibreText": func(state string) string {
		switch state {
		case "ok":
			return "Connected"
		case "unknown":
			return "Not checked yet"
		case "unreachable":
			return "Unreachable"
		case "incompatible":
			return "Incompatible Calibre version"
		case "auth_failed":
			return "Calibre refused the user or password"
		case "forbidden":
			return "The Calibre account may not read the library"
		}
		return "Error"
	},
	// index0 is the first run, or nil, so a template can use with/else.
	"index0": func(runs []verify.RunView) *verify.RunView {
		if len(runs) == 0 {
			return nil
		}
		return &runs[0]
	},
	"deref": func(t *time.Time) time.Time { return *t },
	// runKind says how a run was started and whether it is kept: any of
	// "incremental", "scheduled" and "pinned".
	"runKind": func(r verify.Run) string {
		var parts []string
		if r.Scope.Incremental {
			parts = append(parts, "incremental")
		}
		if !r.Scope.ScheduledFor.IsZero() {
			parts = append(parts, "scheduled")
		}
		if r.Pinned {
			parts = append(parts, "pinned")
		}
		return strings.Join(parts, ", ")
	},
	// localWhen shows a time in its own zone, which the scheduler's times
	// carry.
	"localWhen": func(t time.Time) string {
		if t.IsZero() {
			return ""
		}
		return t.Format("2006-01-02 15:04 MST")
	},
	// clock shows a duration as h:mm:ss, or m:ss under an hour.
	"clock": func(d time.Duration) string {
		d = d.Round(time.Second)
		h, m, sec := int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60
		if h > 0 {
			return fmt.Sprintf("%d:%02d:%02d", h, m, sec)
		}
		return fmt.Sprintf("%d:%02d", m, sec)
	},
	// tenths shows an integer number of tenths as a decimal.
	"tenths": func(n int) string { return fmt.Sprintf("%d.%d", n/10, n%10) },
	"when": func(t time.Time) string {
		if t.IsZero() {
			return ""
		}
		return t.UTC().Format("2006-01-02 15:04 UTC")
	},
}

func (s *Server) runsPage(w http.ResponseWriter, r *http.Request) {
	data, err := s.runsData(r)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, http.StatusOK, "runs.html", data)
}

func (s *Server) runsData(r *http.Request) (pageData, error) {
	runs, err := s.opts.Runs.List(r.Context(), runsShown)
	if err != nil {
		return pageData{}, err
	}
	var burnIn *store.BurnIn
	if s.opts.Ledger != nil {
		// The ledger is a side panel: if it can't be read, the runs still show.
		if b, err := s.opts.Ledger.BurnIn(r.Context()); err != nil {
			s.opts.Logger.Warn("burn-in panel hidden: the run ledger could not be read", "error", err)
		} else {
			burnIn = &b
		}
	}
	return pageData{
		Title: "Runs", SignedIn: true, CSRFToken: sessionFrom(r.Context()).CSRFToken,
		Calibre: s.opts.Runs.Calibre(), Runs: runs, RetainRuns: s.opts.RetainRuns, BurnIn: burnIn,
	}, nil
}

func (s *Server) startRun(w http.ResponseWriter, r *http.Request) {
	var scope verify.Scope
	if v := strings.TrimSpace(r.PostFormValue("book_id")); v != "" {
		id, err := strconv.Atoi(v)
		if err != nil || id < 1 {
			http.Error(w, "The book id must be a whole number above 0.", http.StatusBadRequest)
			return
		}
		scope.BookID = id
	}
	switch modes := r.PostForm["mode"]; {
	case len(modes) == 0, len(modes) == 1 && modes[0] == "full":
	case len(modes) == 1 && modes[0] == "incremental":
		scope.Incremental = true
	default:
		http.Error(w, "The mode must be full or incremental.", http.StatusBadRequest)
		return
	}
	run, err := s.opts.Runs.Start(r.Context(), scope)
	if err == nil {
		http.Redirect(w, r, "/runs/"+strconv.FormatInt(run.ID, 10), http.StatusSeeOther)
		return
	}
	data, listErr := s.runsData(r)
	if listErr != nil {
		s.serverError(w, r, listErr)
		return
	}
	switch {
	case errors.Is(err, verify.ErrRunActive):
		for _, v := range data.Runs {
			if !v.Run.Status.Terminal() {
				data.ActiveRun = v.Run.ID
				break
			}
		}
		data.Error = "A check is already running."
		s.render(w, http.StatusConflict, "runs.html", data)
	case errors.Is(err, verify.ErrCalibreNotReady):
		data.Error = "Calibre isn't ready: " + data.Calibre.Message
		s.render(w, http.StatusServiceUnavailable, "runs.html", data)
	case errors.Is(err, library.ErrBookNotFound):
		data.Error = fmt.Sprintf("Calibre has no book with id %d.", scope.BookID)
		s.render(w, http.StatusNotFound, "runs.html", data)
	default:
		s.serverError(w, r, err)
	}
}

// runFor reads the {id} path value and the run; it answers 404 itself.
func (s *Server) runFor(w http.ResponseWriter, r *http.Request) (verify.RunView, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return verify.RunView{}, false
	}
	v, err := s.opts.Runs.View(r.Context(), id)
	if errors.Is(err, verify.ErrNoSuchRun) {
		http.NotFound(w, r)
		return verify.RunView{}, false
	}
	if err != nil {
		s.serverError(w, r, err)
		return verify.RunView{}, false
	}
	return v, true
}

func (s *Server) runPage(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), reportPageBudget)
	defer cancel()
	r = r.WithContext(ctx)
	q, err := strictQuery(r, "after_book")
	if err != nil {
		s.reportError(w, r, err)
		return
	}
	after := 0
	if q.Has("after_book") {
		id, err := positiveID(q.Get("after_book"))
		if err != nil || id > 2147483647 {
			s.reportError(w, r, report.ErrInvalidFilter)
			return
		}
		after = int(id)
	}
	v, err := s.reportRun(r)
	if err != nil {
		s.reportError(w, r, err)
		return
	}
	books, err := s.opts.Reports.Books(ctx, v.Run.ID, after)
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		s.reportError(w, r, err)
		return
	}
	data := pageData{
		Title: fmt.Sprintf("Run %d", v.Run.ID), SignedIn: true,
		CSRFToken: sessionFrom(r.Context()).CSRFToken, Run: v, BookPage: books,
	}
	if books.NextBookID != 0 {
		data.NextURL = fmt.Sprintf("/runs/%d?after_book=%d", v.Run.ID, books.NextBookID)
	}
	data.TimeSpent = timeSpentOf(v.Run.Stats)
	if err := s.agentSummary(r.Context(), v, &data); err != nil {
		s.reportError(w, r, err)
		return
	}
	s.render(w, http.StatusOK, "run.html", data)
}

// htmxStopPolling is the status that makes htmx stop polling after it
// swaps the answer in.
const htmxStopPolling = 286

func (s *Server) runProgress(w http.ResponseWriter, r *http.Request) {
	v, ok := s.runFor(w, r)
	if !ok {
		return
	}
	// Only the counts are swapped, into the page's live region; the Cancel
	// button, and the keyboard focus on it, stay where they are. When the
	// run has ended, 286 tells htmx to stop polling and the Cancel form is
	// removed out of band.
	var buf bytes.Buffer
	data := pageData{CSRFToken: sessionFrom(r.Context()).CSRFToken, Run: v}
	err := s.fragment.ExecuteTemplate(&buf, "progress-counts", data)
	status := http.StatusOK
	if err == nil && v.Run.Status.Terminal() {
		err = s.fragment.ExecuteTemplate(&buf, "progress-end", data)
		status = htmxStopPolling
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

// pinRun pins a run, so that retention keeps it, or unpins it. The baseline of
// a rule-change measurement (bookwarden eval) is the case for it.
func (s *Server) pinRun(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		http.NotFound(w, r)
		return
	}
	var pinned bool
	switch values := r.PostForm["value"]; {
	case len(values) == 1 && values[0] == "on":
		pinned = true
	case len(values) == 1 && values[0] == "off":
	default:
		http.Error(w, "The value must be on or off.", http.StatusBadRequest)
		return
	}
	err = s.opts.Runs.SetPinned(r.Context(), id, pinned)
	if errors.Is(err, verify.ErrNoSuchRun) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/runs/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

func (s *Server) cancelRun(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	err = s.opts.Runs.Cancel(r.Context(), id)
	if errors.Is(err, verify.ErrNoSuchRun) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/runs/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// timeSpent is a run's stats as the run page shows them (B5).
type timeSpent struct {
	Stages    []stageTime
	Lookups   []sourceLookups
	Extractor verify.ExtractorCounts
}

type stageTime struct{ Label, Time string }

type sourceLookups struct{ Source, Counts string }

// stageLabels names the stages for the page.
var stageLabels = map[string]string{
	verify.StageCalibre:       "Reading Calibre records",
	verify.StageExportInspect: "Downloading and inspecting files",
	verify.StageOCR:           "OCR and text layers",
	verify.StageLookups:       "Catalogue lookups",
	verify.StageExtraction:    "Model extraction",
	verify.StageStore:         "Storing results",
}

// timeSpentOf returns the table for st, or nil when nothing was counted.
func timeSpentOf(st verify.Stats) *timeSpent {
	if st.Empty() {
		return nil
	}
	t := &timeSpent{Extractor: st.Extractor}
	for _, stage := range verify.Stages {
		d := time.Duration(st.StageSeconds[stage] * float64(time.Second)).Round(time.Second)
		t.Stages = append(t.Stages, stageTime{Label: stageLabels[stage], Time: d.String()})
	}
	for _, src := range slices.Sorted(maps.Keys(st.Lookups)) {
		var parts []string
		for _, outcome := range slices.Sorted(maps.Keys(st.Lookups[src])) {
			parts = append(parts, fmt.Sprintf("%s %d", outcome, st.Lookups[src][outcome]))
		}
		t.Lookups = append(t.Lookups, sourceLookups{Source: src, Counts: strings.Join(parts, " · ")})
	}
	return t
}
