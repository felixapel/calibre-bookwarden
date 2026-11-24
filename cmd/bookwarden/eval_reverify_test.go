package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/agent"
	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/eval"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/extraction"
	"github.com/felixapel/calibre-bookwarden/internal/extractor"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/store"
	"github.com/felixapel/calibre-bookwarden/internal/testutil/pgtest"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

type answerSeat struct{ text string }

func (s answerSeat) Name() string  { return "fake" }
func (s answerSeat) Model() string { return "m" }
func (s answerSeat) Judge(context.Context, string, string) (agent.Reply, error) {
	return agent.Reply{Text: s.text, Model: "m"}, nil
}

// TestEvalReverifyCommand runs --extraction --reverify, as text and JSON, on a
// run with one sealed extraction.
func TestEvalReverifyCommand(t *testing.T) {
	ctx := context.Background()
	url := pgtest.URL(t)
	st, err := store.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	runs := store.NewRunRepo(st)
	b := library.BookRef{ID: 1, UUID: "u1"}
	r, err := runs.CreateRun(ctx, verify.Scope{}, "library", "v1", []library.BookRef{b})
	if err != nil {
		t.Fatal(err)
	}
	const title = "THE QWXMARKERSAGA"
	answer := `{"claims":[{"field":"title","value":"` + title + `","quote":"` + title + `"}]}`
	eb := evidence.NewExcerptBuilder(evidence.ExcerptEPUBText)
	eb.Add(1, evidence.LandmarkTitle, title)
	ex := eb.Excerpt()
	ex.Complete = true
	body := ex.Text
	es := []extractor.Extractor{extractor.NewWithSeat("glm", "glm", "m", "", answerSeat{answer}), extractor.NewWithSeat("luna", "luna", "m", "", answerSeat{answer})}
	svc := extraction.New(extraction.Config{Extractors: es, Hosts: map[string]string{"glm": "h", "luna": "h"},
		MaxCalls: map[string]int{"glm": 10, "luna": 10}, Quotas: agent.NewQuotas(), Timeout: time.Second})
	svc.BeginRun(r.ID)
	x, raws, err := svc.Extract(ctx, *ex, body)
	if err != nil {
		t.Fatal(err)
	}
	title2 := title
	p := evidence.Package{
		SchemaVersion: evidence.SchemaVersion, LibraryID: "library", BookID: b.ID, UUID: b.UUID,
		Calibre: evidence.Record{Title: "Zyxwv Fixture Saga", Authors: []string{"Ada Reed"}, Languages: []string{"eng"}, Identifiers: map[string]string{},
			TitleSort: &title2, AuthorSort: "Reed, Ada", AuthorSortMap: map[string]string{"Ada Reed": "Reed, Ada"}},
	}
	ex.Text = ""
	p.Formats = []evidence.FormatEvidence{{Format: "EPUB", State: evidence.StateOK, Detected: "epub", Excerpt: ex, Extraction: x}}
	raw, seal, err := decide.Seal(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	out := verify.Outcome{State: verify.BookChecked, Evidence: raw, Seal: seal, Excerpts: map[string]string{ex.SHA256: body}, Raws: raws}
	if err := runs.FinishBook(ctx, r.ID, b, out); err != nil {
		t.Fatal(err)
	}
	if err := runs.SetStatus(ctx, r.ID, verify.RunCompleted, ""); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"BOOKWARDEN_DATABASE_URL": url}
	call := func(args ...string) (int, string, string) {
		var stdout, stderr bytes.Buffer
		code := run(ctx, append([]string{"eval"}, args...), func(k string) string { return env[k] }, strings.NewReader(""), &stdout, &stderr)
		return code, stdout.String(), stderr.String()
	}
	id := fmt.Sprint(r.ID)

	code, text, errOut := call("--run", id, "--extraction", "--reverify")
	if code != 0 || errOut != "" || !strings.Contains(text, "re-verification of 1 formats") || !strings.Contains(text, "formats whose agreed set changed: 0") {
		t.Fatalf("text: code %d, stdout %q, stderr %q", code, text, errOut)
	}
	code, js, errOut := call("--run", id, "--extraction", "--reverify", "--json")
	var rep eval.ReverifyReport
	if err := json.Unmarshal([]byte(js), &rep); err != nil || code != 0 || errOut != "" {
		t.Fatalf("json: code %d, err %v, stderr %q, stdout %q", code, err, errOut, js)
	}
	if rep.Formats != 1 || rep.Skipped != 0 || rep.AgreedChanged != 0 || rep.Sources["glm"] == nil || rep.Sources["glm"].Derived["grounded"] != 1 {
		t.Fatalf("report = %+v", rep)
	}
	for _, o := range []string{text, js} {
		for _, leak := range []string{"QWXMARKER", "Zyxwv", "Ada Reed"} {
			if strings.Contains(o, leak) {
				t.Fatalf("output leaks %q: %s", leak, o)
			}
		}
	}
}
