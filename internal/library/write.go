package library

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// Change is one set-fields entry: a Calibre field name ("title", "sort",
// "authors", "author_sort", "languages", "identifiers", "publisher",
// "pubdate") and the JSON value sent.
type Change struct {
	Field string          `json:"field"`
	Value json.RawMessage `json:"value"`
}

// SetFields sends changes to one book, in order: Calibre applies them in body
// order, so a "sort" after a "title" overrides the sort the title recomputes.
// It returns Calibre's answer for the book. The answer carries no formats or
// cover, so Formats is empty and HasCover false; TitleSort comes from "sort".
func (c *Client) SetFields(ctx context.Context, lib string, id int, changes []Change) (Book, error) {
	if lib == "" {
		return Book{}, ErrNoLibrary
	}
	if len(changes) == 0 {
		return Book{}, errors.New("library: set-fields: no changes")
	}
	// encoding/json sorts map keys, so the body is built by hand.
	var body bytes.Buffer
	body.WriteString(`{"changes":{`)
	for i, ch := range changes {
		if i > 0 {
			body.WriteByte(',')
		}
		name, err := json.Marshal(ch.Field)
		if err != nil {
			return Book{}, fmt.Errorf("library: set-fields: %w", err)
		}
		body.Write(name)
		body.WriteByte(':')
		if err := json.Compact(&body, ch.Value); err != nil {
			return Book{}, fmt.Errorf("library: set-fields: value for %s: %w", ch.Field, err)
		}
	}
	fmt.Fprintf(&body, `},"loaded_book_ids":[%d]}`, id)

	u := c.cfg.BaseURL.JoinPath("cdb", "set-fields", strconv.Itoa(id), lib)
	var answer map[string]json.RawMessage
	// Not under MetaTimeout: a write is bounded by its caller, which gives it
	// longer (internal/fix's writeTimeout), since a write cut short leaves
	// its outcome unknown and has to be read back and restored.
	if err := c.fetchJSONOnce(ctx, "set-fields", http.MethodPost, u, body.Bytes(), &answer); err != nil {
		return Book{}, err
	}
	raw, ok := answer[strconv.Itoa(id)]
	if !ok || string(raw) == "null" {
		return Book{}, fmt.Errorf("library: set-fields: book %d is missing from the answer", id)
	}
	return decodeAnswer(id, raw)
}

// decodeAnswer reads set-fields' book_as_json, which omits empty values and
// unset dates.
func decodeAnswer(id int, raw json.RawMessage) (Book, error) {
	var values map[string]json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return Book{}, fmt.Errorf("library: set-fields: decode book %d: %w", id, err)
	}
	key := strconv.Itoa(id)
	res := listing{BookIDs: []int{id}, Data: map[string]map[string]json.RawMessage{}}
	for name, v := range values {
		res.Data[name] = map[string]json.RawMessage{key: v}
	}
	b := Book{ID: id, Formats: []Format{}}
	var lastModified, pubDate string
	var titleSort *string
	for name, dst := range map[string]any{
		"uuid": &b.UUID, "title": &b.Title, "authors": &b.Authors, "author_sort": &b.AuthorSort,
		"languages": &b.Languages, "identifiers": &b.Identifiers, "publisher": &b.Publisher,
		"last_modified": &lastModified, "pubdate": &pubDate, "sort": &titleSort,
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
	b.TitleSort = titleSort
	return b, nil
}

// FieldNames lists every name Calibre has for a field ("authors",
// "publisher"), as its web editor's autocomplete does.
func (c *Client) FieldNames(ctx context.Context, lib, field string) ([]string, error) {
	if lib == "" {
		return nil, ErrNoLibrary
	}
	u := c.cfg.BaseURL.JoinPath("interface-data", "field-names", field)
	u.RawQuery = url.Values{"library_id": {lib}}.Encode()
	var names []string
	if err := c.fetchJSON(ctx, "field-names", http.MethodGet, u, nil, &names); err != nil {
		return nil, err
	}
	return names, nil
}
