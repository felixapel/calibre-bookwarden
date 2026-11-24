package store

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/findings"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/report"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

// recallRun stores a checked run of three books: an EPUB in English, a PDF
// with no language, and a book with an EPUB and a PDF in German.
func recallRun(t *testing.T) (*RecallRepo, *Store, verify.Run, []library.BookRef) {
	t.Helper()
	ctx := context.Background()
	r, s := runRepo(t)
	books := refs(3)
	run, err := r.CreateRun(ctx, verify.Scope{}, "library", "v1", books)
	if err != nil {
		t.Fatal(err)
	}
	shape := []struct {
		langs   []string
		formats []string
	}{{[]string{"eng"}, []string{"EPUB"}}, {nil, []string{"PDF"}}, {[]string{"deu", "eng"}, []string{"PDF", "EPUB"}}}
	for i, ref := range books {
		p := cleanEvidence(ref)
		p.Calibre.Languages = shape[i].langs
		p.Formats = nil
		for _, f := range shape[i].formats {
			p.Formats = append(p.Formats, evidence.FormatEvidence{Format: f, State: evidence.StateMissing})
		}
		if err := r.FinishBook(ctx, run.ID, ref, findingOutcome(t, p, false)); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.SetStatus(ctx, run.ID, verify.RunCompleted, ""); err != nil {
		t.Fatal(err)
	}
	return NewRecallRepo(s), s, run, books
}

func TestRecallRepoReadsTheCheckedRun(t *testing.T) {
	ctx := context.Background()
	repo, s, run, books := recallRun(t)
	if _, err := repo.LatestCheckedRun(ctx); !errors.Is(err, ErrNoCheckedRun) {
		t.Fatalf("before a check: %v, want ErrNoCheckedRun", err)
	}
	if check, err := report.New(NewReportRepo(s)).CheckSeals(ctx, run.ID); err != nil || check.Broken != 0 {
		t.Fatalf("CheckSeals = %+v, %v", check, err)
	}
	got, err := repo.LatestCheckedRun(ctx)
	if err != nil || got.ID != run.ID || got.LibraryID != "library" {
		t.Fatalf("LatestCheckedRun = %+v, %v", got, err)
	}
	cands, err := repo.Candidates(ctx, run.ID)
	if err != nil || len(cands) != 3 {
		t.Fatalf("Candidates = %+v, %v", cands, err)
	}
	for i, want := range []struct {
		lang    string
		formats []string
	}{{"eng", []string{"EPUB"}}, {"", []string{"PDF"}}, {"deu", []string{"PDF", "EPUB"}}} {
		c := cands[i]
		if c.BookID != books[i].ID || c.UUID != books[i].UUID || c.Language != want.lang || !reflect.DeepEqual(c.Formats, want.formats) || c.Tier == "" {
			t.Errorf("candidate %d = %+v, want language %q formats %v", i, c, want.lang, want.formats)
		}
	}
}

func TestRecallSampleAndConfirmations(t *testing.T) {
	ctx := context.Background()
	repo, s, run, books := recallRun(t)
	if _, err := repo.LatestSample(ctx); !errors.Is(err, ErrNoSuchSample) {
		t.Fatalf("no sample yet: %v", err)
	}
	if _, err := repo.CreateSample(ctx, RecallSample{LibraryID: "library", RunID: run.ID, Size: 1, Strata: map[string]StratumCount{}}, nil); err == nil {
		t.Fatal("a run without a seal check was sampled")
	}
	if _, err := report.New(NewReportRepo(s)).CheckSeals(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	sample := RecallSample{LibraryID: "library", RunID: run.ID, Seed: 11, Size: 2, CreatedBy: "admin",
		Strata: map[string]StratumCount{"EPUB|eng|A|nofix": {Library: 3, Sampled: 2}}}
	id, err := repo.CreateSample(ctx, sample, []SampleBook{
		{BookID: books[2].ID, UUID: books[2].UUID, Stratum: "EPUB|eng|A|nofix", Position: 1},
		{BookID: books[0].ID, UUID: books[0].UUID, Stratum: "EPUB|eng|A|nofix", Position: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := repo.LatestSample(ctx)
	if err != nil || got.FindingsVersion != findings.Version || got.DecisionVersion != decide.Version || got.ClosedAt != nil {
		t.Fatalf("versions %d/%d, closed %v, %v", got.FindingsVersion, got.DecisionVersion, got.ClosedAt, err)
	}
	if err != nil || got.ID != id || got.Seed != 11 || got.Size != 2 || got.CreatedBy != "admin" || !reflect.DeepEqual(got.Strata, sample.Strata) {
		t.Fatalf("LatestSample = %+v, %v", got, err)
	}
	var pinned bool
	if err := s.Pool.QueryRow(ctx, `SELECT pinned FROM runs WHERE id=$1`, run.ID).Scan(&pinned); err != nil || !pinned {
		t.Errorf("the sample's run is pinned = %v, %v", pinned, err)
	}
	sb, err := repo.SampleBooks(ctx, id)
	if err != nil || len(sb) != 2 || sb[0].BookID != books[2].ID || sb[1].Position != 2 {
		t.Fatalf("SampleBooks = %+v, %v", sb, err)
	}
	if _, err := repo.SampleBookAt(ctx, id, 3); !errors.Is(err, ErrNoSuchSample) {
		t.Errorf("position 3: %v", err)
	}

	// The latest answer wins; the earlier one stays in the table.
	typed := "Binary Tides II"
	if err := repo.Confirm(ctx, id, 2, []Confirmation{
		{Field: "title", Answer: AnswerWrong, Typed: &typed, By: "admin"},
		{Field: "authors", Answer: AnswerRight, By: "admin"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Confirm(ctx, id, 2, []Confirmation{{Field: "title", Answer: AnswerRight, By: "admin"}}); err != nil {
		t.Fatal(err)
	}
	latest, err := repo.LatestConfirmations(ctx, id)
	if err != nil || len(latest) != 2 {
		t.Fatalf("LatestConfirmations = %+v, %v", latest, err)
	}
	byField := map[string]Confirmation{}
	for _, c := range latest {
		byField[c.Field] = c
	}
	if c := byField["title"]; c.Answer != AnswerRight || c.Typed != nil || c.BookID != books[0].ID || c.UUID != books[0].UUID || c.By != "admin" {
		t.Errorf("title = %+v, want the later right answer", c)
	}
	if byField["authors"].Answer != AnswerRight {
		t.Errorf("authors = %+v", byField["authors"])
	}
	var rows int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM confirmations`).Scan(&rows); err != nil || rows != 3 {
		t.Errorf("%d confirmation rows, %v; want 3 (append-only)", rows, err)
	}

	// Append-only: no update, no delete, in any of the three tables.
	for _, q := range []string{
		`UPDATE confirmations SET answer='skip'`, `DELETE FROM confirmations`,
		`UPDATE recall_sample_books SET stratum='x'`, `DELETE FROM recall_sample_books`,
		`UPDATE recall_samples SET seed=1`, `DELETE FROM recall_samples`,
	} {
		if _, err := s.Pool.Exec(ctx, q); err == nil {
			t.Errorf("%s succeeded", q)
		}
	}

	// A typed value needs "wrong"; unknown fields and answers are refused by
	// the schema, and a failed batch records nothing.
	for _, bad := range []Confirmation{
		{Field: "title", Answer: AnswerRight, Typed: &typed, By: "admin"},
		{Field: "series", Answer: AnswerRight, By: "admin"},
		{Field: "title", Answer: "maybe", By: "admin"},
	} {
		if err := repo.Confirm(ctx, id, 1, []Confirmation{{Field: "isbn", Answer: AnswerSkip, By: "admin"}, bad}); err == nil {
			t.Errorf("%+v was accepted", bad)
		}
	}
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM confirmations`).Scan(&rows); err != nil || rows != 3 {
		t.Errorf("%d confirmation rows after refused batches, %v; want 3", rows, err)
	}
	if err := repo.Confirm(ctx, id, 9, []Confirmation{{Field: "title", Answer: AnswerSkip, By: "admin"}}); !errors.Is(err, ErrNoSuchSample) {
		t.Errorf("confirm at position 9: %v", err)
	}

	// Confirmations are not labels: nothing reached the decision tables.
	for _, table := range []string{"proposals", "verdict_reveals", "fix_type_unmutes"} {
		if n := rowCount(t, s, table); n != 0 {
			t.Errorf("%s has %d rows after confirmations", table, n)
		}
	}
}

func TestRecallSampleFactsAreFrozenAtTheDraw(t *testing.T) {
	ctx := context.Background()
	repo, s, run, books := recallRun(t)
	if _, err := report.New(NewReportRepo(s)).CheckSeals(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	// Two fixes of one book in the verified projection.
	for _, f := range []struct {
		book     int
		field    string
		proposal string
	}{
		{books[0].ID, "title", `{"field":"title","current":["a"],"proposed":["Binary Tides II"],"roots":["text"]}`},
		{books[0].ID, "pubdate", `{"field":"pubdate","current":[],"proposed":["2001-07-01"],"roots":["dnb","file"]}`},
	} {
		if _, err := s.Pool.Exec(ctx, `INSERT INTO fix_projection (run_id, book_id, ordinal, field, proposal) VALUES ($1, $2, (SELECT count(*) FROM fix_projection WHERE run_id=$1 AND book_id=$2), $3, $4::jsonb)`,
			run.ID, f.book, f.field, f.proposal); err != nil {
			t.Fatal(err)
		}
	}
	id, err := repo.CreateSample(ctx, RecallSample{LibraryID: "library", RunID: run.ID, Seed: 1, Size: 2, Strata: map[string]StratumCount{}}, []SampleBook{
		{BookID: books[0].ID, UUID: books[0].UUID, Stratum: "s", Language: "eng", Position: 1},
		{BookID: books[1].ID, UUID: books[1].UUID, Stratum: "s", Position: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[int]SampleFacts{
		books[0].ID: {Language: "eng", Fixes: map[string][]string{"title": {"Binary Tides II"}, "pubdate": {"2001-07-01"}}},
		books[1].ID: {Language: "", Fixes: map[string][]string{}},
	}
	facts, err := repo.SampleFacts(ctx, id)
	if err != nil || !reflect.DeepEqual(facts, want) {
		t.Fatalf("SampleFacts = %+v, %v; want %+v", facts, err, want)
	}
	for _, q := range []string{`UPDATE recall_sample_facts SET field='x'`, `DELETE FROM recall_sample_facts`} {
		if _, err := s.Pool.Exec(ctx, q); err == nil {
			t.Errorf("%s succeeded", q)
		}
	}
	// The run goes: its projection and its books are deleted.
	if _, err := s.Pool.Exec(ctx, `DELETE FROM book_projection WHERE run_id=$1`, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE runs SET pinned=false WHERE id=$1`, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Candidates(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if again, err := repo.SampleFacts(ctx, id); err != nil || !reflect.DeepEqual(again, want) {
		t.Errorf("after the projection was cleared: %+v, %v", again, err)
	}
	var n int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM fix_projection WHERE run_id=$1`, run.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("the projection still has %d fixes, %v", n, err)
	}
}

func TestRecallClosure(t *testing.T) {
	ctx := context.Background()
	repo, s, run, books := recallRun(t)
	if _, err := report.New(NewReportRepo(s)).CheckSeals(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	id, err := repo.CreateSample(ctx, RecallSample{LibraryID: "library", RunID: run.ID, Seed: 1, Size: 1, Strata: map[string]StratumCount{}},
		[]SampleBook{{BookID: books[0].ID, UUID: books[0].UUID, Stratum: "s", Position: 1}})
	if err != nil {
		t.Fatal(err)
	}
	answer := []Confirmation{{Field: "title", Answer: AnswerRight, By: "admin"}}
	if err := repo.Confirm(ctx, id, 1, answer); err != nil {
		t.Fatal(err)
	}
	first, err := repo.Close(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	// Closing again keeps the first time; answers are refused from then on.
	if second, err := repo.Close(ctx, id); err != nil || !second.Equal(first) {
		t.Errorf("second close at %v, %v; want %v", second, err, first)
	}
	if err := repo.Confirm(ctx, id, 1, answer); !errors.Is(err, ErrSampleClosed) {
		t.Errorf("confirm after close: %v, want ErrSampleClosed", err)
	}
	sample, err := repo.Sample(ctx, id)
	if err != nil || sample.ClosedAt == nil || !sample.ClosedAt.Equal(first) {
		t.Errorf("sample closed at %v, %v", sample.ClosedAt, err)
	}
	for _, q := range []string{`UPDATE recall_closures SET closed_at=now()`, `DELETE FROM recall_closures`} {
		if _, err := s.Pool.Exec(ctx, q); err == nil {
			t.Errorf("%s succeeded", q)
		}
	}
	var rows int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM confirmations`).Scan(&rows); err != nil || rows != 1 {
		t.Errorf("%d confirmation rows, %v", rows, err)
	}
}
