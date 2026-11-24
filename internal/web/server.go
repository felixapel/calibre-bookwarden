// Package web serves bookwarden's pages, rendered on the server.
package web

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/agent"
	"github.com/felixapel/calibre-bookwarden/internal/auth"
	"github.com/felixapel/calibre-bookwarden/internal/findings"
	"github.com/felixapel/calibre-bookwarden/internal/fix"
	"github.com/felixapel/calibre-bookwarden/internal/metrics"
	"github.com/felixapel/calibre-bookwarden/internal/report"
	"github.com/felixapel/calibre-bookwarden/internal/schedule"
	"github.com/felixapel/calibre-bookwarden/internal/store"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

//go:embed templates/*.html static/*
var assets embed.FS

// Options configure the web server.
type Options struct {
	AllowedHosts   []string
	TrustedProxies []netip.Prefix
	Auth           *auth.Service
	Health         func(context.Context) error
	// Ready are the conditions of /readyz; none means it always answers 200.
	Ready []ReadyCheck
	// ReadyTimeout bounds each readiness check; zero means 2 seconds.
	ReadyTimeout time.Duration
	// MetricsToken, when set, turns /metrics on behind "Authorization: Bearer
	// <token>"; Metrics then writes the exposition. Without a token /metrics
	// is a 404.
	MetricsToken string
	Metrics      func(context.Context, *metrics.Writer) error
	Logger       *slog.Logger
	Runs         Runs
	Reports      Reports
	// Excerpts serves the front-matter text on the book page (ADR-032 part
	// 2); nil shows every excerpt as unavailable.
	Excerpts ExcerptReader
	Fixing   Fixing
	// Agents is agent fixing (ADR-029); nil hides it.
	Agents Agents
	// Recall is the recall sample (design section 6.4); nil hides it.
	Recall Recall
	// Schedule is the scheduler (ADR-039); nil shows scheduled checks as off.
	Schedule Schedule
	// Ledger is the run ledger behind the Runs page's burn-in panel; nil
	// hides the panel.
	Ledger Ledger
	// RetainRuns is how many runs retention keeps, 0 for every run; the Runs
	// and Settings pages say so.
	RetainRuns int
	// Sources is the metadata sources' status as Settings shows it.
	Sources []SourceStatus
	// SourceNotes, when set, adds a note to a source's state by name, such
	// as a failed probe.
	SourceNotes func() map[string]string

	// AuthWait is how long setup and login wait for a free hashing slot
	// before answering 503. Zero means 5 seconds.
	AuthWait time.Duration
}

// maxConcurrentHashes caps argon2id work, because each hash needs 64 MiB.
const maxConcurrentHashes = 2

// Server holds the parsed pages and dependencies.
type Server struct {
	opts             Options
	pages            map[string]*template.Template
	fragment         *template.Template // the progress fragment, without the layout
	static           fs.FS
	authSlots        chan struct{}
	exportSlots      chan struct{}
	createExportFile func(string) (io.WriteCloser, error)
	setupDone        atomic.Bool
}

type pageData struct {
	Title     string
	Error     string
	Notice    string
	CSRFToken string
	SignedIn  bool
	Username  string

	// Excerpts holds the book page's excerpt of each format by format name.
	Excerpts map[string]*excerptView

	Calibre     verify.CalibreStatus
	Runs        []verify.RunView
	Run         verify.RunView
	ActiveRun   int64 // the run that blocked a start
	Book        report.Book
	BookPage    report.BooksPage
	FindingPage report.Page
	FixPage     report.FixPage
	FixFilter   report.FixFilter
	FixFields   []string
	Filter      report.Filter
	Catalog     []findings.Definition
	NextURL     string
	PrevURL     string
	CSVURL      string

	Duplicates    report.DuplicatePage
	DuplicateKind string

	Cursor        string // the current page's cursor, for forms that come back to it
	FixViews      []FixView
	ApprovedCount int
	Paused        bool
	Problems      []string
	BackURL       string
	BackText      string

	Apply      fix.ApplyStatus
	Operations []fix.Operation
	Operation  fix.Operation
	Events     []fix.Event
	Undoable   bool
	Incidents  []fix.Incident

	WritesOn       bool
	Schedule       schedule.Status
	BurnIn         *store.BurnIn
	RetainRuns     int
	SettingChanges []fix.SettingChange
	Sources        []SourceStatus

	AgentsOn       bool // agent fixing is wired in
	AgentMode      string
	AgentSeats     []agent.SeatStatus
	AgentMaxTokens int64
	AgentSummary   agent.RunSummary
	// TimeSpent is the run page's "Where the time went" table; nil hides it.
	TimeSpent *timeSpent
	// FieldDecisions says who decided each field of an operation, when the
	// agent decided one of them.
	FieldDecisions []fix.ProposalRow
	FixHidden      int    // rows of the page the decided filter hides
	Reveal         bool   // the page shows the agents' verdicts on undecided rows
	RevealAsked    bool   // ... because the URL asked for them
	RevealURL      string // the link that shows or hides them
	Precision      []PrecisionRow
	Muting         []MuteRow
	MutedURL       string // the link to the muted filter, or back from it

	Sample     *SampleView     // the recall sample page, nil with no sample yet
	SampleBook *SampleBookView // one blind page of the recall sample
}

// SourceStatus is one metadata source and whether it is on, with why not.
type SourceStatus struct{ Name, State string }

// New parses the embedded templates and checks the options.
func New(opts Options) (*Server, error) {
	if opts.Auth == nil || opts.Health == nil || opts.Logger == nil || opts.Runs == nil || opts.Reports == nil || opts.Fixing == nil || len(opts.AllowedHosts) == 0 {
		return nil, errors.New("web: Auth, Health, Logger, Runs, Reports, Fixing and AllowedHosts are required")
	}
	if opts.MetricsToken != "" && opts.Metrics == nil {
		return nil, errors.New("web: MetricsToken needs Metrics")
	}
	pages := map[string]*template.Template{}
	for _, name := range []string{"setup.html", "login.html", "overview.html", "runs.html", "run.html", "findings.html", "fixes.html", "precision.html", "sample.html", "sample_book.html", "book.html", "duplicates.html", "refused.html", "history.html", "operation.html", "incidents.html", "settings.html"} {
		t, err := template.New(name).Funcs(templateFuncs).ParseFS(assets, "templates/layout.html", "templates/progress.html", "templates/"+name)
		if err != nil {
			return nil, fmt.Errorf("web: parse %s: %w", name, err)
		}
		pages[name] = t
	}
	fragment, err := template.New("progress.html").Funcs(templateFuncs).ParseFS(assets, "templates/progress.html")
	if err != nil {
		return nil, fmt.Errorf("web: parse progress.html: %w", err)
	}
	static, err := fs.Sub(assets, "static")
	if err != nil {
		return nil, fmt.Errorf("web: static files: %w", err)
	}
	if opts.AuthWait <= 0 {
		opts.AuthWait = 5 * time.Second
	}
	return &Server{
		opts:        opts,
		pages:       pages,
		fragment:    fragment,
		static:      static,
		authSlots:   make(chan struct{}, maxConcurrentHashes),
		exportSlots: make(chan struct{}, 1),
		createExportFile: func(path string) (io.WriteCloser, error) {
			return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600) //nolint:gosec // server-created 0700 directory and constant filename, never request input
		},
	}, nil
}

// Handler returns the complete handler chain.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /readyz", s.readyz)
	if s.opts.MetricsToken != "" {
		mux.HandleFunc("GET /metrics", s.serveMetrics)
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(s.static)))
	mux.HandleFunc("GET /setup", s.setupForm)
	mux.HandleFunc("POST /setup", s.setupOpen(s.limitAuthWork(s.setupSubmit)))
	mux.HandleFunc("GET /login", s.loginForm)
	mux.HandleFunc("POST /login", s.limitAuthWork(s.loginSubmit))
	mux.Handle("POST /logout", s.requireSession(http.HandlerFunc(s.logout)))
	mux.Handle("GET /{$}", s.requireSession(http.HandlerFunc(s.overview)))
	mux.Handle("GET /runs", s.requireSession(http.HandlerFunc(s.runsPage)))
	mux.Handle("POST /runs", s.requireSession(http.HandlerFunc(s.startRun)))
	mux.Handle("GET /runs/{id}", s.requireSession(http.HandlerFunc(s.runPage)))
	mux.Handle("GET /runs/{id}/progress", s.requireSession(http.HandlerFunc(s.runProgress)))
	mux.Handle("POST /runs/{id}/cancel", s.requireSession(http.HandlerFunc(s.cancelRun)))
	mux.Handle("POST /runs/{id}/pin", s.requireSession(http.HandlerFunc(s.pinRun)))
	mux.Handle("GET /findings", s.requireSession(http.HandlerFunc(s.findingsLatest)))
	mux.Handle("GET /runs/{id}/findings", s.requireSession(http.HandlerFunc(s.findingsPage)))
	mux.Handle("GET /fixes", s.requireSession(http.HandlerFunc(s.fixesLatest)))
	mux.Handle("GET /precision", s.requireSession(http.HandlerFunc(s.precisionPage)))
	mux.Handle("POST /precision/unmute", s.requireSession(http.HandlerFunc(s.unmuteType)))
	mux.Handle("GET /sample", s.requireSession(http.HandlerFunc(s.samplePage)))
	mux.Handle("POST /sample", s.requireSession(http.HandlerFunc(s.startSample)))
	mux.Handle("GET /sample/{id}/book/{n}", s.requireSession(http.HandlerFunc(s.sampleBookPage)))
	mux.Handle("POST /sample/{id}/book/{n}", s.requireSession(http.HandlerFunc(s.confirmSample)))
	mux.Handle("GET /runs/{id}/fixes", s.requireSession(http.HandlerFunc(s.fixesPage)))
	mux.Handle("POST /runs/{id}/fixes/decide", s.requireSession(http.HandlerFunc(s.decideFixes)))
	mux.Handle("POST /runs/{id}/fixes/approve-all", s.requireSession(http.HandlerFunc(s.approveAll)))
	mux.Handle("POST /fixes/apply", s.requireSession(http.HandlerFunc(s.applyFixes)))
	mux.Handle("GET /history", s.requireSession(http.HandlerFunc(s.historyPage)))
	mux.Handle("GET /history/{id}", s.requireSession(http.HandlerFunc(s.operationPage)))
	mux.Handle("POST /history/{id}/undo", s.requireSession(http.HandlerFunc(s.undoOperation)))
	mux.Handle("GET /incidents", s.requireSession(http.HandlerFunc(s.incidentsPage)))
	mux.Handle("POST /incidents/{id}/acknowledge", s.requireSession(http.HandlerFunc(s.acknowledgeIncident)))
	mux.Handle("POST /incidents/{id}/retry", s.requireSession(http.HandlerFunc(s.retryIncident)))
	mux.Handle("GET /settings", s.requireSession(http.HandlerFunc(s.settingsPage)))
	mux.Handle("POST /settings/writes", s.requireSession(http.HandlerFunc(s.setWrites)))
	mux.Handle("POST /settings/agent", s.requireSession(http.HandlerFunc(s.setAgentMode)))
	mux.Handle("POST /runs/{id}/fixes/review", s.requireSession(http.HandlerFunc(s.reviewRun)))
	mux.Handle("GET /duplicates", s.requireSession(http.HandlerFunc(s.duplicatesLatest)))
	mux.Handle("GET /runs/{id}/duplicates", s.requireSession(http.HandlerFunc(s.duplicatesPage)))
	mux.Handle("GET /runs/{id}/books/{book}", s.requireSession(http.HandlerFunc(s.bookPage)))
	mux.Handle("GET /runs/{id}/findings.csv", s.requireSession(http.HandlerFunc(s.findingsCSV)))
	mux.Handle("POST /runs/{id}/books/{book}/recheck", s.requireSession(http.HandlerFunc(s.recheckBook)))

	// Innermost first: a request passes these from the bottom up.
	var h http.Handler = mux
	h = s.setupGate(h)
	h = http.NewCrossOriginProtection().Handler(h)
	h = limitBody(h)
	h = requireAllowedHost(s.opts.AllowedHosts, h)
	h = securityHeaders(h)
	h = logRequests(s.opts.Logger, h)
	h = withClient(s.opts.TrustedProxies, h)
	return recoverPanics(s.opts.Logger, h)
}

// setupComplete reports whether the admin exists, and remembers a yes.
func (s *Server) setupComplete(ctx context.Context) (bool, error) {
	if s.setupDone.Load() {
		return true, nil
	}
	needed, err := s.opts.Auth.SetupNeeded(ctx)
	if err != nil {
		return false, err
	}
	if !needed {
		s.setupDone.Store(true)
	}
	return !needed, nil
}

// setupGate sends every page to /setup until the admin exists.
func (s *Server) setupGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if p == "/setup" || isProbePath(p) || strings.HasPrefix(p, "/static/") {
			next.ServeHTTP(w, r)
			return
		}
		done, err := s.setupComplete(r.Context())
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		if !done {
			http.Redirect(w, r, "/setup", http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// setupOpen sends setup submissions to /login once the admin exists, before
// they can take a hashing slot.
func (s *Server) setupOpen(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		done, err := s.setupComplete(r.Context())
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		if done {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

// limitAuthWork lets at most maxConcurrentHashes setup or login requests run
// at once. The rest wait up to AuthWait, then get 503. Past the login
// limiter's global budget every request first waits LoginDelay, before it
// takes a slot; a client that leaves meanwhile gets nothing.
func (s *Server) limitAuthWork(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d := s.opts.Auth.LoginDelay(); d > 0 {
			t := time.NewTimer(d)
			select {
			case <-r.Context().Done():
				t.Stop()
				return
			case <-t.C:
			}
		}
		ctx, cancel := context.WithTimeout(r.Context(), s.opts.AuthWait)
		defer cancel()
		select {
		case s.authSlots <- struct{}{}:
		case <-ctx.Done():
			w.Header().Set("Retry-After", "1")
			http.Error(w, "Bookwarden is busy. Try again in a moment.", http.StatusServiceUnavailable)
			return
		}
		defer func() { <-s.authSlots }()
		next(w, r)
	}
}

func (s *Server) render(w http.ResponseWriter, status int, page string, data pageData) {
	var buf bytes.Buffer
	if err := s.pages[page].ExecuteTemplate(&buf, "layout", data); err != nil {
		s.opts.Logger.Error("render page", "page", page, "error", err)
		http.Error(w, "Something went wrong. Check the bookwarden log.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

func (s *Server) serverError(w http.ResponseWriter, r *http.Request, err error) {
	s.opts.Logger.Error("request failed", "method", r.Method, "path", r.URL.Path, "error", err)
	http.Error(w, "Something went wrong. Check the bookwarden log.", http.StatusInternalServerError)
}
