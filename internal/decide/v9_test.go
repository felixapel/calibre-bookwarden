package decide_test

import (
	"encoding/json"
	"os"
	"slices"
	"strconv"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/findings"
)

// Decision version 9 (ADR-036, "Decision v9"): a pubdate that only shifts the
// time zone is no proposal, and a text-rooted publisher printed in capitals is
// shown in title case.

// v9Dated is the base book whose file and DNB record state sourceDate while
// Calibre's pubdate is calibreDate.
func v9Dated(calibreDate, sourceDate string) evidence.Package {
	return v8Book(baseFacts(), func(p *evidence.Package) {
		p.Calibre.PubDate = calibreDate
		p.Formats[0].Metadata.Date = sourceDate
		p.Lookups[0].Results[0].Records[0].Date = sourceDate
	})
}

// v9Imprint is a book whose Calibre publisher is empty and whose text states
// printed as its imprint. The file states file; the DNB record states record,
// or no record exists when record is empty.
func v9Imprint(printed, file, record string) evidence.Package {
	return v8Book(factSet{"imprint": agreedFact("imprint", av(printed))}, func(p *evidence.Package) {
		p.Calibre.Publisher, p.Formats[0].Metadata.Publisher = "", file
		if record == "" {
			noLookups(p)
			return
		}
		p.Lookups[0].Results[0].Records[0].Publisher = record
	})
}

func proposalOf(d decide.Decision, field string) *evidence.Proposal {
	for i := range d.Proposals {
		if d.Proposals[i].Field == field {
			return &d.Proposals[i]
		}
	}
	return nil
}

func TestDecisionV9Pubdate(t *testing.T) {
	const berlin = "2005-03-24T23:00:00+00:00" // 25 March in Berlin
	for _, tt := range []struct {
		name         string
		calibre, src string
		v8, v9       []string // the proposed pubdate, nil for none
	}{
		{"the next day, Calibre's time zone shift", berlin, "2005-03-25", []string{"2005-03-25"}, nil},
		{"the same UTC day", berlin, "2005-03-24", nil, nil},
		{"two days later is a real difference", berlin, "2005-03-26", []string{"2005-03-26"}, []string{"2005-03-26"}},
		{"the day before is a real difference", berlin, "2005-03-23", []string{"2005-03-23"}, []string{"2005-03-23"}},
		{"midnight UTC, a real difference", "2005-03-24T00:00:00+00:00", "2005-03-26", []string{"2005-03-26"}, []string{"2005-03-26"}},
		{"midnight UTC, the next day", "2005-03-24T00:00:00+00:00", "2005-03-25", []string{"2005-03-25"}, []string{"2005-03-25"}},
		{"the day before a morning timestamp is real", "2005-03-24T05:00:00+00:00", "2005-03-23", []string{"2005-03-23"}, []string{"2005-03-23"}},
		{"midnight UTC, the day before is real", "2005-03-24T00:00:00+00:00", "2005-03-23", []string{"2005-03-23"}, []string{"2005-03-23"}},
		{"an offset timestamp, its UTC date", "2005-03-25T01:00:00+02:00", "2005-03-24", []string{"2005-03-24"}, nil},
		{"an offset timestamp, the next day is real", "2005-03-25T01:00:00+02:00", "2005-03-26", []string{"2005-03-26"}, []string{"2005-03-26"}},
		{"a malformed timestamp behaves as v8", "2005-03-24T25:00:00+00:00", "2005-03-25", []string{"2005-03-25"}, []string{"2005-03-25"}},
		{"a date without a time is exact", "2005-03-24", "2005-03-25", []string{"2005-03-25"}, []string{"2005-03-25"}},
		{"a date without a time, equal", "2005-03-24", "2005-03-24", nil, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for version, want := range map[int][]string{8: tt.v8, 9: tt.v9} {
				d, err := decide.AtVersion(t.Context(), v9Dated(tt.calibre, tt.src), version)
				if err != nil {
					t.Fatal(err)
				}
				p := proposalOf(d, "pubdate")
				switch {
				case want == nil && p != nil:
					t.Errorf("v%d: proposes %+v, want none", version, *p)
				case want != nil && (p == nil || !slices.Equal(p.Proposed, want) || !slices.Equal(p.Current, []string{tt.calibre})):
					t.Errorf("v%d: pubdate proposal %+v, want %v", version, p, want)
				}
			}
		})
	}
}

// The loose equivalence only removes the pubdate proposal: nothing else moves.
func TestDecisionV9PubdateChangesNothingElse(t *testing.T) {
	p := v9Dated("2005-03-24T23:00:00+00:00", "2005-03-25")
	now, err := decide.AtVersion(t.Context(), p, 9)
	if err != nil {
		t.Fatal(err)
	}
	old, err := decide.AtVersion(t.Context(), p, 8)
	if err != nil {
		t.Fatal(err)
	}
	old.Proposals = slices.DeleteFunc(old.Proposals, func(p evidence.Proposal) bool { return p.Field == "pubdate" })
	a, _ := json.Marshal(now)
	b, _ := json.Marshal(old)
	if string(a) != string(b) {
		t.Errorf("v9 differs from v8 beyond the pubdate\n%s\n%s", a, b)
	}
}

func TestDecisionV9Publisher(t *testing.T) {
	for _, tt := range []struct {
		name                  string
		printed, file, record string
		v8, v9                string
		roots                 []string
	}{
		{name: "caps with a corporate suffix", printed: "HYPERION PRESS, INC.", v8: "HYPERION PRESS, INC.", v9: "Hyperion Press, Inc."},
		{name: "initials stay", printed: "ALFRED A. KNOPF", v8: "ALFRED A. KNOPF", v9: "Alfred A. Knopf"},
		{name: "small words", printed: "THE CATHOLIC UNIVERSITY OF AMERICA PRESS", v8: "THE CATHOLIC UNIVERSITY OF AMERICA PRESS", v9: "The Catholic University of America Press"},
		{name: "and and for", printed: "ART AND DESIGN FOR PEOPLE", v8: "ART AND DESIGN FOR PEOPLE", v9: "Art and Design for People"},
		{name: "acronym DK", printed: "DK PUBLISHING", v8: "DK PUBLISHING", v9: "DK Publishing"},
		{name: "acronym CRC", printed: "CRC PRESS", v8: "CRC PRESS", v9: "CRC Press"},
		{name: "acronym BBC", printed: "BBC BOOKS", v8: "BBC BOOKS", v9: "BBC Books"},
		{name: "acronym DC", printed: "DC COMICS", v8: "DC COMICS", v9: "DC Comics"},
		{name: "corporate forms", printed: "VERLAG KIEPENHEUER & WITSCH GMBH & CO. KG", v8: "VERLAG KIEPENHEUER & WITSCH GMBH & CO. KG", v9: "Verlag Kiepenheuer & Witsch GmbH & Co. KG"},
		{name: "ag", printed: "ACME AG", v8: "ACME AG", v9: "Acme AG"},
		{name: "short words with vowels", printed: "NEW DIRECTIONS", v8: "NEW DIRECTIONS", v9: "New Directions"},
		{name: "short words with vowels, pan", printed: "PAN BOOKS", v8: "PAN BOOKS", v9: "Pan Books"},
		{name: "bracketed acronym", printed: "PENGUIN GROUP (USA) INC.", v8: "PENGUIN GROUP (USA) INC.", v9: "Penguin Group (USA) Inc."},
		{name: "leading quote", printed: `"ACME" PRESS`, v8: `"ACME" PRESS`, v9: `"Acme" Press`},
		{name: "abbreviation with a period", printed: "ST. MARTIN'S PRESS", v8: "ST. MARTIN'S PRESS", v9: "St. Martin's Press"},
		{name: "bros", printed: "WARNER BROS.", v8: "WARNER BROS.", v9: "Warner Bros."},
		{name: "dotted initials", printed: "U.S. NAVAL INSTITUTE", v8: "U.S. NAVAL INSTITUTE", v9: "U.S. Naval Institute"},
		{name: "spanish small words", printed: "EDITORIAL DE LA UNIVERSIDAD DE GRANADA", v8: "EDITORIAL DE LA UNIVERSIDAD DE GRANADA", v9: "Editorial de la Universidad de Granada"},
		{name: "french small words", printed: "ÉDITIONS DU SEUIL", v8: "ÉDITIONS DU SEUIL", v9: "Éditions du Seuil"},
		{name: "on", printed: "BOOKS ON TAPE", v8: "BOOKS ON TAPE", v9: "Books on Tape"},
		{name: "o'reilly", printed: "O'REILLY MEDIA, INC.", v8: "O'REILLY MEDIA, INC.", v9: "O'Reilly Media, Inc."},
		{name: "curly o'reilly", printed: "O’REILLY MEDIA", v8: "O’REILLY MEDIA", v9: "O’Reilly Media"},
		{name: "l'ecole", printed: "L'ÉCOLE DES LOISIRS", v8: "L'ÉCOLE DES LOISIRS", v9: "L'École des Loisirs"},
		{name: "suffix without a period", printed: "ACME LTD", v8: "ACME LTD", v9: "Acme Ltd"},
		{name: "suffix with a period", printed: "ACME LLC.", v8: "ACME LLC.", v9: "Acme LLC."},
		{name: "plain caps", printed: "VINTAGE BOOKS", v8: "VINTAGE BOOKS", v9: "Vintage Books"},
		{name: "mixed case stays as printed", printed: "McGraw-Hill Education", v8: "McGraw-Hill Education", v9: "McGraw-Hill Education"},
		{name: "one lowercase letter stays", printed: "VINTAGE bOOKS", v8: "VINTAGE bOOKS", v9: "VINTAGE bOOKS"},
		{name: "the record's spelling wins", printed: "VINTAGE BOOKS", record: "Vintage Books", v8: "Vintage Books", v9: "Vintage Books", roots: []string{"dnb", "text"}},
		{name: "the file's spelling wins", printed: "VINTAGE BOOKS", file: "Vintage Books", v8: "Vintage Books", v9: "Vintage Books", roots: []string{"file", "text"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := v9Imprint(tt.printed, tt.file, tt.record)
			roots := tt.roots
			if roots == nil {
				roots = []string{"text"}
			}
			for version, want := range map[int]string{8: tt.v8, 9: tt.v9} {
				d, err := decide.AtVersion(t.Context(), p, version)
				if err != nil {
					t.Fatal(err)
				}
				got := proposalOf(d, "publisher")
				if got == nil || !slices.Equal(got.Proposed, []string{want}) || !slices.Equal(got.Roots, roots) {
					t.Errorf("v%d: publisher proposal %+v, want %q from %v", version, got, want, roots)
					continue
				}
				if tt.record == "" && tt.file == "" && findings.PublisherKey(got.Proposed[0]) != findings.PublisherKey(tt.printed) {
					t.Errorf("v%d: key of %q differs from the key of %q", version, got.Proposed[0], tt.printed)
				}
			}
		})
	}
}

// Rule 1 in the text-alone path: a record that vetoes the main path leaves the
// text alone, and the file that states the same key lends its spelling.
func TestDecisionV9PublisherRuleOneTextAlone(t *testing.T) {
	p := v9Imprint("MCGRAW-HILL EDUCATION", "McGraw-Hill Education", "Unknown")
	for version, want := range map[int]string{8: "MCGRAW-HILL EDUCATION", 9: "McGraw-Hill Education"} {
		d, err := decide.AtVersion(t.Context(), p, version)
		if err != nil {
			t.Fatal(err)
		}
		got := proposalOf(d, "publisher")
		if got == nil || !slices.Equal(got.Proposed, []string{want}) || !slices.Equal(got.Roots, []string{"text"}) {
			t.Errorf("v%d: publisher proposal %+v, want %q from [text]", version, got, want)
		}
	}
}

// v9GoldenFixtures are the version 9 rows plus the version 8 ones.
func v9GoldenFixtures() map[string]evidence.Package {
	fx := map[string]evidence.Package{
		"pubdate: shifted by the time zone":    v9Dated("2005-03-24T23:00:00+00:00", "2005-03-25"),
		"pubdate: the same UTC day":            v9Dated("2005-03-24T23:00:00+00:00", "2005-03-24"),
		"pubdate: a real difference":           v9Dated("2005-03-24T23:00:00+00:00", "2005-03-26"),
		"pubdate: midnight, a real difference": v9Dated("2005-03-24T00:00:00+00:00", "2005-03-26"),
		"pubdate: a date without a time":       v9Dated("2005-03-24", "2005-03-25"),
		"pubdate: a morning, the day before":   v9Dated("2005-03-24T05:00:00+00:00", "2005-03-23"),
		"pubdate: an offset timestamp":         v9Dated("2005-03-25T01:00:00+02:00", "2005-03-24"),
		"pubdate: a malformed timestamp":       v9Dated("2005-03-24T25:00:00+00:00", "2005-03-25"),
		"publisher: acronym":                   v9Imprint("CRC PRESS", "", ""),
		"publisher: rule one, text alone":      v9Imprint("MCGRAW-HILL EDUCATION", "McGraw-Hill Education", "Unknown"),
		"publisher: capitals, the text alone":  v9Imprint("HYPERION PRESS, INC.", "", ""),
		"publisher: initials":                  v9Imprint("ALFRED A. KNOPF", "", ""),
		"publisher: small words":               v9Imprint("THE CATHOLIC UNIVERSITY OF AMERICA PRESS", "", ""),
		"publisher: mixed case":                v9Imprint("McGraw-Hill Education", "", ""),
		"publisher: a record's spelling":       v9Imprint("VINTAGE BOOKS", "", "Vintage Books"),
		"publisher: the file's spelling":       v9Imprint("VINTAGE BOOKS", "Vintage Books", ""),
	}
	for name, p := range v8GoldenFixtures() {
		fx["v8: "+name] = p
	}
	return fx
}

// TestReplayGoldenV9Fixtures: the version 9 fixtures derive at versions 1 to 8
// the bytes the code before version 9 derived. testdata/replay_v9_golden.json
// is generated with UPDATE_GOLDEN_V9 on the decide code of version 8 plus
// these fixtures, never on code that has v9, so a v9 change that leaks into an
// earlier version fails here.
func TestReplayGoldenV9Fixtures(t *testing.T) {
	const file = "testdata/replay_v9_golden.json"
	got := map[string]json.RawMessage{}
	for name, p := range v9GoldenFixtures() {
		for version := 1; version <= 8; version++ {
			d, err := decide.AtVersion(t.Context(), p, version)
			if err != nil {
				t.Fatalf("%s v%d: %v", name, version, err)
			}
			raw, _ := json.Marshal(d)
			got[name+"/v"+strconv.Itoa(version)] = raw
		}
	}
	if os.Getenv("UPDATE_GOLDEN_V9") != "" {
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

// A v9 seal carries the title-cased value, and Open derives it again.
func TestV9Seals(t *testing.T) {
	src := v9Imprint("HYPERION PRESS, INC.", "", "")
	raw, _, err := decide.Seal(t.Context(), src)
	if err != nil {
		t.Fatal(err)
	}
	v9, err := decide.AtVersion(t.Context(), src, 9)
	if err != nil {
		t.Fatal(err)
	}
	raw, seal := reseal(t, raw, func(p *evidence.Package) {
		p.DecisionVersion, p.Tier, p.TierReasons, p.Proposals = 9, v9.Tier, v9.Reasons, v9.Proposals
	})
	q, d, err := decide.Open(t.Context(), raw, seal)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got := proposalOf(d, "publisher"); q.DecisionVersion != 9 || got == nil || !slices.Equal(got.Proposed, []string{"Hyperion Press, Inc."}) {
		t.Errorf("version %d, proposal %+v", q.DecisionVersion, got)
	}
}
