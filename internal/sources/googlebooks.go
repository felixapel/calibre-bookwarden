package sources

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

type googleBooks struct{ base, key string }

// GoogleBooks queries volumes by ISBN. Without a key it is not configured and
// makes no request: Google answers 429 to keyless queries.
func GoogleBooks(key string) Adapter {
	return &googleBooks{base: "https://www.googleapis.com", key: key}
}

func (g *googleBooks) Name() string { return evidence.SourceGoogleBooks }

type gbResponse struct {
	Error      *json.RawMessage `json:"error"`
	TotalItems *int             `json:"totalItems"`
	Items      []struct {
		ID         string `json:"id"`
		VolumeInfo struct {
			Title               string   `json:"title"`
			Subtitle            string   `json:"subtitle"`
			Authors             []string `json:"authors"`
			Publisher           string   `json:"publisher"`
			PublishedDate       string   `json:"publishedDate"`
			Language            string   `json:"language"`
			IndustryIdentifiers []struct {
				Type       string `json:"type"`
				Identifier string `json:"identifier"`
			} `json:"industryIdentifiers"`
		} `json:"volumeInfo"`
	} `json:"items"`
}

// probeISBN is a volume Google Books certainly holds (Orwell's "1984",
// Signet Classics).
const probeISBN = "9780451524935"

// Probe looks probeISBN up. Since late September 2026 Google answers every
// isbn: query with 200 and no items, which Lookup can't tell from a true
// no_match; the probe can. Without a key it passes: lookups say
// not_configured and make no request.
func (g *googleBooks) Probe(ctx context.Context, f Fetcher) bool {
	if g.key == "" {
		return true
	}
	outcome, _ := g.Lookup(ctx, f, probeISBN)
	return outcome == evidence.SourceOK
}

// Configured reports whether a key was given.
func (g *googleBooks) Configured() bool { return g.key != "" }

func (g *googleBooks) Lookup(ctx context.Context, f Fetcher, isbn string) (string, []evidence.SourceRecord) {
	if g.key == "" {
		return evidence.SourceNotConfigured, nil
	}
	// The key travels only in the header, never in the URL (ADR-019).
	status, body, err := f.Get(ctx, g.base+"/books/v1/volumes?q=isbn:"+isbn+"&maxResults=10", http.Header{"X-Goog-Api-Key": {g.key}}, maxJSONBody)
	if err != nil || status != http.StatusOK {
		return evidence.SourceUnavailable, nil
	}
	var resp gbResponse
	if json.Unmarshal(body, &resp) != nil || resp.Error != nil || resp.TotalItems == nil ||
		(len(resp.Items) == 0) != (*resp.TotalItems == 0) {
		return evidence.SourceUnavailable, nil
	}
	recs := make([]evidence.SourceRecord, 0, len(resp.Items))
	for _, item := range resp.Items {
		v := item.VolumeInfo
		rec := evidence.SourceRecord{ID: id(item.ID), Title: text(v.Title), Subtitle: text(v.Subtitle),
			Publisher: text(v.Publisher), Date: text(v.PublishedDate)}
		for _, ident := range v.IndustryIdentifiers {
			if ident.Type == "ISBN_10" || ident.Type == "ISBN_13" {
				rec.ISBNs = appendISBN(rec.ISBNs, ident.Identifier)
			}
		}
		for _, a := range v.Authors {
			rec.Authors = appendText(rec.Authors, a)
		}
		rec.Languages = appendText(rec.Languages, lang(v.Language))
		recs = append(recs, rec)
	}
	return accept(isbn, recs)
}
