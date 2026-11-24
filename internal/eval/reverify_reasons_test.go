package eval_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/eval"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/extract"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/store"
	"github.com/felixapel/calibre-bookwarden/internal/testutil/pgtest"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

// seedRefusals finishes a run of one book where luna's answer has three
// values the quotes don't derive, each for another reason v4 doesn't fold,
// and glm's answer has none.
func seedRefusals(ctx context.Context, t *testing.T) (*store.Store, int64) {
	t.Helper()
	st, err := store.Open(ctx, pgtest.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	runs := store.NewRunRepo(st)
	b := library.BookRef{ID: 1, UUID: "u1"}
	r, err := runs.CreateRun(ctx, verify.Scope{}, "library", "v1", []library.BookRef{b})
	if err != nil {
		t.Fatal(err)
	}
	answers := map[string]string{
		"luna": `{"claims":[` +
			`{"field":"contributor","value":"Jane Doe-John Roe","role":"author","quote":"Jane Doe—John Roe"},` +
			`{"field":"contributor","value":"Mary Sample","role":"author","quote":"Mary Sam\u200bple"},` +
			`{"field":"contributor","value":"Nobody Here","role":"author","quote":"NIGHT FERRY"}]}`,
		"glm": `{"claims":[]}`,
	}
	eb := evidence.NewExcerptBuilder(evidence.ExcerptEPUBText)
	eb.Add(1, evidence.LandmarkTitle, "NIGHT FERRY\nJane Doe—John Roe\nMary Sam\u200bple")
	ex := eb.Excerpt()
	ex.Complete = true
	body := ex.Text
	x := &evidence.Extraction{PromptVersion: "extract-2", ExtractVersion: extract.Version, ExcerptSHA256: ex.SHA256}
	var raws []verify.RawOutput
	for _, name := range []string{"glm", "luna"} {
		sum := sha256.Sum256([]byte(answers[name]))
		sha := hex.EncodeToString(sum[:])
		x.Extractors = append(x.Extractors, evidence.ExtractorRun{Name: name, Family: name, Model: "m", State: evidence.ExtractOK, RawSHA256: sha})
		raws = append(raws, verify.RawOutput{SHA256: sha, Body: answers[name]})
	}
	p := pkg(b, false)
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
	return st, r.ID
}

func TestReverifyCountsRefusalReasons(t *testing.T) {
	ctx := context.Background()
	st, run := seedRefusals(ctx, t)
	res, err := eval.Reverify(ctx, store.NewReportRepo(st), run)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]map[string]eval.RefusalCount{
		// Since extract v4 (ADR-046) a title in another case or an imprint
		// with another dash verifies; a name is folded for case only.
		extract.RefusedNotInQuote: {"contributor": {Values: 1, Books: 1}},
		extract.RefusedDash:       {"contributor": {Values: 1, Books: 1}},
		extract.RefusedFormatChar: {"contributor": {Values: 1, Books: 1}},
	}
	got, _ := json.Marshal(res.Refusals)
	if w, _ := json.Marshal(want); string(got) != string(w) {
		t.Fatalf("refusals = %s, want %s", got, w)
	}
	raw, _ := json.Marshal(res)
	if !strings.Contains(string(raw), `"refusals":{"dash":{"contributor":{"values":1,"books":1}}`) {
		t.Errorf("JSON lacks the refusals: %s", raw)
	}
	var text strings.Builder
	res.WriteText(&text)
	for _, row := range []string{"format_char", "dash", "value_not_in_quote"} {
		if !strings.Contains(text.String(), row) {
			t.Errorf("no %s row in\n%s", row, text.String())
		}
	}
	for _, out := range []string{text.String(), string(raw)} {
		for _, leak := range []string{"Night", "NIGHT", "Jane", "Mary", "Nobody"} {
			if strings.Contains(out, leak) {
				t.Fatalf("output leaks %q: %s", leak, out)
			}
		}
	}
	t.Log("\n" + text.String())
}
