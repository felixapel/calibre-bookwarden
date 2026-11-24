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

// Version 9 pinned before version 10 (plan v4, C1): the cases below close the
// gaps a mutation run of v9 found, and TestReplayGoldenV10Fixtures pins what
// versions 1 to 9 decide on every fixture, byte for byte.

type v10Case struct {
	name string
	p    evidence.Package
	// field is the proposal the case is about; want its proposed values, nil
	// for no proposal of field.
	field string
	want  []string
	// reason must (has) or must not (hasNot) be among the decision's reasons.
	has, hasNot string
}

func v10Cases() []v10Case {
	return []v10Case{
		// The v9 pubdate window (v9.go sameLocalDate): Calibre's timestamp
		// may lie up to 14 h before the key's midnight, read as UTC.
		{name: "pubdate: 14 h before the key's midnight is a time zone shift",
			p: v9Dated("2005-03-24T10:00:00Z", "2005-03-25"), field: "pubdate"},
		{name: "pubdate: a second more is a real difference",
			p: v9Dated("2005-03-24T09:59:59Z", "2005-03-25"), field: "pubdate", want: []string{"2005-03-25"}},
		// The westmost edge, 12 h after the key's midnight, and a second past
		// it: the timestamp's own UTC date is the key, so both match.
		{name: "pubdate: 12 h after the key's midnight",
			p: v9Dated("2005-03-25T12:00:00Z", "2005-03-25"), field: "pubdate"},
		{name: "pubdate: a second past 12 h after the key's midnight",
			p: v9Dated("2005-03-25T12:00:01Z", "2005-03-25"), field: "pubdate"},

		// Two formats state the same ISBN: one labels it ebook beside a print
		// ISBN, the other prints it as its only ISBN. Their flags combine
		// with AND (text.go collectText), so neither holds and the ISBN is
		// not filled from the text alone.
		{name: "ISBN fill refused: ebook-labelled in one format, the only one printed in the other",
			p: v8Book(factSet{"isbn": isbnFact(labelled(I, "ebook"), labelled(I3, "print"))}, func(p *evidence.Package) {
				noISBNs(p)
				secondEPUB(p, factSet{"isbn": isbnFact(av(I))})
			}), field: "isbn", has: "isbn_missing"},

		// ISBN dissent from a record root alone (text.go textDissent): the
		// identity comes from the copyright page and differs from the text.
		{name: "ISBN: the records' ISBN dissents from the text",
			p: func() evidence.Package {
				r := dnbT()
				r.ISBNs = []string{I3}
				p := schema2(book([]evidence.FormatEvidence{hinted(T, copyrightISBN(I3, evidence.QualifierEbook))},
					lookup(I3, []string{evidence.HintEPUBText}, ok(evidence.SourceDNB, r))))
				p.Calibre.Identifiers = nil
				factSet{"isbn": isbnFact(av(I))}.stamp(&p)
				return p
			}(), field: "isbn", has: "text_roots_disagree"},

		// A record that keeps the subtitle in its own field states the
		// joined title, so it does not dissent from the text.
		{name: "title: a record with the subtitle in its own field agrees",
			p: v8Book(factSet{"title": agreedFact("title", av(printedTitle)), "subtitle": agreedFact("subtitle", av(printedSub))}, func(p *evidence.Package) {
				p.Calibre.Title, p.Formats[0].Metadata.Title = "Untitled", ""
				p.Lookups[0].Results[0].Records[0].Subtitle = printedSub
			}), field: "title", want: []string{printedTitle + ": " + printedSub}, hasNot: "text_roots_disagree"},
	}
}

func TestDecisionV9Pinned(t *testing.T) {
	for _, tt := range v10Cases() {
		t.Run(tt.name, func(t *testing.T) {
			d, err := decide.AtVersion(t.Context(), tt.p, 9)
			if err != nil {
				t.Fatal(err)
			}
			switch p := proposalOf(d, tt.field); {
			case tt.want == nil && p != nil:
				t.Errorf("proposes %+v, want no %s proposal", *p, tt.field)
			case tt.want != nil && (p == nil || !slices.Equal(p.Proposed, tt.want)):
				t.Errorf("%s proposal %+v, want %q", tt.field, p, tt.want)
			}
			if tt.has != "" && !slices.Contains(d.Reasons, tt.has) {
				t.Errorf("reasons %v lack %s", d.Reasons, tt.has)
			}
			if tt.hasNot != "" && slices.Contains(d.Reasons, tt.hasNot) {
				t.Errorf("reasons %v have %s", d.Reasons, tt.hasNot)
			}
		})
	}
}

// subtitleWithoutValue is malformed (evidence.Check refuses it), but Seal
// decides before it validates.
func subtitleWithoutValue() evidence.Package {
	return v8Book(baseFacts().with(agreedFact("subtitle")), nil)
}

// An agreed subtitle without a value states no title, and nothing panics.
func TestDecideAgreedSubtitleWithoutValue(t *testing.T) {
	for version := 8; version <= decide.Version; version++ {
		d, err := decide.AtVersion(t.Context(), subtitleWithoutValue(), version)
		if err != nil {
			t.Fatalf("v%d: %v", version, err)
		}
		if pr := proposalOf(d, "title"); pr != nil && slices.Equal(pr.Roots, []string{"text"}) {
			t.Errorf("v%d: a text-alone title from a subtitle without a value: %+v", version, *pr)
		}
	}
}

// v10GoldenFixtures are the cases above plus every fixture the four earlier
// goldens use (replay_golden.json and the v8, v9 ones, which include v7's).
func v10GoldenFixtures() map[string]evidence.Package {
	fx := map[string]evidence.Package{"subtitle without a value": subtitleWithoutValue()}
	for _, c := range v10Cases() {
		fx[c.name] = c.p
	}
	for name, p := range v9GoldenFixtures() {
		fx["v9: "+name] = p
	}
	for name, p := range replayFixtures() {
		fx["v1: "+name] = p
	}
	return fx
}

// TestReplayGoldenV10Fixtures: every fixture derives at versions 1 to 9 the
// bytes the code before version 10 derived. testdata/replay_v10_golden.json
// was generated once with UPDATE_GOLDEN_V10 on code without any v10 rule;
// never regenerate it: a difference here is a change to a sealed version.
func TestReplayGoldenV10Fixtures(t *testing.T) {
	const file = "testdata/replay_v10_golden.json"
	got := map[string]json.RawMessage{}
	for name, p := range v10GoldenFixtures() {
		for version := 1; version <= 9; version++ {
			d, err := decide.AtVersion(t.Context(), p, version)
			if err != nil {
				t.Fatalf("%s v%d: %v", name, version, err)
			}
			raw, _ := json.Marshal(d)
			got[name+"/v"+strconv.Itoa(version)] = raw
		}
	}
	if os.Getenv("UPDATE_GOLDEN_V10") != "" {
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
