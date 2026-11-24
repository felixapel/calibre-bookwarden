package evidence_test

import (
	"bytes"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/library"
)

// schema4 reads testdata/schema4_package.json: a schema 4 package with an
// excerpt, an extraction, hints, findings, lookups, a decision and a cover,
// sealed by the code that predates schema 5. Its bytes and seal must never
// change.
func schema4(t *testing.T) (raw []byte, seal string) {
	t.Helper()
	raw, err := os.ReadFile("testdata/schema4_package.json")
	if err != nil {
		t.Fatal(err)
	}
	s, err := os.ReadFile("testdata/schema4_package.seal")
	if err != nil {
		t.Fatal(err)
	}
	return raw, strings.TrimSpace(string(s))
}

func TestSchema4PackageReplays(t *testing.T) {
	raw, seal := schema4(t)
	p, err := evidence.Open(raw, seal)
	if err != nil {
		t.Fatalf("a schema 4 package no longer opens: %v", err)
	}
	if p.SchemaVersion != evidence.SchemaVersionV4 || p.Formats[1].Extraction == nil {
		t.Fatalf("opened as schema %d, extraction %v", p.SchemaVersion, p.Formats[1].Extraction)
	}
	again, againSeal, err := evidence.Seal(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again, raw) || againSeal != seal {
		t.Errorf("re-sealing a schema 4 package changed it:\n got %s (%s)\nwant %s (%s)", again, againSeal, raw, seal)
	}
}

// A schema 5 package that has no series differs from the same package at
// schema 4 by the version number alone: the new fields leave no key behind.
func TestSchema5WithoutSeriesAddsNoKeys(t *testing.T) {
	raw, seal := schema4(t)
	p, err := evidence.Open(raw, seal)
	if err != nil {
		t.Fatal(err)
	}
	p.SchemaVersion = evidence.SchemaVersion
	got, newSeal, err := evidence.Seal(p)
	if err != nil {
		t.Fatal(err)
	}
	want := bytes.Replace(raw, []byte(`"schema_version":4`), []byte(`"schema_version":5`), 1)
	if !bytes.Equal(got, want) {
		t.Errorf("schema 5 without a series:\n got %s\nwant %s", got, want)
	}
	if _, err := evidence.Open(got, newSeal); err != nil {
		t.Errorf("Open: %v", err)
	}
}

func seriesPackage(t *testing.T, version int) evidence.Package {
	t.Helper()
	b := book()
	b.Series, b.SeriesIndex = "The Spike Saga", "2.5"
	p := pkg(t)
	p.SchemaVersion = version
	p.Calibre = evidence.RecordFrom(b)
	p.Formats = append([]evidence.FormatEvidence(nil), p.Formats...)
	p.Formats[1] = evidence.FormatEvidence{
		Format: "EPUB", Size: 2326, State: evidence.StateOK, Text: evidence.TextPresent,
		Metadata: &evidence.FileMetadata{
			Title: "Binary Tides", Series: "Spike Saga", SeriesIndex: "3",
			Limits: &evidence.MetadataLimits{Version: 1},
		},
	}
	p.Lookups = []evidence.Lookup{{ISBN: "9780000000026", Seeds: []string{"file"}, Results: []evidence.SourceResult{
		{Source: evidence.SourceWikidata, Outcome: evidence.SourceOK, Records: []evidence.SourceRecord{
			{ID: "Q1", ISBNs: []string{"9780000000026"}, Series: "The Spike Saga", SeriesIndex: "0.5"},
		}},
	}}}
	return p
}

func TestSeriesRoundTrip(t *testing.T) {
	p := seriesPackage(t, evidence.SchemaVersion)
	raw, seal, err := evidence.Seal(p)
	if err != nil {
		t.Fatal(err)
	}
	// The index is a string: the canonical JSON has no fractional numbers.
	for _, want := range []string{`"series":"The Spike Saga"`, `"series_index":"2.5"`, `"series_index":"3"`, `"series_index":"0.5"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("sealed bytes lack %s: %s", want, raw)
		}
	}
	got, err := evidence.Open(raw, seal)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, p) {
		t.Errorf("Open(Seal(p)):\n got %+v\nwant %+v", got, p)
	}
	again, againSeal, err := evidence.Seal(got)
	if err != nil || !bytes.Equal(again, raw) || againSeal != seal {
		t.Errorf("a second seal differs: %v", err)
	}
}

func TestRecordFromSeries(t *testing.T) {
	tests := []struct {
		name             string
		series, index    string
		wantSer, wantIdx string
	}{
		{"series and index", "Saga", "2.5", "Saga", "2.5"},
		{"whole index", "Saga", "1", "Saga", "1"},
		{"no series drops the index Calibre reports", "", "1", "", ""},
		{"series without an index", "Saga", "", "Saga", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := evidence.RecordFrom(library.Book{Series: tt.series, SeriesIndex: tt.index})
			if r.Series != tt.wantSer || r.SeriesIndex != tt.wantIdx {
				t.Errorf("Series %q, SeriesIndex %q; want %q, %q", r.Series, r.SeriesIndex, tt.wantSer, tt.wantIdx)
			}
		})
	}
}

func TestSeriesRefusedBeforeSchema5(t *testing.T) {
	places := map[string]func(*evidence.Package){
		"calibre series": func(p *evidence.Package) { p.Calibre.Series = "S" },
		"calibre index":  func(p *evidence.Package) { p.Calibre.Series, p.Calibre.SeriesIndex = "S", "1" },
		"file series":    func(p *evidence.Package) { p.Formats[1].Metadata = &evidence.FileMetadata{Series: "S"} },
		"file index": func(p *evidence.Package) {
			p.Formats[1].Metadata = &evidence.FileMetadata{Series: "S", SeriesIndex: "1"}
		},
		"file series limit": func(p *evidence.Package) {
			p.Formats[1].Metadata = &evidence.FileMetadata{Series: "S", Limits: &evidence.MetadataLimits{Version: 1, Series: true}}
		},
		"file index limit": func(p *evidence.Package) {
			p.Formats[1].Metadata = &evidence.FileMetadata{Series: "S", Limits: &evidence.MetadataLimits{Version: 1, SeriesIndex: true}}
		},
		"source record series": func(p *evidence.Package) {
			p.Lookups = []evidence.Lookup{lookupWith(evidence.SourceRecord{ID: "Q1", ISBNs: []string{"9"}, Series: "S"})}
		},
		"source record index": func(p *evidence.Package) {
			p.Lookups = []evidence.Lookup{lookupWith(evidence.SourceRecord{ID: "Q1", ISBNs: []string{"9"}, Series: "S", SeriesIndex: "2"})}
		},
	}
	for name, set := range places {
		for version := evidence.SchemaVersionV1; version <= evidence.SchemaVersion; version++ {
			t.Run(name, func(t *testing.T) {
				p := pkg(t)
				p.SchemaVersion = version
				set(&p)
				raw, seal, err := evidence.Seal(p)
				if err != nil {
					t.Fatal(err)
				}
				_, err = evidence.Open(raw, seal)
				if version < evidence.SchemaVersion && !errors.Is(err, evidence.ErrInvalidEvidence) {
					t.Errorf("schema %d: Open = %v, want ErrInvalidEvidence", version, err)
				}
				if version == evidence.SchemaVersion && err != nil {
					t.Errorf("schema %d: Open = %v", version, err)
				}
			})
		}
	}
}

func lookupWith(r evidence.SourceRecord) evidence.Lookup {
	return evidence.Lookup{ISBN: "9", Seeds: []string{"file"}, Results: []evidence.SourceResult{{Source: evidence.SourceWikidata, Outcome: evidence.SourceOK, Records: []evidence.SourceRecord{r}}}}
}

func TestSeriesIndexMustBeCanonical(t *testing.T) {
	for _, index := range []string{"2.50", "1.0", "abc", "-1", "1e3", " 2", "01"} {
		for name, set := range map[string]func(*evidence.Package, string){
			"calibre": func(p *evidence.Package, v string) { p.Calibre.SeriesIndex = v },
			"file":    func(p *evidence.Package, v string) { p.Formats[1].Metadata = &evidence.FileMetadata{SeriesIndex: v} },
			"source": func(p *evidence.Package, v string) {
				p.Lookups = []evidence.Lookup{lookupWith(evidence.SourceRecord{ID: "Q1", ISBNs: []string{"9"}, SeriesIndex: v})}
			},
		} {
			p := pkg(t)
			p.SchemaVersion = evidence.SchemaVersion
			set(&p, index)
			raw, seal, err := evidence.Seal(p)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := evidence.Open(raw, seal); !errors.Is(err, evidence.ErrInvalidEvidence) {
				t.Errorf("%s index %q: Open = %v, want ErrInvalidEvidence", name, index, err)
			}
		}
	}
}

// An index, or a limit flag about one, is meaningless without a series.
func TestSeriesIndexNeedsASeries(t *testing.T) {
	for name, set := range map[string]func(*evidence.Package){
		"calibre index": func(p *evidence.Package) { p.Calibre.SeriesIndex = "1" },
		"file index":    func(p *evidence.Package) { p.Formats[1].Metadata = &evidence.FileMetadata{SeriesIndex: "1"} },
		"file index limit": func(p *evidence.Package) {
			p.Formats[1].Metadata = &evidence.FileMetadata{Limits: &evidence.MetadataLimits{Version: 1, SeriesIndex: true}}
		},
		"file series limit": func(p *evidence.Package) {
			p.Formats[1].Metadata = &evidence.FileMetadata{Limits: &evidence.MetadataLimits{Version: 1, Series: true}}
		},
		"source record index": func(p *evidence.Package) {
			p.Lookups = []evidence.Lookup{lookupWith(evidence.SourceRecord{ID: "Q1", ISBNs: []string{"9"}, SeriesIndex: "2"})}
		},
	} {
		t.Run(name, func(t *testing.T) {
			p := pkg(t)
			p.SchemaVersion = evidence.SchemaVersion
			set(&p)
			raw, seal, err := evidence.Seal(p)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := evidence.Open(raw, seal); !errors.Is(err, evidence.ErrInvalidEvidence) {
				t.Errorf("Open = %v, want ErrInvalidEvidence", err)
			}
		})
	}
}
