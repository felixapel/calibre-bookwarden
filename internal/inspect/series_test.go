package inspect_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/inspect"
	"github.com/felixapel/calibre-bookwarden/internal/inspect/inspecttest"
)

// opfWith wraps raw metadata elements in a minimal OPF.
func opfWith(version, metadata string) []byte {
	return []byte(`<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="` + version + `" unique-identifier="uid">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:opf="http://www.idpf.org/2007/opf">
    <dc:title>Binary Tides</dc:title>
` + metadata + `
  </metadata>
  <manifest/><spine/>
</package>`)
}

func TestOPFSeries(t *testing.T) {
	long := strings.Repeat("é", 600)
	tests := []struct {
		name          string
		version       string
		metadata      string
		series, index string
		seriesLimit   bool
		indexInvalid  bool
	}{
		{"none", "2.0", ``, "", "", false, false},
		{"EPUB 2", "2.0", `<meta name="calibre:series" content="The Spike Saga"/><meta name="calibre:series_index" content="2"/>`, "The Spike Saga", "2", false, false},
		{"EPUB 2 fractional", "2.0", `<meta name="calibre:series" content="Saga"/><meta name="calibre:series_index" content="2.5"/>`, "Saga", "2.5", false, false},
		{"EPUB 2 index first", "2.0", `<meta name="calibre:series_index" content="4"/><meta name="calibre:series" content="Saga"/>`, "Saga", "4", false, false},
		{"EPUB 2 missing index", "2.0", `<meta name="calibre:series" content="Saga"/>`, "Saga", "", false, false},
		{"EPUB 2 index 1.0", "2.0", `<meta name="calibre:series" content="Saga"/><meta name="calibre:series_index" content="1.0"/>`, "Saga", "1", false, false},
		{"EPUB 2 index 2.50", "2.0", `<meta name="calibre:series" content="Saga"/><meta name="calibre:series_index" content="2.50"/>`, "Saga", "2.5", false, false},
		{"EPUB 2 index 007", "2.0", `<meta name="calibre:series" content="Saga"/><meta name="calibre:series_index" content=" 007 "/>`, "Saga", "7", false, false},
		{"EPUB 2 junk index", "2.0", `<meta name="calibre:series" content="Saga"/><meta name="calibre:series_index" content="two"/>`, "Saga", "", false, true},
		{"EPUB 2 roman index", "2.0", `<meta name="calibre:series" content="Saga"/><meta name="calibre:series_index" content="II"/>`, "Saga", "", false, true},
		{"EPUB 2 negative index", "2.0", `<meta name="calibre:series" content="Saga"/><meta name="calibre:series_index" content="-1"/>`, "Saga", "", false, true},
		{"EPUB 2 exponent index", "2.0", `<meta name="calibre:series" content="Saga"/><meta name="calibre:series_index" content="1e3"/>`, "Saga", "1000", false, false},
		{"EPUB 2 exponent index, small", "2.0", `<meta name="calibre:series" content="Saga"/><meta name="calibre:series_index" content="1e-05"/>`, "Saga", "0.00001", false, false},
		{"EPUB 2 overlong index", "2.0", `<meta name="calibre:series" content="Saga"/><meta name="calibre:series_index" content="` + strings.Repeat("9", 600) + `"/>`, "Saga", "", false, true},
		{"EPUB 2 overlong series", "2.0", `<meta name="calibre:series" content="` + long + `"/><meta name="calibre:series_index" content="3"/>`, strings.Repeat("é", 512), "3", true, false},
		{"EPUB 2 blank series", "2.0", `<meta name="calibre:series" content="   "/><meta name="calibre:series_index" content="3"/>`, "", "", false, false},
		{"EPUB 2 whitespace in the name", "2.0", `<meta name="calibre:series" content=" The   Spike&#10;Saga "/>`, "The Spike Saga", "", false, false},
		{"EPUB 2 first of two", "2.0", `<meta name="calibre:series" content="First"/><meta name="calibre:series" content="Second"/>`, "First", "", false, false},
		{"EPUB 3 refines", "3.0", `<meta property="belongs-to-collection" id="c1">The Spike Saga</meta>
		<meta refines="#c1" property="collection-type">series</meta>
		<meta refines="#c1" property="group-position">2.5</meta>`, "The Spike Saga", "2.5", false, false},
		{"EPUB 3 refines before the collection", "3.0", `<meta refines="#c1" property="group-position">3</meta>
		<meta refines="#c1" property="collection-type">series</meta>
		<meta property="belongs-to-collection" id="c1">Saga</meta>`, "Saga", "3", false, false},
		{"EPUB 3 no collection type is skipped", "3.0", `<meta property="belongs-to-collection" id="c1">Saga</meta><meta refines="#c1" property="group-position">1</meta>`, "", "", false, false},
		{"EPUB 3 collection with no id is skipped", "3.0", `<meta property="belongs-to-collection">Saga</meta>`, "", "", false, false},
		{"EPUB 3 series type without an id is skipped", "3.0", `<meta property="belongs-to-collection">Saga</meta><meta refines="#" property="collection-type">series</meta>`, "", "", false, false},
		{"EPUB 3 type is case sensitive", "3.0", `<meta property="belongs-to-collection" id="c1">Saga</meta><meta refines="#c1" property="collection-type">Series</meta>`, "", "", false, false},
		{"EPUB 3 missing position", "3.0", `<meta property="belongs-to-collection" id="c1">Saga</meta><meta refines="#c1" property="collection-type">series</meta>`, "Saga", "", false, false},
		{"EPUB 3 a set is not a series", "3.0", `<meta property="belongs-to-collection" id="c1">Box Set</meta><meta refines="#c1" property="collection-type">set</meta><meta refines="#c1" property="group-position">1</meta>`, "", "", false, false},
		{"EPUB 3 skips the set, takes the series", "3.0", `<meta property="belongs-to-collection" id="c1">Box Set</meta><meta refines="#c1" property="collection-type">set</meta>
		<meta property="belongs-to-collection" id="c2">Saga</meta><meta refines="#c2" property="collection-type">series</meta><meta refines="#c2" property="group-position">6</meta>`, "Saga", "6", false, false},
		{"EPUB 3 nested collection is not kept", "3.0", `<meta property="belongs-to-collection" id="c1">Saga</meta><meta refines="#c1" property="collection-type">series</meta>
		<meta property="belongs-to-collection" refines="#c1" id="c2">Arc</meta><meta refines="#c2" property="group-position">9</meta>`, "Saga", "", false, false},
		{"EPUB 3 junk position", "3.0", `<meta property="belongs-to-collection" id="c1">Saga</meta><meta refines="#c1" property="collection-type">series</meta><meta refines="#c1" property="group-position">book two</meta>`, "Saga", "", false, true},
		{"EPUB 3 overlong series", "3.0", `<meta property="belongs-to-collection" id="c1">` + long + `</meta><meta refines="#c1" property="collection-type">series</meta>`, strings.Repeat("é", 512), "", true, false},
		{"EPUB 3 series collection wins over calibre:series", "3.0", `<meta property="belongs-to-collection" id="c1">Other</meta><meta refines="#c1" property="collection-type">series</meta><meta refines="#c1" property="group-position">9</meta>
		<meta name="calibre:series" content="Saga"/><meta name="calibre:series_index" content="2"/>`, "Other", "9", false, false},
		{"calibre:series is used when the collection is skipped", "3.0", `<meta property="belongs-to-collection" id="c1">Other</meta><meta refines="#c1" property="group-position">9</meta>
		<meta name="calibre:series" content="Saga"/><meta name="calibre:series_index" content="2"/>`, "Saga", "2", false, false},
		{"an index without a series", "2.0", `<meta name="calibre:series_index" content="2"/>`, "", "", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := inspect.OPFMetadata(opfWith(tt.version, tt.metadata))
			if err != nil {
				t.Fatal(err)
			}
			if m == nil || m.Limits == nil {
				t.Fatalf("metadata %+v", m)
			}
			if m.Series != tt.series || m.SeriesIndex != tt.index || m.Limits.Series != tt.seriesLimit || m.Limits.SeriesIndex != tt.indexInvalid {
				t.Errorf("series %q index %q limits %+v; want %q, %q, series cut %v, index invalid %v",
					m.Series, m.SeriesIndex, *m.Limits, tt.series, tt.index, tt.seriesLimit, tt.indexInvalid)
			}
			if n := utf8.RuneCountInString(m.Series); n > 512 {
				t.Errorf("series has %d runes", n)
			}
			if _, err := evidence.Canonical(m); err != nil {
				t.Errorf("the metadata doesn't seal: %v", err)
			}
		})
	}
}

// The series comes through the whole EPUB inspector, and a file that names
// nothing else still has metadata.
func TestEPUBSeriesEndToEnd(t *testing.T) {
	data := inspecttest.EPUB(inspecttest.EPUBOptions{
		Title: "Binary Tides", Text: "Once upon a time.",
		ExtraMeta: `<meta name="calibre:series" content="The Spike Saga"/><meta name="calibre:series_index" content="2.0"/>`,
	})
	res := inspect.Inspect(context.Background(), "EPUB", bytes.NewReader(data), int64(len(data)), inspect.Limits{})
	if res.State != evidence.StateOK || res.Metadata == nil || res.Metadata.Series != "The Spike Saga" || res.Metadata.SeriesIndex != "2" {
		t.Fatalf("state %s, metadata %+v", res.State, res.Metadata)
	}
}

func TestNewMetaSeriesOnly(t *testing.T) {
	if m := inspect.NewMeta(evidence.FileMetadata{Series: "Saga", SeriesIndex: "1"}); m == nil || m.Series != "Saga" {
		t.Errorf("a series alone left metadata %+v", m)
	}
	if m := inspect.NewMeta(evidence.FileMetadata{SeriesIndex: "1"}); m != nil {
		t.Errorf("an index alone left metadata %+v", m)
	}
}
