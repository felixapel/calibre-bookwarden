package library_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/library"
)

func change(field, value string) library.Change {
	return library.Change{Field: field, Value: json.RawMessage(value)}
}

const setFieldsRoute = "POST /cdb/set-fields/7/lib one"

const book7Answer = `{"7": {"title": "Lighthouse Signals", "sort": "Lighthouse Signals",
	"authors": ["Bea Sample"], "author_sort": "Sample, Bea", "languages": ["eng"],
	"identifiers": {"isbn": "9780000000019"}, "publisher": "Harbour Press",
	"pubdate": "2001-07-26T00:00:00+00:00", "last_modified": "2026-10-19T08:00:00.123456+00:00",
	"uuid": "u-7", "formats": ["EPUB"], "lang_names": {"eng": "English"}}}`

func TestSetFieldsRequest(t *testing.T) {
	var method, path, ctype string
	var body []byte
	c := fakeCalibre(t, map[string]http.HandlerFunc{
		setFieldsRoute: func(w http.ResponseWriter, r *http.Request) {
			method, path, ctype = r.Method, r.URL.EscapedPath(), r.Header.Get("Content-Type")
			body, _ = io.ReadAll(r.Body)
			jsonAnswer(book7Answer)(w, r)
		},
	})
	_, err := c.SetFields(context.Background(), "lib one", 7, []library.Change{change("title", `"T"`), change("sort", `"S"`)})
	if err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPost || path != "/cdb/set-fields/7/lib%20one" || ctype != "application/json" {
		t.Errorf("request = %s %s (%s)", method, path, ctype)
	}
	if want := `{"changes":{"title":"T","sort":"S"},"loaded_book_ids":[7]}`; string(body) != want {
		t.Errorf("body = %s, want %s", body, want)
	}
}

func TestSetFieldsAnswer(t *testing.T) {
	c := fakeCalibre(t, map[string]http.HandlerFunc{setFieldsRoute: jsonAnswer(book7Answer)})
	got, err := c.SetFields(context.Background(), "lib one", 7, []library.Change{change("title", `"Lighthouse Signals"`)})
	if err != nil {
		t.Fatal(err)
	}
	sort := "Lighthouse Signals"
	want := library.Book{
		ID: 7, UUID: "u-7", Title: "Lighthouse Signals", Authors: []string{"Bea Sample"}, AuthorSort: "Sample, Bea",
		Languages: []string{"eng"}, Identifiers: map[string]string{"isbn": "9780000000019"}, Publisher: "Harbour Press",
		PubDate: "2001-07-26T00:00:00+00:00", LastModified: time.Date(2026, 10, 19, 8, 0, 0, 123456000, time.UTC),
		Formats: []library.Format{}, TitleSort: &sort,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("answer = %+v\nwant     %+v", got, want)
	}

	c = fakeCalibre(t, map[string]http.HandlerFunc{setFieldsRoute: jsonAnswer(`{"7": {"title": "T", "authors": ["A"], "last_modified": "2026-10-19T08:00:00+00:00"}}`)})
	got, err = c.SetFields(context.Background(), "lib one", 7, []library.Change{change("publisher", `""`)})
	if err != nil || got.Publisher != "" || got.PubDate != "" || got.TitleSort != nil {
		t.Errorf("answer without publisher, pubdate and sort = %+v, %v", got, err)
	}
	c = fakeCalibre(t, map[string]http.HandlerFunc{setFieldsRoute: jsonAnswer(`{"7": {"title": "T", "pubdate": "0101-01-01T00:00:00+00:00"}}`)})
	if got, err = c.SetFields(context.Background(), "lib one", 7, []library.Change{change("pubdate", `null`)}); err != nil || got.PubDate != "" {
		t.Errorf("unset pubdate = %q, %v", got.PubDate, err)
	}
}

func TestSetFieldsErrors(t *testing.T) {
	one := []library.Change{change("title", `"T"`)}
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		changes []library.Change
		is      error
		text    string
	}{
		{name: "read-only account", handler: status(403, "Forbidden"), changes: one, is: library.ErrForbidden},
		{name: "bad password", handler: status(401, "Unauthorized"), changes: one, is: library.ErrAuth},
		{name: "server error", handler: status(500, "boom"), changes: one, is: library.ErrUnreachable},
		{name: "bad request", handler: status(400, "Data must be of the form"), changes: one, text: "HTTP 400"},
		{name: "book missing from the answer", handler: jsonAnswer(`{"8": {"title": "T"}}`), changes: one, text: "book 7"},
		{name: "no changes", handler: jsonAnswer(book7Answer), changes: nil, text: "no changes"},
		{name: "invalid value", handler: jsonAnswer(book7Answer), changes: []library.Change{change("title", `"T`)}, text: "title"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			c := fakeCalibre(t, map[string]http.HandlerFunc{setFieldsRoute: func(w http.ResponseWriter, r *http.Request) {
				called = true
				tc.handler(w, r)
			}})
			_, err := c.SetFields(context.Background(), "lib one", 7, tc.changes)
			switch {
			case err == nil:
				t.Fatal("SetFields succeeded")
			case tc.is != nil && !errors.Is(err, tc.is):
				t.Errorf("err = %v, want %v", err, tc.is)
			case tc.text != "" && !strings.Contains(err.Error(), tc.text):
				t.Errorf("err = %v, want it to mention %q", err, tc.text)
			}
			if called != (len(tc.changes) > 0 && tc.text != "title") {
				t.Errorf("request made = %v", called)
			}
		})
	}
}

func TestFieldNames(t *testing.T) {
	var query string
	c := fakeCalibre(t, map[string]http.HandlerFunc{
		"GET /interface-data/field-names/authors": func(w http.ResponseWriter, r *http.Request) {
			query = r.URL.RawQuery
			jsonAnswer(`["A", "b"]`)(w, r)
		},
		"GET /interface-data/field-names/publisher": jsonAnswer(`{"a": 1}`),
	})
	got, err := c.FieldNames(context.Background(), "lib one", "authors")
	if err != nil || !reflect.DeepEqual(got, []string{"A", "b"}) || query != "library_id=lib+one" {
		t.Errorf("FieldNames = %v, %v (query %q)", got, err, query)
	}
	if _, err := c.FieldNames(context.Background(), "lib one", "publisher"); err == nil {
		t.Error("a non-array answer was accepted")
	}
}

func TestWritesCarryNoSecrets(t *testing.T) {
	var sawPassword bool
	spy := func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.String(), testPassword) || strings.Contains(r.Header.Get("Authorization"), testPassword) {
			sawPassword = true
		}
		status(500, "internal error")(w, r)
	}
	c := fakeCalibre(t, map[string]http.HandlerFunc{setFieldsRoute: spy, "GET /interface-data/field-names/authors": spy})
	_, err1 := c.SetFields(context.Background(), "lib one", 7, []library.Change{change("title", `"T"`)})
	_, err2 := c.FieldNames(context.Background(), "lib one", "authors")
	for _, err := range []error{err1, err2} {
		if err == nil || strings.Contains(err.Error(), testPassword) {
			t.Errorf("err = %v", err)
		}
	}
	if sawPassword {
		t.Error("the password travelled in clear text")
	}
}
