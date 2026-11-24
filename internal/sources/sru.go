package sources

import (
	"context"
	"net/url"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

const maxSRUBody = 4 << 20

// sru is a national library's SRU endpoint answering MARCXML.
type sru struct {
	name, base string
	query      func(base, isbn string) string
}

// DNB is the Deutsche Nationalbibliothek, read as MARC21-xml (ADR-019).
func DNB() Adapter {
	return &sru{name: evidence.SourceDNB, base: "https://services.dnb.de", query: func(base, isbn string) string {
		return base + "/sru/dnb?version=1.1&operation=searchRetrieve&query=" + url.QueryEscape("num="+isbn) + "&recordSchema=MARC21-xml&maximumRecords=10"
	}}
}

// BNE is the Biblioteca Nacional de España's Alma SRU.
func BNE() Adapter {
	return &sru{name: evidence.SourceBNE, base: "https://catalogo.bne.es", query: func(base, isbn string) string {
		return base + "/view/sru/34BNE_INST?version=1.2&operation=searchRetrieve&query=" + url.QueryEscape("alma.isbn="+isbn) + "&recordSchema=marcxml&maximumRecords=10"
	}}
}

func (s *sru) Name() string { return s.name }

func (s *sru) Lookup(ctx context.Context, f Fetcher, isbn string) (string, []evidence.SourceRecord) {
	status, body, err := f.Get(ctx, s.query(s.base, isbn), nil, maxSRUBody)
	if err != nil || status != 200 {
		return evidence.SourceUnavailable, nil
	}
	recs, total, err := parseSRU(body)
	if err != nil {
		return evidence.SourceUnavailable, nil
	}
	mapped := make([]evidence.SourceRecord, 0, len(recs))
	for _, r := range recs {
		mapped = append(mapped, marcSource(r))
	}
	// The page holds fewer records than the server matched (maximumRecords
	// caps it). The matching record, or another one for the same ISBN that
	// conflicts with those returned, may be on a page we never saw, so the
	// page answers nothing: neither no_match nor a partial ok.
	if total > len(recs) {
		return evidence.SourceUnavailable, nil
	}
	return accept(isbn, mapped)
}
