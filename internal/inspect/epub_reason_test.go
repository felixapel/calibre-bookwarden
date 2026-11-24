package inspect_test

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/inspect"
	"github.com/felixapel/calibre-bookwarden/internal/inspect/inspecttest"
)

// bigComment is a comment of n bytes: markup with no text.
func bigComment(n int) string { return "<!--" + strings.Repeat("x", n) + "-->" }

// The failure reason is a diagnostic. It must never reach the sealed
// evidence: the hints' canonical JSON is pinned to what origin/main sealed,
// and holds no reason.
func TestEPUBFailReasons(t *testing.T) {
	broken := `<html><body><p>ISBN 978-0-00-000004-0</p><p class="x`
	brokenNav := `<html xmlns:epub="http://www.idpf.org/2007/ops"><body><nav epub:type="landmarks"><ol><li><a epub:type="bodymatter" href="x`
	tests := []struct {
		name       string
		opts       inspecttest.EPUBOptions
		limits     inspect.Limits
		budget     int // the scan's total text bound, 0 for the default
		state      evidence.State
		hint       string // the hints' state, "" for none
		reason     string
		goldenJSON string // the sealed hints
	}{
		{
			name:   "the scan's text budget runs out inside a document",
			budget: 20 << 10,
			opts:   book(page(`<p>ISBN 978-0-00-000004-0</p><p>`+strings.Repeat("lorem ipsum ", 30_000)+`</p>`), filler(2, nil), ""),
			state:  evidence.StateOK, hint: evidence.HintFailed, reason: inspect.FailTextBudget,
			goldenJSON: `{"context":[{"isbn":"9780000000040","landmark":"front"}],"isbns":["9780000000040"],"method":"epub_text","pages":[],"state":"failed"}`,
		},
		{
			name:   "the budget is spent before the next document",
			budget: 20 << 10,
			opts:   book(page(`<p>`+strings.Repeat("a", 262144)+`</p>`), filler(2, nil), ""),
			state:  evidence.StateOK, hint: evidence.HintFailed, reason: inspect.FailTextBudget,
			goldenJSON: `{"method":"epub_text","pages":[],"state":"failed"}`,
		},
		{
			name:   "a document is cut at the per-document read limit",
			opts:   book(page(bigComment(5<<20)+`<p>ISBN 978-0-00-000004-0</p>`), filler(2, nil), ""),
			limits: hugeLimits,
			state:  evidence.StateOK, hint: evidence.HintFailed, reason: inspect.FailDocReadLimit,
			goldenJSON: `{"method":"epub_text","pages":[2,3],"state":"failed"}`,
		},
		{
			name:  "a document that doesn't parse",
			opts:  book(page(`<p>Title</p>`), []string{broken, page(`<p>Next ISBN 978-0-00-000006-4</p>`)}, ""),
			state: evidence.StateOK, hint: evidence.HintFailed, reason: inspect.FailXMLError,
			goldenJSON: `{"context":[{"isbn":"9780000000040","landmark":"front"},{"isbn":"9780000000064","landmark":"front"}],"isbns":["9780000000040","9780000000064"],"method":"epub_text","pages":[1,3],"state":"failed"}`,
		},
		{
			name:  "a navigation document that doesn't parse",
			opts:  book(page(`<p>x</p>`), filler(2, nil), brokenNav),
			state: evidence.StateOK, hint: evidence.HintFailed, reason: inspect.FailNavUnreadable,
			goldenJSON: `{"method":"epub_text","pages":[1,2,3],"state":"failed"}`,
		},
		{
			name:  "a listed navigation document that isn't there",
			opts:  with(book(page(`<p>x</p>`), filler(2, nil), ""), func(o *inspecttest.EPUBOptions) { o.MissingNav = true }),
			state: evidence.StateOK, hint: evidence.HintFailed, reason: inspect.FailNavUnreadable,
			goldenJSON: `{"method":"epub_text","pages":[1,2,3],"state":"failed"}`,
		},
		{
			name:   "a navigation document past the XML size bound",
			opts:   book(page(`<p>x</p>`), filler(2, nil), bigComment(5<<20)),
			limits: hugeLimits,
			state:  evidence.StateOK, hint: evidence.HintFailed, reason: inspect.FailXMLLimit,
			goldenJSON: `{"method":"epub_text","pages":[1,2,3],"state":"failed"}`,
		},
		{
			name:   "too many zip entries",
			opts:   book(page(`<p>x</p>`), nil, ""),
			limits: inspect.Limits{MaxEntries: 3},
			state:  evidence.StateNotInspected, reason: inspect.FailZipLimit,
		},
		{
			name:  "adept rights",
			opts:  with(book(page(`<p>x</p>`), nil, ""), func(o *inspecttest.EPUBOptions) { o.Rights = true }),
			state: evidence.StateDRM, reason: inspect.FailDRM,
		},
		{
			name:  "a missing OPF",
			opts:  with(book(page(`<p>x</p>`), nil, ""), func(o *inspecttest.EPUBOptions) { o.NoOPF = true }),
			state: evidence.StateCorrupt, reason: inspect.FailOther,
		},
		{
			name:  "a clean scan has no reason",
			opts:  book(page(`<p>x</p>`), nil, ""),
			state: evidence.StateOK, hint: evidence.HintOK, reason: "",
			goldenJSON: `{"method":"epub_text","pages":[1],"state":"ok"}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.budget > 0 {
				inspect.SetScanBudget(t, tc.budget)
			}
			data := inspecttest.EPUB(tc.opts)
			res := inspect.Inspect(context.Background(), "EPUB", bytes.NewReader(data), int64(len(data)), tc.limits)
			if res.State != tc.state || res.FailReason != tc.reason {
				t.Errorf("state %s/%s reason %q; want %s reason %q", res.State, res.Reason, res.FailReason, tc.state, tc.reason)
			}
			// Everything the format's evidence is built from, pinned to what
			// origin/main produced: state, reason, kind, text, metadata, hints
			// and the excerpt (its text only through its SHA-256).
			if got := sealedResult(t, res); got != resultGoldens[tc.name] {
				t.Errorf("sealed result changed:\n got %s\nwant %s", got, resultGoldens[tc.name])
			}
			if tc.hint == "" {
				if res.ISBNHints != nil {
					t.Fatalf("hints %+v, want none", res.ISBNHints)
				}
				return
			}
			if res.ISBNHints == nil || res.ISBNHints.State != tc.hint {
				t.Fatalf("hints %+v, want state %s", res.ISBNHints, tc.hint)
			}
			raw, err := evidence.Canonical(res.ISBNHints)
			if err != nil {
				t.Fatal(err)
			}
			if string(raw) != tc.goldenJSON {
				t.Errorf("sealed hints changed:\n got %s\nwant %s", raw, tc.goldenJSON)
			}
			if res.FailReason != "" && bytes.Contains(raw, []byte(res.FailReason)) {
				t.Errorf("the reason reached the sealed hints: %s", raw)
			}
		})
	}
}

func TestEPUBScanDocsListWhatWasRead(t *testing.T) {
	nav := landmarkNav(landmarkLink("copyright-page", "text/extra0.xhtml"), landmarkLink("bodymatter", "text/extra1.xhtml"))
	broken := `<html><body><p>cut off <p class="x`
	res := inspectEPUBOptions(book(page(`<p>Half title</p>`), []string{page(`<p>Imprint</p>`), page(`<p>One</p>`), broken}, nav))
	want := []inspect.ScanDoc{
		{Pos: 1, Landmark: evidence.LandmarkFront, Whole: true},
		{Pos: 2, Landmark: evidence.LandmarkCopyright, Whole: true},
		{Pos: 3, Landmark: evidence.LandmarkBack, Whole: true},
		{Pos: 4, Landmark: evidence.LandmarkBack, Whole: false},
	}
	if !reflect.DeepEqual(res.ScanDocs, want) {
		t.Fatalf("scan docs %+v, want %+v", res.ScanDocs, want)
	}
}

func TestEPUBFailReasonDeadline(t *testing.T) {
	o := book(page(`<p>Title</p>`), filler(3, nil), "")
	data := inspecttest.EPUB(o)
	// endScanAfterFirstDocument ends the scan's own context, not the file's,
	// once the first document is read: the scan times out and the file's
	// result stays, as when the margin before a real deadline is reached.
	// No clock is involved, so a loaded runner can't change the outcome.
	endScanAfterFirstDocument := func(t *testing.T) context.Context {
		var stop context.CancelFunc
		inspect.SetScanContext(t, func(parent context.Context, _ time.Time) (context.Context, context.CancelFunc) {
			var ctx context.Context
			ctx, stop = context.WithCancel(parent)
			return ctx, stop
		})
		inspect.SetScanTextHook(t, func() { stop() })
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		t.Cleanup(cancel)
		return ctx
	}
	t.Run("the scan runs out of time midway", func(t *testing.T) {
		ctx := endScanAfterFirstDocument(t)
		res := inspect.Inspect(ctx, "EPUB", bytes.NewReader(data), int64(len(data)), inspect.Limits{})
		if h := res.ISBNHints; h == nil || h.State != evidence.HintTimeout || !reflect.DeepEqual(h.Pages, []int{1}) || res.FailReason != inspect.FailDeadline {
			t.Fatalf("hints %+v reason %q, want timeout/deadline after the first document", h, res.FailReason)
		}
	})
	t.Run("a deadline overrides an earlier reason", func(t *testing.T) {
		// The first document doesn't parse (xml_error); the deadline then
		// ends the scan, and is what the reason says.
		ctx := endScanAfterFirstDocument(t)
		broken := inspecttest.EPUB(book(`<html><body><p>cut <p class="x`, filler(3, nil), ""))
		res := inspect.Inspect(ctx, "EPUB", bytes.NewReader(broken), int64(len(broken)), inspect.Limits{})
		if h := res.ISBNHints; h == nil || h.State != evidence.HintTimeout || res.FailReason != inspect.FailDeadline {
			t.Fatalf("hints %+v reason %q, want timeout/deadline", h, res.FailReason)
		}
	})
	t.Run("less than the margin left", func(t *testing.T) {
		inspect.SetScanMargin(t, time.Hour)
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		res := inspect.Inspect(ctx, "EPUB", bytes.NewReader(data), int64(len(data)), inspect.Limits{})
		if h := res.ISBNHints; h == nil || h.State != evidence.HintTimeout || res.FailReason != inspect.FailDeadline {
			t.Fatalf("hints %+v reason %q, want timeout/deadline", h, res.FailReason)
		}
	})
	t.Run("the context has ended before the file is read", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		res := inspect.Inspect(ctx, "EPUB", bytes.NewReader(data), int64(len(data)), inspect.Limits{})
		if res.State != evidence.StateNotInspected || res.Reason != evidence.ReasonTimeout || res.FailReason != inspect.FailDeadline {
			t.Fatalf("got %s/%s reason %q", res.State, res.Reason, res.FailReason)
		}
	})
}
