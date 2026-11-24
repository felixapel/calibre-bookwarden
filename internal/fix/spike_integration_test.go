//go:build calibre

package fix

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/icholy/digest"

	"github.com/felixapel/calibre-bookwarden/internal/library"
)

// spike talks to the content server without the library client, because the
// client has no series or cover fields: it reads the raw listing and the raw
// set-fields answer.
type spike struct {
	t    *testing.T
	base *url.URL
	http *http.Client
	lib  string
	cl   *library.Client
}

func newSpike(t *testing.T) *spike {
	t.Helper()
	w := itClient(t, "writer", "BW_IT_WRITER_PASSWORD")
	lib, err := w.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	base, err := url.Parse(os.Getenv("BW_IT_CALIBRE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	return &spike{t: t, base: base, lib: lib, cl: w,
		http: &http.Client{Transport: &digest.Transport{Username: "writer", Password: os.Getenv("BW_IT_WRITER_PASSWORD")}}}
}

// on returns the spike bound to a subtest, so a failure there fails only
// that subtest.
func (s *spike) on(t *testing.T) *spike {
	c := *s
	c.t = t
	return &c
}

func (s *spike) post(u *url.URL, body string) []byte {
	s.t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, u.String(), strings.NewReader(body))
	if err != nil {
		s.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := s.http.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		s.t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		s.t.Fatalf("%s: HTTP %d: %s", u.Path, resp.StatusCode, data)
	}
	return data
}

// set sends a raw set-fields body (the changes object, as JSON text) and
// returns the raw answer for the book.
func (s *spike) set(id int, changes string) map[string]json.RawMessage {
	s.t.Helper()
	u := s.base.JoinPath("cdb", "set-fields", strconv.Itoa(id), s.lib)
	data := s.post(u, fmt.Sprintf(`{"changes":%s,"loaded_book_ids":[%d]}`, changes, id))
	var ans map[string]map[string]json.RawMessage
	if err := json.Unmarshal(data, &ans); err != nil {
		s.t.Fatalf("set-fields answer %s: %v", data, err)
	}
	return ans[strconv.Itoa(id)]
}

// listed is the listing's value of the named fields for one book, as raw JSON.
func (s *spike) listed(id int, fields ...string) map[string]string {
	s.t.Helper()
	u := s.base.JoinPath("cdb", "cmd", "list", "0")
	u.RawQuery = url.Values{"library_id": {s.lib}}.Encode()
	args, _ := json.Marshal([]any{fields, "id", true, "id:" + strconv.Itoa(id), -1})
	var env struct {
		Result struct {
			Data map[string]map[string]json.RawMessage `json:"data"`
		} `json:"result"`
	}
	data := s.post(u, string(args))
	if err := json.Unmarshal(data, &env); err != nil {
		s.t.Fatalf("list answer %s: %v", data, err)
	}
	out := map[string]string{}
	for _, f := range fields {
		out[f] = string(env.Result.Data[f][strconv.Itoa(id)])
	}
	return out
}

// cover returns the stored cover bytes, nil when the book has none.
func (s *spike) cover(id int) []byte {
	s.t.Helper()
	var buf bytes.Buffer
	res, err := s.cl.ExportCoverInLibrary(context.Background(), s.lib, id, &buf, library.CoverCap)
	if err != nil {
		s.t.Fatal(err)
	}
	if res.Status == library.ExportNotInCalibre {
		return nil
	}
	return buf.Bytes()
}

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func synthJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{uint8(x * 255 / w), uint8(y * 255 / h), 128, 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func synthPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{128, uint8(x * 255 / w), uint8(y * 255 / h), 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func dataURL(mime string, b []byte) string {
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(b)
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }
