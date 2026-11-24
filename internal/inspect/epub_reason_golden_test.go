package inspect_test

import (
	"encoding/json"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/inspect"
)

// sealedResult is the JSON of everything of a Result that verify copies into
// a format's evidence. The diagnostics (FailReason, ScanDocs) and the
// excerpt's text are left out, as they are not sealed.
func sealedResult(t testing.TB, r inspect.Result) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"state": r.State, "reason": r.Reason, "detected": r.Detected, "text": r.Text,
		"metadata": r.Metadata, "pages": r.Pages, "hints": r.ISBNHints, "excerpt": r.Excerpt,
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// resultGoldens are keyed by the case names of TestEPUBFailReasons. They were
// captured from the scan as it was before the reason existed.
var resultGoldens = map[string]string{
	"the scan's text budget runs out inside a document": `{"detected":"epub","excerpt":{"method":"epub_text","units":[{"pos":1,"landmark":"front","start":0,"end":11999}],"sha256":"1b1fbed95d25bb6e9c02e570519e44c0990019f5bb6756086e08e7987198a5e2","chars":11999,"truncated":true,"complete":true},"hints":{"method":"epub_text","state":"failed","pages":[],"isbns":["9780000000040"],"context":[{"isbn":"9780000000040","landmark":"front"}]},"metadata":{"title":"Binary Tides","limits":{"version":1}},"pages":0,"reason":"","state":"ok","text":"present"}`,
	"the budget is spent before the next document":      `{"detected":"epub","excerpt":{"method":"epub_text","units":[{"pos":1,"landmark":"front","start":0,"end":12000}],"sha256":"34f8bc846ceca5054db0a22380147ceb23c90f4522daa21ea0b4c597bbd2f620","chars":12000,"truncated":true,"complete":true},"hints":{"method":"epub_text","state":"failed","pages":[]},"metadata":{"title":"Binary Tides","limits":{"version":1}},"pages":0,"reason":"","state":"ok","text":"present"}`,
	"a document is cut at the per-document read limit":  `{"detected":"epub","excerpt":{"method":"epub_text","units":[{"pos":2,"landmark":"front","start":0,"end":19},{"pos":3,"landmark":"front","start":21,"end":40}],"sha256":"25958bdd544b1b3f8d2dfa70a07315e6fb10880ef38523de8175735530c6dab0","chars":40,"truncated":true,"complete":true},"hints":{"method":"epub_text","state":"failed","pages":[2,3]},"metadata":{"title":"Binary Tides","limits":{"version":1}},"pages":0,"reason":"","state":"ok","text":"present"}`,
	"a document that doesn't parse":                     `{"detected":"epub","excerpt":{"method":"epub_text","units":[{"pos":1,"landmark":"front","start":0,"end":5},{"pos":3,"landmark":"front","start":7,"end":34}],"sha256":"60ea3c7af2896509989e47f5a3a49e9ef8a1d16cfd6924427f4964afaca483f2","chars":34,"truncated":false,"complete":false},"hints":{"method":"epub_text","state":"failed","pages":[1,3],"isbns":["9780000000040","9780000000064"],"context":[{"isbn":"9780000000040","landmark":"front"},{"isbn":"9780000000064","landmark":"front"}]},"metadata":{"title":"Binary Tides","limits":{"version":1}},"pages":0,"reason":"","state":"ok","text":"present"}`,
	"a navigation document that doesn't parse":          `{"detected":"epub","excerpt":{"method":"epub_text","units":[{"pos":1,"landmark":"front","start":0,"end":1},{"pos":2,"landmark":"front","start":3,"end":22},{"pos":3,"landmark":"front","start":24,"end":43}],"sha256":"c8cbc8e5d7d7db22a5c7f38e345e526ad9ee7a25d9e459fffb8c15ca0c0d0d90","chars":43,"truncated":false,"complete":true},"hints":{"method":"epub_text","state":"failed","pages":[1,2,3]},"metadata":{"title":"Binary Tides","limits":{"version":1}},"pages":0,"reason":"","state":"ok","text":"present"}`,
	"a listed navigation document that isn't there":     `{"detected":"epub","excerpt":{"method":"epub_text","units":[{"pos":1,"landmark":"front","start":0,"end":1},{"pos":2,"landmark":"front","start":3,"end":22},{"pos":3,"landmark":"front","start":24,"end":43}],"sha256":"c8cbc8e5d7d7db22a5c7f38e345e526ad9ee7a25d9e459fffb8c15ca0c0d0d90","chars":43,"truncated":false,"complete":true},"hints":{"method":"epub_text","state":"failed","pages":[1,2,3]},"metadata":{"title":"Binary Tides","limits":{"version":1}},"pages":0,"reason":"","state":"ok","text":"present"}`,
	"a navigation document past the XML size bound":     `{"detected":"epub","excerpt":{"method":"epub_text","units":[{"pos":1,"landmark":"front","start":0,"end":1},{"pos":2,"landmark":"front","start":3,"end":22},{"pos":3,"landmark":"front","start":24,"end":43}],"sha256":"c8cbc8e5d7d7db22a5c7f38e345e526ad9ee7a25d9e459fffb8c15ca0c0d0d90","chars":43,"truncated":false,"complete":true},"hints":{"method":"epub_text","state":"failed","pages":[1,2,3]},"metadata":{"title":"Binary Tides","limits":{"version":1}},"pages":0,"reason":"","state":"ok","text":"present"}`,
	"too many zip entries":                              `{"detected":"epub","excerpt":null,"hints":null,"metadata":null,"pages":0,"reason":"too_many_entries","state":"not_inspected","text":""}`,
	"adept rights":                                      `{"detected":"epub","excerpt":null,"hints":null,"metadata":{"title":"Binary Tides","limits":{"version":1}},"pages":0,"reason":"","state":"drm","text":"unverified"}`,
	"a missing OPF":                                     `{"detected":"epub","excerpt":null,"hints":null,"metadata":null,"pages":0,"reason":"","state":"corrupt","text":""}`,
	"a clean scan has no reason":                        `{"detected":"epub","excerpt":{"method":"epub_text","units":[{"pos":1,"landmark":"front","start":0,"end":1}],"sha256":"2d711642b726b04401627ca9fbac32f5c8530fb1903cc4db02258717921a4881","chars":1,"truncated":false,"complete":true},"hints":{"method":"epub_text","state":"ok","pages":[1]},"metadata":{"title":"Binary Tides","limits":{"version":1}},"pages":0,"reason":"","state":"ok","text":"present"}`,
}
