package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/agent"
	"github.com/felixapel/calibre-bookwarden/internal/config"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/extract"
	"github.com/felixapel/calibre-bookwarden/internal/extractor"
	"github.com/felixapel/calibre-bookwarden/internal/store"
)

// defaultBenchExtractor builds a real extractor; tests replace
// newBenchExtractor.
func defaultBenchExtractor(cfg agent.SeatConfig, hc *http.Client) (extractor.Extractor, error) {
	return extractor.New(cfg, hc)
}

var newBenchExtractor = defaultBenchExtractor

// effortFlag collects --effort name=e1,e2 overrides.
type effortFlag map[string][]string

func (f effortFlag) String() string { return "" }
func (f effortFlag) Set(v string) error {
	name, list, ok := strings.Cut(v, "=")
	if !ok || name == "" || list == "" {
		return errors.New("want name=effort list")
	}
	f[name] = strings.Split(list, ",")
	return nil
}

// candidateFlag collects --candidate name=model:family, a model to measure on
// the configured gateway instead of the configured extractors.
type candidateFlag []struct{ name, model, family string }

func (f *candidateFlag) String() string { return "" }
func (f *candidateFlag) Set(v string) error {
	name, rest, ok := strings.Cut(v, "=")
	i := strings.LastIndex(rest, ":")
	if !ok || name == "" || i <= 0 || i == len(rest)-1 {
		return errors.New("want name=model:family")
	}
	for _, c := range *f {
		if c.name == name {
			return errors.New("candidate named twice")
		}
	}
	*f = append(*f, struct{ name, model, family string }{name, rest[:i], rest[i+1:]})
	return nil
}

const benchUsage = "usage: bookwarden extract-bench [--synthetic N] [--real N --run ID] [--seed S] [--extractors a,b] " +
	"[--candidate name=model:family]... [--efforts e1,e2] [--effort name=e1,e2]... [--concurrency N] [--max-calls N] [--timeout D]"

// extractBenchCommand runs the benchmark of M4 design section 4a: each
// extractor at each effort over synthetic cases with truth and a sample of a
// run's real excerpts. It reads the database and calls the configured
// extractors; it writes nothing, and prints counts and rates, never text.
func extractBenchCommand(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("extract-bench", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	synthetic := fs.Int("synthetic", 30, "")
	realN := fs.Int("real", 0, "")
	runID := fs.Int64("run", 0, "")
	seed := fs.String("seed", "bench-1", "")
	only := fs.String("extractors", "", "")
	efforts := fs.String("efforts", "default", "")
	perExtractor := effortFlag{}
	fs.Var(perExtractor, "effort", "")
	var candidates candidateFlag
	fs.Var(&candidates, "candidate", "")
	concurrency := fs.Int("concurrency", 4, "")
	maxCalls := fs.Int64("max-calls", 300, "")
	timeout := fs.Duration("timeout", 3*time.Minute, "")
	usage := func() int { fmt.Fprintln(stderr, benchUsage); return 2 }
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *synthetic < 0 || *realN < 0 || (*realN > 0 && *runID <= 0) ||
		*maxCalls < 1 || *concurrency < 1 || *timeout <= 0 {
		return usage()
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "extract-bench: %v\n", err)
		return 1
	}
	xc, err := config.ExtractorsFromEnv(getenv)
	if err != nil {
		return fail(err)
	}
	pool := xc.Extractors
	if len(candidates) > 0 {
		if !xc.Gateway.Configured() {
			return fail(errors.New("candidates need a gateway: BOOKWARDEN_EXTRACTOR_BIFROST_URL or BOOKWARDEN_EXTRACTOR_KEY"))
		}
		pool = nil
		for _, c := range candidates {
			p := xc.Gateway.Seat
			p.Name, p.Model, p.Family = c.name, c.model, c.family
			pool = append(pool, p)
		}
	}
	var cfgs []agent.SeatConfig
	for _, c := range pool {
		if *only == "" || slices.Contains(strings.Split(*only, ","), c.Name) {
			cfgs = append(cfgs, c)
		}
	}
	for name := range perExtractor {
		if !slices.ContainsFunc(cfgs, func(c agent.SeatConfig) bool { return c.Name == name }) {
			fmt.Fprintf(stderr, "extract-bench: --effort names %q, which is not a configured extractor\n", name)
			return 2
		}
	}
	if len(cfgs) == 0 {
		return fail(errors.New("no extractor is configured (BOOKWARDEN_EXTRACTOR_BIFROST_URL or BOOKWARDEN_EXTRACTOR_KEY)"))
	}

	var samples []extractor.Sample
	for _, c := range extractor.Synthetic(seedNumber(*seed), *synthetic) {
		samples = append(samples, extractor.SyntheticSample(c))
	}
	if *realN > 0 {
		st, err := store.OpenReadOnly(ctx, getenv("BOOKWARDEN_DATABASE_URL"))
		if err != nil {
			return fail(err)
		}
		exs, err := store.NewReportRepo(st).SampleExcerpts(ctx, *runID, *realN, *seed)
		st.Close()
		if err != nil {
			return fail(err)
		}
		for _, e := range exs {
			x := extract.NewExcerpt(evidence.Excerpt{Text: e.Text, Units: e.Units, Method: e.Method})
			samples = append(samples, extractor.RealSample(fmt.Sprintf("book-%d", e.BookID), x))
		}
	}

	budget := extractor.NewBudget(*maxCalls)
	opts := extractor.RunOptions{Concurrency: *concurrency, Budget: budget, Timeout: *timeout}
	fmt.Fprintf(stdout, "extract-bench: prompt %s, verifier v%d, %d synthetic + %d real samples, at most %d calls\n",
		extractor.PromptVersion, extract.Version, *synthetic, len(samples)-*synthetic, *maxCalls)
	var cells []extractor.CellStats
	for _, cfg := range cfgs {
		list := strings.Split(*efforts, ",")
		if l, ok := perExtractor[cfg.Name]; ok {
			list = l
		}
		hc, err := agent.NewSeatClient(cfg.URL, cfg.AllowCIDR)
		if err != nil {
			return fail(err)
		}
		for _, effort := range list {
			c := cfg
			c.Effort = effort
			if effort == "default" {
				c.Effort = ""
			}
			e, err := newBenchExtractor(c, hc)
			if err != nil {
				return fail(err)
			}
			st := extractor.RunCell(ctx, e, samples, opts)
			st.Effort = effort
			cells = append(cells, st)
			if ctx.Err() != nil {
				return fail(ctx.Err())
			}
		}
	}
	printBench(stdout, cells)
	return 0
}

// seedNumber turns the seed text into the synthetic generator's seed.
func seedNumber(s string) uint64 {
	var h uint64 = 14695981039346656037
	for i := 0; i < len(s); i++ {
		h = (h ^ uint64(s[i])) * 1099511628211
	}
	return h
}

func pct(f float64) string { return fmt.Sprintf("%.1f", 100*f) }

func perCall(n int64, calls int) string {
	if calls == 0 {
		return "-"
	}
	return fmt.Sprint(n / int64(calls))
}

func printBench(w io.Writer, cells []extractor.CellStats) {
	tw := tabwriter.NewWriter(w, 2, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(tw, "cell\tcalls\tskipped\terrors\tp50\tp95\tin/call\tout/call\treason/call\tclaims\tgrounded%\tP%\tR%\ttraps\textra\tparser-agree%\tparser-cover%\t")
	for _, c := range cells {
		errs := 0
		for _, n := range c.Errors {
			errs += n
		}
		fmt.Fprintf(tw, "%s/%s\t%d\t%d\t%d\t%s\t%s\t%s\t%s\t%s\t%d\t%s\t%s\t%s\t%d\t%d\t%s\t%s\t\n",
			c.Extractor, c.Effort, c.Calls, c.Skipped, errs,
			c.Percentile(50).Round(100*time.Millisecond), c.Percentile(95).Round(100*time.Millisecond),
			perCall(c.InputTokens, c.Calls), perCall(c.OutputTokens, c.Calls), perCall(c.ReasoningTokens, c.Calls),
			c.Claims, pct(c.GroundedShare()), pct(c.Precision()), pct(c.Recall()), c.TrapHits, c.Extra,
			pct(ratioOf(c.ParserAgree, c.ParserPatterned)), pct(ratioOf(c.ParserCovered, c.ParserTotal)))
	}
	_ = tw.Flush()
	for _, c := range cells {
		var parts []string
		for _, m := range []map[string]int{c.Errors, c.Status, c.FieldFP, c.Models} {
			parts = append(parts, sortedCounts(m))
		}
		fmt.Fprintf(w, "%s/%s: errors {%s} verifier {%s} false positives {%s} models {%s}\n", c.Extractor, c.Effort, parts[0], parts[1], parts[2], parts[3])
	}
	// Agreement between every two cells of different extractors.
	for i, a := range cells {
		for _, b := range cells[i+1:] {
			if a.Extractor != b.Extractor {
				fmt.Fprintf(w, "agreement %s/%s ~ %s/%s: %s%%\n", a.Extractor, a.Effort, b.Extractor, b.Effort, pct(extractor.Agreement(a, b)))
			}
		}
	}
	skipped := 0
	for _, c := range cells {
		skipped += c.Skipped
	}
	if skipped > 0 {
		fmt.Fprintf(w, "warning: the call budget ran out: %d calls skipped; the cells measured different samples and are not comparable\n", skipped)
	}
	sug := extractor.SuggestEfforts(cells)
	names := make([]string, 0, len(sug))
	for n := range sug {
		names = append(names, n+"="+sug[n])
	}
	slices.Sort(names)
	fmt.Fprintf(w, "suggested efforts: %s\n", strings.Join(names, " "))
}

func ratioOf(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b)
}

func sortedCounts(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s=%d", k, m[k])
	}
	return strings.Join(parts, " ")
}
