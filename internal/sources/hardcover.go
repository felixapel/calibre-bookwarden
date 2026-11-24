package sources

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

// hardcoverQuery asks for the editions with one ISBN-13. The ISBN is inlined
// (the adapter only ever gets digits), so the query doesn't depend on the
// variable's declared type. The field names are pinned by
// testdata/hc-query.graphql; language { code3 } is the least certain one.
const hardcoverQuery = `query {
  editions(where: {isbn_13: {_eq: "{ISBN}"}}, limit: 11) {
    id
    book_id
    title
    subtitle
    isbn_10
    isbn_13
    release_date
    language { code3 }
    publisher { name }
    contributions { contribution author { name } }
  }
}`

// Hardcover allows 5,000 requests a day; the adapter stops at 4,500 so other
// uses of the same token keep some room. editionsAsked is the query's limit
// plus one: a full page may be cut off, so it isn't an answer.
const (
	hardcoverDailyCap = 4500
	editionsAsked     = 11
)

type hardcover struct {
	base, token string
	logger      *slog.Logger
	refused     sync.Once
	errLogged   sync.Once

	now       func() time.Time
	mu        sync.Mutex
	day       string // UTC day the count belongs to
	count     int
	capLogged string // the day the daily cap was last logged
}

// Hardcover looks an ISBN-13 up in Hardcover's GraphQL API (ADR-035). It
// needs a token (BOOKWARDEN_HARDCOVER_TOKEN); without one it makes no request
// and answers not_configured. The token travels only in the Authorization
// header. A refused token is logged once, without the token. logger may be
// nil.
func Hardcover(token string, logger *slog.Logger) Adapter {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	// Hardcover's settings page shows the token with its "Bearer " prefix.
	if len(token) > 7 && strings.EqualFold(token[:7], "bearer ") {
		token = token[7:]
	}
	return &hardcover{base: "https://api.hardcover.app", token: strings.TrimSpace(token), logger: logger, now: time.Now}
}

func (h *hardcover) Name() string { return evidence.SourceHardcover }

type hcEdition struct {
	ID          int64  `json:"id"`
	BookID      int64  `json:"book_id"`
	Title       string `json:"title"`
	Subtitle    string `json:"subtitle"`
	ISBN10      string `json:"isbn_10"`
	ISBN13      string `json:"isbn_13"`
	ReleaseDate string `json:"release_date"`
	Language    *struct {
		Code3 string `json:"code3"`
	} `json:"language"`
	Publisher *struct {
		Name string `json:"name"`
	} `json:"publisher"`
	Contributions []struct {
		Contribution *string `json:"contribution"`
		Author       *struct {
			Name string `json:"name"`
		} `json:"author"`
	} `json:"contributions"`
}

type hcResponse struct {
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
	Data *struct {
		Editions *[]hcEdition `json:"editions"`
	} `json:"data"`
}

// Configured reports whether a token was given.
func (h *hardcover) Configured() bool { return h.token != "" }

func (h *hardcover) Lookup(ctx context.Context, f Fetcher, isbn string) (string, []evidence.SourceRecord) {
	if h.token == "" {
		return evidence.SourceNotConfigured, nil
	}
	for _, r := range isbn {
		if r < '0' || r > '9' {
			return evidence.SourceUnavailable, nil
		}
	}
	if !h.allow() {
		return evidence.SourceUnavailable, nil
	}
	payload, err := json.Marshal(map[string]string{"query": strings.ReplaceAll(hardcoverQuery, "{ISBN}", isbn)})
	if err != nil {
		return evidence.SourceUnavailable, nil
	}
	header := http.Header{
		"Authorization": {"Bearer " + h.token},
		"Content-Type":  {"application/json"},
		"Accept":        {"application/json"},
	}
	status, body, err := f.Post(ctx, h.base+"/v1/graphql", header, payload, maxJSONBody)
	if errors.Is(err, ErrUnauthorized) {
		h.refused.Do(func() {
			h.logger.Warn("hardcover token refused (HTTP 401); check BOOKWARDEN_HARDCOVER_TOKEN")
		})
	}
	if err != nil || status != http.StatusOK {
		return evidence.SourceUnavailable, nil
	}
	var resp hcResponse
	if json.Unmarshal(body, &resp) != nil {
		return evidence.SourceUnavailable, nil
	}
	if len(resp.Errors) > 0 {
		// Once per process, so a wrong field name is visible. The message is
		// cleaned, capped and stripped of the token.
		h.errLogged.Do(func() {
			msg := strings.ReplaceAll(text(resp.Errors[0].Message), h.token, "[redacted]")
			h.logger.Warn("hardcover answered with a GraphQL error", "message", msg)
		})
		return evidence.SourceUnavailable, nil
	}
	if resp.Data == nil || resp.Data.Editions == nil || len(*resp.Data.Editions) >= editionsAsked {
		return evidence.SourceUnavailable, nil
	}
	// The answer's order isn't fixed (and the schema isn't verified enough to
	// ask for one), so sort by book, then edition.
	editions := slices.Clone(*resp.Data.Editions)
	slices.SortStableFunc(editions, func(a, b hcEdition) int {
		if a.BookID != b.BookID {
			return cmpInt64(a.BookID, b.BookID)
		}
		return cmpInt64(a.ID, b.ID)
	})
	recs := make([]evidence.SourceRecord, 0, len(editions))
	for _, e := range editions {
		if e.BookID <= 0 {
			continue
		}
		rec := evidence.SourceRecord{ID: id(strconv.FormatInt(e.BookID, 10)), Title: text(e.Title), Subtitle: text(e.Subtitle),
			Date: text(e.ReleaseDate)}
		rec.ISBNs = appendISBN(rec.ISBNs, e.ISBN13)
		rec.ISBNs = appendISBN(rec.ISBNs, e.ISBN10)
		if e.Publisher != nil {
			rec.Publisher = text(e.Publisher.Name)
		}
		if e.Language != nil {
			rec.Languages = appendText(rec.Languages, lang(e.Language.Code3))
		}
		for _, c := range e.Contributions {
			role := ""
			if c.Contribution != nil {
				role = strings.TrimSpace(*c.Contribution)
			}
			if c.Author == nil || (role != "" && !strings.EqualFold(role, "author")) {
				continue
			}
			rec.Authors = appendText(rec.Authors, c.Author.Name)
		}
		recs = append(recs, rec)
	}
	return accept(isbn, recs)
}

func cmpInt64(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// allow counts one request against today's (UTC) budget. Over the cap it
// refuses, and logs once a day.
func (h *hardcover) allow() bool {
	day := h.now().UTC().Format("2006-01-02")
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.day != day {
		h.day, h.count = day, 0
	}
	if h.count >= hardcoverDailyCap {
		if h.capLogged != day {
			h.capLogged = day
			h.logger.Warn("hardcover daily request cap reached; skipped until the next UTC day", "cap", hardcoverDailyCap)
		}
		return false
	}
	h.count++
	return true
}
