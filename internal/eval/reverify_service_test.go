package eval_test

import (
	"context"
	"encoding/json"
	"slices"
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

// textSeat answers every call with text.
type textSeat struct{ text string }

func (s textSeat) Name() string  { return "fake" }
func (s textSeat) Model() string { return "m" }
func (s textSeat) Judge(context.Context, string, string) (agent.Reply, error) {
	return agent.Reply{Text: s.text, Model: "m"}, nil
}

const (
	svcTitle  = "THE QWXMARKERSAGA"
	svcAuthor = "Ann Roe"
)

// bookSpec is one book of a run sealed through the extraction service at the
// current versions: what glm and luna answer, and an edit of the block just
// before it is sealed (to stand for a sealing under other rules).
type bookSpec struct{ glm, luna string }

// sealThroughService finishes a completed run with one book per spec, each
// with an EPUB whose extraction is what Service.Extract made of the answers,
// after edit.
func sealThroughService(ctx context.Context, t *testing.T, specs []bookSpec, edit func(book int, x *evidence.Extraction)) (*store.Store, int64) {
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
	var books []library.BookRef
	for i := range specs {
		books = append(books, library.BookRef{ID: i + 1, UUID: "u" + string(rune('a'+i))})
	}
	r, err := runs.CreateRun(ctx, verify.Scope{}, "library", "v1", books)
	if err != nil {
		t.Fatal(err)
	}
	for i, b := range books {
		eb := evidence.NewExcerptBuilder(evidence.ExcerptEPUBText)
		eb.Add(1, evidence.LandmarkTitle, svcTitle)
		eb.Add(2, evidence.LandmarkCopyright, "by "+svcAuthor+"\n© 2019 "+svcAuthor)
		ex := eb.Excerpt()
		ex.Complete = true
		body := ex.Text
		es := []extractor.Extractor{
			extractor.NewWithSeat("glm", "glm", "m", "", textSeat{specs[i].glm}),
			extractor.NewWithSeat("luna", "luna", "m", "", textSeat{specs[i].luna}),
		}
		svc := extraction.New(extraction.Config{Extractors: es, Hosts: map[string]string{"glm": "h", "luna": "h"},
			MaxCalls: map[string]int{"glm": 100, "luna": 100}, Quotas: agent.NewQuotas(), Timeout: time.Second})
		svc.BeginRun(r.ID)
		x, raws, err := svc.Extract(ctx, *ex, body)
		if err != nil {
			t.Fatal(err)
		}
		if edit != nil {
			edit(b.ID, x)
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
	}
	if err := runs.SetStatus(ctx, r.ID, verify.RunCompleted, ""); err != nil {
		t.Fatal(err)
	}
	return st, r.ID
}

const goodAnswer = `{"claims":[{"field":"title","value":"` + svcTitle + `","quote":"` + svcTitle + `"},` +
	`{"field":"contributor","value":"` + svcAuthor + `","role":"author","quote":"` + svcAuthor + `"},` +
	`{"field":"contributor","value":"Nobody Here","role":"author","quote":"` + svcAuthor + `"}]}`

func TestReverifyOfABlockSealedTodayChangesNothing(t *testing.T) {
	ctx := context.Background()
	st, run := sealThroughService(ctx, t, []bookSpec{{goodAnswer, goodAnswer}, {goodAnswer, `{"claims":[]}`}}, nil)
	res, err := eval.Reverify(ctx, store.NewReportRepo(st), run)
	if err != nil {
		t.Fatal(err)
	}
	if res.Formats != 2 || res.Skipped != 0 || res.AgreedChanged != 0 || len(res.AgreedChangedBooks) != 0 || res.StateChanged != 0 || res.Unsealable != 0 || res.Broken != 0 {
		t.Fatalf("report = %+v", res)
	}
	for name, s := range res.Sources {
		for _, st := range []string{"grounded", "excluded_section", "ambiguous_quote", "value_not_derived", "role_unproven", "not_found"} {
			if s.Sealed[st] != s.Derived[st] {
				t.Errorf("%s %s: sealed %d, re-derived %d", name, st, s.Sealed[st], s.Derived[st])
			}
		}
	}
	if len(res.Breakdown) == 0 {
		t.Fatal("the answers should have produced a value_not_derived claim")
	}
	for status, b := range res.Breakdown {
		for f, n := range b.Sealed {
			if b.Derived[f] != n {
				t.Errorf("%s %s: sealed %d, re-derived %d", status, f, n, b.Derived[f])
			}
		}
		for f, n := range b.Derived {
			if b.Sealed[f] != n {
				t.Errorf("%s %s: sealed %d, re-derived %d", status, f, b.Sealed[f], n)
			}
		}
	}
	for key, k := range res.Keys {
		if k.Sealed != k.Derived {
			t.Errorf("key %s: sealed %+v, re-derived %+v", key, k.Sealed, k.Derived)
		}
	}
}

func TestReverifyCountsParseStateChanges(t *testing.T) {
	ctx := context.Background()
	// Book 1: luna's answer parses, but its state was sealed invalid_output.
	// Book 2: glm's answer doesn't parse, but its state was sealed ok.
	st, run := sealThroughService(ctx, t, []bookSpec{{goodAnswer, goodAnswer}, {"not json", goodAnswer}, {goodAnswer, goodAnswer}},
		func(book int, x *evidence.Extraction) {
			switch book {
			case 1:
				// An invalid_output extractor holds no claims.
				x.Extractors[1].State = evidence.ExtractInvalidOutput
				x.Claims = slices.DeleteFunc(x.Claims, func(c evidence.ExtractedClaim) bool { return c.Source == "luna" })
				x.Agreed = nil
			case 2:
				x.Extractors[0].State = evidence.ExtractOK
			}
		})
	res, err := eval.Reverify(ctx, store.NewReportRepo(st), run)
	if err != nil {
		t.Fatal(err)
	}
	if res.Formats != 3 || res.StateChanged != 2 || res.Skipped != 0 {
		t.Fatalf("formats %d, state changed %d, skipped %d", res.Formats, res.StateChanged, res.Skipped)
	}
	var text strings.Builder
	res.WriteText(&text)
	if !strings.Contains(text.String(), "parse state changed: 2") {
		t.Fatalf("text = %s", text.String())
	}
}

func TestReverifyNeverPrintsAModelsWords(t *testing.T) {
	ctx := context.Background()
	const m = "zzmarkerword"
	odd := `{"claims":[{"field":"` + m + `","value":"` + m + `","role":"` + m + `","label":"` + m + `","quote":"` + m + `"},` +
		`{"field":"contributor","value":"` + m + `","role":"` + m + `","quote":"` + m + `"},` +
		`{"field":"year","value":"2019","label":"` + m + `","quote":"2019"}]}`
	st, run := sealThroughService(ctx, t, []bookSpec{{odd, odd}}, nil)
	res, err := eval.Reverify(ctx, store.NewReportRepo(st), run)
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	res.WriteText(&text)
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text.String(), "other") && !strings.Contains(text.String(), "contributor/other") {
		t.Fatalf("the unknown words should be reduced to other: %s", text.String())
	}
	for _, out := range []string{text.String(), string(raw)} {
		for _, leak := range []string{m, svcTitle, svcAuthor, "QWXMARKER"} {
			if strings.Contains(out, leak) {
				t.Fatalf("output leaks %q: %s", leak, out)
			}
		}
	}
}

func TestReverifyListsBrokenSealsOnce(t *testing.T) {
	ctx := context.Background()
	st, run := sealThroughService(ctx, t, []bookSpec{{goodAnswer, goodAnswer}, {goodAnswer, goodAnswer}}, nil)
	if _, err := st.Pool.Exec(ctx, `UPDATE evidence SET package=jsonb_set(package,'{calibre_record,title}','"Edited"') WHERE run_id=$1 AND book_id=2`, run); err != nil {
		t.Fatal(err)
	}
	res, err := eval.Reverify(ctx, store.NewReportRepo(st), run)
	if err != nil {
		t.Fatal(err)
	}
	if res.Formats != 1 || res.Broken != 1 || len(res.BrokenBooks) != 1 || res.BrokenBooks[0] != 2 {
		t.Fatalf("formats %d, broken %d %v", res.Formats, res.Broken, res.BrokenBooks)
	}
}
