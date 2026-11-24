package library

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/seriesindex"
)

// BookRef identifies a book. A changed UUID under the same id is a
// different book.
type BookRef struct {
	ID   int
	UUID string
}

// Format is one stored format. Name is upper-case. Size is -1 when Calibre
// reports no size for it. Missing means Calibre lists the format but can't
// find its file: format_metadata holds an empty entry for it.
type Format struct {
	Name    string
	Size    int64
	MTime   time.Time
	Missing bool
}

// Book is Calibre's record of a book. PubDate is Calibre's ISO string, or ""
// when unset. Formats are sorted by name and never nil.
type Book struct {
	ID           int
	UUID, Title  string
	Authors      []string
	AuthorSort   string
	Languages    []string
	Identifiers  map[string]string
	Publisher    string
	PubDate      string
	LastModified time.Time
	HasCover     bool
	Formats      []Format
	// TitleSort is Calibre's title sort, from /ajax/books (calibredb's list
	// rejects the field). Nil means the server didn't send one.
	TitleSort *string
	// AuthorSortMap is each author's sort in Calibre's authors table, from
	// /ajax/books. Nil means the server didn't send one.
	AuthorSortMap map[string]string
	// Series is Calibre's series name, or "" when the book has none.
	// SeriesIndex is the position as a canonical decimal string ("1", "2.5",
	// see package seriesindex), or "" when Calibre sent none or one that
	// isn't a plain non-negative decimal. Calibre reports an index of 1.0
	// for a book without a series (docs/calibre-protocol.md), so the index
	// means something only next to a series.
	Series, SeriesIndex string
}

type listing struct {
	BookIDs []int                                 `json:"book_ids"`
	Data    map[string]map[string]json.RawMessage `json:"data"`
}

// typeKey marks Calibre's typed values: {"jPoAv3zOyHvQ5JFNYg4hJ9": <type>, "v": <data>}.
const typeKey = "jPoAv3zOyHvQ5JFNYg4hJ9"

var bookFields = []string{
	"title", "authors", "author_sort", "languages", "identifiers", "formats",
	"last_modified", "uuid", "publisher", "pubdate", "cover", "series", "series_index",
}

// ListIDs returns every book's id and uuid, in id order.
func (c *Client) ListIDs(ctx context.Context) ([]BookRef, error) {
	lib, err := c.library(ctx)
	if err != nil {
		return nil, err
	}
	return c.ListIDsInLibrary(ctx, lib)
}

// ListIDsInLibrary reads the explicit library without consulting the default cache.
func (c *Client) ListIDsInLibrary(ctx context.Context, lib string) ([]BookRef, error) {
	if lib == "" {
		return nil, ErrNoLibrary
	}
	var res listing
	if err := c.cdb(ctx, lib, "list", []any{[]string{"uuid"}, "id", true, "", -1}, &res); err != nil {
		return nil, err
	}
	refs := make([]BookRef, 0, len(res.BookIDs))
	for _, id := range res.BookIDs {
		var uuid string
		if err := field(res, "uuid", id, &uuid); err != nil {
			return nil, err
		}
		refs = append(refs, BookRef{ID: id, UUID: uuid})
	}
	slices.SortFunc(refs, func(a, b BookRef) int { return a.ID - b.ID })
	return refs, nil
}

// Book reads one book's record from the listing and its per-format sizes
// from /ajax/books. It returns ErrBookNotFound when either answer lacks it.
func (c *Client) Book(ctx context.Context, id int) (Book, error) {
	lib, err := c.library(ctx)
	if err != nil {
		return Book{}, err
	}
	return c.BookInLibrary(ctx, lib, id)
}

// BookInLibrary binds both record and format metadata reads to one library.
func (c *Client) BookInLibrary(ctx context.Context, lib string, id int) (Book, error) {
	if lib == "" {
		return Book{}, ErrNoLibrary
	}
	var res listing
	if err := c.cdb(ctx, lib, "list", []any{bookFields, "id", true, "id:" + strconv.Itoa(id), -1}, &res); err != nil {
		return Book{}, err
	}
	if !slices.Contains(res.BookIDs, id) {
		return Book{}, fmt.Errorf("library: book %d: %w", id, ErrBookNotFound)
	}
	b := Book{ID: id}
	var (
		lastModified, pubDate string
		formats               []string
		cover                 json.Number
		seriesIndex           json.Number
	)
	for name, dst := range map[string]any{
		"uuid": &b.UUID, "title": &b.Title, "authors": &b.Authors, "author_sort": &b.AuthorSort,
		"languages": &b.Languages, "identifiers": &b.Identifiers, "publisher": &b.Publisher,
		"last_modified": &lastModified, "pubdate": &pubDate, "formats": &formats, "cover": &cover,
		"series": &b.Series, "series_index": &seriesIndex,
	} {
		if err := field(res, name, id, dst); err != nil {
			return Book{}, err
		}
	}
	if t, ok := calibreTime(lastModified); ok {
		b.LastModified = t
	}
	if _, ok := calibreTime(pubDate); ok {
		b.PubDate = pubDate
	}
	b.HasCover = cover != "" && cover != "0"
	b.SeriesIndex, _ = seriesindex.Normalize(seriesIndex.String())

	meta, x, err := c.bookExtras(ctx, lib, id)
	if err != nil {
		return Book{}, err
	}
	b.TitleSort, b.AuthorSortMap = x.TitleSort, x.AuthorSortMap
	b.Formats = make([]Format, 0, len(formats))
	for _, name := range formats {
		f := Format{Name: strings.ToUpper(name), Size: -1}
		switch m, ok := meta[strings.ToLower(name)]; {
		case ok && m.Size != nil:
			f.Size = *m.Size
			f.MTime, _ = calibreTime(m.MTime)
		case ok:
			// Calibre's format_metadata is {} when format_abspath finds no
			// file, so the format is listed but gone from disk.
			f.Missing = true
		}
		b.Formats = append(b.Formats, f)
	}
	slices.SortFunc(b.Formats, func(x, y Format) int { return strings.Compare(x.Name, y.Name) })
	return b, nil
}

// formatMeta is the part of format_metadata bookwarden keeps. The server
// path Calibre sends next to it is never decoded.
type formatMeta struct {
	Size  *int64 `json:"size"`
	MTime string `json:"mtime"`
}

// sorts is what bookExtras reads besides the format metadata.
type sorts struct {
	TitleSort     *string           `json:"title_sort"`
	AuthorSortMap map[string]string `json:"author_sort_map"`
}

// bookExtras reads what only /ajax/books has: the format metadata, the
// title sort and the authors' sorts.
func (c *Client) bookExtras(ctx context.Context, lib string, id int) (map[string]formatMeta, sorts, error) {
	u := c.cfg.BaseURL.JoinPath("ajax", "books", lib)
	u.RawQuery = url.Values{"ids": {strconv.Itoa(id)}}.Encode()
	var books map[string]*struct {
		FormatMetadata map[string]formatMeta `json:"format_metadata"`
		sorts
	}
	if err := c.fetchJSON(ctx, "books", http.MethodGet, u, nil, &books); err != nil {
		return nil, sorts{}, err
	}
	entry := books[strconv.Itoa(id)]
	if entry == nil {
		return nil, sorts{}, fmt.Errorf("library: book %d: %w", id, ErrBookNotFound)
	}
	return entry.FormatMetadata, entry.sorts, nil
}

// field decodes one listed value, unwrapping Calibre's typed values. A
// missing or null value leaves dst unchanged.
func field(res listing, name string, id int, dst any) error {
	raw, ok := res.Data[name][strconv.Itoa(id)]
	if !ok || string(raw) == "null" {
		return nil
	}
	var typed map[string]json.RawMessage
	if json.Unmarshal(raw, &typed) == nil {
		if v, ok := typed["v"]; ok {
			if _, ok := typed[typeKey]; ok {
				raw = v
			}
		}
	}
	if string(raw) == "null" {
		return nil
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("library: book %d: decode %s: %w", id, name, err)
	}
	return nil
}

// calibreTime parses a Calibre datetime. Calibre stores "unset" as a date in
// the year 101, so anything before the year 1000 counts as unset.
func calibreTime(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil || t.Year() < 1000 {
		return time.Time{}, false
	}
	return t.UTC(), true
}
