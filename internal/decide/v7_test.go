package decide_test

import (
	"encoding/json"
	"os"
	"slices"
	"strconv"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

// TestToleranceV7 covers ADR-030's amendment "Decision v7 tolerances": two
// records that spell a title or an author list differently are not a
// conflict, but a different work still is.
func TestToleranceV7(t *testing.T) {
	for _, r := range v7Rows() {
		t.Run(r.name, func(t *testing.T) {
			d6, err := decide.AtVersion(t.Context(), r.pkg(), 6)
			if err != nil {
				t.Fatal(err)
			}
			if d6.Tier != evidence.TierC || !slices.Equal(d6.Reasons, r.v6) {
				t.Fatalf("version 6 = %s %v, want C %v", d6.Tier, d6.Reasons, r.v6)
			}
			d7, err := decide.AtVersion(t.Context(), r.pkg(), 7)
			if err != nil {
				t.Fatal(err)
			}
			if !r.want7 {
				// records_disagree may clear: a file author list consistent with a
				// record's is a match. The conflict stays.
				noDisagree := func(rs []string) []string {
					return slices.DeleteFunc(slices.Clone(rs), func(r string) bool { return r == "records_disagree" })
				}
				if d7.Tier != d6.Tier || !slices.Equal(noDisagree(d7.Reasons), noDisagree(d6.Reasons)) || len(d7.Proposals) != len(d6.Proposals) {
					t.Fatalf("version 7 = %+v, want version 6's %+v", d7, d6)
				}
				return
			}
			if d7.Tier != evidence.TierA || len(d7.Reasons) != 0 {
				t.Fatalf("version 7 = %s %v, want A with no reasons", d7.Tier, d7.Reasons)
			}
		})
	}
}

// withOldCalibre makes Calibre's title and authors differ from everything
// else, so every title or authors agreement could produce a proposal.
func withOldCalibre(p evidence.Package) evidence.Package {
	p.Calibre.Title, p.Calibre.Authors = "Old Title", []string{"Unknown Author"}
	return p
}

func proposalFields(d decide.Decision) []string {
	var out []string
	for _, p := range d.Proposals {
		if p.Field != "title_sort" && p.Field != "author_sort" {
			out = append(out, p.Field)
		}
	}
	return out
}

// TestToleranceProposesNoTitleOrAuthors: records that differ in a field and
// agree only through a version 7 tolerance move a tier and nothing else. A
// title or authors proposal needs records that agree strictly, so none comes
// from a book whose records differ in that field; fields the records agree on
// strictly, such as the publisher, are proposed exactly as version 6's rules
// build them.
func TestToleranceProposesNoTitleOrAuthors(t *testing.T) {
	for _, r := range v7Rows() {
		t.Run(r.name, func(t *testing.T) {
			var differ []string // the fields whose records state different values
			if r.olTitle != r.gbTitle {
				differ = append(differ, "title")
			}
			if !slices.Equal(r.olAuthors, r.gbAuthors) {
				differ = append(differ, "authors")
			}
			if len(differ) == 0 {
				t.Fatal("row has no differing field")
			}
			p := withOldCalibre(r.pkg())
			want, err := decide.ProposalsAtV6Ungated(t.Context(), p)
			if err != nil {
				t.Fatal(err)
			}
			d, err := decide.AtVersion(t.Context(), p, 7)
			if err != nil {
				t.Fatal(err)
			}
			if r.want7 && !slices.ContainsFunc(want, func(p evidence.Proposal) bool { return slices.Contains(differ, p.Field) }) {
				t.Fatalf("version 6's rules propose none of %v here, so the row proves nothing: %v", differ, fieldsOf(want))
			}
			for _, f := range proposalFields(d) {
				if slices.Contains(differ, f) {
					t.Fatalf("tolerance-only agreement proposed %s: %+v", f, d.Proposals)
				}
			}
			if d.Tier == evidence.TierC {
				return
			}
			// Every other field proposes exactly as version 6's rules build it.
			rest := func(ps []evidence.Proposal) []evidence.Proposal {
				return slices.DeleteFunc(slices.Clone(ps), func(p evidence.Proposal) bool {
					return slices.Contains(differ, p.Field) || p.Field == "title_sort" || p.Field == "author_sort"
				})
			}
			wj, _ := json.Marshal(rest(want))
			gj, _ := json.Marshal(rest(d.Proposals))
			if string(wj) != string(gj) || len(rest(want)) == 0 {
				t.Fatalf("other proposals\n got %s\nwant %s (non-empty)", gj, wj)
			}
		})
	}
}

// TestStrictAgreementStillProposes: the controls for the test above. Where
// every record states the same value, version 7 proposes as version 6 does.
func TestStrictAgreementStillProposes(t *testing.T) {
	zb := names("Stefan Zweig", "Anthea Bell")
	for _, tt := range []struct {
		name  string
		row   v7Row
		field string
		want  []string
	}{
		{"title", v7Row{lang: "en", fileTitle: "The Precipice", fileAuthors: names("Toby Ord"), olTitle: "The Precipice", olAuthors: names("Toby Ord"), gbTitle: "The Precipice", gbAuthors: names("Toby Ord")}, "title", []string{"The Precipice"}},
		{"authors", v7Row{lang: "en", fileTitle: "Chess Story", fileAuthors: zb, olTitle: "Chess Story", olAuthors: zb, gbTitle: "Chess Story", gbAuthors: zb}, "authors", zb},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d, err := decide.AtVersion(t.Context(), withOldCalibre(tt.row.pkg()), 7)
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range d.Proposals {
				if p.Field == tt.field && slices.Equal(p.Proposed, tt.want) {
					return
				}
			}
			t.Fatalf("no %s proposal %v in %+v", tt.field, tt.want, d.Proposals)
		})
	}
}

// TestTranslatorsAreNotProposedAsAuthors: files and Open Library list a
// translator, Google doesn't (or the reverse). The lists are consistent, so
// the book is Tier A, but no pair of roots may propose adding or dropping the
// name. The style-only title cases are the same rule.
func TestTranslatorsAreNotProposedAsAuthors(t *testing.T) {
	zb, z := names("Stefan Zweig", "Anthea Bell"), names("Stefan Zweig")
	for _, tt := range []struct {
		name    string
		row     v7Row
		calibre func(*evidence.Package)
		field   string
	}{
		{"file and Open Library add a translator", v7Row{lang: "en", fileTitle: "Chess Story", fileAuthors: zb, olTitle: "Chess Story", olAuthors: zb, gbTitle: "Chess Story", gbAuthors: z},
			func(p *evidence.Package) { p.Calibre.Authors = z }, "authors"},
		{"file and Google omit the translator (book 2521)", v7Row{lang: "en", fileTitle: "Chess Story", fileAuthors: z, olTitle: "Chess Story", olAuthors: zb, gbTitle: "Chess Story", gbAuthors: z},
			func(p *evidence.Package) { p.Calibre.Authors = names("Unknown Author") }, "authors"},
		{"the article only", v7Row{lang: "en", fileTitle: "The Precipice", fileAuthors: names("Toby Ord"), olTitle: "The Precipice", olAuthors: names("Toby Ord"), gbTitle: "Precipice", gbAuthors: names("Toby Ord")},
			func(p *evidence.Package) { p.Calibre.Title = "Precipice" }, "title"},
		{"ampersand only", v7Row{lang: "en", fileTitle: "Food and Fun", fileAuthors: names("Tom Wild"), olTitle: "Food and Fun", olAuthors: names("Tom Wild"), gbTitle: "Food & Fun", gbAuthors: names("Tom Wild")},
			func(p *evidence.Package) { p.Calibre.Title = "Food & Fun" }, "title"},
		{"an edition credit gained", v7Row{lang: "en", fileTitle: "History (Illustrated Edition)", fileAuthors: names("Tom Wild"), olTitle: "History (Illustrated Edition)", olAuthors: names("Tom Wild"), gbTitle: "History", gbAuthors: names("Tom Wild")},
			func(p *evidence.Package) { p.Calibre.Title = "History" }, "title"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := tt.row.pkg()
			tt.calibre(&p)
			up, err := decide.ProposalsAtV6Ungated(t.Context(), p)
			if err != nil || !slices.Contains(fieldsOf(up), tt.field) {
				t.Fatalf("version 6's rules propose no %s here, so the case proves nothing: %v %v", tt.field, fieldsOf(up), err)
			}
			d, err := decide.AtVersion(t.Context(), p, 7)
			if err != nil {
				t.Fatal(err)
			}
			if d.Tier != evidence.TierA {
				t.Fatalf("tier = %s %v, want A", d.Tier, d.Reasons)
			}
			f := proposalFields(d)
			if slices.Contains(f, tt.field) {
				t.Fatalf("proposed %s: %+v", tt.field, d.Proposals)
			}
			if !slices.Contains(f, "publisher") {
				t.Fatalf("the publisher, which every root states, was not proposed: %v", f)
			}
		})
	}
}

// TestSupersetProposesNoAuthors: an author list that is a superset of
// another is consistent, but no authors proposal comes from it, whichever
// roots hold the longer list. A proposal exists only where every record
// states the same list (ADR-020, ADR-027, ADR-031).
func TestSupersetProposesNoAuthors(t *testing.T) {
	freud := []string{"Sigmund Freud"}
	superset := v7Rows()[12].pkg() // file and Google [Freud], Open Library [Freud, Gay]
	superset.Calibre.Authors = []string{"Unknown Author"}
	d, err := decide.AtVersion(t.Context(), superset, 7)
	if err != nil {
		t.Fatal(err)
	}
	if d.Tier != evidence.TierA || slices.Contains(proposalFields(d), "authors") {
		t.Fatalf("superset: %s %v %+v, want tier A and no authors proposal", d.Tier, d.Reasons, d.Proposals)
	}
	strict := v7Rows()[12].pkg()
	strict.Lookups[0].Results[0].Records[0].Authors = freud
	strict.Calibre.Authors = []string{"Unknown Author"}
	d, err = decide.AtVersion(t.Context(), strict, 7)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range d.Proposals {
		if p.Field == "authors" {
			found = slices.Equal(p.Proposed, freud)
		}
	}
	if !found {
		t.Fatalf("every record states [Freud], yet no authors proposal: %+v", d.Proposals)
	}
}

func fieldsOf(ps []evidence.Proposal) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Field)
	}
	return out
}

// TestISBNProposalStaysExactV7: the ISBN proposal keeps version 6's
// requirements, an identical title key and a full match by version 6's
// rules, so no version 7 tolerance can add one.
func TestISBNProposalStaysExactV7(t *testing.T) {
	build := func(fileTitle, recTitle string, fileAuthors, recAuthors []string) evidence.Package {
		p := book([]evidence.FormatEvidence{epub(fileTitle, fileAuthors, []string{"en"}, []string{I})},
			lookup(I, []string{evidence.SeedFile}, ok(evidence.SourceOpenLibrary, rec("/books/OL9M", recTitle, recAuthors, []string{"eng"}, I))))
		p.Calibre.Title, p.Calibre.Authors = fileTitle, fileAuthors
		p.Calibre.Identifiers = map[string]string{"isbn": "978-0-306-40615-7"}
		return p
	}
	for _, tt := range []struct {
		name string
		p    evidence.Package
		want bool
	}{
		{"exact title and authors", build("The Precipice", "The Precipice", names("Toby Ord"), names("Toby Ord")), true},
		{"article differs", build("The Precipice", "Precipice", names("Toby Ord"), names("Toby Ord")), false},
		{"honorific differs", build("Jason Fung", "Jason Fung", names("Jason Fung"), names("Dr. Jason Fung")), false},
		{"record lacks a file co-author", build("Civilization", "Civilization", names("Sigmund Freud", "Peter Gay"), names("Sigmund Freud")), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d, err := decide.AtVersion(t.Context(), tt.p, 7)
			if err != nil {
				t.Fatal(err)
			}
			if got := slices.Contains(fieldsOf(d.Proposals), "isbn"); got != tt.want {
				t.Fatalf("isbn proposed = %v, want %v (%+v)", got, tt.want, d.Proposals)
			}
		})
	}
}

// TestOtherWorkGuardV7: records that agree with each other up to a version 7
// tolerance are one work, so the other-work guard fires on them; records that
// disagree for real are not one work, so it does not.
func TestOtherWorkGuardV7(t *testing.T) {
	row := v7Rows()[0]
	p := row.pkg()
	p.Calibre.Title, p.Calibre.Authors = "Another Book", []string{"Someone Else"}
	d, err := decide.AtVersion(t.Context(), p, 7)
	if err != nil {
		t.Fatal(err)
	}
	if d.Tier != evidence.TierC || !slices.Contains(d.Reasons, "other_work") || len(d.Proposals) != 0 {
		t.Fatalf("version 7 = %+v, want C with other_work and no proposals", d)
	}
	q := row.pkg()
	q.Calibre.Title, q.Calibre.Authors = "Another Book", []string{"Someone Else"}
	q.Lookups[0].Results[1].Records[0].Title = "A Different Thing"
	d, err = decide.AtVersion(t.Context(), q, 7)
	if err != nil || slices.Contains(d.Reasons, "other_work") || !slices.Contains(d.Reasons, titleConflict) {
		t.Fatalf("version 7 = %+v %v, want records_title_conflict and no other_work", d, err)
	}
}

// TestFrontMatterDisputeStaysStrictV7: a tolerance never keeps a
// front-matter identity that version 6 dropped for a record conflict.
func TestFrontMatterDisputeStaysStrictV7(t *testing.T) {
	for _, tt := range []struct {
		name  string
		other evidence.SourceResult
	}{
		{"Open Library adds an article", ok(evidence.SourceOpenLibrary, rec("/books/OL1M", "Der "+T, []string{A}, []string{"deu"}, I))},
		{"Google lists a contributor", ok(evidence.SourceGoogleBooks, rec("g", T, []string{A, "Klaus Fritz"}, []string{"deu"}, I))},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := frontPkg([]evidence.ISBNContext{copyrightISBN(I, "")}, ok(evidence.SourceDNB, dnbT()), tt.other)
			d6, err := decide.AtVersion(t.Context(), p, 6)
			if err != nil {
				t.Fatal(err)
			}
			d7, err := decide.AtVersion(t.Context(), p, 7)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Contains(d6.Reasons, "frontmatter_isbn_unconfirmed") {
				t.Fatalf("version 6 = %+v, want the identity dropped", d6)
			}
			j6, _ := json.Marshal(d6)
			j7, _ := json.Marshal(d7)
			if string(j6) != string(j7) {
				t.Fatalf("version 7 = %s, want version 6's %s", j7, j6)
			}
		})
	}
}

// TestFrontMatterConfirmationStaysStrictV7: a record that needs a version 7
// tolerance does not confirm a front-matter identity; the identity path
// keeps version 6's match.
func TestFrontMatterConfirmationStaysStrictV7(t *testing.T) {
	loose := rec("958577617", T, []string{"Dr. " + A}, []string{"deu"}, I)
	p := frontPkg([]evidence.ISBNContext{copyrightISBN(I, "")}, ok(evidence.SourceDNB, loose))
	for _, version := range []int{6, 7} {
		d, err := decide.AtVersion(t.Context(), p, version)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(d.Reasons, "frontmatter_isbn_unconfirmed") {
			t.Fatalf("version %d = %+v, want frontmatter_isbn_unconfirmed", version, d)
		}
	}
}

// v7GoldenFixtures are the version 7 rows plus a few untouched books.
func v7GoldenFixtures() map[string]evidence.Package {
	fx := map[string]evidence.Package{"tier A": tierA()}
	for _, r := range v7Rows() {
		fx[r.name] = r.pkg()
	}
	return fx
}

// TestReplayGoldenV7Fixtures: the version 7 fixtures derive at versions 1 to
// 6 the bytes the code before version 7 derived. testdata/replay_v7_golden.json
// was generated on that code and is never regenerated on code that has v7.
func TestReplayGoldenV7Fixtures(t *testing.T) {
	const file = "testdata/replay_v7_golden.json"
	got := map[string]json.RawMessage{}
	for name, p := range v7GoldenFixtures() {
		for version := 1; version <= 6; version++ {
			d, err := decide.AtVersion(t.Context(), p, version)
			if err != nil {
				t.Fatalf("%s v%d: %v", name, version, err)
			}
			raw, _ := json.Marshal(d)
			got[name+"/v"+strconv.Itoa(version)] = raw
		}
	}
	if os.Getenv("UPDATE_GOLDEN_V7") != "" {
		out, _ := json.MarshalIndent(got, "", " ")
		if err := os.WriteFile(file, append(out, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var wantMap map[string]json.RawMessage
	if err := json.Unmarshal(want, &wantMap); err != nil {
		t.Fatal(err)
	}
	if len(wantMap) != len(got) {
		t.Fatalf("golden has %d decisions, derived %d", len(wantMap), len(got))
	}
	for k, g := range got {
		var a, b any
		_ = json.Unmarshal(g, &a)
		_ = json.Unmarshal(wantMap[k], &b)
		ga, _ := json.Marshal(a)
		gb, _ := json.Marshal(b)
		if string(ga) != string(gb) {
			t.Errorf("%s changed:\n got %s\nwant %s", k, g, wantMap[k])
		}
	}
}
