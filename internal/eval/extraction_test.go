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
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/store"
	"github.com/felixapel/calibre-bookwarden/internal/testutil/pgtest"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

// SeedExtractions finishes a run of two books whose EPUB has an extraction
// where luna and glm agree on a title Calibre doesn't have. It returns the run.
func seedExtractions(ctx context.Context, t *testing.T) (*store.Store, int64) {
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
	books := []library.BookRef{{ID: 1, UUID: "u1"}, {ID: 2, UUID: "u2"}}
	r, err := runs.CreateRun(ctx, verify.Scope{}, "library", "v1", books)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range books {
		eb := evidence.NewExcerptBuilder(evidence.ExcerptEPUBText)
		eb.Add(1, evidence.LandmarkTitle, "THE LANTERN")
		ex := eb.Excerpt()
		ex.Complete = true
		body := ex.Text
		var raws []verify.RawOutput
		x := &evidence.Extraction{PromptVersion: "extract-2", ExtractVersion: 2, ExcerptSHA256: ex.SHA256}
		for _, name := range []string{"glm", "luna"} {
			answer := `{"claims":[]}` + name
			sum := sha256.Sum256([]byte(answer))
			raws = append(raws, verify.RawOutput{SHA256: hex.EncodeToString(sum[:]), Body: answer})
			x.Extractors = append(x.Extractors, evidence.ExtractorRun{Name: name, Family: name, Model: "m", State: evidence.ExtractOK, RawSHA256: raws[len(raws)-1].SHA256})
			x.Claims = append(x.Claims, evidence.ExtractedClaim{Field: "title", Value: "THE LANTERN", Source: name, Status: "grounded", Unit: 1, Start: 0, End: 11})
		}
		x.Agreed = []evidence.AgreedFact{{Key: "title", Outcome: evidence.AgreeAgreed, Values: []evidence.AgreedValue{{Value: "THE LANTERN"}}, Voices: []string{"glm", "luna"}}}
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

func TestExtractionsOverARun(t *testing.T) {
	ctx := context.Background()
	st, run := seedExtractions(ctx, t)
	if _, err := st.Pool.Exec(ctx, `UPDATE evidence SET package=jsonb_set(package,'{calibre_record,title}','"Edited"') WHERE run_id=$1 AND book_id=2`, run); err != nil {
		t.Fatal(err)
	}
	res, err := eval.Extractions(ctx, store.NewReportRepo(st), run)
	if err != nil {
		t.Fatal(err)
	}
	if res.Formats != 1 || res.Broken != 1 || len(res.BrokenBooks) != 1 || res.BrokenBooks[0] != 2 {
		t.Fatalf("formats %d, broken %d %v", res.Formats, res.Broken, res.BrokenBooks)
	}
	if got := res.Keys["title"]; got == nil || *got != (eval.KeyCount{Agreed: 1, Calibre: eval.Comparison{Differ: 1}, Records: eval.Comparison{None: 1}}) {
		t.Fatalf("title = %+v", got)
	}
	if got := res.Extractors["luna"]; got == nil || got.States[evidence.ExtractOK] != 1 {
		t.Fatalf("luna = %+v", got)
	}
	var text strings.Builder
	res.WriteText(&text)
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"shadow extraction of 1 formats", "books with a broken seal: 1", "broken books: 2", "luna", "title"} {
		if !strings.Contains(text.String(), want) {
			t.Errorf("no %q in %s", want, text.String())
		}
	}
	for _, out := range []string{text.String(), string(raw)} {
		for _, leak := range []string{secretTitle, "Zyxwv", "LANTERN", "Ada Reed", "claims"} {
			if strings.Contains(out, leak) {
				t.Fatalf("output leaks %q: %s", leak, out)
			}
		}
	}
}
