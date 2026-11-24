//go:build calibre

package fix

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

// lastModified is the listing's last_modified for a book, with the
// microseconds Calibre keeps after a write.
func (s *spike) lastModified(id int) time.Time {
	s.t.Helper()
	var typed struct {
		V string `json:"v"`
	}
	if err := json.Unmarshal([]byte(s.listed(id, "last_modified")["last_modified"]), &typed); err != nil {
		s.t.Fatal(err)
	}
	ts, err := time.Parse(time.RFC3339Nano, typed.V)
	if err != nil {
		s.t.Fatal(err)
	}
	return ts
}

// setStatus sends a set-fields body and returns the HTTP status and body
// without failing on a refusal.
func (s *spike) setStatus(id int, changes string) (int, string) {
	s.t.Helper()
	u := s.base.JoinPath("cdb", "set-fields", fmt.Sprint(id), s.lib)
	body := fmt.Sprintf(`{"changes":%s,"loaded_book_ids":[%d]}`, changes, id)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, u.String(), strings.NewReader(body))
	if err != nil {
		s.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.http.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		s.t.Fatal(err)
	}
	return resp.StatusCode, strings.TrimSpace(string(data))
}

func (s *spike) series(id int) string { return s.listed(id, "series")["series"] }
func (s *spike) index(id int) string  { return s.listed(id, "series_index")["series_index"] }

func coverChange(mime string, b []byte) string {
	return `{"cover":` + string(mustJSON(dataURL(mime, b))) + `}`
}

func dims(b []byte) (int, int) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return 0, 0
	}
	return cfg.Width, cfg.Height
}

// TestSeriesAndCoverWrites pins how Calibre 9.16's content server takes the
// series, series_index and cover fields (spike S1, docs/calibre-protocol.md).
// It uses fixture books 4 and 5, which have no series and no cover, and puts
// both back as it found them.
func TestSeriesAndCoverWrites(t *testing.T) {
	s := newSpike(t)
	const a, b = 4, 5
	want := func(t *testing.T, label, got, wantText string) {
		t.Helper()
		if got != wantText {
			t.Errorf("%s = %s, want %s", label, got, wantText)
		}
	}
	for _, id := range []int{a, b} {
		if s.series(id) != "null" || s.index(id) != "1.0" {
			t.Fatalf("fixture book %d starts with series %s, index %s", id, s.series(id), s.index(id))
		}
	}
	t.Cleanup(func() {
		s.set(a, `{"series":"","series_index":1.0}`)
		s.set(b, `{"series":"","series_index":1.0,"cover":null}`)
	})
	// Book B is the one cover tests change; book A may already hold a cover
	// from TestCoverOnRealCalibre.
	if s.cover(b) != nil {
		t.Fatal("fixture book 5 starts with a cover")
	}

	t.Run("series is stored as sent and keeps the index at 1.0", func(t *testing.T) {
		s := s.on(t)
		ans := s.set(a, `{"series":"Spike Series"}`)
		want(t, "answer series", string(ans["series"]), `"Spike Series"`)
		want(t, "answer index", string(ans["series_index"]), "1.0")
		want(t, "listed series", s.series(a), `"Spike Series"`)
		want(t, "listed index", s.index(a), "1.0")
	})

	t.Run("a [n] suffix is parsed into the index and stripped from the name", func(t *testing.T) {
		s := s.on(t)
		s.set(a, `{"series":"Spike Series [3]"}`)
		want(t, "series", s.series(a), `"Spike Series"`)
		want(t, "index", s.index(a), "3.0")
		s.set(a, `{"series":"Spike Series [2.5]"}`)
		want(t, "series with [2.5]", s.series(a), `"Spike Series"`)
		want(t, "index with [2.5]", s.index(a), "2.5")
		// The later entry of a body wins, as with every field.
		s.set(a, `{"series":"Spike Series [4]","series_index":6}`)
		want(t, "index, suffix before series_index", s.index(a), "6.0")
		s.set(a, `{"series_index":6,"series":"Spike Series [4]"}`)
		want(t, "index, series_index before suffix", s.index(a), "4.0")
	})

	t.Run("series_index is a JSON float", func(t *testing.T) {
		s := s.on(t)
		for _, tc := range []struct{ sent, stored string }{
			{`2.5`, "2.5"}, {`2`, "2.0"}, {`"3.25"`, "3.25"}, {`0`, "0.0"}, {`12.125`, "12.125"}, {`null`, "1.0"},
		} {
			ans := s.set(a, `{"series_index":`+tc.sent+`}`)
			want(t, "answer index for "+tc.sent, string(ans["series_index"]), tc.stored)
			want(t, "listed index for "+tc.sent, s.index(a), tc.stored)
		}
		s.set(a, `{"series_index":2.5}`)
		lm := s.lastModified(a)
		time.Sleep(5 * time.Millisecond)
		if code, body := s.setStatus(a, `{"series_index":"abc"}`); code != http.StatusInternalServerError || body != "" {
			t.Errorf("series_index \"abc\": HTTP %d %q, want 500 with an empty body", code, body)
		}
		want(t, "index after a refused write", s.index(a), "2.5")
		if got := s.lastModified(a); !got.Equal(lm) {
			t.Errorf("a refused index moved last_modified %v -> %v", lm, got)
		}
		s.set(a, `{"series_index":null}`)
	})

	t.Run("an index can be written to a book without a series", func(t *testing.T) {
		s := s.on(t)
		s.set(a, `{"series":""}`)
		ans := s.set(a, `{"series_index":7}`)
		want(t, "answer index", string(ans["series_index"]), "7.0")
		if v, ok := ans["series"]; ok {
			t.Errorf("answer has series = %s", v)
		}
		want(t, "listed index", s.index(a), "7.0")
		want(t, "listed series", s.series(a), "null")
		s.set(a, `{"series_index":null}`)
	})

	t.Run("clearing the series keeps the index", func(t *testing.T) {
		s := s.on(t)
		s.set(a, `{"series":"Spike Series","series_index":4.5}`)
		for _, clear := range []string{`""`, `null`} {
			s.set(a, `{"series":"Spike Series","series_index":4.5}`)
			ans := s.set(a, `{"series":`+clear+`}`)
			if v, ok := ans["series"]; ok {
				t.Errorf("answer after clearing with %s still has series = %s; Calibre omits an empty value", clear, v)
			}
			want(t, "answer index after "+clear, string(ans["series_index"]), "4.5")
			want(t, "listed series after "+clear, s.series(a), "null")
			want(t, "listed index after "+clear, s.index(a), "4.5")
		}
		s.set(a, `{"series_index":null}`)
		want(t, "index reset", s.index(a), "1.0")
	})

	t.Run("one spelling is shared by every book, and the last write wins", func(t *testing.T) {
		s := s.on(t)
		names := func() []string {
			ns, err := s.cl.FieldNames(context.Background(), s.lib, "series")
			if err != nil {
				t.Fatal(err)
			}
			return ns
		}
		if ns := names(); len(ns) != 0 {
			t.Fatalf("series names before = %q", ns)
		}
		s.set(a, `{"series":"The Spike Saga","series_index":7}`)
		s.set(b, `{"series":"the spike saga"}`)
		want(t, "A's series after B's case variant", s.series(a), `"the spike saga"`)
		want(t, "B's series", s.series(b), `"the spike saga"`)
		want(t, "A's index", s.index(a), "7.0")
		want(t, "B's index", s.index(b), "1.0")
		if ns := names(); !slices.Equal(ns, []string{"the spike saga"}) {
			t.Errorf("series names = %q, want one item with the last spelling", ns)
		}
		s.set(a, `{"series":"THE SPIKE SAGA"}`)
		want(t, "B's series after A rewrites the spelling", s.series(b), `"THE SPIKE SAGA"`)
		if ns := names(); !slices.Equal(ns, []string{"THE SPIKE SAGA"}) {
			t.Errorf("series names = %q", ns)
		}
		// Undo must put the old spelling back through a write to each book.
		s.set(a, `{"series":"","series_index":1.0}`)
		want(t, "B's series after A leaves", s.series(b), `"THE SPIKE SAGA"`)
		s.set(b, `{"series":""}`)
		if ns := names(); len(ns) != 0 {
			t.Errorf("series names after clearing both = %q", ns)
		}
	})

	t.Run("undo restores the earlier series and index exactly", func(t *testing.T) {
		s := s.on(t)
		s.set(a, `{"series":"Old Series","series_index":6}`)
		prior := []string{s.series(a), s.index(a)}
		s.set(a, `{"series":"New Series","series_index":2.5}`)
		s.set(a, `{"series":"Old Series","series_index":6.0}`)
		if got := []string{s.series(a), s.index(a)}; !slices.Equal(got, prior) {
			t.Errorf("after undo (series, index) = %v, want %v", got, prior)
		}
		// From no series: clearing needs the index written back too.
		s.set(a, `{"series":"","series_index":1.0}`)
		s.set(a, `{"series":"Fresh","series_index":9}`)
		s.set(a, `{"series":"","series_index":1.0}`)
		want(t, "series after undoing a first series", s.series(a), "null")
		want(t, "index after undoing a first series", s.index(a), "1.0")
		s.set(a, `{"series":"Fresh","series_index":9}`)
		s.set(a, `{"series":""}`)
		want(t, "index after undo that forgot the index", s.index(a), "9.0")
		s.set(a, `{"series_index":1.0}`)
	})

	t.Run("covers: a JPEG within the size limits is stored byte for byte", func(t *testing.T) {
		s := s.on(t)
		in := synthJPEG(t, 600, 900)
		s.set(b, coverChange("image/jpeg", in))
		out := s.cover(b)
		t.Logf("JPEG 600x900: in %d bytes sha256 %s, out %d bytes sha256 %s", len(in), sum(in), len(out), sum(out))
		if !bytes.Equal(in, out) {
			t.Errorf("a 600x900 JPEG came back different: %d bytes, %s", len(out), sum(out))
		}
		if got := s.listed(b, "cover")["cover"]; got != "1" {
			t.Errorf("listed cover = %s", got)
		}
		in = synthJPEG(t, 64, 96)
		s.set(b, coverChange("image/jpeg", in))
		if out := s.cover(b); !bytes.Equal(in, out) {
			t.Errorf("a 64x96 JPEG came back different: %d bytes", len(out))
		}
	})

	t.Run("covers: a PNG is re-encoded as JPEG", func(t *testing.T) {
		s := s.on(t)
		in := synthPNG(t, 600, 900)
		s.set(b, coverChange("image/png", in))
		out := s.cover(b)
		t.Logf("PNG 600x900: in %d bytes sha256 %s, out %d bytes sha256 %s", len(in), sum(in), len(out), sum(out))
		if !bytes.HasPrefix(out, []byte("\xff\xd8\xff")) {
			t.Fatalf("a PNG came back as %q...", out[:min(8, len(out))])
		}
		if w, h := dims(out); w != 600 || h != 900 {
			t.Errorf("PNG came back %dx%d", w, h)
		}
		// The declared type is not what decides: the bytes are.
		s.set(b, coverChange("image/jpeg", in))
		if out2 := s.cover(b); !bytes.Equal(out, out2) {
			t.Errorf("a PNG labelled image/jpeg gave other bytes")
		}
	})

	t.Run("covers: a large image is scaled down to fit 1650x2200", func(t *testing.T) {
		s := s.on(t)
		for _, tc := range []struct {
			w, h, wantW, wantH int
			png                bool
		}{
			{1200, 1800, 1200, 1800, false}, // within the limit: untouched
			{1600, 2400, 1466, 2200, false}, // too tall
			{3600, 2400, 1650, 1100, false}, // too wide
			{2400, 3600, 1466, 2200, false}, // too big both ways
			{1000, 1500, 1000, 1500, false}, // within the limit
			{2400, 3600, 1466, 2200, true},  // PNG
		} {
			var in []byte
			mime := "image/jpeg"
			if tc.png {
				in, mime = synthPNG(t, tc.w, tc.h), "image/png"
			} else {
				in = synthJPEG(t, tc.w, tc.h)
			}
			s.set(b, coverChange(mime, in))
			out := s.cover(b)
			w, h := dims(out)
			t.Logf("%s %dx%d: in %d bytes sha256 %s, out %dx%d %d bytes sha256 %s", mime, tc.w, tc.h, len(in), sum(in), w, h, len(out), sum(out))
			if w != tc.wantW || h != tc.wantH {
				t.Errorf("%s %dx%d came back %dx%d, want %dx%d", mime, tc.w, tc.h, w, h, tc.wantW, tc.wantH)
			}
			if same := bytes.Equal(in, out); same != (!tc.png && tc.w == tc.wantW) {
				t.Errorf("%s %dx%d byte-identical = %v", mime, tc.w, tc.h, same)
			}
		}
	})

	t.Run("covers: only JPEG and PNG bytes are accepted, and a refusal changes nothing", func(t *testing.T) {
		s := s.on(t)
		s.set(b, coverChange("image/jpeg", synthJPEG(t, 64, 96)))
		keep := sum(s.cover(b))
		lm := s.lastModified(b)
		for _, tc := range []struct {
			sent string
			want string
		}{
			{`""`, "Cover data must be either JPEG or PNG"},
			{`"nonsense"`, "Cover data must be either JPEG or PNG"},
			{`"data:image/jpeg;base64,AAAA"`, "Cover data must be either JPEG or PNG"},
			{`"data:image/gif;base64,R0lGODlhAQABAAAAACw="`, "Cover data must be either JPEG or PNG"},
			{`123`, "Cover data is not valid base64 encoded data"},
		} {
			code, body := s.setStatus(b, `{"cover":`+tc.sent+`}`)
			if code != http.StatusBadRequest || !strings.Contains(body, tc.want) {
				t.Errorf("cover %s: HTTP %d %q, want 400 %q", tc.sent, code, body, tc.want)
			}
		}
		if got := sum(s.cover(b)); got != keep {
			t.Errorf("a refused cover changed the stored one")
		}
		if got := s.lastModified(b); !got.Equal(lm) {
			t.Errorf("a refused cover moved last_modified %v -> %v", lm, got)
		}
	})

	t.Run("covers: null removes the cover, and undo restores the previous bytes", func(t *testing.T) {
		s := s.on(t)
		s.set(b, `{"cover":null}`)
		if s.cover(b) != nil || s.listed(b, "cover")["cover"] != "0" {
			t.Fatalf("book %d still has a cover after null", b)
		}
		in := synthJPEG(t, 600, 900)
		s.set(b, coverChange("image/jpeg", in))
		s.set(b, coverChange("image/png", synthPNG(t, 300, 450)))
		// Undo: write the bytes read before the change.
		s.set(b, coverChange("image/jpeg", in))
		if out := s.cover(b); !bytes.Equal(in, out) {
			t.Errorf("cover after undo differs: %d bytes", len(out))
		}
		// Undo to "no cover": null.
		s.set(b, `{"cover":null}`)
		if s.cover(b) != nil || s.listed(b, "cover")["cover"] != "0" {
			t.Errorf("cover after undoing to none is %d bytes", len(s.cover(b)))
		}
		// A real library's cover reads back byte for byte after a rewrite.
		orig := s.cover(1)
		if orig == nil {
			t.Fatal("fixture book 1 has no cover")
		}
		t.Cleanup(func() { s.set(1, coverChange("image/jpeg", orig)) })
		s.set(1, coverChange("image/jpeg", synthJPEG(t, 300, 450)))
		s.set(1, coverChange("image/jpeg", orig))
		if out := s.cover(1); !bytes.Equal(orig, out) {
			t.Errorf("book 1's cover differs after restoring its own bytes: %d bytes, was %d", len(out), len(orig))
		}
	})

	t.Run("last_modified keeps microseconds once a book is written", func(t *testing.T) {
		s := s.on(t)
		if ns := s.lastModified(7).Nanosecond(); ns != 0 {
			t.Errorf("never-written book 7 has %d ns in last_modified", ns)
		}
		s.set(a, `{"series":"Micro"}`)
		if s.lastModified(a).Nanosecond() == 0 {
			t.Errorf("a written book has whole-second last_modified")
		}
		s.set(a, `{"series":""}`)
	})

	t.Run("a changed series or index and every cover write move last_modified, even the same cover again", func(t *testing.T) {
		s := s.on(t)
		moved := func(label string, id int, changes string) {
			t.Helper()
			before := s.lastModified(id)
			time.Sleep(5 * time.Millisecond)
			s.set(id, changes)
			if after := s.lastModified(id); !after.After(before) {
				t.Errorf("%s left last_modified at %v (was %v)", label, after, before)
			}
		}
		moved("a first series", a, `{"series":"Spike Series"}`)
		moved("a new index", a, `{"series_index":2.5}`)
		moved("a [n] suffix", a, `{"series":"Spike Series [3]"}`)
		moved("a case-only rename", a, `{"series":"SPIKE SERIES"}`)
		moved("clearing the series", a, `{"series":""}`)
		moved("a first cover", b, coverChange("image/jpeg", synthJPEG(t, 64, 96)))
		moved("the same cover again", b, coverChange("image/jpeg", synthJPEG(t, 64, 96)))
		moved("removing the cover", b, `{"cover":null}`)
		moved("removing a cover that isn't there", b, `{"cover":null}`)
	})

	t.Run("writing an unchanged series or index moves nothing", func(t *testing.T) {
		s := s.on(t)
		s.set(a, `{"series":"Still Series","series_index":3.5}`)
		before := s.lastModified(a)
		time.Sleep(5 * time.Millisecond)
		s.set(a, `{"series":"Still Series","series_index":3.5}`)
		after := s.lastModified(a)
		t.Logf("last_modified after rewriting the same series and index: %v -> %v", before, after)
		if !after.Equal(before) {
			t.Errorf("an unchanged series and index moved last_modified")
		}
		s.set(a, `{"series":""}`)
		s.set(a, `{"series_index":1.0}`)
	})

	t.Run("a case-only series rename moves the other book's last_modified", func(t *testing.T) {
		s := s.on(t)
		s.set(a, `{"series":"Pair Series"}`)
		s.set(b, `{"series":"Pair Series"}`)
		before := s.lastModified(b)
		time.Sleep(5 * time.Millisecond)
		s.set(a, `{"series":"PAIR SERIES"}`)
		after := s.lastModified(b)
		t.Logf("B's last_modified after A renamed the shared series: %v -> %v", before, after)
		if !after.After(before) {
			t.Errorf("B's last_modified did not move although its series was renamed")
		}
		s.set(a, `{"series":""}`)
		s.set(b, `{"series":""}`)
	})
}
