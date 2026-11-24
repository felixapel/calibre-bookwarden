package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

// excerptOutcome seals a package whose EPUB has an excerpt of text, and
// returns the outcome with the body.
func excerptOutcome(t *testing.T, ref library.BookRef, texts ...string) (verify.Outcome, []*evidence.Excerpt) {
	t.Helper()
	return excerptOutcomeWith(t, ref, nil, texts...)
}

// excerptOutcomeWith is excerptOutcome after change has edited the package.
func excerptOutcomeWith(t *testing.T, ref library.BookRef, change func(*evidence.Package), texts ...string) (verify.Outcome, []*evidence.Excerpt) {
	t.Helper()
	p := cleanEvidence(ref)
	p.Formats = nil
	var exs []*evidence.Excerpt
	bodies := map[string]string{}
	for i, text := range texts {
		b := evidence.NewExcerptBuilder(evidence.ExcerptEPUBText)
		b.Add(1, evidence.LandmarkCopyright, text)
		ex := b.Excerpt()
		exs = append(exs, ex)
		bodies[ex.SHA256] = ex.Text
		fe := evidence.FormatEvidence{Format: []string{"EPUB", "KEPUB", "ORIGINAL_EPUB"}[i], State: evidence.StateOK, Detected: "epub", Excerpt: ex}
		p.Formats = append(p.Formats, fe)
	}
	if change != nil {
		change(&p)
	}
	raw, seal, err := decide.Seal(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	return verify.Outcome{State: verify.BookChecked, Evidence: raw, Seal: seal, Excerpts: bodies}, exs
}

func excerptCount(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.Pool.QueryRow(context.Background(), `SELECT count(*) FROM excerpts`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestExcerptsTable(t *testing.T) {
	_, s := runRepo(t)
	ctx := context.Background()
	good := strings.Repeat("a", 64)
	for _, tt := range []struct {
		name, sha string
		chars     int
		ok        bool
	}{
		{"lowercase hex", good, 1, true},
		{"uppercase", strings.Repeat("A", 64), 1, false},
		{"short", good[:63], 1, false},
		{"not hex", strings.Repeat("g", 64), 1, false},
	} {
		_, err := s.Pool.Exec(ctx, `INSERT INTO excerpts (sha256, body, chars) VALUES ($1, 'x', $2)`, tt.sha, tt.chars)
		if (err == nil) != tt.ok {
			t.Errorf("%s: insert = %v, want ok=%v", tt.name, err, tt.ok)
		}
	}
	var created bool
	if err := s.Pool.QueryRow(ctx, `SELECT created_at IS NOT NULL FROM excerpts WHERE sha256=$1`, good).Scan(&created); err != nil || !created {
		t.Errorf("created_at = %v, %v", created, err)
	}
}

func TestMigrationsDoNotNeedToBeContiguous(t *testing.T) {
	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	last := ms[len(ms)-1]
	// Other changes add migrations of their own: only this one's number
	// and place in the order are pinned, not that it is the last.
	found := false
	for _, m := range ms {
		found = found || m.version == 16 && strings.HasPrefix(m.name, "0016_excerpts")
	}
	if !found || last.version < 16 {
		t.Fatalf("0016_excerpts is missing; last migration = %d %s", last.version, last.name)
	}
	for i := 1; i < len(ms); i++ {
		if ms[i].version <= ms[i-1].version {
			t.Errorf("migrations out of order at %s", ms[i].name)
		}
	}
}

func TestFinishBookWritesExcerptsInItsTransaction(t *testing.T) {
	ctx := context.Background()
	r, s := runRepo(t)
	rep := NewReportRepo(s)
	books := refs(2)
	run, err := r.CreateRun(ctx, verify.Scope{}, "library", "v1", books)
	if err != nil {
		t.Fatal(err)
	}
	out, exs := excerptOutcome(t, books[0], "Synthetic copyright page", "Synthetic second format")
	if err := r.FinishBook(ctx, run.ID, books[0], out); err != nil {
		t.Fatal(err)
	}
	if n := excerptCount(t, s); n != 2 {
		t.Fatalf("%d excerpts stored, want 2", n)
	}
	for _, ex := range exs {
		body, err := rep.Excerpt(ctx, ex.SHA256)
		if err != nil || body != ex.Text {
			t.Errorf("Excerpt(%s) = %q, %v", ex.SHA256[:8], body, err)
		}
	}
	// The second book shares one excerpt: the duplicate hash is a no-op.
	out2, _ := excerptOutcome(t, books[1], "Synthetic copyright page")
	if err := r.FinishBook(ctx, run.ID, books[1], out2); err != nil {
		t.Fatal(err)
	}
	if n := excerptCount(t, s); n != 2 {
		t.Fatalf("%d excerpts after a duplicate, want 2", n)
	}
	// The package in the database holds the hash and not the text.
	raw, _, err := r.Evidence(ctx, run.ID, books[0].ID)
	if err != nil || strings.Contains(string(raw), "Synthetic copyright") || !strings.Contains(string(raw), exs[0].SHA256) {
		t.Errorf("stored package %v: %s", err, raw)
	}
}

func TestFinishBookRefusesBadExcerptBodies(t *testing.T) {
	ctx := context.Background()
	for _, name := range []string{"missing body", "wrong body", "extra body"} {
		t.Run(name, func(t *testing.T) {
			r, s := runRepo(t)
			ref := refs(1)[0]
			run, err := r.CreateRun(ctx, verify.Scope{}, "library", "v1", []library.BookRef{ref})
			if err != nil {
				t.Fatal(err)
			}
			out, exs := excerptOutcome(t, ref, "Synthetic copyright page")
			hash := exs[0].SHA256
			switch name {
			case "missing body":
				out.Excerpts = nil
			case "wrong body":
				out.Excerpts[hash] = "Another text"
			case "extra body":
				out.Excerpts[evidence.ExcerptHash("unreferenced")] = "unreferenced"
			}
			if err := r.FinishBook(ctx, run.ID, ref, out); !errors.Is(err, errInvalidExcerpts) {
				t.Fatalf("FinishBook = %v, want invalid excerpts", err)
			}
			assertPendingWithoutEvidence(t, s, run.ID, ref.ID)
			if n := excerptCount(t, s); n != 0 {
				t.Errorf("%d excerpts written by a refused book", n)
			}
		})
	}
}

func TestRefusedBookLeavesNoExcerpts(t *testing.T) {
	// The excerpt rows ride in the book's transaction. This book fails after
	// they are inserted (its cover measures can't be stored), so none stays.
	ctx := context.Background()
	r, s := runRepo(t)
	ref := refs(1)[0]
	run, err := r.CreateRun(ctx, verify.Scope{}, "library", "v1", []library.BookRef{ref})
	if err != nil {
		t.Fatal(err)
	}
	out, _ := excerptOutcomeWith(t, ref, func(p *evidence.Package) {
		p.Cover = &evidence.CoverEvidence{State: evidence.StateOK, Bytes: 100, SHA256: strings.Repeat("a", 64), Detected: "png", Width: 200, Height: 300, DHash: "f0f0f0f0f0f0f0f0", Flatness: 10}
	}, "Synthetic copyright page")
	// A trigger makes the cover insert, which follows the excerpt insert, fail.
	for _, q := range []string{
		`CREATE FUNCTION fail_cover() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'late failure'; END $$`,
		`CREATE TRIGGER fail_cover BEFORE INSERT ON covers FOR EACH ROW EXECUTE FUNCTION fail_cover()`,
	} {
		if _, err := s.Pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	err = r.FinishBook(ctx, run.ID, ref, out)
	if err == nil || !strings.Contains(err.Error(), "cover") {
		t.Fatalf("FinishBook = %v, want the late cover failure", err)
	}
	assertPendingWithoutEvidence(t, s, run.ID, ref.ID)
	if n := excerptCount(t, s); n != 0 {
		t.Errorf("%d excerpts written by a refused book", n)
	}
}

func TestFinishBookRepairsACorruptExcerptRow(t *testing.T) {
	ctx := context.Background()
	r, s := runRepo(t)
	rep := NewReportRepo(s)
	books := refs(2)
	run, err := r.CreateRun(ctx, verify.Scope{}, "library", "v1", books)
	if err != nil {
		t.Fatal(err)
	}
	first, exs := excerptOutcome(t, books[0], "Synthetic copyright page")
	if err := r.FinishBook(ctx, run.ID, books[0], first); err != nil {
		t.Fatal(err)
	}
	hash := exs[0].SHA256
	if _, err := s.Pool.Exec(ctx, `UPDATE excerpts SET body='tampered', chars=8 WHERE sha256=$1`, hash); err != nil {
		t.Fatal(err)
	}
	if _, err := rep.Excerpt(ctx, hash); !errors.Is(err, ErrExcerptCorrupt) {
		t.Fatalf("tampered row = %v", err)
	}
	// The next book holds the same, hash-verified text: it heals the row.
	second, _ := excerptOutcome(t, books[1], "Synthetic copyright page")
	if err := r.FinishBook(ctx, run.ID, books[1], second); err != nil {
		t.Fatal(err)
	}
	if body, err := rep.Excerpt(ctx, hash); err != nil || body != exs[0].Text {
		t.Fatalf("after the repair: %q, %v", body, err)
	}
	if n := excerptCount(t, s); n != 1 {
		t.Errorf("%d excerpts, want 1", n)
	}
}

func TestExcerptRefsRefuseTwoLengths(t *testing.T) {
	ex := &evidence.Excerpt{Method: evidence.ExcerptEPUBText, SHA256: strings.Repeat("a", 64), Chars: 5}
	other := *ex
	other.Chars = 6
	p := evidence.Package{Formats: []evidence.FormatEvidence{{Format: "EPUB", Excerpt: ex}, {Format: "KEPUB", Excerpt: &other}}}
	if err := checkExcerptBodies(p, map[string]string{ex.SHA256: "12345"}); !errors.Is(err, errInvalidExcerpts) {
		t.Fatalf("checkExcerptBodies = %v", err)
	}
}

func TestExcerptReadChecksTheHash(t *testing.T) {
	ctx := context.Background()
	r, s := runRepo(t)
	rep := NewReportRepo(s)
	ref := refs(1)[0]
	run, err := r.CreateRun(ctx, verify.Scope{}, "library", "v1", []library.BookRef{ref})
	if err != nil {
		t.Fatal(err)
	}
	out, exs := excerptOutcome(t, ref, "Synthetic copyright page")
	if err := r.FinishBook(ctx, run.ID, ref, out); err != nil {
		t.Fatal(err)
	}
	hash := exs[0].SHA256
	if _, err := rep.Excerpt(ctx, evidence.ExcerptHash("never stored")); !errors.Is(err, ErrExcerptMissing) {
		t.Errorf("missing excerpt = %v", err)
	}
	if _, err := rep.Excerpt(ctx, "not a hash"); !errors.Is(err, ErrExcerptMissing) {
		t.Errorf("malformed hash = %v", err)
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE excerpts SET body='Tampered text of the same kind' WHERE sha256=$1`, hash); err != nil {
		t.Fatal(err)
	}
	body, err := rep.Excerpt(ctx, hash)
	if !errors.Is(err, ErrExcerptCorrupt) || body != "" {
		t.Fatalf("tampered excerpt = %q, %v", body, err)
	}
	if strings.Contains(err.Error(), "Tampered") || strings.Contains(err.Error(), "Synthetic") {
		t.Errorf("the error carries book text: %v", err)
	}
	// Same text, different length than the row says.
	if _, err := s.Pool.Exec(ctx, `UPDATE excerpts SET body='Synthetic copyright page', chars=3 WHERE sha256=$1`, hash); err != nil {
		t.Fatal(err)
	}
	if _, err := rep.Excerpt(ctx, hash); !errors.Is(err, ErrExcerptCorrupt) {
		t.Errorf("wrong chars = %v", err)
	}
}

func TestCheckExcerpts(t *testing.T) {
	ctx := context.Background()
	r, s := runRepo(t)
	rep := NewReportRepo(s)
	books := refs(3)
	run, err := r.CreateRun(ctx, verify.Scope{}, "library", "v1", books)
	if err != nil {
		t.Fatal(err)
	}
	var exs [][]*evidence.Excerpt
	for i, text := range []string{"Synthetic page one", "Synthetic page two", "Synthetic page three"} {
		out, e := excerptOutcome(t, books[i], text)
		exs = append(exs, e)
		if err := r.FinishBook(ctx, run.ID, books[i], out); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.SetStatus(ctx, run.ID, verify.RunCompleted, ""); err != nil {
		t.Fatal(err)
	}
	c, err := rep.CheckExcerpts(ctx, run.ID)
	if err != nil || c != (ExcerptCheck{Referenced: 3}) {
		t.Fatalf("clean run: %+v, %v", c, err)
	}
	if _, err := s.Pool.Exec(ctx, `DELETE FROM excerpts WHERE sha256=$1`, exs[0][0].SHA256); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE excerpts SET body='tampered' WHERE sha256=$1`, exs[1][0].SHA256); err != nil {
		t.Fatal(err)
	}
	c, err = rep.CheckExcerpts(ctx, run.ID)
	if err != nil || c != (ExcerptCheck{Referenced: 3, Missing: 1, Corrupt: 1}) {
		t.Fatalf("damaged run: %+v, %v", c, err)
	}
}

func TestSampleExcerpts(t *testing.T) {
	ctx := context.Background()
	r, s := runRepo(t)
	rep := NewReportRepo(s)
	books := refs(3)
	run, err := r.CreateRun(ctx, verify.Scope{}, "library", "v1", books)
	if err != nil {
		t.Fatal(err)
	}
	texts := map[string]bool{}
	for i, text := range []string{"Synthetic page one", "Synthetic page two", "Synthetic page three"} {
		out, _ := excerptOutcome(t, books[i], text)
		texts[text] = true
		if err := r.FinishBook(ctx, run.ID, books[i], out); err != nil {
			t.Fatal(err)
		}
	}
	got, err := rep.SampleExcerpts(ctx, run.ID, 2, "seed-a")
	if err != nil || len(got) != 2 {
		t.Fatalf("sample of 2: %d, %v", len(got), err)
	}
	for _, e := range got {
		if !strings.Contains(e.Text, "Synthetic page") || e.Method != evidence.ExcerptEPUBText || len(e.Units) == 0 || e.BookID == 0 {
			t.Errorf("excerpt = %+v", e)
		}
	}
	again, _ := rep.SampleExcerpts(ctx, run.ID, 2, "seed-a")
	if again[0].BookID != got[0].BookID || again[1].BookID != got[1].BookID {
		t.Error("the same seed must give the same sample")
	}
	all, err := rep.SampleExcerpts(ctx, run.ID, 10, "seed-a")
	if err != nil || len(all) != 3 {
		t.Fatalf("sample past the end: %d, %v", len(all), err)
	}
	if _, err := rep.SampleExcerpts(ctx, run.ID+999, 1, "x"); err == nil {
		t.Error("an unknown run must fail")
	}
	// Units come only from a package whose seal holds.
	if _, err := s.Pool.Exec(ctx, `UPDATE evidence SET package = jsonb_set(package, '{formats,0,excerpt,units}', '[]') WHERE run_id=$1`, run.ID); err != nil {
		t.Fatal(err)
	}
	if tampered, err := rep.SampleExcerpts(ctx, run.ID, 10, "seed-a"); err != nil || len(tampered) != 0 {
		t.Fatalf("sample of tampered packages: %+v, %v", tampered, err)
	}
}
