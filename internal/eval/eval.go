// Package eval measures what another decision version would change on the
// sealed packages of a stored run, without writing anything. It reports
// counts, tiers, reason codes and book ids; it never carries library text,
// so its output can be pasted into a review.
package eval

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/report"
	"github.com/felixapel/calibre-bookwarden/internal/store"
)

// ErrVersion means a version outside 1 to decide.Version.
var ErrVersion = errors.New("eval: decision version out of range")

// Tiers are the rows and columns of the transition matrix.
var Tiers = []string{evidence.TierA, evidence.TierB, evidence.TierC}

// Options select the run and the two versions. Baseline 0 means each
// package's sealed version (the current one for a package sealed before
// decision versions existed).
type Options struct {
	RunID    int64
	Version  int
	Baseline int
}

// Validate rejects a version outside 1 to decide.Version.
func (o Options) Validate() error {
	if o.Version < 1 || o.Version > decide.Version || o.Baseline < 0 || o.Baseline > decide.Version {
		return fmt.Errorf("%w: want 1 to %d", ErrVersion, decide.Version)
	}
	return nil
}

// FieldDelta counts one field's proposals at both versions. A book counts as
// Added when only the version proposes the field, Removed when only the
// baseline does, and Changed when both do with a different proposed value
// or different roots.
type FieldDelta struct {
	Baseline int `json:"baseline"`
	Version  int `json:"version"`
	Added    int `json:"added"`
	Removed  int `json:"removed"`
	Changed  int `json:"changed"`
}

// ReasonDelta counts the books that gained or lost one reason code.
type ReasonDelta struct {
	Gained int `json:"gained"`
	Lost   int `json:"lost"`
}

// FixType is a fix type of ADR-034 with its proposals at the version.
type FixType struct {
	Field string `json:"field"`
	Path  string `json:"path"`
	Kind  string `json:"kind"`
	Count int    `json:"count"`
}

// BookDelta is one changed book, by id only.
type BookDelta struct {
	Book    int      `json:"book"`
	From    string   `json:"from"`
	To      string   `json:"to"`
	Added   []string `json:"added,omitempty"`
	Removed []string `json:"removed,omitempty"`
	Changed []string `json:"changed,omitempty"`
	Gained  []string `json:"reasons_gained,omitempty"`
	Lost    []string `json:"reasons_lost,omitempty"`
}

// Result is the measurement.
type Result struct {
	Run         int64                     `json:"run"`
	Version     int                       `json:"version"`
	Baseline    int                       `json:"baseline"`
	Compared    int                       `json:"compared"`
	Broken      int                       `json:"broken"`
	BrokenBooks []int                     `json:"broken_books,omitempty"`
	Matrix      map[string]map[string]int `json:"matrix"`
	Fields      map[string]FieldDelta     `json:"fields"`
	Reasons     map[string]ReasonDelta    `json:"reasons"`
	FixTypes    []FixType                 `json:"fix_types"`
	Books       []BookDelta               `json:"books"`
}

// Replay walks the run's books in a read-only snapshot, verifies each seal,
// and derives the decision at both versions. A broken seal is counted and
// listed; the walk goes on.
func Replay(ctx context.Context, st report.Store, o Options) (Result, error) {
	if err := o.Validate(); err != nil {
		return Result{}, err
	}
	snap, err := st.Snapshot(ctx, o.RunID)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = snap.Close(ctx) }()
	res := Result{
		Run: o.RunID, Version: o.Version, Baseline: o.Baseline,
		Matrix: map[string]map[string]int{}, Fields: map[string]FieldDelta{}, Reasons: map[string]ReasonDelta{},
		FixTypes: []FixType{}, Books: []BookDelta{},
	}
	for _, from := range Tiers {
		res.Matrix[from] = map[string]int{}
		for _, to := range Tiers {
			res.Matrix[from][to] = 0
		}
	}
	types := map[FixType]int{}
	broken, err := eachPackage(ctx, snap, func(b report.Book) error {
		// NextBook opened the package at its sealed version.
		base := b.Decision
		var err error
		if o.Baseline != 0 {
			if base, err = decide.AtVersion(ctx, *b.Package, o.Baseline); err != nil {
				return fmt.Errorf("eval: book %d at version %d: %w", b.Ref.ID, o.Baseline, err)
			}
		}
		next, err := decide.AtVersion(ctx, *b.Package, o.Version)
		if err != nil {
			return fmt.Errorf("eval: book %d at version %d: %w", b.Ref.ID, o.Version, err)
		}
		res.Compared++
		res.add(b.Ref.ID, base, next, types)
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	res.Broken, res.BrokenBooks = len(broken), broken
	for t, n := range types {
		t.Count = n
		res.FixTypes = append(res.FixTypes, t)
	}
	slices.SortFunc(res.FixTypes, func(a, b FixType) int {
		return strings.Compare(a.Field+"\x00"+a.Path+"\x00"+a.Kind, b.Field+"\x00"+b.Path+"\x00"+b.Kind)
	})
	return res, nil
}

// eachPackage visits each book of the snapshot that has a package, opened at
// its sealed version, and returns the books whose seal is broken.
func eachPackage(ctx context.Context, snap report.Snapshot, visit func(report.Book) error) ([]int, error) {
	var broken []int
	for after := 0; ; {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		b, found, err := snap.NextBook(ctx, after)
		if !found {
			if err != nil {
				return nil, err
			}
			return broken, nil
		}
		if err != nil {
			if !errors.Is(err, report.ErrIntegrity) || b.Ref.ID <= after {
				return nil, err
			}
			broken = append(broken, b.Ref.ID)
			after = b.Ref.ID
			continue
		}
		after = b.Ref.ID
		if b.Package == nil {
			continue
		}
		if err := visit(b); err != nil {
			return nil, err
		}
	}
}

func (r *Result) add(id int, base, next decide.Decision, types map[FixType]int) {
	r.Matrix[base.Tier][next.Tier]++
	d := BookDelta{Book: id, From: base.Tier, To: next.Tier}
	bp, np := byField(base.Proposals), byField(next.Proposals)
	fields := map[string]struct{}{}
	for f, ps := range bp {
		fd := r.Fields[f]
		fd.Baseline += len(ps)
		r.Fields[f] = fd
		fields[f] = struct{}{}
	}
	for f, ps := range np {
		fd := r.Fields[f]
		fd.Version += len(ps)
		r.Fields[f] = fd
		fields[f] = struct{}{}
		for _, p := range ps {
			types[FixType{Field: p.Field, Path: store.FixPath(p), Kind: store.FixKind(p)}]++
		}
	}
	for _, f := range slices.Sorted(maps.Keys(fields)) {
		fd := r.Fields[f]
		b, n := bp[f], np[f]
		switch {
		case len(b) == 0:
			fd.Added++
			d.Added = append(d.Added, f)
		case len(n) == 0:
			fd.Removed++
			d.Removed = append(d.Removed, f)
		case !slices.EqualFunc(b, n, samePayload):
			fd.Changed++
			d.Changed = append(d.Changed, f)
		}
		r.Fields[f] = fd
	}
	for _, c := range missingFrom(next.Reasons, base.Reasons) {
		rd := r.Reasons[c]
		rd.Gained++
		r.Reasons[c] = rd
		d.Gained = append(d.Gained, c)
	}
	for _, c := range missingFrom(base.Reasons, next.Reasons) {
		rd := r.Reasons[c]
		rd.Lost++
		r.Reasons[c] = rd
		d.Lost = append(d.Lost, c)
	}
	if d.From != d.To || len(d.Added)+len(d.Removed)+len(d.Changed)+len(d.Gained)+len(d.Lost) > 0 {
		r.Books = append(r.Books, d)
	}
}

func byField(ps []evidence.Proposal) map[string][]evidence.Proposal {
	out := map[string][]evidence.Proposal{}
	for _, p := range ps {
		out[p.Field] = append(out[p.Field], p)
	}
	return out
}

func samePayload(a, b evidence.Proposal) bool {
	return slices.Equal(a.Proposed, b.Proposed) && slices.Equal(a.Roots, b.Roots)
}

// missingFrom lists the codes of a that b lacks, sorted.
func missingFrom(a, b []string) []string {
	var out []string
	for _, c := range a {
		if !slices.Contains(b, c) && !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	slices.Sort(out)
	return out
}

func versionName(v int) string {
	if v == 0 {
		return "the sealed version"
	}
	return fmt.Sprintf("version %d", v)
}

// WriteText prints the plain-text summary; books adds a line per changed
// book.
func (r Result) WriteText(w io.Writer, books bool) {
	p := func(format string, a ...any) { _, _ = fmt.Fprintf(w, format, a...) }
	p("run %d: baseline %s, replayed at version %d\n", r.Run, versionName(r.Baseline), r.Version)
	p("books compared: %d\nbooks with a broken seal: %d\n", r.Compared, r.Broken)
	if r.Broken > 0 {
		ids := make([]string, len(r.BrokenBooks))
		for i, id := range r.BrokenBooks {
			ids[i] = fmt.Sprint(id)
		}
		p("broken books: %s\n", strings.Join(ids, " "))
	}
	p("\ntier transitions (rows: baseline, columns: version)\n     ")
	for _, to := range Tiers {
		p("%6s", to)
	}
	p("\n")
	for _, from := range Tiers {
		p("%4s ", from)
		for _, to := range Tiers {
			p("%6d", r.Matrix[from][to])
		}
		p("\n")
	}
	p("\nproposals per field: baseline, version, added, removed, changed\n")
	for _, f := range slices.Sorted(maps.Keys(r.Fields)) {
		d := r.Fields[f]
		p("%-14s %6d %6d %6d %6d %6d\n", f, d.Baseline, d.Version, d.Added, d.Removed, d.Changed)
	}
	p("\nreason codes: gained, lost\n")
	for _, c := range slices.Sorted(maps.Keys(r.Reasons)) {
		p("%-34s %6d %6d\n", c, r.Reasons[c].Gained, r.Reasons[c].Lost)
	}
	p("\nfix types at version %d: field, path, kind, count\n", r.Version)
	for _, t := range r.FixTypes {
		path := t.Path
		if path == "" {
			path = "-"
		}
		p("%-14s %-30s %-8s %6d\n", t.Field, path, t.Kind, t.Count)
	}
	if !books {
		return
	}
	p("\nchanged books: %d\n", len(r.Books))
	for _, b := range r.Books {
		line := fmt.Sprintf("book %d: %s->%s", b.Book, b.From, b.To)
		for _, g := range []struct {
			name string
			v    []string
		}{{"added", b.Added}, {"removed", b.Removed}, {"changed", b.Changed}, {"gained", b.Gained}, {"lost", b.Lost}} {
			if len(g.v) > 0 {
				line += " " + g.name + "=" + strings.Join(g.v, ",")
			}
		}
		p("%s\n", line)
	}
}
