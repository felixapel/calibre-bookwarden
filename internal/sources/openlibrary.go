package sources

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

const (
	maxJSONBody = 1 << 20
	maxAuthors  = 8
)

// Open Library keys become request paths, so they must match exactly.
var (
	workKey   = regexp.MustCompile(`^/works/OL[0-9]{1,12}W$`)
	authorKey = regexp.MustCompile(`^/authors/OL[0-9]{1,12}A$`)
)

type openLibrary struct{ base string }

// OpenLibrary reads the edition by ISBN, then, when the edition names no
// authors, its first work and that work's authors.
func OpenLibrary() Adapter { return &openLibrary{base: "https://openlibrary.org"} }

func (o *openLibrary) Name() string { return evidence.SourceOpenLibrary }

type olKey struct {
	Key string `json:"key"`
}

type olEdition struct {
	Key           string   `json:"key"`
	Title         string   `json:"title"`
	Subtitle      string   `json:"subtitle"`
	Authors       []olKey  `json:"authors"`
	Works         []olKey  `json:"works"`
	Languages     []olKey  `json:"languages"`
	Publishers    []string `json:"publishers"`
	PublishDate   string   `json:"publish_date"`
	ISBN10        []string `json:"isbn_10"`
	ISBN13        []string `json:"isbn_13"`
	SourceRecords []string `json:"source_records"`
}

// getJSON fetches path and decodes it into v; found is false for a 404.
func (o *openLibrary) getJSON(ctx context.Context, f Fetcher, path string, v any) (found bool, ok bool) {
	status, body, err := f.Get(ctx, o.base+path, nil, maxJSONBody)
	switch {
	case err != nil:
		return false, false
	case status == http.StatusNotFound:
		return false, true
	case status != http.StatusOK:
		return false, false
	}
	if json.Unmarshal(body, v) != nil {
		return false, false
	}
	return true, true
}

// getEntity fetches a work or author by key into v. Open Library answers a
// merged one with 200 and a redirect record; one redirect whose location
// matches pattern is followed. Anything else missing reports false.
func (o *openLibrary) getEntity(ctx context.Context, f Fetcher, key string, pattern *regexp.Regexp, v any) bool {
	for range 2 {
		var raw json.RawMessage
		if found, ok := o.getJSON(ctx, f, key+".json", &raw); !ok || !found {
			return false
		}
		var head struct {
			Type     olKey  `json:"type"`
			Location string `json:"location"`
		}
		if json.Unmarshal(raw, &head) != nil {
			return false
		}
		if head.Type.Key != "/type/redirect" {
			return json.Unmarshal(raw, v) == nil
		}
		if !pattern.MatchString(head.Location) {
			return false
		}
		key = head.Location
	}
	return false
}

func (o *openLibrary) Lookup(ctx context.Context, f Fetcher, isbn string) (string, []evidence.SourceRecord) {
	var ed olEdition
	found, ok := o.getJSON(ctx, f, "/isbn/"+isbn+".json", &ed)
	switch {
	case !ok:
		return evidence.SourceUnavailable, nil
	case !found:
		return evidence.SourceNoMatch, nil
	case ed.Key == "":
		return evidence.SourceUnavailable, nil
	}
	rec := evidence.SourceRecord{ID: id(ed.Key), Title: text(ed.Title), Subtitle: text(ed.Subtitle), Date: text(ed.PublishDate)}
	for _, raw := range append(ed.ISBN13, ed.ISBN10...) {
		rec.ISBNs = appendISBN(rec.ISBNs, raw)
	}
	if outcome, _ := accept(isbn, []evidence.SourceRecord{rec}); outcome != evidence.SourceOK {
		return outcome, nil
	}
	for _, l := range ed.Languages {
		if len(l.Key) > len("/languages/") && l.Key[:len("/languages/")] == "/languages/" {
			rec.Languages = appendText(rec.Languages, lang(l.Key[len("/languages/"):]))
		}
	}
	if len(ed.Publishers) > 0 {
		rec.Publisher = text(ed.Publishers[0])
	}
	for _, sr := range ed.SourceRecords {
		rec.SourceRecords = appendText(rec.SourceRecords, sr)
	}
	authors := ed.Authors
	if len(authors) == 0 && len(ed.Works) > 0 {
		if !workKey.MatchString(ed.Works[0].Key) {
			return evidence.SourceUnavailable, nil
		}
		var work struct {
			Authors []struct {
				Author olKey `json:"author"`
			} `json:"authors"`
		}
		if !o.getEntity(ctx, f, ed.Works[0].Key, workKey, &work) {
			return evidence.SourceUnavailable, nil
		}
		for _, a := range work.Authors {
			authors = append(authors, a.Author)
		}
	}
	for i, a := range authors {
		if i == maxAuthors {
			break
		}
		if !authorKey.MatchString(a.Key) {
			return evidence.SourceUnavailable, nil
		}
		var author struct {
			Name string `json:"name"`
		}
		if !o.getEntity(ctx, f, a.Key, authorKey, &author) || text(author.Name) == "" {
			return evidence.SourceUnavailable, nil
		}
		rec.Authors = appendText(rec.Authors, author.Name)
	}
	return accept(isbn, []evidence.SourceRecord{rec})
}
