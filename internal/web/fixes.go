package web

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/felixapel/calibre-bookwarden/internal/agent"
	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/fix"
	"github.com/felixapel/calibre-bookwarden/internal/report"
	"github.com/felixapel/calibre-bookwarden/internal/store"
)

func (s *Server) fixesLatest(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), reportPageBudget)
	defer cancel()
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
		http.Redirect(w, r, fmt.Sprintf("/runs/%d/fixes", chosen), http.StatusSeeOther)
		return
	}
	s.render(w, 200, "fixes.html", pageData{Title: "Fixes", SignedIn: true, CSRFToken: sessionFrom(ctx).CSRFToken, Notice: "No checks yet."})
}

// fixQuery reads the Fixes filters and cursor.
func fixQuery(r *http.Request, runID int64) (report.FixFilter, *report.Cursor, bool, error) {
	q, err := strictQuery(r, "field", "tier", "book_id", "decided", "cursor", "verdicts", "q", "sort", "muted")
	if err != nil {
		return report.FixFilter{}, nil, false, err
	}
	reveal, err := revealFrom(q)
	if err != nil {
		return report.FixFilter{}, nil, false, err
	}
	f, c, err := fixFilterFrom(q, runID)
	return f, c, reveal, err
}

// revealFrom reads the "verdicts" flag: absent, or "1" to show the agents'
// verdicts on undecided rows (ADR-034).
func revealFrom(q url.Values) (bool, error) {
	if !q.Has("verdicts") {
		return false, nil
	}
	if q.Get("verdicts") != "1" {
		return false, report.ErrInvalidFilter
	}
	return true, nil
}

// fixFilterFrom validates the Fixes filters and cursor in q.
func fixFilterFrom(q url.Values, runID int64) (report.FixFilter, *report.Cursor, error) {
	var err error
	f := report.FixFilter{Field: q.Get("field"), Tier: q.Get("tier"), Decided: q.Get("decided"), Query: strings.TrimSpace(q.Get("q"))}
	if !utf8.ValidString(f.Query) {
		return f, nil, report.ErrInvalidFilter
	}
	// An unknown sort is the default order.
	if sort := q.Get("sort"); slices.Contains([]string{report.SortBook, report.SortField, report.SortTier, report.SortDecided}, sort) {
		f.Sort = sort
	}
	if q.Has("muted") {
		if q.Get("muted") != "1" {
			return f, nil, report.ErrInvalidFilter
		}
		f.Muted = true
	}
	if raw := q.Get("book_id"); raw != "" {
		id, err := positiveID(raw)
		if err != nil || id > 2147483647 {
			return f, nil, report.ErrInvalidFilter
		}
		f.BookID = int(id)
	}
	if err := report.ValidateFixFilter(f); err != nil {
		return f, nil, err
	}
	var c *report.Cursor
	if q.Has("cursor") {
		if c, err = decodeCursor(q.Get("cursor"), runID); err != nil {
			return f, nil, err
		}
	}
	return f, c, nil
}

// fixesURL is the Fixes page for a run with the filters and cursor, rebuilt
// from validated values.
func fixesURL(runID int64, f report.FixFilter, c *report.Cursor, reveal bool) string {
	q := fixValues(f)
	if reveal {
		q.Set("verdicts", "1")
	}
	if c != nil {
		q.Set("cursor", encodeCursor(*c))
	}
	if len(q) == 0 {
		return fmt.Sprintf("/runs/%d/fixes", runID)
	}
	return fmt.Sprintf("/runs/%d/fixes?%s", runID, q.Encode())
}

func fixValues(f report.FixFilter) url.Values {
	q := url.Values{}
	if f.Field != "" {
		q.Set("field", f.Field)
	}
	if f.Tier != "" {
		q.Set("tier", f.Tier)
	}
	if f.BookID != 0 {
		q.Set("book_id", strconv.Itoa(f.BookID))
	}
	if f.Decided != "" {
		q.Set("decided", f.Decided)
	}
	if f.Query != "" {
		q.Set("q", f.Query)
	}
	if f.Sort != "" {
		q.Set("sort", f.Sort)
	}
	if f.Muted {
		q.Set("muted", "1")
	}
	return q
}

func (s *Server) fixesPage(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), reportPageBudget)
	defer cancel()
	r = r.WithContext(ctx)
	id, err := positiveID(r.PathValue("id"))
	if err != nil {
		s.reportError(w, r, err)
		return
	}
	f, c, asked, err := fixQuery(r, id)
	if err != nil {
		s.reportError(w, r, err)
		return
	}
	v, err := s.opts.Runs.View(ctx, id)
	if err != nil {
		s.reportError(w, r, err)
		return
	}
	// Muting is computed once per request, for the run's library (§6.3).
	stats, err := s.opts.Fixing.MuteStats(ctx)
	if err != nil {
		s.reportError(w, r, err)
		return
	}
	for _, t := range stats {
		if t.Muted && t.LibraryID == v.Run.LibraryID {
			f.MutedTypes = append(f.MutedTypes, report.FixType(t.FixType))
		}
	}
	// The panel's "left, not unanimous" fixes are known to the agents, not to
	// the report, so the filter is given their keys and the report applies it
	// in SQL before it pages.
	if f.Decided == report.DecidedDisagree && s.opts.Agents != nil {
		outcomes, err := s.opts.Agents.Outcomes(ctx, id)
		if err != nil {
			s.reportError(w, r, err)
			return
		}
		for k, o := range outcomes {
			if o.Outcome == store.OutcomeLeft && agent.NotUnanimous(o.Reason) {
				f.Keys = append(f.Keys, report.FixKey{BookID: k.BookID, Field: k.Field})
			}
		}
		slices.SortFunc(f.Keys, func(a, b report.FixKey) int {
			return cmp.Or(cmp.Compare(a.BookID, b.BookID), strings.Compare(a.Field, b.Field))
		})
	}
	p, err := s.opts.Reports.Fixes(ctx, id, f, c)
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		s.reportError(w, r, err)
		return
	}
	// Filtering by what the panel left for lack of unanimity looks at the
	// verdicts, so it counts as revealing them.
	reveal := asked || f.Decided == report.DecidedDisagree
	data := pageData{Title: fmt.Sprintf("Run %d fixes", id), SignedIn: true, CSRFToken: sessionFrom(ctx).CSRFToken, Run: v, FixPage: p, FixFilter: f, FixFields: decide.Fields(),
		Reveal: reveal, RevealAsked: asked}
	if s.opts.Agents != nil && f.Decided != report.DecidedDisagree {
		data.RevealURL = fixesURL(id, f, c, !asked)
	}
	other := f
	other.Muted = !f.Muted
	data.MutedURL = fixesURL(id, other, nil, asked)
	if c != nil {
		data.Cursor = encodeCursor(*c)
	}
	decided, err := s.opts.Fixing.RunProposals(ctx, id)
	if err == nil {
		data.ApprovedCount, err = s.opts.Fixing.ApprovedCount(ctx)
	}
	if err == nil {
		data.Paused, err = s.opts.Fixing.Paused(ctx)
	}
	if err != nil {
		s.reportError(w, r, err)
		return
	}
	views := fixViews(p.Rows, decided, v.Run.Status.Terminal())
	if s.opts.Agents != nil {
		mode, err := s.opts.Agents.Mode(ctx)
		if err != nil {
			s.reportError(w, r, err)
			return
		}
		// Off looks as before agent fixing: no button, no filter.
		data.AgentsOn = mode != store.ModeOff
		if views, err = s.withVerdicts(ctx, id, views, reveal); err != nil {
			s.reportError(w, r, err)
			return
		}
	}
	data.FixViews = filterDecided(views, f.Decided)
	data.FixHidden = len(views) - len(data.FixViews)
	if reveal && s.opts.Agents != nil {
		// The reveal outlives this page: a later decision on these fixes
		// is never blind, even after the verdicts are hidden again.
		var keys []store.RevealKey
		for _, v := range data.FixViews {
			if v.Status == "" {
				keys = append(keys, store.RevealKey{BookID: v.BookID, Field: v.Proposal.Field})
			}
		}
		if err := s.opts.Fixing.RecordReveals(ctx, id, keys); err != nil {
			s.reportError(w, r, err)
			return
		}
	}
	if !v.Run.Status.Terminal() {
		data.Notice = "This run is active; these results are partial."
	}
	if p.Next != nil {
		q := fixValues(f)
		if asked {
			q.Set("verdicts", "1")
		}
		q.Set("cursor", encodeCursor(*p.Next))
		data.NextURL = fmt.Sprintf("/runs/%d/fixes?%s", id, q.Encode())
	}
	s.render(w, 200, "fixes.html", data)
}

// actor is who the ledger records: there is exactly one admin (ADR-022).
const actor = "admin"

// FixView is one proposal row of the Fixes page with its decision.
type FixView struct {
	report.FixRow
	Status, Reason string // "" while undecided; "applying" while in flight
	Unwritable     string
	Pickable       bool // undecided, approved or rejected, writable, run finished
	Pick           string
	DecidedBy      string
	// CarriedRun and CarriedBook name the decision a carried row
	// copies; both are 0 for any other decision.
	CarriedRun  int64
	CarriedBook int
	// Verdicts are the agents' newest verdicts in panel order; AgentNote is
	// why the panel left the proposal (ADR-029).
	Verdicts  []store.Verdict
	AgentNote string
}

// fixViews joins a page of proposals with the run's decisions.
func fixViews(rows []report.FixRow, decided []fix.ProposalRow, runFinished bool) []FixView {
	type key struct {
		book  int
		field string
	}
	byKey := make(map[key]fix.ProposalRow, len(decided))
	for _, d := range decided {
		byKey[key{d.BookID, d.Proposal.Field}] = d
	}
	out := make([]FixView, 0, len(rows))
	for _, r := range rows {
		v := FixView{FixRow: r, Pick: fmt.Sprintf("%d:%s", r.BookID, r.Proposal.Field)}
		if d, ok := byKey[key{r.BookID, r.Proposal.Field}]; ok {
			v.Status, v.Reason, v.DecidedBy = d.Status, d.Reason, d.DecidedBy
			if d.DecisionMode == store.DecisionCarried && d.CarriedFrom != nil {
				v.CarriedRun, v.CarriedBook = d.CarriedFromRun, d.CarriedFromBook
			}
			if d.OperationID != nil && d.Status == store.ProposalApproved {
				v.Status = "applying"
			}
		}
		open := v.Status == "" || v.Status == store.ProposalApproved || v.Status == store.ProposalRejected
		if open {
			v.Unwritable = fix.Unwritable(r.Proposal)
		}
		v.Pickable = open && v.Unwritable == "" && runFinished
		out = append(out, v)
	}
	return out
}

// reasonText explains a proposal's stale, blocked or failed reason, or why
// it can't be approved.
func reasonText(field, code string) string {
	switch code {
	case "book_changed":
		return "the book changed in Calibre since the check"
	case "superseded":
		return "a newer check replaced this one"
	case "carried":
		return "a newer check carried this approval"
	case "book_gone":
		return "the book is no longer in Calibre"
	case "proposal_changed":
		return "the evidence changed since the approval"
	case "calibre_alters_value":
		if field == "pubdate" {
			return "Calibre can’t store this date exactly"
		}
		return "Calibre can’t store this value exactly"
	case "calibre_cannot_restore":
		return "Calibre couldn’t put the current value back"
	case "renames_shared_name":
		return "it would rename an author or publisher other books share"
	case "readback_mismatch":
		return "Calibre stored something else; the old values were put back"
	case "write_error":
		return "the write failed"
	case "calibre_refused_write":
		return "Calibre refused the write. The Calibre account needs write access (see the README, “Fixing”)"
	case "restore_incomplete":
		return "the old values couldn’t all be put back"
	case "interrupted":
		return "Bookwarden stopped during the write"
	case "text_unverified":
		return "the book’s printed text no longer verifies"
	case "text_old_verifier":
		return "an older verifier read the printed text; recheck the book"
	}
	return code
}

// fixStatus is a row's status as the page shows it.
func fixStatus(v FixView) string {
	with := func(label string) string {
		if v.Reason == "" {
			return label
		}
		return label + ": " + reasonText(v.Proposal.Field, v.Reason)
	}
	switch v.Status {
	case "applying":
		return "Being applied"
	case store.ProposalApplied:
		return "Applied"
	case store.ProposalStale:
		return with("Stale")
	case store.ProposalBlocked:
		return with("Blocked")
	case store.ProposalFailed:
		return with("Failed")
	}
	if v.Unwritable != "" {
		return reasonText(v.Proposal.Field, v.Unwritable)
	}
	switch v.Status {
	case store.ProposalApproved:
		return "Approved"
	case store.ProposalRejected:
		return "Rejected"
	}
	return "Not decided"
}

// maxPicks is the most fixes one decide request takes: a page (spec §8.2).
const maxPicks = 50

var pickPattern = regexp.MustCompile(`^([1-9][0-9]{0,9}):(title|authors|languages|isbn|publisher|pubdate|title_sort|author_sort)$`)

// decodePick reads "<book id>:<field>".
func decodePick(s string) (bookID int, field string, err error) {
	m := pickPattern.FindStringSubmatch(s)
	if m == nil {
		return 0, "", report.ErrInvalidFilter
	}
	id, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil || id > math.MaxInt32 {
		return 0, "", report.ErrInvalidFilter
	}
	return int(id), m[2], nil
}

// strictForm accepts a POST without a query string whose body has only the
// allowed keys besides the CSRF token, each once unless it is repeatable.
func strictForm(r *http.Request, repeatable string, allowed ...string) (url.Values, error) {
	if _, err := strictQuery(r); err != nil {
		return nil, err
	}
	if err := r.ParseForm(); err != nil {
		return nil, report.ErrInvalidFilter
	}
	for k, vs := range r.PostForm {
		if k == "csrf_token" {
			continue
		}
		if !slices.Contains(allowed, k) && k != repeatable {
			return nil, report.ErrInvalidFilter
		}
		if len(vs) != 1 && k != repeatable {
			return nil, report.ErrInvalidFilter
		}
	}
	return r.PostForm, nil
}

// decideBudget bounds a decide request: up to 50 books, each opening its
// sealed evidence.
const decideBudget = 30 * time.Second

// decideFixes approves or rejects the picked fixes, one call per book
// (spec §8.2).
func (s *Server) decideFixes(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), decideBudget)
	defer cancel()
	id, err := positiveID(r.PathValue("id"))
	if err != nil {
		s.reportError(w, r, err)
		return
	}
	form, err := strictForm(r, "pick", "action", "mode", "verdicts", "field", "tier", "book_id", "decided", "cursor", "q", "sort", "muted")
	if err != nil {
		s.reportError(w, r, err)
		return
	}
	reveal, err := revealFrom(form)
	if err != nil {
		s.reportError(w, r, err)
		return
	}
	mode := store.DecisionBulk
	if form.Has("mode") {
		// Only the per-row buttons send a mode, for exactly one fix.
		if form.Get("mode") != store.DecisionIndividual || len(form["pick"]) != 1 {
			s.reportError(w, r, report.ErrInvalidFilter)
			return
		}
		mode = store.DecisionIndividual
	}
	action := form.Get("action")
	if action != "approve" && action != "reject" {
		s.reportError(w, r, report.ErrInvalidFilter)
		return
	}
	picks := form["pick"]
	if len(picks) == 0 || len(picks) > maxPicks {
		s.reportError(w, r, report.ErrInvalidFilter)
		return
	}
	fields := map[int][]string{}
	for _, p := range picks {
		book, field, err := decodePick(p)
		if err != nil {
			s.reportError(w, r, err)
			return
		}
		if !slices.Contains(fields[book], field) {
			fields[book] = append(fields[book], field)
		}
	}
	filters := url.Values{}
	for _, k := range []string{"field", "tier", "book_id", "decided", "cursor", "q", "sort", "muted"} {
		if form.Has(k) {
			filters.Set(k, form.Get(k))
		}
	}
	f, c, err := fixFilterFrom(filters, id)
	if err != nil {
		s.reportError(w, r, err)
		return
	}
	// The disagree filter shows the verdicts, as on the page itself.
	shown := reveal || f.Decided == report.DecidedDisagree
	v, err := s.opts.Runs.View(ctx, id)
	if err != nil {
		s.reportError(w, r, err)
		return
	}
	back := fixesURL(id, f, c, reveal)
	if !v.Run.Status.Terminal() {
		s.refuse(w, r, http.StatusConflict, pageData{Title: "Fixes", Error: "This check is still running. Decide once it has finished.", BackURL: back, BackText: "Back to the fixes"})
		return
	}
	decideFn := s.opts.Fixing.Approve
	if action == "reject" {
		decideFn = s.opts.Fixing.Reject
	}
	// A fix someone already decided showed its verdicts, so deciding it
	// again is never blind.
	decided := map[string]bool{}
	if !shown {
		rows, err := s.opts.Fixing.RunProposals(ctx, id)
		if err != nil {
			s.reportError(w, r, err)
			return
		}
		for _, d := range rows {
			if d.Status != "" {
				decided[fmt.Sprintf("%d:%s", d.BookID, d.Proposal.Field)] = true
			}
		}
	}
	type call struct {
		book   int
		fields []string
		shown  bool
	}
	var calls []call
	for _, book := range slices.Sorted(maps.Keys(fields)) {
		fs := slices.Sorted(slices.Values(fields[book]))
		if shown {
			calls = append(calls, call{book, fs, true})
			continue
		}
		var blind, seen []string
		for _, field := range fs {
			if decided[fmt.Sprintf("%d:%s", book, field)] {
				seen = append(seen, field)
			} else {
				blind = append(blind, field)
			}
		}
		if len(blind) > 0 {
			calls = append(calls, call{book, blind, false})
		}
		if len(seen) > 0 {
			calls = append(calls, call{book, seen, true})
		}
	}
	var problems []string
	for _, c := range calls {
		book := c.book
		err := decideFn(ctx, id, book, c.fields, actor, fix.Basis{Mode: mode, VerdictsShown: c.shown})
		if err == nil {
			continue
		}
		why, ok := decideProblem(err)
		if !ok {
			s.opts.Logger.Error("decide failed", "run", id, "book", book, "action", action, "error", err)
		}
		if errors.As(err, new(*fix.UnwritableError)) {
			why += "; untick it"
		}
		if problem := fmt.Sprintf("Book %d: %s", book, why); !slices.Contains(problems, problem) {
			problems = append(problems, problem)
		}
	}
	if len(problems) > 0 {
		s.refuse(w, r, http.StatusConflict, pageData{Title: "Fixes", Error: "Some books weren’t decided; the others were.", Problems: problems, BackURL: back, BackText: "Back to the fixes"})
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// decideProblem says why a decision was refused; ok is false for an
// unexpected error, which the caller logs.
func decideProblem(err error) (why string, ok bool) {
	var unwritable *fix.UnwritableError
	switch {
	case errors.Is(err, fix.ErrNoProposal):
		return "superseded by a newer check, or no such fix", true
	case errors.Is(err, fix.ErrClosed):
		return "already final", true
	case errors.As(err, &unwritable):
		return fmt.Sprintf("Calibre can’t store its %s exactly", unwritable.Field), true
	case errors.Is(err, fix.ErrTextOldVerifier):
		return "the printed text was read by an older verifier; recheck the book", true
	case errors.Is(err, fix.ErrTextUnverified):
		return "the book’s printed text no longer re-verifies; refused", true
	case errors.Is(err, fix.ErrBlocked):
		return "Calibre can’t store it", true
	}
	return "failed; see the log", false
}

// approveAllBudget bounds "Approve all open fixes" below the server's 60 s
// write timeout; a press that runs out stops cleanly and the next one
// carries on, since what it approved is decided by then.
const approveAllBudget = 50 * time.Second

// approveAll approves every open fix of a finished run that Calibre can
// store, on every page, leaving decided fixes and muted types alone
// (Felix, 2026-11-24). The verdicts stay hidden, so each approval is a
// blind bulk decision.
func (s *Server) approveAll(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), approveAllBudget)
	defer cancel()
	id, err := positiveID(r.PathValue("id"))
	if err != nil {
		s.reportError(w, r, err)
		return
	}
	if _, err := strictForm(r, ""); err != nil {
		s.reportError(w, r, err)
		return
	}
	v, err := s.opts.Runs.View(ctx, id)
	if err != nil {
		s.reportError(w, r, err)
		return
	}
	back := fmt.Sprintf("/runs/%d/fixes", id)
	if !v.Run.Status.Terminal() {
		s.refuse(w, r, http.StatusConflict, pageData{Title: "Fixes", Error: "This check is still running. Decide once it has finished.", BackURL: back, BackText: "Back to the fixes"})
		return
	}
	var f report.FixFilter
	stats, err := s.opts.Fixing.MuteStats(ctx)
	if err != nil {
		s.reportError(w, r, err)
		return
	}
	for _, t := range stats {
		if t.Muted && t.LibraryID == v.Run.LibraryID {
			f.MutedTypes = append(f.MutedTypes, report.FixType(t.FixType))
		}
	}
	rows, err := s.opts.Fixing.RunProposals(ctx, id)
	if err != nil {
		s.reportError(w, r, err)
		return
	}
	decided := map[string]bool{}
	for _, d := range rows {
		if d.Status != "" {
			decided[fmt.Sprintf("%d:%s", d.BookID, d.Proposal.Field)] = true
		}
	}
	type pick struct {
		book  int
		field string
	}
	var picks []pick
	// refused collects, per book and reason in order, the fields not
	// approved, so a book-wide reason is one line.
	type refusal struct {
		book   int
		why    string
		fields []string
	}
	var refused []refusal
	refuse := func(book int, field, why string) {
		for i := range refused {
			if refused[i].book == book && refused[i].why == why {
				refused[i].fields = append(refused[i].fields, field)
				return
			}
		}
		refused = append(refused, refusal{book, why, []string{field}})
	}
	skipped := 0
	for c := (*report.Cursor)(nil); ; {
		p, err := s.opts.Reports.Fixes(ctx, id, f, c)
		if err == nil {
			err = ctx.Err()
		}
		if err != nil {
			s.reportError(w, r, err)
			return
		}
		for _, row := range p.Rows {
			if decided[fmt.Sprintf("%d:%s", row.BookID, row.Proposal.Field)] {
				skipped++
				continue
			}
			if fix.Unwritable(row.Proposal) != "" {
				refuse(row.BookID, row.Proposal.Field, "Calibre can’t store it exactly")
				continue
			}
			picks = append(picks, pick{row.BookID, row.Proposal.Field})
		}
		if p.Next == nil {
			break
		}
		c = p.Next
	}
	// One fix at a time, so a fix that can't be approved holds back no other
	// of its book, and only where nobody has decided yet: a rejection made
	// meanwhile stays.
	approved, superseded, left := 0, 0, 0
	for i, p := range picks {
		if ctx.Err() != nil {
			left = len(picks) - i
			break
		}
		err := s.opts.Fixing.Approve(ctx, id, p.book, []string{p.field}, actor, fix.Basis{Mode: store.DecisionBulk, OnlyOpen: true})
		switch {
		case err == nil:
			approved++
			continue
		case errors.Is(err, fix.ErrDecided):
			skipped++
			continue
		case errors.Is(err, fix.ErrNoProposal):
			// A newer check of the book, or no such fix: one line for all.
			superseded++
			continue
		}
		why, ok := decideProblem(err)
		if !ok {
			s.opts.Logger.Error("approve all failed", "run", id, "book", p.book, "field", p.field, "error", err)
		}
		refuse(p.book, p.field, why)
	}
	var problems []string
	notApproved := superseded + left
	if superseded > 0 {
		line := fmt.Sprintf("%d fixes belong to books a newer check replaced; decide them in the newest run.", superseded)
		if superseded == 1 {
			line = "1 fix belongs to a book a newer check replaced; decide it in the newest run."
		}
		problems = append(problems, line)
	}
	for _, rf := range refused {
		notApproved += len(rf.fields)
		problems = append(problems, fmt.Sprintf("Book %d, %s: %s", rf.book, strings.Join(rf.fields, ", "), rf.why))
	}
	if left > 0 {
		problems = append(problems, fmt.Sprintf("Time ran out with %d fixes left; press Approve all again for the rest.", left))
	}
	notice := fmt.Sprintf("Approved %d fixes. Left alone: %d already decided", approved, skipped)
	if approved == 1 {
		notice = fmt.Sprintf("Approved 1 fix. Left alone: %d already decided", skipped)
	}
	if len(f.MutedTypes) > 0 {
		notice += ", and the muted fix types"
	}
	if notApproved > 0 {
		notice += fmt.Sprintf("; %d not approved, listed below", notApproved)
	}
	s.render(w, http.StatusOK, "refused.html", pageData{Title: "Approve all open fixes", SignedIn: true, CSRFToken: sessionFrom(r.Context()).CSRFToken,
		Notice: notice + ".", Problems: problems, BackURL: back, BackText: "Back to the fixes"})
}

// applyFixes queues the background apply (ADR-022).
func (s *Server) applyFixes(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), reportPageBudget)
	defer cancel()
	if _, err := strictForm(r, ""); err != nil {
		s.reportError(w, r, err)
		return
	}
	err := s.opts.Fixing.StartApply(ctx, actor)
	switch {
	case err == nil:
		http.Redirect(w, r, "/history", http.StatusSeeOther)
	case errors.Is(err, fix.ErrWritesOff):
		s.refuse(w, r, http.StatusConflict, pageData{Title: "Apply", Error: "Changes to Calibre are switched off.", BackURL: "/settings", BackText: "Turn them on in Settings"})
	case errors.Is(err, fix.ErrPaused):
		s.refuse(w, r, http.StatusConflict, pageData{Title: "Apply", Error: "Writes are paused until an incident is acknowledged.", BackURL: "/incidents", BackText: "See the incidents"})
	case errors.Is(err, fix.ErrBusy):
		s.refuse(w, r, http.StatusConflict, pageData{Title: "Apply", Error: "The approved fixes are already being applied.", BackURL: "/history", BackText: "See the history"})
	default:
		s.serverError(w, r, err)
	}
}

// refuse renders a refusal with its reasons and a way on.
func (s *Server) refuse(w http.ResponseWriter, r *http.Request, status int, data pageData) {
	data.SignedIn = true
	data.CSRFToken = sessionFrom(r.Context()).CSRFToken
	s.render(w, status, "refused.html", data)
}
