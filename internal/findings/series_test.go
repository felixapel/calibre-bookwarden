package findings

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

// seriesOnly keeps the findings version 6 adds: the two series codes, and
// metadata_unverified about a series field.
func seriesOnly(fs []evidence.Finding) []evidence.Finding {
	var out []evidence.Finding
	for _, f := range fs {
		if f.Code == "series_missing" || f.Code == "series_mismatch" || f.Code == "series_name_mismatch" ||
			f.Code == "metadata_unverified" && strings.HasPrefix(f.Field, "series") {
			out = append(out, f)
		}
	}
	return out
}

func TestVersionSix(t *testing.T) {
	if Version != 6 {
		t.Fatalf("Version = %d, want 6", Version)
	}
	ctx := context.Background()
	if _, err := DeriveVersion(ctx, goodPackage(), 7); !errors.Is(err, ErrUnknownVersion) {
		t.Fatalf("version 7: %v", err)
	}
	for v := 1; v <= 6; v++ {
		if _, err := DeriveVersion(ctx, goodPackage(), v); err != nil {
			t.Fatalf("version %d: %v", v, err)
		}
	}
}

func TestSeriesKey(t *testing.T) {
	same := [][]string{
		{"The Spike Saga", "the spike saga", "Spike Saga", "Spike Saga, The", "THE  SPIKE   SAGA", "The Spike-Saga", "Spike Saga!"},
		{"A Song of Ice", "Song of Ice", "song of ice, a"},
		{"Mr. Mercedes", "Mr Mercedes", "MR MERCEDES"},
		{"Café Noir", "CAFÉ noir"},
	}
	for _, group := range same {
		want := seriesKey(group[0])
		if want == "" {
			t.Errorf("seriesKey(%q) is empty", group[0])
		}
		for _, s := range group[1:] {
			if got := seriesKey(s); got != want {
				t.Errorf("seriesKey(%q) = %q, want %q (as %q)", s, got, want, group[0])
			}
		}
	}
	different := [][2]string{
		{"Spike Saga", "Spike Sagas"},
		{"The Spike Saga", "The Other Saga"},
		{"Cafe Noir", "Café Noir"},
		{"I Robot", "Robot"}, // "I" is not an article here
		{"Die Hard", "Hard"}, // only English articles are ignored
		{"Der Ring", "Ring"},
		{"L'Aventure", "Aventure"},
		{"Le Petit Prince", "Petit Prince"},
		{"The", "A"}, // a lone article is a name
		{"Saga 1", "Saga 2"},
	}
	for _, p := range different {
		if seriesKey(p[0]) == seriesKey(p[1]) {
			t.Errorf("seriesKey(%q) = seriesKey(%q) = %q", p[0], p[1], seriesKey(p[0]))
		}
	}
	for _, s := range []string{"", "   ", "!!!", "—"} {
		if got := seriesKey(s); got != "" {
			t.Errorf("seriesKey(%q) = %q, want empty", s, got)
		}
	}
}

func TestSeriesFindings(t *testing.T) {
	// calibre and file set Calibre's record and the EPUB's metadata.
	with := func(calSeries, calIndex, fileSeries, fileIndex string, more ...func(*evidence.Package)) func(*evidence.Package) {
		return func(p *evidence.Package) {
			p.Calibre.Series, p.Calibre.SeriesIndex = calSeries, calIndex
			p.Formats[0].Metadata.Series, p.Formats[0].Metadata.SeriesIndex = fileSeries, fileIndex
			for _, f := range more {
				f(p)
			}
		}
	}
	limits := func(l evidence.MetadataLimits) func(*evidence.Package) {
		return func(p *evidence.Package) { l.Version = 1; p.Formats[0].Metadata.Limits = &l }
	}
	finding := func(code, field, reason string) []evidence.Finding {
		return []evidence.Finding{{Code: code, Format: "EPUB", Field: field, Reason: reason}}
	}
	tests := []struct {
		name string
		edit func(*evidence.Package)
		want []evidence.Finding
	}{
		{"neither has a series", with("", "", "", ""), nil},
		{"Calibre has no series, the file does", with("", "", "The Spike Saga", "2"), finding("series_missing", "series", "")},
		{"Calibre has no series, the file has a name and no index", with("", "", "The Spike Saga", ""), finding("series_missing", "series", "")},
		{"Calibre has a series, the file none", with("The Spike Saga", "2", "", ""), nil},
		{"same series and index", with("The Spike Saga", "2", "The Spike Saga", "2"), nil},
		{"case differs", with("The Spike Saga", "2", "the spike saga", "2"), nil},
		{"punctuation differs", with("Spike-Saga", "2", "Spike Saga!", "2"), nil},
		{"a leading article differs", with("The Spike Saga", "2", "Spike Saga", "2"), nil},
		{"Calibre's trailing article", with("Spike Saga, The", "2", "The Spike Saga", "2"), nil},
		{"different series", with("The Spike Saga", "2", "The Other Saga", "2"), finding("series_name_mismatch", "series", "")},
		{"a name mismatch hides the index", with("The Spike Saga", "2", "The Other Saga", "3"), finding("series_name_mismatch", "series", "")},
		{"index differs", with("Saga", "2", "Saga", "3"), finding("series_mismatch", "series_index", "")},
		{"fractional index differs", with("Saga", "2.5", "Saga", "2"), finding("series_mismatch", "series_index", "")},
		{"index equal numerically", with("Saga", "2.5", "Saga", "2.5"), nil},
		{"index 10 is not 9", with("Saga", "10", "Saga", "9"), finding("series_mismatch", "series_index", "")},
		{"the file has no index", with("Saga", "2", "Saga", ""), nil},
		{"Calibre has no index", with("Saga", "", "Saga", "2"), nil},
		{"the file's index was junk", with("Saga", "2", "Saga", "", limits(evidence.MetadataLimits{SeriesIndex: true})),
			finding("metadata_unverified", "series_index", "invalid_value")},
		{"the file's series was cut", with("Saga", "2", strings.Repeat("a", 512), "", limits(evidence.MetadataLimits{Series: true})),
			finding("metadata_unverified", "series", "field_too_long")},
		{"a series made of punctuation", with("Saga", "2", "!!!", "2"), nil},
		{"a Calibre series made of punctuation", with("???", "2", "Saga", "2"), nil},
		{"an unreadable format is not compared", with("", "", "Saga", "1", func(p *evidence.Package) { p.Formats[0].State = evidence.StateCorrupt }),
			[]evidence.Finding(nil)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := goodPackage()
			tt.edit(&p)
			fs, err := DeriveVersion(context.Background(), p, 6)
			if err != nil {
				t.Fatal(err)
			}
			if got := seriesOnly(fs); !slices.Equal(got, tt.want) {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
			if cur, err := Derive(context.Background(), p); err != nil || !slices.Equal(cur, fs) {
				t.Fatalf("Derive differs from version 6: %+v, %v", cur, err)
			}
			// Older versions neither emit the codes nor change anything else:
			// each derives what it does for the same package without a series.
			bare := p
			bare.Calibre.Series, bare.Calibre.SeriesIndex = "", ""
			bare.Formats = []evidence.FormatEvidence{p.Formats[0]}
			m := *p.Formats[0].Metadata
			m.Series, m.SeriesIndex = "", ""
			bare.Formats[0].Metadata = &m
			for v := 1; v <= 5; v++ {
				old, err := DeriveVersion(context.Background(), p, v)
				if err != nil {
					t.Fatalf("version %d: %v", v, err)
				}
				want, err := DeriveVersion(context.Background(), bare, v)
				if err != nil {
					t.Fatalf("version %d without series: %v", v, err)
				}
				if !slices.Equal(old, want) {
					t.Errorf("version %d derived %+v, want %+v", v, old, want)
				}
			}
		})
	}
}

func TestSeriesFindingsPerFormat(t *testing.T) {
	p := goodPackage()
	p.Formats = append(p.Formats, evidence.FormatEvidence{Format: "MOBI", State: evidence.StateOK, Detected: "mobi", Metadata: &evidence.FileMetadata{
		Title: "Synthetic", Series: "Other Saga", SeriesIndex: "1",
	}})
	p.Calibre.Series, p.Calibre.SeriesIndex = "Saga", "1"
	p.Formats[0].Metadata.Series, p.Formats[0].Metadata.SeriesIndex = "Saga", "2"
	fs, err := DeriveVersion(context.Background(), p, 6)
	if err != nil {
		t.Fatal(err)
	}
	want := []evidence.Finding{
		{Code: "series_mismatch", Format: "EPUB", Field: "series_index"},
		{Code: "series_name_mismatch", Format: "MOBI", Field: "series"},
	}
	if got := seriesOnly(fs); !slices.Equal(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestSeriesEvidenceIsChecked(t *testing.T) {
	for name, edit := range map[string]func(*evidence.Package){
		"a non-canonical index": func(p *evidence.Package) { p.Formats[0].Metadata.SeriesIndex = "2.50" },
		"a junk index":          func(p *evidence.Package) { p.Formats[0].Metadata.SeriesIndex = "two" },
	} {
		t.Run(name, func(t *testing.T) {
			p := goodPackage()
			edit(&p)
			if _, err := DeriveVersion(context.Background(), p, 6); !errors.Is(err, ErrInvalidEvidence) {
				t.Fatalf("got %v, want ErrInvalidEvidence", err)
			}
			if _, err := DeriveVersion(context.Background(), p, 5); err != nil {
				t.Fatalf("version 5: %v", err)
			}
		})
	}
}

// An overlong series is treated like an overlong title: nothing is compared,
// the book still derives and seals.
func TestOverlongSeriesIsUnverified(t *testing.T) {
	long := strings.Repeat("a", 513)
	tests := []struct {
		name string
		edit func(*evidence.Package)
		want []evidence.Finding
	}{
		{"Calibre series", func(p *evidence.Package) { p.Calibre.Series, p.Calibre.SeriesIndex = long, "1" },
			[]evidence.Finding{{Code: "metadata_unverified", Field: "series", Reason: "field_too_long"}}},
		{"Calibre series, file names another", func(p *evidence.Package) {
			p.Calibre.Series, p.Calibre.SeriesIndex = long, "1"
			p.Formats[0].Metadata.Series = "Other"
		}, []evidence.Finding{{Code: "metadata_unverified", Field: "series", Reason: "field_too_long"}}},
		{"file series", func(p *evidence.Package) { p.Formats[0].Metadata.Series = long },
			[]evidence.Finding{{Code: "metadata_unverified", Format: "EPUB", Field: "series", Reason: "field_too_long"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := goodPackage()
			p.SchemaVersion = evidence.SchemaVersion
			tt.edit(&p)
			fs, err := DeriveVersion(context.Background(), p, 6)
			if err != nil {
				t.Fatal(err)
			}
			if got := seriesOnly(fs); !slices.Equal(got, tt.want) {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
			if _, _, err := Seal(context.Background(), p); err != nil {
				t.Fatalf("the book no longer seals: %v", err)
			}
		})
	}
}

func TestSeriesCodesAreCatalogued(t *testing.T) {
	seen := map[string]Definition{}
	for _, d := range Catalog() {
		seen[d.Code] = d
	}
	for code, severity := range map[string]string{"series_missing": "info", "series_name_mismatch": "info", "series_mismatch": "warning"} {
		if d, ok := seen[code]; !ok || d.Label == "" || d.Severity != severity {
			t.Errorf("%s: %+v", code, d)
		}
	}
}

// A sealed package opens at its own findings version: version 6 holds the
// series codes, version 5 does not, and a version 5 package that claims one
// is refused.
func TestSeriesSealAndOpen(t *testing.T) {
	ctx := context.Background()
	p := goodPackage()
	p.SchemaVersion = evidence.SchemaVersion
	p.Formats[0].Metadata.Series, p.Formats[0].Metadata.SeriesIndex = "The Spike Saga", "2"
	raw, hash, err := Seal(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Open(ctx, raw, hash)
	if err != nil || got.FindingsVersion != 6 || !slices.Contains(got.Findings, evidence.Finding{Code: "series_missing", Format: "EPUB", Field: "series"}) {
		t.Fatalf("Open: version %d, %+v, %v", got.FindingsVersion, got.Findings, err)
	}
	claim := p
	claim.FindingsVersion = 5
	claim.Findings = got.Findings
	raw, hash, err = evidence.Seal(claim)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ctx, raw, hash); !errors.Is(err, ErrMismatch) {
		t.Fatalf("a version 5 package with a version 6 finding opened: %v", err)
	}
	claim.Findings, _ = DeriveVersion(ctx, claim, 5)
	raw, hash, err = evidence.Seal(claim)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ctx, raw, hash); err != nil {
		t.Fatalf("a version 5 package: %v", err)
	}
}

// v5Golden is what findings version 5 derived for the history corpus, from
// the code that predates version 6. Versions 1 to 5 must keep deriving it
// even when the packages carry a series (testdata/derive_v1_v4_golden.json
// pins 1 to 4 the same way).
const v5Golden = "testdata/derive_v5_golden.json"

func TestOlderVersionsReplayWithSeries(t *testing.T) {
	read := func(file string, into any) {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, into); err != nil {
			t.Fatal(err)
		}
	}
	var v1to4 map[string]map[string]historyEntry
	var v5 map[string]historyEntry
	read(historyGolden, &v1to4)
	read(v5Golden, &v5)
	corpus := historyCorpus()
	if len(v5) != len(corpus) {
		t.Fatalf("golden has %d packages, corpus %d", len(v5), len(corpus))
	}
	for name, p := range corpus {
		// The same package, now with a series on both sides that disagree.
		withSeries := p
		withSeries.Calibre.Series, withSeries.Calibre.SeriesIndex = "The Spike Saga", "2"
		withSeries.Formats = slices.Clone(p.Formats)
		for i, f := range withSeries.Formats {
			if f.Metadata == nil {
				continue
			}
			m := *f.Metadata
			m.Series, m.SeriesIndex = "Another Saga", "5"
			withSeries.Formats[i].Metadata = &m
		}
		for v := 1; v <= 5; v++ {
			want := v5[name]
			if v < 5 {
				want = v1to4[name][strconv.Itoa(v)]
			}
			for label, pkg := range map[string]evidence.Package{"as it was": p, "with a series": withSeries} {
				got := deriveHistory(pkg, v)
				if got.Error != want.Error || !slices.Equal(got.Findings, want.Findings) {
					t.Errorf("%s (%s) at version %d changed:\n got %+v\nwant %+v", name, label, v, got, want)
				}
			}
		}
	}
}
