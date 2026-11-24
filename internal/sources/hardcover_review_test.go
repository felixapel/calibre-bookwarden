package sources_test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/sources"
)

func hcEdition(id, bookID int, isbn string) string {
	return fmt.Sprintf(`{"id":%d,"book_id":%d,"title":"T%d","isbn_13":"%s"}`, id, bookID, id, isbn)
}

func hcEditions(editions ...string) answer {
	return answer{status: 200, body: []byte(`{"data":{"editions":[` + strings.Join(editions, ",") + `]}}`)}
}

// The query asks for 11 editions; a full 11 may be cut off.
func TestHardcoverTruncated(t *testing.T) {
	const isbn = "9780765326355"
	var eds []string
	for i := range 10 {
		eds = append(eds, hcEdition(i+1, i+1, isbn))
	}
	if outcome, recs := lookup(t, hcAdapter(hcToken, nil), hcFetcher(hcEditions(eds...)), isbn); outcome != evidence.SourceOK || len(recs) != 8 {
		t.Fatalf("10 editions: %s %d", outcome, len(recs))
	}
	eds = append(eds, hcEdition(11, 11, isbn))
	if outcome, recs := lookup(t, hcAdapter(hcToken, nil), hcFetcher(hcEditions(eds...)), isbn); outcome != evidence.SourceUnavailable || recs != nil {
		t.Fatalf("11 editions: %s %v", outcome, recs)
	}
}

// The same editions in another order give the same records.
func TestHardcoverOrderIndependent(t *testing.T) {
	const isbn = "9780765326355"
	eds := []string{hcEdition(5, 2, isbn), hcEdition(9, 1, isbn), hcEdition(3, 2, isbn)}
	_, a := lookup(t, hcAdapter(hcToken, nil), hcFetcher(hcEditions(eds...)), isbn)
	_, b := lookup(t, hcAdapter(hcToken, nil), hcFetcher(hcEditions(eds[2], eds[0], eds[1])), isbn)
	if len(a) != 3 || len(b) != 3 {
		t.Fatalf("%+v / %+v", a, b)
	}
	for i := range a {
		if !equalRecord(a[i], b[i]) {
			t.Fatalf("record %d differs:\n%+v\n%+v", i, a[i], b[i])
		}
	}
	if a[0].ID != "1" || a[1].Title != "T3" || a[2].Title != "T5" {
		t.Fatalf("order = %+v", a)
	}
}

// 4,500 requests a UTC day, then unavailable without a request; logged once
// a day; a new day starts again.
func TestHardcoverDailyLimit(t *testing.T) {
	var buf bytes.Buffer
	a := hcAdapter(hcToken, &buf)
	now := time.Date(2026, 10, 29, 23, 0, 0, 0, time.UTC)
	sources.SetClock(a, func() time.Time { return now })
	f := hcFetcher(hcEditions())
	for i := range 4500 {
		if outcome, _ := lookup(t, a, f, "9780765326355"); outcome != evidence.SourceNoMatch {
			t.Fatalf("request %d: %s", i+1, outcome)
		}
	}
	for range 3 {
		if outcome, _ := lookup(t, a, f, "9780765326355"); outcome != evidence.SourceUnavailable {
			t.Fatalf("over the limit: %s", outcome)
		}
	}
	if len(f.urls) != 4500 {
		t.Fatalf("%d requests", len(f.urls))
	}
	if strings.Count(buf.String(), "daily") != 1 || strings.Contains(buf.String(), hcToken) {
		t.Fatalf("log = %q", buf.String())
	}
	now = now.Add(2 * time.Hour) // past midnight UTC
	if outcome, _ := lookup(t, a, f, "9780765326355"); outcome != evidence.SourceNoMatch || len(f.urls) != 4501 {
		t.Fatalf("next day: %s, %d requests", outcome, len(f.urls))
	}
}

// The first GraphQL error message is logged once, token redacted, so a wrong
// field name shows.
func TestHardcoverLogsFirstGraphQLError(t *testing.T) {
	var buf bytes.Buffer
	a := hcAdapter(hcToken, &buf)
	bad := answer{status: 200, body: []byte(`{"errors":[{"message":"field 'language' not found in type: 'editions' ` + hcToken + `"}]}`)}
	for range 3 {
		if outcome, _ := lookup(t, a, hcFetcher(bad), "9780765326355"); outcome != evidence.SourceUnavailable {
			t.Fatal(outcome)
		}
	}
	out := buf.String()
	if strings.Count(out, "\n") != 1 || !strings.Contains(out, "field 'language' not found") || strings.Contains(out, hcToken) {
		t.Fatalf("log = %q", out)
	}
}
