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

const reverifyMarker = "QWXMARKERSAGA"

// seedOldVerifier finishes a run of two books whose EPUB extraction was
// sealed under an older verifier: luna's title quote was judged
// not_found then, and today it grounds. It returns the raw answer's hash of
// each book.
func seedOldVerifier(ctx context.Context, t *testing.T) (*store.Store, int64, map[int]string) {
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
	title := "THE " + reverifyMarker
	rawOf := map[int]string{}
	for _, b := range books {
		// A trailing newline per book keeps each book's raw answer distinct.
		answer := `{"claims":[{"field":"title","value":"` + title + `","quote":"` + title + `"},` +
			`{"field":"contributor","value":"Nobody Here","role":"author","quote":"` + title + `"}]}` + strings.Repeat("\n", b.ID)
		sum := sha256.Sum256([]byte(answer))
		rawSHA := hex.EncodeToString(sum[:])
		rawOf[b.ID] = rawSHA
		eb := evidence.NewExcerptBuilder(evidence.ExcerptEPUBText)
		eb.Add(1, evidence.LandmarkTitle, title)
		ex := eb.Excerpt()
		ex.Complete = true
		body := ex.Text
		x := &evidence.Extraction{PromptVersion: "extract-2", ExtractVersion: 1, ExcerptSHA256: ex.SHA256}
		for _, name := range []string{"glm", "luna"} {
			// Only glm's quote was found then.
			tc := evidence.ExtractedClaim{Field: "title", Source: name, Status: "not_found"}
			if name == "glm" {
				tc = evidence.ExtractedClaim{Field: "title", Value: title, Source: name, Status: "grounded", Unit: 1, Start: 0, End: len([]rune(title))}
			}
			x.Extractors = append(x.Extractors, evidence.ExtractorRun{Name: name, Family: name, Model: "m", State: evidence.ExtractOK, RawSHA256: rawSHA})
			x.Claims = append(x.Claims,
				tc,
				evidence.ExtractedClaim{Field: "contributor", Source: name, Status: "role_unproven", Unit: 1, Start: 0, End: 5})
		}
		x.Agreed = []evidence.AgreedFact{{Key: "title", Outcome: evidence.AgreeSingle, Values: []evidence.AgreedValue{{Value: title}}, Voices: []string{"glm"}}}
		p := pkg(b, false)
		ex.Text = ""
		p.Formats = []evidence.FormatEvidence{{Format: "EPUB", State: evidence.StateOK, Detected: "epub", Excerpt: ex, Extraction: x}}
		raw, seal, err := decide.Seal(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		out := verify.Outcome{State: verify.BookChecked, Evidence: raw, Seal: seal, Excerpts: map[string]string{ex.SHA256: body}, Raws: []verify.RawOutput{{SHA256: rawSHA, Body: answer}}}
		if err := runs.FinishBook(ctx, r.ID, b, out); err != nil {
			t.Fatal(err)
		}
	}
	if err := runs.SetStatus(ctx, r.ID, verify.RunCompleted, ""); err != nil {
		t.Fatal(err)
	}
	return st, r.ID, rawOf
}

func TestReverifyShowsWhatTodaysVerifierChanges(t *testing.T) {
	ctx := context.Background()
	st, run, _ := seedOldVerifier(ctx, t)
	repo := store.NewReportRepo(st)
	res, err := eval.Reverify(ctx, repo, run)
	if err != nil {
		t.Fatal(err)
	}
	if res.CurrentVersion != extract.Version || res.Formats != 2 || res.Skipped != 0 || res.SealedVersions[1] != 2 {
		t.Fatalf("report = %+v", res)
	}
	luna := res.Sources["luna"]
	if luna == nil || luna.Sealed["not_found"] != 2 || luna.Derived["not_found"] != 0 || luna.Derived["grounded"] < 2 {
		t.Fatalf("luna = %+v", luna)
	}
	title := res.Keys["title"]
	if title == nil || title.Sealed.Single != 2 || title.Derived.Agreed != 2 {
		t.Fatalf("title = %+v", title)
	}
	if res.AgreedChanged != 2 || len(res.AgreedChangedBooks) != 2 {
		t.Fatalf("agreed changed = %d %v", res.AgreedChanged, res.AgreedChangedBooks)
	}
	// The contributor claim names a person the quote doesn't derive.
	if b := res.Breakdown[extract.StatusValueNotDerived]; b == nil || b.Derived["contributor"] != 4 || b.Classes["contributor/author"] != 4 || b.Sealed["contributor"] != 0 {
		t.Fatalf("value_not_derived = %+v", b)
	}
	// Four refused values, two per book: counted once per book.
	if c := res.Refusals[extract.RefusedNotInQuote]["contributor"]; c != (eval.RefusalCount{Values: 4, Books: 2}) || len(res.Refusals) != 1 {
		t.Fatalf("refusals = %+v", res.Refusals)
	}
	if b := res.Breakdown[extract.StatusRoleUnproven]; b == nil || b.Sealed["contributor"] != 4 || len(b.Derived) != 0 || len(b.Classes) != 0 {
		t.Fatalf("role_unproven = %+v", b)
	}
	var text strings.Builder
	res.WriteText(&text)
	t.Log("\n" + text.String())
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"skipped for a missing or corrupt excerpt or raw answer: 0", "not_found", "by class (re-derived only)", "contributor/author", "formats whose agreed set changed: 2"} {
		if !strings.Contains(text.String(), want) {
			t.Errorf("no %q in %s", want, text.String())
		}
	}
	for _, out := range []string{text.String(), string(raw)} {
		for _, leak := range []string{reverifyMarker, "Nobody", "THE ", "Zyxwv", "Ada Reed"} {
			if strings.Contains(out, leak) {
				t.Fatalf("output leaks %q: %s", leak, out)
			}
		}
	}
	// A second run gives the same report. (That nothing is written is
	// store.OpenReadOnly's guarantee, which the command uses; this only shows
	// that the report is repeatable.)
	again, err := eval.Reverify(ctx, repo, run)
	if err != nil {
		t.Fatal(err)
	}
	if a, _ := json.Marshal(again); string(a) != string(raw) {
		t.Fatalf("second run differs:\n%s\n%s", a, raw)
	}
}

func TestReverifyCountsAMissingRawAsSkipped(t *testing.T) {
	ctx := context.Background()
	st, run, rawOf := seedOldVerifier(ctx, t)
	if _, err := st.Pool.Exec(ctx, `DELETE FROM extraction_raw WHERE sha256=$1`, rawOf[2]); err != nil {
		t.Fatal(err)
	}
	res, err := eval.Reverify(ctx, store.NewReportRepo(st), run)
	if err != nil {
		t.Fatal(err)
	}
	if res.Formats != 2 || res.Skipped != 1 {
		t.Fatalf("formats %d, skipped %d", res.Formats, res.Skipped)
	}
	if g := res.Sources["luna"]; g == nil || g.Sealed["not_found"] != 1 || res.AgreedChanged != 1 {
		t.Fatalf("a skipped format must not count: %+v, changed %d", g, res.AgreedChanged)
	}
}
