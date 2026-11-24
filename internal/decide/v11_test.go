package decide_test

import (
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

// From version 11 a fix is not proposed when an undo couldn't put Calibre's
// current value back exactly (ADR-048): run 33 held 9 such pubdates and a
// title sort, each one Approve refused as calibre_cannot_restore.
func TestDecisionV11ProposesOnlyWhatAnUndoCanRestore(t *testing.T) {
	const mid = "2010-05-05T12:00:00+00:00"
	for _, tt := range []struct {
		name  string
		p     func() evidence.Package
		field string
		v11   bool
	}{
		{"pubdate over a year-only placeholder (the 1st)", func() evidence.Package { return v9Dated("2007-01-01T00:00:00+00:00", "2007-02-13") }, "pubdate", false},
		{"pubdate over a day Calibre shifted to the month's end", func() evidence.Package { return v9Dated("1991-07-31T22:00:00+00:00", "2007-04-17") }, "pubdate", false},
		{"pubdate over a mid-month day", func() evidence.Package { return v9Dated(mid, "2007-02-13") }, "pubdate", true},
		{"pubdate over 31 December", func() evidence.Package { return v9Dated("2004-12-31T12:00:00+00:00", "2007-02-13") }, "pubdate", true},
		{"pubdate fill", func() evidence.Package { return v9Dated("", "2007-02-13") }, "pubdate", true},
		{"title sort with a double space", sorted(str("Stein  der Weisen"), rowlingSort, map[string]string{A: rowlingSort}), "title_sort", false},
		{"title sort with plain spaces", sorted(str("Stein der Weisen"), rowlingSort, map[string]string{A: rowlingSort}), "title_sort", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for version, want := range map[int]bool{10: true, 11: tt.v11} {
				d, err := decide.AtVersion(t.Context(), tt.p(), version)
				if err != nil {
					t.Fatal(err)
				}
				if got := proposalOf(d, tt.field) != nil; got != want {
					t.Errorf("v%d: %s proposed = %v, want %v (proposals %+v)", version, tt.field, got, want, d.Proposals)
				}
			}
		})
	}
}

// A title write brings the title sort back on undo (ADR-028), so a title over
// a title sort Calibre can't store back isn't proposed either.
func TestDecisionV11TitleNeedsARestorableSort(t *testing.T) {
	p := sorted(str("Stein  der Weisen"), rowlingSort, map[string]string{A: rowlingSort})()
	p.Calibre.Title = "Harry Potter"
	for version, want := range map[int]bool{10: true, 11: false} {
		d, err := decide.AtVersion(t.Context(), p, version)
		if err != nil {
			t.Fatal(err)
		}
		if got := proposalOf(d, "title") != nil; got != want {
			t.Errorf("v%d: title proposed = %v, want %v", version, got, want)
		}
	}
}

func TestDecideUsesVersion11(t *testing.T) {
	if decide.Version != 11 {
		t.Fatalf("Version = %d", decide.Version)
	}
	// Wikidata and Hardcover stay recorded only.
	p := v9Imprint("MIT PRESS", "MIT Press", "")
	now, err := decide.Decide(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	at, err := decide.AtVersion(t.Context(), p, 11)
	if err != nil {
		t.Fatal(err)
	}
	if !equalProposals(now.Proposals, at.Proposals) {
		t.Fatalf("Decide %+v, AtVersion(11) %+v", now.Proposals, at.Proposals)
	}
	raw, seal, err := decide.Seal(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	if q, _, err := decide.Open(t.Context(), raw, seal); err != nil || q.DecisionVersion != 11 {
		t.Fatalf("Open: version %d, %v", q.DecisionVersion, err)
	}
	for _, src := range []string{evidence.SourceWikidata, evidence.SourceHardcover} {
		if !decide.RecordedOnly(src, 11) {
			t.Fatalf("v11 decides on %s", src)
		}
	}
}
