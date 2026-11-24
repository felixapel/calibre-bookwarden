package library_test

import (
	"context"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/library"
)

const testPassword = "s3cret-reader-pass"

// fakeCalibre serves routes keyed by "METHOD /path". Routes the test doesn't
// set answer the way a healthy Calibre with one library, "library", does.
func fakeCalibre(t *testing.T, routes map[string]http.HandlerFunc) *library.Client {
	t.Helper()
	c, _ := fakeCalibreConfig(t, routes, "")
	return c
}

func fakeCalibreConfig(t *testing.T, routes map[string]http.HandlerFunc, libraryID string) (*library.Client, *httptest.Server) {
	t.Helper()
	all := map[string]http.HandlerFunc{
		"GET /ajax/library-info": jsonAnswer(`{"library_map": {"library": "library"}, "default_library": "library"}`),
		"POST /cdb/cmd/list/0":   jsonAnswer(`{"result": {"book_ids": [], "data": {"uuid": {}}}}`),
		"POST /cdb/cmd/export/0": msgpackAnswer("\x81\xa6result\xc0"),
	}
	for k, v := range routes {
		all[k] = v
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h, ok := all[r.Method+" "+r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	base, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return library.New(library.Config{BaseURL: base, User: "reader", Password: testPassword, LibraryID: libraryID}), srv
}

func jsonAnswer(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=UTF-8")
		_, _ = io.WriteString(w, body)
	}
}

func msgpackAnswer(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-msgpack")
		_, _ = io.WriteString(w, body)
	}
}

func status(code int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(code)
		_, _ = io.WriteString(w, body)
	}
}

// listFor answers a cdb list request for book 3 with the given field values,
// each a JSON fragment; fields left out get fixture book 3's values.
func listFor(fields map[string]string) http.HandlerFunc {
	vals := map[string]string{
		"title":         `"Binary Tides"`,
		"authors":       `["Cy Placeholder"]`,
		"author_sort":   `"Placeholder, Cy"`,
		"languages":     `["eng"]`,
		"identifiers":   `{"isbn": "9780000000026"}`,
		"formats":       `["AZW3", "EPUB"]`,
		"last_modified": `{"jPoAv3zOyHvQ5JFNYg4hJ9": 0, "v": "2026-10-12T21:05:55+00:00"}`,
		"uuid":          `"0c57e75c-16aa-4d88-ae0e-916c8f0e8b8e"`,
		"publisher":     `null`,
		"pubdate":       `{"jPoAv3zOyHvQ5JFNYg4hJ9": 0, "v": "0101-01-01T00:00:00+00:00"}`,
		"cover":         `0`,
	}
	for k, v := range fields {
		vals[k] = v
	}
	var b strings.Builder
	b.WriteString(`{"result": {"book_ids": [3], "data": {`)
	first := true
	for k, v := range vals {
		if !first {
			b.WriteString(", ")
		}
		first = false
		b.WriteString(`"` + k + `": {"3": ` + v + `}`)
	}
	b.WriteString(`}}}`)
	return jsonAnswer(b.String())
}

const book3Metadata = `{"3": {"title": "Binary Tides", "format_metadata": {
	"azw3": {"path": "/library/Cy Placeholder/Binary Tides (3)/Binary Tides - Cy Placeholder.azw3", "size": 11075, "mtime": "2026-10-12T21:05:55.498462+00:00"},
	"epub": {"path": "/library/Cy Placeholder/Binary Tides (3)/Binary Tides - Cy Placeholder.epub", "size": 2326, "mtime": "2026-10-12T21:05:55.024520+00:00"}}}}`

var book3Routes = map[string]http.HandlerFunc{
	"POST /cdb/cmd/list/0":    listFor(nil),
	"GET /ajax/books/library": jsonAnswer(book3Metadata),
}

func TestBook(t *testing.T) {
	c := fakeCalibre(t, book3Routes)
	got, err := c.Book(context.Background(), 3)
	if err != nil {
		t.Fatal(err)
	}
	want := library.Book{
		ID: 3, UUID: "0c57e75c-16aa-4d88-ae0e-916c8f0e8b8e", Title: "Binary Tides",
		Authors: []string{"Cy Placeholder"}, AuthorSort: "Placeholder, Cy", Languages: []string{"eng"},
		Identifiers: map[string]string{"isbn": "9780000000026"}, Publisher: "", PubDate: "",
		LastModified: time.Date(2026, 10, 12, 21, 5, 55, 0, time.UTC), HasCover: false,
		Formats: []library.Format{
			{Name: "AZW3", Size: 11075, MTime: time.Date(2026, 10, 12, 21, 5, 55, 498462000, time.UTC)},
			{Name: "EPUB", Size: 2326, MTime: time.Date(2026, 10, 12, 21, 5, 55, 24520000, time.UTC)},
		},
	}
	if !reflect.DeepEqual(utc(got), want) {
		t.Fatalf("Book(3):\n got %+v\nwant %+v", got, want)
	}
}

// utc normalises the book's times so DeepEqual compares instants.
func utc(b library.Book) library.Book {
	b.LastModified = b.LastModified.UTC()
	for i := range b.Formats {
		b.Formats[i].MTime = b.Formats[i].MTime.UTC()
	}
	return b
}

func TestBookFields(t *testing.T) {
	tests := []struct {
		name  string
		list  map[string]string
		meta  string // the /ajax/books answer; "" means book 3's
		check func(t *testing.T, b library.Book, err error)
	}{
		{
			name: "placeholder pubdate",
			check: func(t *testing.T, b library.Book, err error) {
				mustOK(t, err)
				if b.PubDate != "" {
					t.Errorf("PubDate = %q, want empty", b.PubDate)
				}
			},
		},
		{
			name: "null pubdate",
			list: map[string]string{"pubdate": `null`},
			check: func(t *testing.T, b library.Book, err error) {
				mustOK(t, err)
				if b.PubDate != "" {
					t.Errorf("PubDate = %q, want empty", b.PubDate)
				}
			},
		},
		{
			name: "real pubdate",
			list: map[string]string{"pubdate": `{"jPoAv3zOyHvQ5JFNYg4hJ9": 0, "v": "2019-05-04T00:00:00+00:00"}`},
			check: func(t *testing.T, b library.Book, err error) {
				mustOK(t, err)
				if b.PubDate != "2019-05-04T00:00:00+00:00" {
					t.Errorf("PubDate = %q", b.PubDate)
				}
			},
		},
		{
			name: "format absent from format_metadata",
			list: map[string]string{"formats": `["EPUB", "PDF"]`},
			meta: `{"3": {"format_metadata": {"epub": {"path": "/x", "size": 5, "mtime": "2026-10-12T21:05:55+00:00"}}}}`,
			check: func(t *testing.T, b library.Book, err error) {
				mustOK(t, err)
				if len(b.Formats) != 2 || b.Formats[1].Name != "PDF" || b.Formats[1].Size != -1 || b.Formats[1].Missing {
					t.Errorf("Formats = %+v, want PDF with Size -1", b.Formats)
				}
				if b.Formats[0].Size != 5 {
					t.Errorf("EPUB size = %d, want 5", b.Formats[0].Size)
				}
			},
		},
		{
			// The file was already gone when calibre-server started: Calibre
			// lists the format but can't find it, and sends an empty entry.
			name: "file gone from disk",
			list: map[string]string{"formats": `["EPUB"]`},
			meta: `{"3": {"format_metadata": {"epub": {}}}}`,
			check: func(t *testing.T, b library.Book, err error) {
				mustOK(t, err)
				if len(b.Formats) != 1 || !b.Formats[0].Missing || b.Formats[0].Size != -1 {
					t.Errorf("Formats = %+v, want EPUB Missing with Size -1", b.Formats)
				}
			},
		},
		{
			name: "no formats",
			list: map[string]string{"formats": `[]`},
			meta: `{"3": {"format_metadata": {}}}`,
			check: func(t *testing.T, b library.Book, err error) {
				mustOK(t, err)
				if b.Formats == nil || len(b.Formats) != 0 {
					t.Errorf("Formats = %#v, want empty and non-nil", b.Formats)
				}
			},
		},
		{
			name: "null publisher",
			list: map[string]string{"publisher": `null`},
			check: func(t *testing.T, b library.Book, err error) {
				mustOK(t, err)
				if b.Publisher != "" {
					t.Errorf("Publisher = %q", b.Publisher)
				}
			},
		},
		{
			name: "real publisher and cover",
			list: map[string]string{"publisher": `"Example Press"`, "cover": `1`},
			check: func(t *testing.T, b library.Book, err error) {
				mustOK(t, err)
				if b.Publisher != "Example Press" || !b.HasCover {
					t.Errorf("Publisher = %q, HasCover = %v", b.Publisher, b.HasCover)
				}
			},
		},
		{
			name: "no series",
			list: map[string]string{"series": `null`, "series_index": `1.0`},
			check: func(t *testing.T, b library.Book, err error) {
				mustOK(t, err)
				if b.Series != "" || b.SeriesIndex != "1" {
					t.Errorf("Series = %q, SeriesIndex = %q", b.Series, b.SeriesIndex)
				}
			},
		},
		{
			name: "series fields not sent",
			check: func(t *testing.T, b library.Book, err error) {
				mustOK(t, err)
				if b.Series != "" || b.SeriesIndex != "" {
					t.Errorf("Series = %q, SeriesIndex = %q", b.Series, b.SeriesIndex)
				}
			},
		},
		{
			name: "series with a fractional index",
			list: map[string]string{"series": `"The Spike Saga"`, "series_index": `2.5`},
			check: func(t *testing.T, b library.Book, err error) {
				mustOK(t, err)
				if b.Series != "The Spike Saga" || b.SeriesIndex != "2.5" {
					t.Errorf("Series = %q, SeriesIndex = %q", b.Series, b.SeriesIndex)
				}
			},
		},
		{
			name: "series with a whole index Python writes as a float",
			list: map[string]string{"series": `"The Spike Saga"`, "series_index": `3.0`},
			check: func(t *testing.T, b library.Book, err error) {
				mustOK(t, err)
				if b.SeriesIndex != "3" {
					t.Errorf("SeriesIndex = %q, want 3", b.SeriesIndex)
				}
			},
		},
		{
			name: "series index in exponent form",
			list: map[string]string{"series": `"S"`, "series_index": `1e-05`},
			check: func(t *testing.T, b library.Book, err error) {
				mustOK(t, err)
				if b.SeriesIndex != "0.00001" {
					t.Errorf("SeriesIndex = %q", b.SeriesIndex)
				}
			},
		},
		{
			name: "negative series index",
			list: map[string]string{"series": `"S"`, "series_index": `-2.0`},
			check: func(t *testing.T, b library.Book, err error) {
				mustOK(t, err)
				if b.Series != "S" || b.SeriesIndex != "" {
					t.Errorf("Series = %q, SeriesIndex = %q", b.Series, b.SeriesIndex)
				}
			},
		},
		{
			name: "deleted between calls",
			meta: `{"3": null}`,
			check: func(t *testing.T, _ library.Book, err error) {
				if !errors.Is(err, library.ErrBookNotFound) {
					t.Errorf("err = %v, want ErrBookNotFound", err)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			meta := tt.meta
			if meta == "" {
				meta = book3Metadata
			}
			c := fakeCalibre(t, map[string]http.HandlerFunc{
				"POST /cdb/cmd/list/0":    listFor(tt.list),
				"GET /ajax/books/library": jsonAnswer(meta),
			})
			b, err := c.Book(context.Background(), 3)
			tt.check(t, b, err)
		})
	}
}

func TestBookErrors(t *testing.T) {
	tests := []struct {
		name string
		list string
		want func(t *testing.T, err error)
	}{
		{
			name: "unknown id",
			list: `{"result": {"book_ids": [], "data": {"uuid": {}}}}`,
			want: func(t *testing.T, err error) {
				if !errors.Is(err, library.ErrBookNotFound) {
					t.Errorf("err = %v, want ErrBookNotFound", err)
				}
			},
		},
		{
			name: "unknown field",
			list: `{"result": "Unknown fields: sort"}`,
			want: func(t *testing.T, err error) {
				var ce *library.CalibreError
				if !errors.As(err, &ce) || ce.Message != "Unknown fields: sort" {
					t.Errorf("err = %v, want CalibreError(Unknown fields: sort)", err)
				}
			},
		},
		{
			name: "Calibre error",
			list: `{"err": "boom", "tb": "Traceback (most recent call last):\n  File secret.py"}`,
			want: func(t *testing.T, err error) {
				var ce *library.CalibreError
				if !errors.As(err, &ce) || ce.Message != "boom" {
					t.Fatalf("err = %v, want CalibreError(boom)", err)
				}
				if strings.Contains(err.Error(), "Traceback") {
					t.Errorf("error carries the traceback: %v", err)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// The library id is already resolved, so the list answer reaches Book.
			c := fakeCalibre(t, map[string]http.HandlerFunc{
				"POST /cdb/cmd/list/0":    bookListOrProbe(tt.list),
				"GET /ajax/books/library": jsonAnswer(book3Metadata),
			})
			_, err := c.Book(context.Background(), 3)
			tt.want(t, err)
		})
	}
}

// bookListOrProbe answers Check's probe (a search that matches nothing)
// normally, and the request for book 3 with answer.
func bookListOrProbe(answer string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"id:3"`) {
			jsonAnswer(answer)(w, r)
			return
		}
		jsonAnswer(`{"result": {"book_ids": [], "data": {"uuid": {}}}}`)(w, r)
	}
}

func mustOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

const versionText = "The module list is not available in version: 0.Make sure that you are using the same version of calibre on the client and server"

func TestCheck(t *testing.T) {
	challenge := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", `Digest realm="calibre", nonce="abc", qop="auth", algorithm="MD5"`)
		w.WriteHeader(http.StatusUnauthorized)
	}
	tests := []struct {
		name      string
		libraryID string
		routes    map[string]http.HandlerFunc
		closed    bool
		want      string
		wantErr   error
		errHas    string
	}{
		{name: "default library", want: "library"},
		{name: "configured library", libraryID: "library", want: "library"},
		{name: "library not in the map", libraryID: "other", wantErr: library.ErrNoLibrary},
		{
			name:    "list version mismatch",
			routes:  map[string]http.HandlerFunc{"POST /cdb/cmd/list/0": status(404, versionText)},
			wantErr: library.ErrIncompatible, errHas: "list",
		},
		{
			name:    "export version mismatch",
			routes:  map[string]http.HandlerFunc{"POST /cdb/cmd/export/0": status(404, strings.Replace(versionText, "list", "export", 1))},
			wantErr: library.ErrIncompatible, errHas: "export",
		},
		{
			name: "wrong password",
			routes: map[string]http.HandlerFunc{
				"GET /ajax/library-info": challenge,
				"POST /cdb/cmd/list/0":   challenge,
				"POST /cdb/cmd/export/0": challenge,
			},
			wantErr: library.ErrAuth,
		},
		{
			name: "account without cdb access",
			routes: map[string]http.HandlerFunc{
				"POST /cdb/cmd/list/0":   status(403, "Forbidden"),
				"POST /cdb/cmd/export/0": status(403, "Forbidden"),
			},
			wantErr: library.ErrForbidden,
		},
		{
			name:    "server error",
			routes:  map[string]http.HandlerFunc{"GET /ajax/library-info": status(503, "busy")},
			wantErr: library.ErrUnreachable,
		},
		{name: "server closed", closed: true, wantErr: library.ErrUnreachable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, srv := fakeCalibreConfig(t, tt.routes, tt.libraryID)
			if tt.closed {
				srv.Close()
			}
			got, err := c.Check(context.Background())
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Check() err = %v, want %v", err, tt.wantErr)
				}
				if tt.errHas != "" && !strings.Contains(err.Error(), tt.errHas) {
					t.Errorf("error %q doesn't name %q", err, tt.errHas)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("Check() = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestCheckReturnsContextErrors(t *testing.T) {
	c := fakeCalibre(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.Check(ctx)
	if !errors.Is(err, context.Canceled) || errors.Is(err, library.ErrUnreachable) {
		t.Fatalf("err = %v, want context.Canceled and not ErrUnreachable", err)
	}
}

func TestListIDs(t *testing.T) {
	var body string
	c := fakeCalibre(t, map[string]http.HandlerFunc{
		"POST /cdb/cmd/list/0": func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			if strings.Contains(string(b), `"id:0"`) { // Check's probe
				jsonAnswer(`{"result": {"book_ids": [], "data": {"uuid": {}}}}`)(w, r)
				return
			}
			body = string(b)
			if r.URL.Query().Get("library_id") != "library" {
				t.Errorf("library_id = %q", r.URL.Query().Get("library_id"))
			}
			jsonAnswer(`{"result": {"book_ids": [4, 1, 2], "data": {"uuid": {"1": "u1", "2": "u2", "4": "u4"}}}}`)(w, r)
		},
	})
	got, err := c.ListIDs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []library.BookRef{{ID: 1, UUID: "u1"}, {ID: 2, UUID: "u2"}, {ID: 4, UUID: "u4"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ListIDs() = %+v, want %+v", got, want)
	}
	if body != `[["uuid"],"id",true,"",-1]` {
		t.Errorf("request body = %s", body)
	}
}

func TestRequestsCarryNoSecrets(t *testing.T) {
	var sawPassword bool
	c := fakeCalibre(t, map[string]http.HandlerFunc{
		"GET /ajax/library-info": func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.String(), testPassword) || strings.Contains(r.Header.Get("Authorization"), testPassword) {
				sawPassword = true
			}
			status(500, "internal error")(w, r)
		},
	})
	_, err := c.Check(context.Background())
	if err == nil {
		t.Fatal("Check() succeeded against a failing server")
	}
	if strings.Contains(err.Error(), testPassword) {
		t.Errorf("error carries the password: %v", err)
	}
	if sawPassword {
		t.Error("the password travelled in clear text")
	}
}

func TestBookTitleSort(t *testing.T) {
	sort := func(s string) *string { return &s }
	for _, tt := range []struct {
		name, meta string
		want       *string
	}{
		{"captured", `{"3": {"title_sort": "Clockwork Garden, The", "format_metadata": {}}}`, sort("Clockwork Garden, The")},
		{"empty", `{"3": {"title_sort": "", "format_metadata": {}}}`, sort("")},
		{"absent", `{"3": {"format_metadata": {}}}`, nil},
		{"null", `{"3": {"title_sort": null, "format_metadata": {}}}`, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := fakeCalibre(t, map[string]http.HandlerFunc{
				"POST /cdb/cmd/list/0":    listFor(nil),
				"GET /ajax/books/library": jsonAnswer(tt.meta),
			})
			b, err := c.Book(context.Background(), 3)
			if err != nil {
				t.Fatal(err)
			}
			switch {
			case tt.want == nil && b.TitleSort != nil:
				t.Fatalf("TitleSort = %q, want nil", *b.TitleSort)
			case tt.want != nil && (b.TitleSort == nil || *b.TitleSort != *tt.want):
				t.Fatalf("TitleSort = %v, want %q", b.TitleSort, *tt.want)
			}
		})
	}
}

func TestBookAuthorSortMap(t *testing.T) {
	for _, tt := range []struct {
		name, meta string
		want       map[string]string
	}{
		{"captured", `{"3": {"author_sort_map": {"Ada Quill": "Quill, Ada"}, "format_metadata": {}}}`, map[string]string{"Ada Quill": "Quill, Ada"}},
		{"absent", `{"3": {"format_metadata": {}}}`, nil},
		{"null", `{"3": {"author_sort_map": null, "format_metadata": {}}}`, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := fakeCalibre(t, map[string]http.HandlerFunc{
				"POST /cdb/cmd/list/0":    listFor(nil),
				"GET /ajax/books/library": jsonAnswer(tt.meta),
			})
			b, err := c.Book(context.Background(), 3)
			if err != nil {
				t.Fatal(err)
			}
			if (tt.want == nil) != (b.AuthorSortMap == nil) || !maps.Equal(b.AuthorSortMap, tt.want) {
				t.Fatalf("AuthorSortMap = %v, want %v", b.AuthorSortMap, tt.want)
			}
		})
	}
}
