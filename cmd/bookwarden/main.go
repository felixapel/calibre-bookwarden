// Command bookwarden checks a Calibre library and applies approved fixes.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"slices"
	"sync"
	"syscall"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/agent"
	"github.com/felixapel/calibre-bookwarden/internal/auth"
	"github.com/felixapel/calibre-bookwarden/internal/buildinfo"
	"github.com/felixapel/calibre-bookwarden/internal/config"
	"github.com/felixapel/calibre-bookwarden/internal/eval"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/extraction"
	"github.com/felixapel/calibre-bookwarden/internal/fix"
	"github.com/felixapel/calibre-bookwarden/internal/heartbeat"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/metrics"
	"github.com/felixapel/calibre-bookwarden/internal/ocr"
	"github.com/felixapel/calibre-bookwarden/internal/parity"
	"github.com/felixapel/calibre-bookwarden/internal/report"
	"github.com/felixapel/calibre-bookwarden/internal/retention"
	"github.com/felixapel/calibre-bookwarden/internal/sandbox"
	"github.com/felixapel/calibre-bookwarden/internal/schedule"
	"github.com/felixapel/calibre-bookwarden/internal/sources"
	"github.com/felixapel/calibre-bookwarden/internal/store"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
	"github.com/felixapel/calibre-bookwarden/internal/web"
)

func main() {
	// The OCR sandbox re-executes this binary; the launcher must lock itself
	// down before anything else runs.
	if sandbox.IsChild(os.Args) {
		sandbox.Main()
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Getenv, os.Stdin, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// versionLine is what "bookwarden version" prints: the version and, when the
// build stamped one in, the first 12 characters of the revision. The deploy
// script looks for its label (a short commit sha) in it.
func versionLine(version, revision string) string {
	if revision == "" || revision == "unknown" {
		return "bookwarden " + version
	}
	return fmt.Sprintf("bookwarden %s (%s)", version, revision[:min(len(revision), 12)])
}

func run(ctx context.Context, args []string, getenv func(string) string, stdin io.Reader, stdout, stderr io.Writer) int {
	cmd := "serve"
	if len(args) > 0 {
		cmd = args[0]
	}
	switch cmd {
	case "version":
		fmt.Fprintln(stdout, versionLine(buildinfo.Version, buildinfo.Revision))
		return 0
	case "healthcheck":
		return healthcheck(ctx, getenv, stderr)
	case "ocr-check":
		cctx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		if err := ocr.New(ocr.DefaultTools).SelfCheck(cctx, os.TempDir()); err != nil {
			fmt.Fprintf(stderr, "ocr: unavailable: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, "ocr: OK")
		return 0
	case "serve":
		logger := newLogger(stderr, getenv)
		err := serve(ctx, getenv, logger)
		switch {
		case err == nil, errors.Is(err, context.Canceled):
			// A stop signal, possibly while still waiting for the database.
			return 0
		default:
			logger.Error("bookwarden stopped", "error", err)
			return 1
		}
	case "parity":
		return parityCommand(ctx, args[1:], getenv, stdin, stdout, stderr)
	case "agent-review":
		return agentReviewCommand(ctx, args[1:], getenv, stdout, stderr)
	case "verify-seals":
		return verifySealsCommand(ctx, args[1:], getenv, stdout, stderr)
	case "eval":
		return evalCommand(ctx, args[1:], getenv, stdout, stderr)
	case "inspect":
		return inspectCommand(ctx, args[1:], getenv, stdout, stderr)
	case "extract-bench":
		return extractBenchCommand(ctx, args[1:], getenv, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown command %q (want: serve, healthcheck, version, ocr-check, parity, agent-review, verify-seals, eval, inspect, extract-bench)\n", cmd)
		return 2
	}
}

// newLogger is the JSON logger at BOOKWARDEN_LOG_LEVEL. A bad value logs at
// info; serve then stops on it as a configuration error.
func newLogger(w io.Writer, getenv func(string) string) *slog.Logger {
	level, _ := config.LogLevelFrom(getenv)
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level}))
}

// agentService builds the agent panel from the configured seats (ADR-029).
func agentService(cfg config.AgentConfig, st *store.Store, logger *slog.Logger, seatOpts ...agent.SeatOption) *agent.Service {
	seats, status, quotas := buildSeats(cfg, logger, seatOpts...)
	for _, name := range cfg.Missing {
		status = append(status, agent.SeatStatus{Name: name})
	}
	agents := store.NewAgentRepo(st)
	panel := make([]string, len(status))
	for i, st := range status {
		panel[i] = st.Name
	}
	reviewer := agent.NewReviewer(agent.Options{
		Store: agents, Evidence: store.NewRunRepo(st), Fixes: store.NewFixRepo(st), Seats: seats, Panel: panel,
		MaxTokens: cfg.MaxTokensPerRun, Logger: logger,
	})
	return agent.NewService(agent.ServiceOptions{Reviewer: reviewer, Store: agents, Decisions: store.NewFixRepo(st), Seats: status, Quotas: quotas, Logger: logger})
}

// buildSeats builds the configured seats, each with its own HTTP client, and
// the Quotas they all record into.
func buildSeats(cfg config.AgentConfig, logger *slog.Logger, seatOpts ...agent.SeatOption) ([]agent.Seat, []agent.SeatStatus, *agent.Quotas) {
	quotas := agent.NewQuotas()
	var seats []agent.Seat
	var status []agent.SeatStatus
	for _, c := range cfg.Seats {
		// Every seat gets its own client, so an egress allowance never
		// reaches another seat (ADR-033).
		hc, err := agent.NewSeatClient(c.URL, c.AllowCIDR)
		var s agent.Seat
		if err == nil {
			s, err = agent.NewSeat(c, hc, append([]agent.SeatOption{agent.WithQuotas(quotas)}, seatOpts...)...)
		}
		if err != nil {
			logger.Warn("agent: seat not usable", "seat", c.Name, "error", err)
			status = append(status, agent.SeatStatus{Name: c.Name, Model: c.Model, Family: c.Family})
			continue
		}
		warnPlainHTTP(logger, c)
		seats = append(seats, s)
		status = append(status, agent.SeatStatus{Name: c.Name, Model: c.Model, Family: c.Family, Configured: true})
	}
	return seats, status, quotas
}

// warnPlainHTTP logs a startup warning for a seat on plain http (ADR-033). It
// names the seat and host, never the key.
func warnPlainHTTP(logger *slog.Logger, c agent.SeatConfig) {
	if u, err := url.Parse(c.URL); err == nil && u.Scheme == "http" {
		logger.Warn("agent: seat uses plain http; its key travels unencrypted on the network", "seat", c.Name, "host", u.Host)
	}
}

// agentReviewCommand queues a calibration review of a run (ADR-029): the
// panel judges every proposal of the run and decides nothing. The serving
// container runs it.
func agentReviewCommand(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("agent-review", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	runID := fs.Int64("run", 0, "")
	calibrate := fs.Bool("calibrate", false, "")
	if err := fs.Parse(args); err != nil || *runID <= 0 || !*calibrate || fs.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: bookwarden agent-review --run N --calibrate")
		return 2
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "agent-review: %v\n", err)
		return 1
	}
	st, err := store.Open(ctx, getenv("BOOKWARDEN_DATABASE_URL"))
	if err != nil {
		return fail(err)
	}
	defer st.Close()
	if _, err := store.NewRunRepo(st).Run(ctx, *runID); err != nil {
		return fail(err)
	}
	rv, ok, err := store.NewAgentRepo(st).Queue(ctx, *runID, store.ReviewCalibration, store.ModeShadow, "cli")
	if err != nil {
		return fail(err)
	}
	if !ok {
		return fail(errors.New("a calibration review of this run is already waiting"))
	}
	fmt.Fprintf(stdout, "queued calibration review %d of run %d\n", rv.ID, *runID)
	return 0
}

// parityCommand compares a run with Certificate A's archived evidence
// (ADR-025): the CSV of differences goes to stdout, the counts to stderr. The
// export file "-" is stdin, since the container's filesystem is read-only.
func parityCommand(ctx context.Context, args []string, getenv func(string) string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("parity", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	runID := fs.Int64("run", 0, "")
	file := fs.String("certificate-a", "", "")
	if err := fs.Parse(args); err != nil || *runID <= 0 || *file == "" || fs.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: bookwarden parity --run N --certificate-a FILE")
		return 2
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "parity: %v\n", err)
		return 1
	}
	in := stdin
	if *file != "-" {
		f, err := os.Open(*file)
		if err != nil {
			return fail(err)
		}
		defer func() { _ = f.Close() }()
		in = f
	}
	export, err := parity.ReadExport(in)
	if err != nil {
		return fail(err)
	}
	st, err := store.Open(ctx, getenv("BOOKWARDEN_DATABASE_URL"))
	if err != nil {
		return fail(err)
	}
	defer st.Close()
	sum, err := parity.Compare(ctx, report.New(store.NewReportRepo(st)), *runID, export, stdout)
	if err != nil {
		return fail(err)
	}
	fmt.Fprintf(stderr, "books compared: %d\n", sum.Books)
	for _, k := range slices.Sorted(maps.Keys(sum.Kinds)) {
		fmt.Fprintf(stderr, "%s: %d\n", k, sum.Kinds[k])
	}
	for _, k := range slices.Sorted(maps.Keys(sum.Tiers)) {
		fmt.Fprintf(stderr, "tier %s: %d\n", k, sum.Tiers[k])
	}
	return 0
}

func serve(ctx context.Context, getenv func(string) string, logger *slog.Logger) error {
	cfg, err := config.FromEnv(getenv)
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	logger.Info("starting bookwarden", "version", buildinfo.Version, "config", cfg)
	for _, w := range cfg.Warnings {
		logger.Warn("configuration: " + w)
	}

	life := newLifecycle(cfg.StopGrace, logger)
	st, err := store.OpenWithRetry(ctx, cfg.DatabaseURL, logger)
	if err != nil {
		return err
	}
	// Registered first, so it runs last: after the workers are stopped. If a
	// worker had to be abandoned, the store stays open (see closeStore).
	defer closeStore(life, st, logger)
	if err := st.Migrate(ctx); err != nil {
		return err
	}

	authSvc := auth.NewService(store.NewAuthRepo(st), auth.DefaultParams, nil)
	code, needed, err := authSvc.PrepareSetup(ctx)
	if err != nil {
		return err
	}
	if needed {
		logger.Warn("first-time setup: open bookwarden in your browser and enter this setup code", "setup_code", code)
	}

	lib := library.New(library.Config{
		BaseURL:   cfg.CalibreURL,
		User:      cfg.CalibreUser,
		Password:  cfg.CalibrePassword,
		LibraryID: cfg.CalibreLibrary,
	})
	hinter := ocr.New(ocr.DefaultTools)
	checkCtx, cancelCheck := context.WithTimeout(ctx, time.Minute)
	if err := hinter.SelfCheck(checkCtx, os.TempDir()); err != nil {
		logger.Warn("OCR is unavailable; PDF ISBN hints will be recorded as unavailable", "error", err)
	} else {
		logger.Info("OCR self-check passed")
	}
	cancelCheck()
	lookups := sources.NewService(
		sources.NewClient(sources.ClientOptions{UserAgent: sources.UserAgent(buildinfo.Version), AllowHosts: sources.Hosts, HostPace: sources.HostPace}),
		store.NewSourceCache(st, sources.MappingVersion),
		sources.DNB(), sources.BNE(), sources.OpenLibrary(), sources.GoogleBooks(cfg.GoogleBooksKey),
		sources.Wikidata(), sources.Hardcover(cfg.HardcoverToken, logger),
	)
	lookups.Logger = logger
	proc := metrics.New()
	lookups.Metrics = proc
	// The writer recovers unfinished operations, then applies in the
	// background; a stop lets the current book finish (ADR-022).
	fixer := fix.New(store.NewFixRepo(st), store.NewRunRepo(st), lib, logger)
	reportRepo := store.NewReportRepo(st)
	fixer.SetTextCheck(textCheck(reportRepo))
	agents := agentService(cfg.Agent, st, logger, agent.WithCalls(proc))
	reports := report.New(reportRepo)
	// A stop drains instead of cancelling (ADR-022): the runner takes no new
	// book and the writer no new apply, the book in hand and the operation in
	// progress finish, all within BOOKWARDEN_STOP_GRACE; past it the work
	// contexts are cancelled hard. Stop runs before the store closes: the
	// deferred calls run in reverse, and this one is registered after
	// st.Close.
	//
	// stopCtx ends the loops that only wait for work (the writer's queue, the
	// agent reviews, the sweep). workCtx is the runner's, and outlives the
	// signal until the grace is over.
	stopCtx, stopAccepting := context.WithCancel(ctx)
	workCtx, hardCancel := context.WithCancel(context.WithoutCancel(ctx))
	life.OnHard(hardCancel)
	defer func() {
		life.Stop()
		stopAccepting()
		hardCancel()
	}()
	wg := &life.wg
	runnerCtx := workCtx
	runRepo := store.NewRunRepo(st)
	// The agents' review is queued by the same hook, after the seal check, but
	// only for a run a person started (ADR-039).
	// The heartbeat, when configured, follows the seal check on its own so it
	// never delays the review (ADR-041).
	var beats []sealsChecked
	if cfg.Heartbeat.URL != "" {
		sender, err := heartbeat.New(cfg.Heartbeat.URL, cfg.Heartbeat.AllowCIDR, logger)
		if err != nil {
			return fmt.Errorf("heartbeat: %w", err)
		}
		beats = append(beats, heartbeatAfterSeals(sender.Send, runRepo, logger))
	}
	sealFinished := sealOnFinish(stopCtx, wg, reports, logger, reviewUnlessScheduled(runRepo, agents.RunFinished, logger), beats...)
	// Retention runs when a run ends and once a day (ADR-039); the store
	// decides what may go.
	retain := &retention.Policy{Keep: cfg.RetainRuns, Logger: logger, Prune: func(ctx context.Context, keep int) (retention.Result, error) {
		res, err := runRepo.Prune(ctx, keep)
		return retention.Result{Runs: res.Runs, Excerpts: res.Excerpts, Raws: res.Raws}, err
	}}
	finished := finishedHook(sealFinished, retain)
	runOpts := verify.Options{AppVersion: buildinfo.Version, Logger: logger, Hinter: hinter, Sources: lookups, ToolVersions: ocr.ToolVersions(ctx, ocr.DefaultTools),
		Finished: finished}
	// M4's shadow extraction (design section 9); a nil service stays a nil
	// interface, so a run without extractors seals no extraction.
	extractions, err := extractionService(cfg.Extract, runRepo, proc, logger)
	if err != nil {
		return err
	}
	if extractions != nil {
		runOpts.Extraction = extractions
	}
	runner := verify.NewRunner(runRepo, lib, runOpts)
	life.OnDrain(runner.Drain)
	life.OnDrain(stopAccepting)
	life.Describe(func() []any {
		return []any{"active_run", runner.ActiveRunID(), "apply_running", fixer.ApplyStatus().Running, "undos_running", fixer.Undoing()}
	})
	// Without a writable scratch tmpfs no run could finish; refuse to start.
	if err := runner.Prepare(); err != nil {
		return err
	}
	// Runs whose check is missing or a day old are checked at startup and
	// then nightly.
	sweep := report.DefaultSweep
	sweep.Logger = logger
	wg.Go(func() { reports.Sweep(stopCtx, sweep) })
	wg.Go(func() {
		if err := runner.Serve(runnerCtx); err != nil {
			logger.Error("the check runner stopped", "error", err)
		}
	})
	wg.Go(func() { _ = fixer.Serve(stopCtx) })
	// Agent reviews resume, then run as checks complete (ADR-029).
	wg.Go(func() { _ = agents.Serve(stopCtx) })
	// Retention sweeps once a day, and the scheduler starts runs on its
	// slots; both stop with the drain, which ends stopCtx (ADR-022).
	sched := &schedule.Scheduler{
		Config:  cfg.Schedule,
		Runs:    scheduledRuns{runner},
		History: scheduleHistory{runRepo},
		Busy:    busyReason(func() bool { return fixer.ApplyStatus().Running }, fixer.Undoing, store.NewAgentRepo(st).Reviewing),
		Logger:  logger,
	}
	startBackground(stopCtx, life, sched, retain, logger)
	google := "on"
	if cfg.GoogleBooksKey == "" {
		google = "off: no BOOKWARDEN_GOOGLE_BOOKS_KEY"
	}
	hardcover := "recorded only"
	if cfg.HardcoverToken == "" {
		hardcover = "off: no BOOKWARDEN_HARDCOVER_TOKEN"
	}

	srv, err := web.New(web.Options{
		AllowedHosts:   cfg.AllowedHosts,
		TrustedProxies: cfg.TrustedProxies,
		Auth:           authSvc,
		Health:         st.Ping,
		Ready: []web.ReadyCheck{
			{Name: "database", Check: st.SelectOne},
			{Name: "migrations", Check: st.MigrationsCurrent},
			{Name: "calibre", Check: runner.CalibreReady},
		},
		MetricsToken: cfg.MetricsToken,
		Metrics:      metricsWriter(store.NewMetricsRepo(st), proc),
		Logger:       logger,
		Runs:         runner,
		Reports:      reports,
		Excerpts:     reportRepo,
		Fixing:       fixer,
		Agents:       agents,
		Schedule:     sched,
		Ledger:       runRepo,
		Recall:       eval.NewRecallService(store.NewRecallRepo(st)),
		RetainRuns:   cfg.RetainRuns,
		Sources: []web.SourceStatus{
			{Name: "DNB", State: "on"}, {Name: "BNE", State: "on"}, {Name: "Open Library", State: "on"}, {Name: "Google Books", State: google},
			{Name: "Wikidata", State: "recorded only"}, {Name: "Hardcover", State: hardcover},
		},
		SourceNotes: func() map[string]string {
			at, failed := lookups.ProbeFailed(evidence.SourceGoogleBooks)
			if !failed {
				return nil
			}
			return map[string]string{"Google Books": "skipped since " + at.UTC().Format("2006-01-02 15:04 UTC") +
				": its probe found no volume, so its empty answers aren't trusted"}
		},
	})
	if err != nil {
		return err
	}
	httpServer := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	go purgeSessions(ctx, authSvc, logger)

	errCh := make(chan error, 1)
	go func() { errCh <- httpServer.ListenAndServe() }()
	logger.Info("listening", "addr", cfg.Listen)

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
		logger.Info("shutting down", "grace", cfg.StopGrace.String())
		life.Begin()
		shutdownCtx, cancel := context.WithDeadline(context.WithoutCancel(ctx), life.Deadline())
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("http shutdown: %w", err)
		}
		life.Stop()
		return nil
	}
}

// sealOnFinish returns the runner hook that checks the seals of a run that
// ends, in the background and until ctx ends, so its pages read the
// projection (ADR-038). A run that is still going is left alone.
//
// review, when given, runs in the same goroutine after the check has
// returned, whether it succeeded or not. The check is what carries a person's
// earlier rejections into the run (ADR-034), so the agent panel's review is
// queued only once they are in place and can't decide those fixes first.
func sealOnFinish(ctx context.Context, wg *sync.WaitGroup, reports *report.Service, logger *slog.Logger, review func(context.Context, int64, verify.RunStatus), afterSeals ...sealsChecked) func(context.Context, int64, verify.RunStatus) {
	return func(_ context.Context, runID int64, status verify.RunStatus) {
		if status.Terminal() {
			wg.Go(func() {
				check, err := checkSeals(ctx, reports, runID, logger)
				// Each follower (the heartbeat) runs on its own, so
				// neither delays the agent review.
				for _, after := range afterSeals {
					wg.Go(func() { after(ctx, runID, status, check, err) })
				}
				if review != nil {
					review(ctx, runID, status)
				}
			})
		}
	}
}

// sealsChecked is told how a finished run's seal check went.
type sealsChecked func(ctx context.Context, runID int64, status verify.RunStatus, check report.SealCheck, err error)

// checkSeals verifies a finished run's seals and logs what it found.
func checkSeals(ctx context.Context, reports *report.Service, runID int64, logger *slog.Logger) (report.SealCheck, error) {
	check, err := reports.CheckSeals(ctx, runID)
	if err != nil {
		switch {
		case ctx.Err() != nil:
		case errors.Is(err, verify.ErrNoSuchRun):
			// Pruned while it was being checked.
			logger.Debug("run is gone, not checked", "run", runID)
		default:
			logger.Warn("seal check failed", "run", runID, "error", err)
		}
		return check, err
	}
	logger.Info("seals checked", "run", runID, "books", check.Books, "broken", check.Broken, "first_broken", check.FirstBroken)
	return check, nil
}

// verifySealsCommand checks the seals of one finished run, or of every
// finished run, and prints a line per run. It exits 1 when a run has damage
// or can't be checked (ADR-038).
func verifySealsCommand(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("verify-seals", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	runID := fs.Int64("run", 0, "")
	if err := fs.Parse(args); err != nil || *runID < 0 || fs.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: bookwarden verify-seals [--run N]")
		return 2
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "verify-seals: %v\n", err)
		return 1
	}
	st, err := store.Open(ctx, getenv("BOOKWARDEN_DATABASE_URL"))
	if err != nil {
		return fail(err)
	}
	defer st.Close()
	repo := store.NewReportRepo(st)
	svc := report.New(repo)
	ids := []int64{*runID}
	if *runID == 0 {
		// Every finished run, whatever the age of its check.
		if ids, err = repo.FinishedRuns(ctx); err != nil {
			return fail(err)
		}
	}
	code := 0
	for _, id := range ids {
		check, err := svc.CheckSeals(ctx, id)
		if err != nil {
			fmt.Fprintf(stderr, "verify-seals: run %d: %v\n", id, err)
			code = 1
			continue
		}
		if check.Broken > 0 {
			fmt.Fprintf(stdout, "run %d: DAMAGED, %d of %d books broken, first book %d\n", id, check.Broken, check.Books, check.FirstBroken)
			code = 1
		} else {
			fmt.Fprintf(stdout, "run %d: ok, %d books\n", id, check.Books)
		}
		// The excerpts the packages refer to (ADR-032 part 2). A run without
		// any prints nothing more. Counts only: never the text.
		ex, err := repo.CheckExcerpts(ctx, id)
		switch {
		case err != nil:
			fmt.Fprintf(stderr, "verify-seals: run %d: excerpts: %v\n", id, err)
			code = 1
		case ex.Missing+ex.Corrupt > 0:
			fmt.Fprintf(stdout, "run %d: EXCERPTS DAMAGED, %d missing, %d corrupt of %d referenced\n", id, ex.Missing, ex.Corrupt, ex.Referenced)
			code = 1
		case ex.Referenced > 0:
			fmt.Fprintf(stdout, "run %d: excerpts ok, %d referenced\n", id, ex.Referenced)
		}
		if !checkExtractions(ctx, repo, id, stdout, stderr) {
			code = 1
		}
	}
	return code
}

// checkExtractions re-derives every extraction of a run from its stored
// excerpt and raw answers (M4 §8) and prints counts only. A run without any
// prints nothing. It reports whether the run's extractions are sound.
func checkExtractions(ctx context.Context, repo *store.ReportRepo, id int64, stdout, stderr io.Writer) bool {
	var checked, differ, missing, other, first int
	err := repo.Extractions(ctx, id, func(x store.ExtractionRef) error {
		checked++
		bad := false
		switch err := extraction.Reverify(x.Excerpt, x.Body, x.Extraction, x.Raws); {
		case x.Missing:
			missing, bad = missing+1, true
		case errors.Is(err, extraction.ErrOtherVersion):
			other++
		case err != nil:
			differ, bad = differ+1, true
		}
		if bad && first == 0 {
			first = x.BookID
		}
		return nil
	})
	switch {
	case err != nil:
		fmt.Fprintf(stderr, "verify-seals: run %d: extractions: %v\n", id, err)
		return false
	case differ+missing > 0:
		fmt.Fprintf(stdout, "run %d: EXTRACTIONS DAMAGED, %d differ, %d missing their text or answers, of %d; first book %d\n", id, differ, missing, checked, first)
		return false
	case other > 0:
		fmt.Fprintf(stdout, "run %d: extractions ok, %d re-verified, %d under other verifier rules\n", id, checked-other, other)
	case checked > 0:
		fmt.Fprintf(stdout, "run %d: extractions ok, %d re-verified\n", id, checked)
	}
	return true
}

const evalUsage = "usage: bookwarden eval --run N --version V [--baseline B] [--books] [--json]\n       bookwarden eval --run N --extraction [--reverify] [--json]\n       bookwarden eval --recall [--sample N] [--json]"

// recallCommand prints the recall of a recall sample (design section 6.4), the
// newest one unless sample names another. It reads through a read-only
// connection and prints counts and book ids, never library text. The answers
// are confirmations, not labels.
func recallCommand(ctx context.Context, sample int64, asJSON bool, getenv func(string) string, stdout, stderr io.Writer) int {
	fail := func(err error) int {
		fmt.Fprintf(stderr, "eval: %v\n", err)
		return 1
	}
	st, err := store.OpenReadOnly(ctx, getenv("BOOKWARDEN_DATABASE_URL"))
	if err != nil {
		return fail(err)
	}
	defer st.Close()
	svc := eval.NewRecallService(store.NewRecallRepo(st))
	if sample == 0 {
		latest, err := svc.Latest(ctx)
		if err != nil {
			return fail(err)
		}
		sample = latest.ID
	}
	// The recall is Bookwarden's opinion of books answered blind: only a
	// closed sample (every book answered, results shown on /sample) prints it.
	// This connection is read-only, so it can't close one.
	status, err := svc.Status(ctx, sample, false)
	if err != nil {
		return fail(err)
	}
	if status.Result == nil {
		if asJSON {
			if err := writeJSON(stdout, map[string]any{"sample": status.Sample.ID, "answered": status.Answered, "size": status.Sample.Size, "results": nil}); err != nil {
				return fail(err)
			}
			return 0
		}
		fmt.Fprintf(stdout, "Recall sample %d: %d of %d books answered. The results are withheld until every book is answered and /sample has shown them, so that the answers stay blind.\n", status.Sample.ID, status.Answered, status.Sample.Size)
		return 0
	}
	res := *status.Result
	if asJSON {
		if err := writeJSON(stdout, res); err != nil {
			return fail(err)
		}
		return 0
	}
	res.WriteText(stdout)
	return 0
}

// evalCommand replays a run's sealed packages at another decision version and
// prints what would change, or with --extraction measures the run's shadow
// extraction, and with --reverify re-derives it under today's verifier
// rules. It reads only BOOKWARDEN_DATABASE_URL, through a read-only
// connection, and prints counts and book ids, never library text.
// A broken seal is reported and makes the exit code 1.
func evalCommand(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("eval", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	opts := eval.Options{}
	fs.Int64Var(&opts.RunID, "run", 0, "")
	fs.IntVar(&opts.Version, "version", 0, "")
	fs.IntVar(&opts.Baseline, "baseline", 0, "")
	books := fs.Bool("books", false, "")
	asJSON := fs.Bool("json", false, "")
	extraction := fs.Bool("extraction", false, "")
	reverify := fs.Bool("reverify", false, "")
	recall := fs.Bool("recall", false, "")
	sample := fs.Int64("sample", 0, "")
	err := fs.Parse(args)
	if *recall {
		if err == nil && (opts.RunID != 0 || opts.Version != 0 || opts.Baseline != 0 || *books || *extraction || *reverify || *sample < 0 || fs.NArg() != 0) {
			err = errors.New("--recall takes only --sample and --json")
		}
		if err != nil {
			fmt.Fprintln(stderr, evalUsage)
			return 2
		}
		return recallCommand(ctx, *sample, *asJSON, getenv, stdout, stderr)
	}
	if *sample != 0 {
		err = errors.New("--sample needs --recall")
	}
	if *reverify && !*extraction {
		err = errors.New("--reverify needs --extraction")
	}
	if *extraction {
		if opts.Version != 0 || opts.Baseline != 0 || *books {
			err = errors.New("--extraction takes no version")
		}
	} else if err == nil {
		err = opts.Validate()
	}
	if err != nil || opts.RunID <= 0 || fs.NArg() != 0 {
		fmt.Fprintln(stderr, evalUsage)
		return 2
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "eval: %v\n", err)
		return 1
	}
	st, err := store.OpenReadOnly(ctx, getenv("BOOKWARDEN_DATABASE_URL"))
	if err != nil {
		return fail(err)
	}
	defer st.Close()
	if *extraction && *reverify {
		res, err := eval.Reverify(ctx, store.NewReportRepo(st), opts.RunID)
		if err != nil {
			return fail(err)
		}
		if *asJSON {
			if err := writeJSON(stdout, res); err != nil {
				return fail(err)
			}
		} else {
			res.WriteText(stdout)
		}
		if res.Broken > 0 {
			return 1
		}
		return 0
	}
	if *extraction {
		res, err := eval.Extractions(ctx, store.NewReportRepo(st), opts.RunID)
		if err != nil {
			return fail(err)
		}
		if *asJSON {
			if err := writeJSON(stdout, res); err != nil {
				return fail(err)
			}
		} else {
			res.WriteText(stdout)
		}
		if res.Broken > 0 {
			return 1
		}
		return 0
	}
	res, err := eval.Replay(ctx, store.NewReportRepo(st), opts)
	if err != nil {
		return fail(err)
	}
	if *asJSON {
		if err := writeJSON(stdout, res); err != nil {
			return fail(err)
		}
	} else {
		res.WriteText(stdout, *books)
	}
	if res.Broken > 0 {
		return 1
	}
	return 0
}

// writeJSON prints v as indented JSON.
func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// purgeSessions deletes expired sessions every hour until ctx ends.
func purgeSessions(ctx context.Context, svc *auth.Service, logger *slog.Logger) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if n, err := svc.PurgeExpiredSessions(ctx); err != nil {
				logger.Warn("purging expired sessions failed", "error", err)
			} else if n > 0 {
				logger.Info("purged expired sessions", "count", n)
			}
		}
	}
}

// healthcheck is the Docker HEALTHCHECK: it asks the local server's /healthz.
func healthcheck(ctx context.Context, getenv func(string) string, stderr io.Writer) int {
	listen := getenv("BOOKWARDEN_LISTEN")
	if listen == "" {
		listen = ":8080"
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		fmt.Fprintln(stderr, "healthcheck: BOOKWARDEN_LISTEN must be host:port or :port")
		return 1
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+net.JoinHostPort(host, port)+"/healthz", nil)
	if err != nil {
		fmt.Fprintf(stderr, "healthcheck: %v\n", err)
		return 1
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintf(stderr, "healthcheck: %v\n", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(stderr, "healthcheck: HTTP %d\n", resp.StatusCode)
		return 1
	}
	return 0
}

// metricsWriter writes /metrics: the build, the database-backed numbers
// read per scrape, and the process counters.
func metricsWriter(repo *store.MetricsRepo, proc *metrics.Process) func(context.Context, *metrics.Writer) error {
	return func(ctx context.Context, w *metrics.Writer) error {
		snap, err := repo.Snapshot(ctx)
		if err != nil {
			return err
		}
		w.BuildInfo(buildinfo.Version)
		metrics.WriteSnapshot(w, snap)
		proc.WriteTo(w)
		return w.Err()
	}
}
