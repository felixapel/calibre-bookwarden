package findings

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strconv"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

// historyGolden pins what versions 1 to 4 derive for a corpus of packages. A
// later version adds rules only above itself, so these bytes never change;
// the file was generated from the version-4 code before findings v5 landed.
// Regenerating it needs UPDATE_FINDINGS_HISTORY=1 and a reason in review.
const historyGolden = "testdata/derive_v1_v4_golden.json"

type historyEntry struct {
	Findings []evidence.Finding `json:"findings"`
	Error    string             `json:"error,omitempty"`
}

// historyCorpus covers the existing fixtures' shapes (clean, file states,
// capture limits, covers) and every input the v5 detectors flag, so a v5 rule
// that leaked into an older version would change a golden entry.
func historyCorpus() map[string]evidence.Package {
	edit := func(f func(*evidence.Package)) evidence.Package {
		p := goodPackage()
		f(&p)
		return p
	}
	withCover := func(f func(*evidence.Package)) evidence.Package {
		return edit(func(p *evidence.Package) {
			p.Calibre.HasCover = true
			p.Cover = decoded(800, 1200, 500)
			f(p)
		})
	}
	titled := func(title string) evidence.Package {
		return edit(func(p *evidence.Package) { p.Calibre.Title = title; p.Formats[0].Metadata.Title = title })
	}
	authored := func(authors ...string) evidence.Package {
		return edit(func(p *evidence.Package) { p.Calibre.Authors = authors; p.Formats[0].Metadata.Authors = authors })
	}
	published := func(publisher string) evidence.Package {
		return edit(func(p *evidence.Package) { p.Calibre.Publisher = publisher })
	}
	dated := func(pubdate string) evidence.Package {
		return edit(func(p *evidence.Package) {
			p.Calibre.PubDate = pubdate
			p.Calibre.LastModified = "2020-05-05T00:00:00Z"
		})
	}
	return map[string]evidence.Package{
		"clean":               goodPackage(),
		"no formats":          edit(func(p *evidence.Package) { p.Formats = nil }),
		"empty file":          edit(func(p *evidence.Package) { p.Formats[0].State = evidence.StateEmpty }),
		"title mismatch":      edit(func(p *evidence.Package) { p.Formats[0].Metadata.Title = "Other" }),
		"january first":       edit(func(p *evidence.Package) { p.Calibre.PubDate = "2020-01-01"; p.Formats[0].Metadata.Date = "2020" }),
		"title sort mismatch": edit(func(p *evidence.Package) { p.Calibre.TitleSort = ptr("Something Else") }),
		"capture limits": edit(func(p *evidence.Package) {
			p.Formats[0].Metadata.Limits = &evidence.MetadataLimits{Version: 1, Title: true}
		}),
		"cover ok":          withCover(func(*evidence.Package) {}),
		"cover low":         withCover(func(p *evidence.Package) { p.Cover = decoded(100, 100, 500) }),
		"junk title word":   titled("Microsoft Word - report.doc"),
		"junk title ext":    titled("scan0042.pdf"),
		"junk title blank":  titled("Untitled"),
		"junk title digits": titled("20190312_153045"),
		"noise series":      titled("Emma (Penguin Classics)"),
		"noise translator":  titled("Der Zauberberg, Translated by Helen Lowe-Porter"),
		"noise prefix": edit(func(p *evidence.Package) {
			p.Calibre.Authors = []string{"Thomas Mann"}
			p.Calibre.Title = "Mann, Thomas - Der Zauberberg"
		}),
		"noise edition":     titled("Emma (Annotated)"),
		"placeholder date":  dated("0101-01-01T00:00:00+00:00"),
		"future date":       dated("2031-01-01T00:00:00+00:00"),
		"joined and":        authored("John Smith and Jane Doe"),
		"joined ampersand":  authored("Smith & Jones"),
		"joined semicolon":  authored("Adam Ant; Bea Bee"),
		"junk publisher":    published("Unknown"),
		"publisher domain":  published("www.example.com"),
		"publisher scanner": published("Z-Library"),
		"publisher real":    published("Smashwords"),
	}
}

func deriveHistory(p evidence.Package, version int) historyEntry {
	fs, err := DeriveVersion(context.Background(), p, version)
	if err != nil {
		return historyEntry{Findings: []evidence.Finding{}, Error: err.Error()}
	}
	if fs == nil {
		fs = []evidence.Finding{}
	}
	return historyEntry{Findings: fs}
}

func TestHistoricalVersionsAreUnchanged(t *testing.T) {
	corpus := historyCorpus()
	if os.Getenv("UPDATE_FINDINGS_HISTORY") != "" {
		golden := map[string]map[string]historyEntry{}
		for name, p := range corpus {
			golden[name] = map[string]historyEntry{}
			for v := 1; v <= 4; v++ {
				golden[name][strconv.Itoa(v)] = deriveHistory(p, v)
			}
		}
		raw, err := json.MarshalIndent(golden, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(historyGolden, append(raw, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(historyGolden)
	if err != nil {
		t.Fatal(err)
	}
	var golden map[string]map[string]historyEntry
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatal(err)
	}
	if len(golden) != len(corpus) {
		t.Fatalf("golden has %d packages, corpus %d", len(golden), len(corpus))
	}
	for name, p := range corpus {
		for v := 1; v <= 4; v++ {
			want, ok := golden[name][strconv.Itoa(v)]
			if !ok {
				t.Fatalf("%s: no golden for version %d", name, v)
			}
			got := deriveHistory(p, v)
			if got.Error != want.Error || !slices.Equal(got.Findings, want.Findings) {
				t.Errorf("%s at version %d changed:\n got %+v\nwant %+v", name, v, got, want)
			}
		}
	}
}
