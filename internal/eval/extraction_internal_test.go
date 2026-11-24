package eval

import (
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

func agreed(key string, vals ...evidence.AgreedValue) evidence.AgreedFact {
	return evidence.AgreedFact{Key: key, Outcome: evidence.AgreeAgreed, Values: vals, Voices: []string{"glm", "luna"}}
}

func val(v string) evidence.AgreedValue { return evidence.AgreedValue{Value: v} }

// measured is a package whose EPUB carries facts, with Calibre's record and
// one exact-ISBN record.
func measured(c evidence.Record, rec *evidence.SourceRecord, facts ...evidence.AgreedFact) evidence.Package {
	p := evidence.Package{Calibre: c, Formats: []evidence.FormatEvidence{{
		Format: "EPUB",
		Extraction: &evidence.Extraction{
			Extractors: []evidence.ExtractorRun{
				{Name: "luna", State: evidence.ExtractOK, Cached: true},
				{Name: "deepseek", State: evidence.ExtractModelMismatch},
				{Name: "glm", State: evidence.ExtractOK},
			},
			Agreed: facts,
		},
	}, {Format: "PDF"}}}
	if rec != nil {
		p.Lookups = []evidence.Lookup{{ISBN: "9780306406157", Results: []evidence.SourceResult{
			{Source: "dnb", Outcome: "no_match"},
			{Source: "openlibrary", Outcome: "ok", Records: []evidence.SourceRecord{*rec}},
		}}}
	}
	return p
}

func TestExtractionCounts(t *testing.T) {
	r := newExtractionReport(7)
	cal := evidence.Record{Title: "The Lantern Years", Authors: []string{"Jane Doe", "Jo Ray"}, Publisher: "Salt Press", PubDate: "2019-05-01T00:00:00+00:00", Identifiers: map[string]string{"isbn": "0-306-40615-2"}}
	rec := &evidence.SourceRecord{Title: "The lantern years", Authors: []string{"Doe, Jane"}, Publisher: "Salt Press Ltd", Date: "2018"}
	r.add(measured(cal, rec,
		agreed("title", val("THE LANTERN YEARS")),
		agreed("contributor", evidence.AgreedValue{Value: "Jane Doe", Role: "author"}, evidence.AgreedValue{Value: "Jo Ray", Role: "translator"}),
		agreed("imprint", val("Salt Press")),
		agreed("isbn", evidence.AgreedValue{Value: "9780306406157", Label: "ebook"}),
		agreed("year/copyright", evidence.AgreedValue{Value: "2019", Label: "copyright"}),
		agreed("series", val("Salt Road")),
		evidence.AgreedFact{Key: "edition", Outcome: evidence.AgreeDisagree, Voices: []string{"glm", "luna"}},
		evidence.AgreedFact{Key: "subtitle", Outcome: evidence.AgreeSingle, Values: []evidence.AgreedValue{val("A Novel")}, Voices: []string{"glm"}},
	))
	// Calibre's empty placeholder date, no record, and another title.
	r.add(measured(evidence.Record{Title: "Other", PubDate: "0101-01-01T00:00:00+00:00"}, nil,
		agreed("title", val("THE LANTERN")),
		agreed("year/copyright", evidence.AgreedValue{Value: "2019", Label: "copyright"}),
	))
	r.add(evidence.Package{Formats: []evidence.FormatEvidence{{Format: "EPUB"}}}) // no extraction

	if r.Formats != 2 {
		t.Errorf("Formats = %d, want 2", r.Formats)
	}
	if got := r.Extractors["luna"]; got.States[evidence.ExtractOK] != 2 || got.Cached != 2 {
		t.Errorf("luna = %+v", got)
	}
	if got := r.Extractors["deepseek"]; got.States[evidence.ExtractModelMismatch] != 2 || got.Cached != 0 {
		t.Errorf("deepseek = %+v", got)
	}
	want := map[string]KeyCount{
		// Case, punctuation and diacritics are ignored; the record's
		// inverted name is not a match.
		"title":          {Agreed: 2, Calibre: Comparison{Match: 1, Differ: 1}, Records: Comparison{Match: 1, None: 1}},
		"contributor":    {Agreed: 1, Calibre: Comparison{Differ: 1}, Records: Comparison{Differ: 1}},
		"imprint":        {Agreed: 1, Calibre: Comparison{Match: 1}, Records: Comparison{Differ: 1}},
		"isbn":           {Agreed: 1, Calibre: Comparison{Match: 1}},
		"year/copyright": {Agreed: 2},
		"series":         {Agreed: 1},
		"edition":        {Disagree: 1},
		"subtitle":       {Single: 1},
	}
	for k, w := range want {
		if got := r.Keys[k]; got == nil || *got != w {
			t.Errorf("%s = %+v, want %+v", k, got, w)
		}
	}
	if len(r.Keys) != len(want) {
		t.Errorf("%d keys, want %d", len(r.Keys), len(want))
	}
}

func TestContributorComparesAuthorsOnly(t *testing.T) {
	r := newExtractionReport(1)
	r.add(measured(evidence.Record{Authors: []string{"Jane Doe"}}, nil,
		agreed("contributor", evidence.AgreedValue{Value: "JANE DOE", Role: "author"}, evidence.AgreedValue{Value: "Jo Ray", Role: "translator"})))
	r.add(measured(evidence.Record{Authors: []string{"Jane Doe"}}, nil,
		agreed("contributor", evidence.AgreedValue{Value: "Jo Ray", Role: "editor"})))
	if got, want := *r.Keys["contributor"], (KeyCount{Agreed: 2, Calibre: Comparison{Match: 1}, Records: Comparison{None: 1}}); got != want {
		t.Errorf("contributor = %+v, want %+v (no author voice compares nowhere)", got, want)
	}
}

func TestComparisonsFollowTheStyleGuide(t *testing.T) {
	r := newExtractionReport(1)
	// Calibre keeps "Main: Subtitle" (ADR-031), one e-book ISBN, and this
	// edition's date; the record holds the ISBN it was found by.
	cal := evidence.Record{Title: "The Lantern Years: A Novel", PubDate: "2019-05-01T00:00:00+00:00", Identifiers: map[string]string{"isbn": "9780306406157"}}
	rec := &evidence.SourceRecord{Title: "The Lantern Years", Subtitle: "A novel", ISBNs: []string{"9780306406157"}, Date: "June 5, 2003"}
	r.add(measured(cal, rec,
		agreed("title", val("THE LANTERN YEARS")),
		agreed("subtitle", val("A NOVEL")),
		agreed("isbn", evidence.AgreedValue{Value: "9780306406157", Label: "ebook"}, evidence.AgreedValue{Value: "9781861972712", Label: "hardcover"}),
		agreed("year/first_published", evidence.AgreedValue{Value: "1960", Label: "first_published"}),
		agreed("year/this_edition", evidence.AgreedValue{Value: "2003", Label: "this_edition"}),
	))
	want := map[string]KeyCount{
		"title":    {Agreed: 1, Calibre: Comparison{Match: 1}, Records: Comparison{Match: 1}},
		"subtitle": {Agreed: 1, Records: Comparison{Match: 1}},
		// Calibre's ISBN is one the book prints; records are seeded with
		// the book's ISBNs, so comparing them would be circular.
		"isbn": {Agreed: 1, Calibre: Comparison{Match: 1}},
		// Only this edition's year is Calibre's date; a free-text date gives
		// its year.
		"year/first_published": {Agreed: 1},
		"year/this_edition":    {Agreed: 1, Calibre: Comparison{Differ: 1}, Records: Comparison{Match: 1}},
	}
	for k, w := range want {
		if got := r.Keys[k]; got == nil || *got != w {
			t.Errorf("%s = %+v, want %+v", k, got, w)
		}
	}
}

func TestCoverageCountsAnsweredFormats(t *testing.T) {
	r := newExtractionReport(1)
	r.add(measured(evidence.Record{}, nil, agreed("title", val("A"))))
	unanswered := measured(evidence.Record{}, nil)
	for i := range unanswered.Formats[0].Extraction.Extractors {
		unanswered.Formats[0].Extraction.Extractors[i].State = evidence.ExtractUnavailable
	}
	r.add(unanswered)
	if r.Formats != 2 || r.Answered != 1 {
		t.Errorf("formats %d, answered %d; want 2 and 1", r.Formats, r.Answered)
	}
	var out strings.Builder
	r.WriteText(&out)
	if !strings.Contains(out.String(), "100.0") {
		t.Errorf("coverage is not over the answered formats: %s", out.String())
	}
}
