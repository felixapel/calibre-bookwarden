package decide_test

import (
	"encoding/json"
	"os"
	"strconv"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

// hinted is an EPUB format without a metadata ISBN whose front matter was
// read (schema 2): its hints carry the given contexts.
func hinted(title string, ctx ...evidence.ISBNContext) evidence.FormatEvidence {
	f := epub(title, []string{A}, []string{"de"}, nil)
	h := &evidence.ISBNHints{Method: evidence.HintEPUBText, State: evidence.HintOK, Pages: []int{1, 2}, Context: ctx}
	for _, c := range ctx {
		h.ISBNs = append(h.ISBNs, c.ISBN)
	}
	f.ISBNHints = h
	return f
}

func copyrightISBN(isbn, qualifier string) evidence.ISBNContext {
	return evidence.ISBNContext{ISBN: isbn, Landmark: evidence.LandmarkCopyright, Qualifier: qualifier}
}

func schema2(p evidence.Package) evidence.Package {
	p.SchemaVersion = evidence.SchemaVersion
	return p
}

func otherWorkRecord() evidence.SourceRecord {
	return rec("958577618", "Another Book", []string{"Someone Else"}, []string{"deu"}, I)
}

// replayFixtures are packages whose decisions at versions 1 to 5 are pinned
// in testdata/replay_golden.json: every sealed older package must derive the
// same bytes after a new version lands.
func replayFixtures() map[string]evidence.Package {
	fx := map[string]evidence.Package{"tier A": tierA()}
	p := tierA()
	p.Calibre.Languages = nil
	fx["language proposal"] = p
	p = tierA()
	p.Calibre.Identifiers = map[string]string{"isbn": "978-0-306-40615-7"}
	fx["isbn proposal"] = p
	p = tierA()
	p.Calibre.Identifiers = map[string]string{"isbn": "978-0-306-40615-7"}
	p.Lookups[0].Results[0].Records[0].Authors = []string{"Someone Else"}
	fx["isbn proposal, record authors differ"] = p
	p = tierA()
	p.Calibre.Title, p.Calibre.Authors = "Another Book", []string{"Someone Else"}
	fx["other work on the v5 path"] = p
	fx["no records"] = book([]evidence.FormatEvidence{epub(T, []string{A}, []string{"de"}, []string{I})})
	fx["hints, no metadata ISBN, confirmed"] = schema2(book([]evidence.FormatEvidence{hinted(T, copyrightISBN(I, evidence.QualifierEbook))},
		lookup(I, []string{evidence.HintEPUBText}, ok(evidence.SourceDNB, dnbT()))))
	fx["hints, no metadata ISBN, other work"] = schema2(book([]evidence.FormatEvidence{hinted(T, copyrightISBN(I, ""))},
		lookup(I, []string{evidence.HintEPUBText}, ok(evidence.SourceDNB, otherWorkRecord()))))
	fx["hints, no metadata ISBN, two"] = schema2(book([]evidence.FormatEvidence{hinted(T, copyrightISBN(I, ""), copyrightISBN("9780306406157", ""))},
		lookup(I, []string{evidence.HintEPUBText}, ok(evidence.SourceDNB, dnbT()))))
	withISBN := hinted(T, copyrightISBN("9780306406157", evidence.QualifierEbook))
	withISBN.Metadata.ISBNs = []string{I}
	fx["hints and a metadata ISBN"] = schema2(book([]evidence.FormatEvidence{withISBN}, lookup(I, []string{evidence.SeedFile}, ok(evidence.SourceDNB, dnbT()))))
	// Added with version 6: its output at versions 1 to 5 is not older code's.
	p = frontPkg([]evidence.ISBNContext{copyrightISBN(I, "")}, ok(evidence.SourceDNB, dnbT()))
	p.Calibre.Identifiers, p.Calibre.Languages = map[string]string{"isbn": I2}, []string{"deu"}
	fx["v6: Calibre's e-book ISBN, the page prints the print ISBN"] = p
	p = tierA()
	p.Calibre.Title, p.Calibre.Authors = "Another Book", []string{"Someone Else"}
	fx["v6: other work, files and records agree"] = p
	p = tierA()
	p.Calibre.Title, p.Calibre.Authors = "Another Book", []string{"Unknown"}
	fx["v6: placeholder Calibre authors"] = p
	fx["v6: front matter, record conflict"] = frontPkg([]evidence.ISBNContext{copyrightISBN(I, "")}, ok(evidence.SourceDNB, dnbT()),
		ok(evidence.SourceGoogleBooks, rec("g", "A Different Title", []string{A}, []string{"deu"}, I)))
	fx["wikidata and hardcover"] = book([]evidence.FormatEvidence{epub(T, []string{A}, []string{"de"}, []string{I})},
		lookup(I, []string{evidence.SeedFile}, ok(evidence.SourceWikidata, rec("Q1", T, []string{A}, []string{"deu"}, I)),
			ok(evidence.SourceHardcover, rec("1", T, []string{A}, []string{"deu"}, I))))
	return fx
}

// TestReplayGolden: versions 1 to 6 derive the bytes they derived before
// version 6, hints or not. Regenerate with UPDATE_GOLDEN=1 only on code that
// predates the version being added.
func TestReplayGolden(t *testing.T) {
	const file = "testdata/replay_golden.json"
	got := map[string]json.RawMessage{}
	for name, p := range replayFixtures() {
		for version := 1; version <= 6; version++ {
			d, err := decide.AtVersion(t.Context(), p, version)
			if err != nil {
				t.Fatalf("%s v%d: %v", name, version, err)
			}
			raw, _ := json.Marshal(d)
			got[name+"/v"+strconv.Itoa(version)] = raw
		}
	}
	if os.Getenv("UPDATE_GOLDEN") != "" {
		out, _ := json.MarshalIndent(got, "", " ")
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
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

// TestHintsChangeNothingBeforeV6: removing every front-matter hint leaves
// the decision of versions 1 to 5 as it is.
func TestHintsChangeNothingBeforeV6(t *testing.T) {
	for name, p := range replayFixtures() {
		bare := p
		bare.Formats = nil
		for _, f := range p.Formats {
			f.ISBNHints = nil
			bare.Formats = append(bare.Formats, f)
		}
		for version := 1; version <= 5; version++ {
			a, err := decide.AtVersion(t.Context(), p, version)
			if err != nil {
				t.Fatal(err)
			}
			b, err := decide.AtVersion(t.Context(), bare, version)
			if err != nil {
				t.Fatal(err)
			}
			ja, _ := json.Marshal(a)
			jb, _ := json.Marshal(b)
			if string(ja) != string(jb) {
				t.Errorf("%s v%d: hints changed the decision\nwith:    %s\nwithout: %s", name, version, ja, jb)
			}
		}
	}
}
