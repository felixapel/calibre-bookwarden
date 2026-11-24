package eval

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/findings"
	lib "github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/report"
	"github.com/felixapel/calibre-bookwarden/internal/store"
	"github.com/felixapel/calibre-bookwarden/internal/testutil/pgtest"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

// TestRecallServiceEndToEnd draws a sample from a real checked run with real
// fixes in fix_projection, answers it, reads the result, then destroys the
// run's projection: the result must not move.
func TestRecallServiceEndToEnd(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, pgtest.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	runs := store.NewRunRepo(st)
	books := make([]lib.BookRef, 6)
	for i := range books {
		books[i] = lib.BookRef{ID: i + 1, UUID: fmt.Sprintf("uuid-%d", i+1)}
	}
	run, err := runs.CreateRun(ctx, verify.Scope{}, "library", "v1", books)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range books {
		p := evidence.Package{
			SchemaVersion: evidence.SchemaVersion, LibraryID: "library", BookID: b.ID, UUID: b.UUID,
			Calibre: evidence.Record{Title: "Binary Tides", Authors: []string{"Ada Reed"}, Languages: []string{"eng"}, Identifiers: map[string]string{}},
			Formats: []evidence.FormatEvidence{{Format: "EPUB", State: evidence.StateMissing}},
		}
		raw, seal, err := findings.Seal(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		if err := runs.FinishBook(ctx, run.ID, b, verify.Outcome{State: verify.BookChecked, Evidence: raw, Seal: seal}); err != nil {
			t.Fatal(err)
		}
	}
	if err := runs.SetStatus(ctx, run.ID, verify.RunCompleted, ""); err != nil {
		t.Fatal(err)
	}
	if check, err := report.New(store.NewReportRepo(st)).CheckSeals(ctx, run.ID); err != nil || check.Broken != 0 {
		t.Fatalf("CheckSeals = %+v, %v", check, err)
	}
	// The verified projection's fixes: book 2 a title, book 3 an author, book
	// 4 a title.
	for _, f := range []struct {
		book            int
		field, proposed string
	}{{2, "title", "Binary Tides II"}, {3, "authors", "Ada Reed"}, {4, "title", "Other"}} {
		if _, err := st.Pool.Exec(ctx, `INSERT INTO fix_projection (run_id, book_id, ordinal, field, proposal) VALUES ($1, $2, 0, $3, jsonb_build_object('field', $3::text, 'current', '[]'::jsonb, 'proposed', jsonb_build_array($4::text), 'roots', '["text"]'::jsonb))`,
			run.ID, f.book, f.field, f.proposed); err != nil {
			t.Fatal(err)
		}
	}

	svc := NewRecallService(store.NewRecallRepo(st))
	seed := int64(5)
	id, err := svc.Start(ctx, &seed, "admin")
	if err != nil {
		t.Fatal(err)
	}
	var pinned bool
	if err := st.Pool.QueryRow(ctx, `SELECT pinned FROM runs WHERE id=$1`, run.ID).Scan(&pinned); err != nil || !pinned {
		t.Errorf("run pinned = %v, %v", pinned, err)
	}
	sample, err := svc.Sample(ctx, id)
	if err != nil || sample.RunID != run.ID || sample.Size != 6 || sample.Seed != 5 || sample.DecisionVersion != decide.Version || sample.FindingsVersion != findings.Version {
		t.Fatalf("sample %+v, %v", sample, err)
	}
	// A library of six gives everyone; the same seed draws it again the same way.
	id2, err := svc.Start(ctx, &seed, "admin")
	if err != nil {
		t.Fatal(err)
	}
	a, _ := svc.repo.SampleBooks(ctx, id)
	b, _ := svc.repo.SampleBooks(ctx, id2)
	if !reflect.DeepEqual(a, b) {
		t.Error("the same seed drew other books from the same run")
	}
	// Answer the second sample's six books; titles: book 2 wrong with an
	// equivalent typed value (found), book 3 wrong and nothing proposed
	// (missed), book 4 right but a fix exists (false alarm); the rest skip.
	pos := map[int]int{}
	for _, sb := range b {
		pos[sb.BookID] = sb.Position
	}
	typed := func(s string) *string { return &s }
	title := map[int]store.Confirmation{
		2: {Field: "title", Answer: store.AnswerRight, Typed: typed("binary tides ii")}, // a typed value implies wrong
		3: {Field: "title", Answer: store.AnswerWrong},
		4: {Field: "title", Answer: store.AnswerRight},
	}
	for _, bk := range books {
		answers := []store.Confirmation{{Field: "title", Answer: store.AnswerSkip}}
		if t, ok := title[bk.ID]; ok {
			answers = []store.Confirmation{t}
		}
		if err := svc.Confirm(ctx, id2, pos[bk.ID], answers, "admin"); err != nil {
			t.Fatal(err)
		}
	}
	open, err := svc.Status(ctx, id2, true)
	if err != nil || open.Result == nil {
		t.Fatalf("Status = %+v, %v", open, err)
	}
	got := open.Result
	var tf FieldRecall
	for _, f := range got.Fields {
		if f.Field == "title" {
			tf = f
		}
	}
	if tf.Wrong != 2 || tf.Found != 1 || tf.Recall != 0.5 || !reflect.DeepEqual(tf.FalseAlarms, []int{4}) || tf.Skipped != 3 || got.DecisionVersion != decide.Version || got.Unrepresented != 0 {
		t.Fatalf("title recall %+v, result %+v", tf, got)
	}

	// The run's projection and its pin go; the result is the same.
	if _, err := st.Pool.Exec(ctx, `DELETE FROM book_projection WHERE run_id=$1`, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool.Exec(ctx, `UPDATE runs SET pinned=false WHERE id=$1`, run.ID); err != nil {
		t.Fatal(err)
	}
	again, err := svc.Status(ctx, id2, true)
	if err != nil || again.Result == nil || !reflect.DeepEqual(*again.Result, *got) {
		t.Errorf("after the projection was cleared: %+v, %v", again.Result, err)
	}
	// The sample is closed, and it won't take another answer.
	if err := svc.Confirm(ctx, id2, 1, []store.Confirmation{{Field: "title", Answer: store.AnswerWrong}}, "admin"); err == nil {
		t.Error("a closed sample took an answer")
	}
}
