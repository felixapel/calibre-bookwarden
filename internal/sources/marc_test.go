package sources_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/sources"
)

func lookup(t *testing.T, a sources.Adapter, f *fakeFetcher, isbn string) (string, []evidence.SourceRecord) {
	t.Helper()
	sources.SetBase(a, "https://test.invalid")
	return a.Lookup(context.Background(), f, isbn)
}

func TestDNB(t *testing.T) {
	f := &fakeFetcher{routes: map[string]answer{"/sru/dnb": file("dnb-9783551551672.xml")}}
	outcome, recs := lookup(t, sources.DNB(), f, "9783551551672")
	want := evidence.SourceRecord{ID: "958577617", Title: "Harry Potter und der Stein der Weisen", Authors: []string{"J. K. Rowling"},
		Languages: []string{"deu"}, Publisher: "Carlsen", Date: "1998", ISBNs: []string{"9783551551672"}}
	if outcome != evidence.SourceOK || len(recs) != 1 || !equalRecord(recs[0], want) {
		t.Fatalf("%s %+v", outcome, recs)
	}
	if len(f.urls) != 1 || f.urls[0] != "https://test.invalid/sru/dnb?version=1.1&operation=searchRetrieve&query=num%3D9783551551672&recordSchema=MARC21-xml&maximumRecords=10" {
		t.Fatalf("urls = %v", f.urls)
	}
	f = &fakeFetcher{routes: map[string]answer{"/sru/dnb": file("dnb-none.xml")}}
	if outcome, recs := lookup(t, sources.DNB(), f, "9780000000002"); outcome != evidence.SourceNoMatch || recs != nil {
		t.Fatalf("none: %s %+v", outcome, recs)
	}
}

func equalRecord(a, b evidence.SourceRecord) bool {
	return a.ID == b.ID && a.Title == b.Title && a.Subtitle == b.Subtitle && slices.Equal(a.Authors, b.Authors) &&
		slices.Equal(a.Languages, b.Languages) && a.Publisher == b.Publisher && a.Date == b.Date &&
		slices.Equal(a.ISBNs, b.ISBNs) && slices.Equal(a.SourceRecords, b.SourceRecords) &&
		a.Series == b.Series && a.SeriesIndex == b.SeriesIndex
}

func TestBNE(t *testing.T) {
	f := &fakeFetcher{routes: map[string]answer{"/view/sru/34BNE_INST": file("bne-9788478884452.xml")}}
	outcome, recs := lookup(t, sources.BNE(), f, "9788478884452")
	if outcome != evidence.SourceOK || len(recs) != 2 {
		t.Fatalf("%s %d records", outcome, len(recs))
	}
	if f.urls[0] != "https://test.invalid/view/sru/34BNE_INST?version=1.2&operation=searchRetrieve&query=alma.isbn%3D9788478884452&recordSchema=marcxml&maximumRecords=10" {
		t.Fatalf("url = %s", f.urls[0])
	}
	for i, w := range []struct{ id, publisher, date string }{{"991004606819708606", "Salamandra", "2005"}, {"991053479349708606", "Emecé", "1999"}} {
		r := recs[i]
		if r.ID != w.id || r.Title != "Harry Potter y la piedra filosofal" || !slices.Equal(r.Authors, []string{"J. K. Rowling"}) ||
			!slices.Equal(r.Languages, []string{"spa"}) || r.Publisher != w.publisher || r.Date != w.date || !slices.Contains(r.ISBNs, "9788478884452") {
			t.Errorf("record %d = %+v", i, r)
		}
	}
}

// sruXML wraps MARC datafields (already XML) in an SRU answer with one record.
func sruXML(control008 string, fields ...string) []byte {
	return fmt.Appendf(nil, `<?xml version="1.0"?><searchRetrieveResponse xmlns="http://www.loc.gov/zing/srw/"><version>1.1</version>
<numberOfRecords>1</numberOfRecords><records><record><recordData><record xmlns="http://www.loc.gov/MARC21/slim">
<leader>00000nam a2200000 c 4500</leader><controlfield tag="001">42</controlfield><controlfield tag="008">%s</controlfield>%s
</record></recordData></record></records></searchRetrieveResponse>`, control008, strings.Join(fields, ""))
}

func df(tag, ind1, ind2 string, subs ...string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<datafield tag="%s" ind1="%s" ind2="%s">`, tag, ind1, ind2)
	for i := 0; i+1 < len(subs); i += 2 {
		fmt.Fprintf(&b, `<subfield code="%s">%s</subfield>`, subs[i], subs[i+1])
	}
	b.WriteString(`</datafield>`)
	return b.String()
}

const (
	isbnOK  = "9783551551672"
	date008 = "000301s1998    gw ||||| |||| 00||||ger  "
)

func dnbWith(body []byte) *fakeFetcher {
	return &fakeFetcher{routes: map[string]answer{"/sru/dnb": {status: 200, body: body}}}
}

func TestMARCAcceptsOnlyMatchingISBN(t *testing.T) {
	for name, isbn := range map[string]string{"another book": "9780306406157", "bad checksum": "9783551551671"} {
		f := dnbWith(sruXML(date008, df("020", " ", " ", "a", isbn), df("245", "1", "0", "a", "Other")))
		if outcome, recs := lookup(t, sources.DNB(), f, isbnOK); outcome != evidence.SourceNoMatch || recs != nil {
			t.Fatalf("%s: %s %+v", name, outcome, recs)
		}
	}
}

func TestMARCFields(t *testing.T) {
	isbn := df("020", " ", " ", "a", "3-551-55167-7 (Pp.)")
	var many []string
	for i := range 20 {
		many = append(many, df("700", "1", " ", "a", fmt.Sprintf("Author%02d, A.", i), "4", "aut"))
	}
	for _, tt := range []struct {
		name   string
		c008   string
		fields []string
		check  func(r evidence.SourceRecord) bool
	}{
		{"un-invert", date008, []string{df("100", "1", " ", "a", "Mann, Thomas,", "d", "1875-1955")}, func(r evidence.SourceRecord) bool { return slices.Equal(r.Authors, []string{"Thomas Mann"}) }},
		{"keeps initials", date008, []string{df("100", "1", " ", "a", "Rowling, J. K.")}, func(r evidence.SourceRecord) bool { return slices.Equal(r.Authors, []string{"J. K. Rowling"}) }},
		{"forename heading", date008, []string{df("100", "0", " ", "a", "Elizabeth", "b", "II")}, func(r evidence.SourceRecord) bool { return slices.Equal(r.Authors, []string{"Elizabeth"}) }},
		{"compound surname", date008, []string{df("100", "1", " ", "a", "García Márquez, Gabriel")}, func(r evidence.SourceRecord) bool {
			return slices.Equal(r.Authors, []string{"Gabriel García Márquez"})
		}},
		{"700 rules", date008, []string{df("100", "1", " ", "a", "Kafka, Franz"), df("700", "1", " ", "a", "Kafka, Franz", "t", "Der Process"), df("700", "1", " ", "a", "Brod, Max", "4", "edt"),
			df("700", "1", " ", "a", "Muir, Willa", "e", "Übersetzer"), df("700", "1", " ", "a", "Muir, Edwin", "4", "aut"), df("700", "1", " ", "a", "Plain, Pat")},
			func(r evidence.SourceRecord) bool {
				return slices.Equal(r.Authors, []string{"Franz Kafka", "Edwin Muir", "Pat Plain"})
			}},
		{"title ISBD", date008, []string{df("245", "1", "0", "a", "Der Prozess /", "b", "Roman :", "h", "[Texto impreso]", "c", "Franz Kafka")}, func(r evidence.SourceRecord) bool {
			return r.Title == "Der Prozess" && r.Subtitle == "Roman"
		}},
		{"bracketed title", date008, []string{df("245", "0", "0", "a", "[Ohne Titel].")}, func(r evidence.SourceRecord) bool { return r.Title == "Ohne Titel" }},
		{"date from 260", "000301s        gw ||||| |||| 00||||ger  ", []string{df("260", " ", " ", "a", "Barcelona :", "b", "Emecé,", "c", "[1999]")}, func(r evidence.SourceRecord) bool {
			return r.Date == "1999" && r.Publisher == "Emecé"
		}},
		{"264 publisher", date008, []string{df("264", " ", "1", "a", "Hamburg", "b", "Carlsen", "c", "1998"), df("264", " ", "4", "b", "Copyright Holder")}, func(r evidence.SourceRecord) bool {
			return r.Publisher == "Carlsen"
		}},
		{"language from 008", date008, nil, func(r evidence.SourceRecord) bool { return slices.Equal(r.Languages, []string{"deu"}) }},
		{"041 several", date008, []string{df("041", "1", " ", "a", "spa", "a", "eng", "h", "fre")}, func(r evidence.SourceRecord) bool {
			return slices.Equal(r.Languages, []string{"spa", "eng"})
		}},
		{"041 undetermined", date008, []string{df("041", " ", " ", "a", "und")}, func(r evidence.SourceRecord) bool { return len(r.Languages) == 0 }},
		{"16 authors at most", date008, many, func(r evidence.SourceRecord) bool { return len(r.Authors) == 16 && r.Authors[0] == "A. Author00" }},
		{"long title capped", date008, []string{df("245", "1", "0", "a", strings.Repeat("x", 600))}, func(r evidence.SourceRecord) bool {
			return len([]rune(r.Title)) == 512
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := dnbWith(sruXML(tt.c008, append([]string{isbn}, tt.fields...)...))
			outcome, recs := lookup(t, sources.DNB(), f, isbnOK)
			if outcome != evidence.SourceOK || len(recs) != 1 || !tt.check(recs[0]) || recs[0].ID != "42" {
				t.Fatalf("%s %+v", outcome, recs)
			}
		})
	}
}

func TestSRUHostile(t *testing.T) {
	deep := strings.Repeat("<x>", 65) + strings.Repeat("</x>", 65)
	for name, body := range map[string]string{
		"doctype":     `<?xml version="1.0"?><!DOCTYPE x [<!ENTITY a "b">]><searchRetrieveResponse><numberOfRecords>0</numberOfRecords></searchRetrieveResponse>`,
		"diagnostics": `<?xml version="1.0"?><searchRetrieveResponse xmlns="http://www.loc.gov/zing/srw/"><numberOfRecords>0</numberOfRecords><diagnostics><diagnostic><message>General System Error</message></diagnostic></diagnostics></searchRetrieveResponse>`,
		"too deep":    `<?xml version="1.0"?><searchRetrieveResponse><numberOfRecords>0</numberOfRecords>` + deep + `</searchRetrieveResponse>`,
		"not xml":     `<html>busy</html`,
		"wrong root":  `<?xml version="1.0"?><html><body>maintenance</body></html>`,
	} {
		t.Run(name, func(t *testing.T) {
			if outcome, recs := lookup(t, sources.DNB(), dnbWith([]byte(body)), isbnOK); outcome != evidence.SourceUnavailable || recs != nil {
				t.Fatalf("%s %+v", outcome, recs)
			}
		})
	}
	f := &fakeFetcher{routes: map[string]answer{"/sru/dnb": {err: sources.ErrUnavailable}}}
	if outcome, _ := lookup(t, sources.DNB(), f, isbnOK); outcome != evidence.SourceUnavailable {
		t.Fatalf("fetch error: %s", outcome)
	}
}

func TestSharedRoot(t *testing.T) {
	r := evidence.SourceRecord{ID: "/books/OL1M", SourceRecords: []string{"marc:dnb_dump/x.mrc:1:2", "marc:dnb_dump/y.mrc:3:4"}}
	if got := sources.SharedRoot(r); got != "" {
		t.Fatalf("empty table shared %q", got)
	}
	defer sources.SetNationalPrefixes(map[string][]string{evidence.SourceDNB: {"marc:dnb_dump/"}})()
	if got := sources.SharedRoot(r); got != evidence.SourceDNB {
		t.Fatalf("shared root = %q", got)
	}
	r.SourceRecords = append(r.SourceRecords, "amazon:123")
	if got := sources.SharedRoot(r); got != "" {
		t.Fatalf("mixed records shared %q", got)
	}
	if got := sources.SharedRoot(evidence.SourceRecord{}); got != "" {
		t.Fatalf("no records shared %q", got)
	}
}

func FuzzMARC(f *testing.F) {
	for _, name := range []string{"dnb-9783551551672.xml", "dnb-none.xml", "bne-9788478884452.xml"} {
		f.Add(file(name).body)
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		for _, isbn := range []string{isbnOK, "9788478884452"} {
			_, recs := lookup(t, sources.DNB(), dnbWith(body), isbn)
			if len(recs) > 8 {
				t.Fatalf("%d records", len(recs))
			}
			for _, r := range recs {
				if !slices.Contains(r.ISBNs, isbn) || len(r.Authors) > 16 || len(r.ISBNs) > 16 || len([]rune(r.Title)) > 512 {
					t.Fatalf("record %+v", r)
				}
			}
		}
	})
}
