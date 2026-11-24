package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/findings"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

func rawOf(body, key string) verify.RawOutput {
	sum := sha256.Sum256([]byte(body))
	return verify.RawOutput{Key: key, SHA256: hex.EncodeToString(sum[:]), Body: body}
}

func cacheKey(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:])
}

// extractionOutcome seals a package whose one EPUB format carries an
// extraction with one ok extractor per raw, and returns the outcome with the
// raws attached.
func extractionOutcome(t *testing.T, ref library.BookRef, raws ...verify.RawOutput) verify.Outcome {
	t.Helper()
	out, _ := excerptOutcomeWith(t, ref, func(p *evidence.Package) {
		x := &evidence.Extraction{PromptVersion: "extract-2", ExtractVersion: 2, ExcerptSHA256: p.Formats[0].Excerpt.SHA256}
		for i, r := range raws {
			x.Extractors = append(x.Extractors, evidence.ExtractorRun{
				Name: "ex" + string(rune('a'+i)), Family: "fam", Model: "m", State: evidence.ExtractOK, RawSHA256: r.SHA256,
			})
		}
		p.Formats[0].Extraction = x
	}, "extraction excerpt text")
	out.Raws = raws
	return out
}

func rawCount(t *testing.T, s *Store, table string) int {
	t.Helper()
	var n int
	if err := s.Pool.QueryRow(context.Background(), `SELECT count(*) FROM `+table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestFinishBookStoresRaws(t *testing.T) {
	ctx := context.Background()
	ref := refs(1)[0]
	a, b := rawOf(`{"claims":["a"]}`, cacheKey("a")), rawOf(`{"claims":["b"]}`, "")

	t.Run("stores rows, links and cache", func(t *testing.T) {
		r, s := runRepo(t)
		finishedRun(t, r, verify.RunCompleted, extractionOutcome(t, ref, a, b))
		if n := rawCount(t, s, "extraction_raw"); n != 2 {
			t.Errorf("%d raws, want 2", n)
		}
		if n := rawCount(t, s, "evidence_extraction_raw"); n != 2 {
			t.Errorf("%d links, want 2", n)
		}
		if n := rawCount(t, s, "extraction_cache"); n != 1 {
			t.Errorf("%d cache rows, want 1 (only the keyed raw)", n)
		}
	})

	refuse := func(name string, build func(t *testing.T) verify.Outcome) {
		t.Run(name, func(t *testing.T) {
			r, s := runRepo(t)
			run, err := r.CreateRun(ctx, verify.Scope{}, "library", "v1", []library.BookRef{ref})
			if err != nil {
				t.Fatal(err)
			}
			err = r.FinishBook(ctx, run.ID, ref, build(t))
			if err == nil || !errors.Is(err, findings.ErrInvalidEvidence) {
				t.Fatalf("FinishBook = %v, want ErrInvalidEvidence", err)
			}
			for _, tbl := range []string{"extraction_raw", "evidence_extraction_raw", "extraction_cache", "evidence", "excerpts"} {
				if n := rawCount(t, s, tbl); n != 0 {
					t.Errorf("%s has %d rows after a refused book", tbl, n)
				}
			}
		})
	}
	refuse("body does not match its hash", func(t *testing.T) verify.Outcome {
		bad := a
		bad.Body = "something else"
		return extractionOutcome(t, ref, bad, b)
	})
	refuse("unreferenced raw", func(t *testing.T) verify.Outcome {
		out := extractionOutcome(t, ref, a)
		out.Raws = append(out.Raws, b)
		return out
	})
	refuse("referenced hash without a raw", func(t *testing.T) verify.Outcome {
		out := extractionOutcome(t, ref, a, b)
		out.Raws = out.Raws[:1]
		return out
	})
	refuse("raws without any extraction", func(t *testing.T) verify.Outcome {
		out, _ := excerptOutcome(t, ref, "plain")
		out.Raws = []verify.RawOutput{a}
		return out
	})
}

func TestFinishBookWithoutExtractionIsUnchanged(t *testing.T) {
	r, s := runRepo(t)
	out, _ := excerptOutcome(t, refs(1)[0], "plain")
	finishedRun(t, r, verify.RunCompleted, out)
	for _, tbl := range []string{"extraction_raw", "evidence_extraction_raw", "extraction_cache"} {
		if n := rawCount(t, s, tbl); n != 0 {
			t.Errorf("%s has %d rows", tbl, n)
		}
	}
}

func TestCachedRaw(t *testing.T) {
	ctx := context.Background()
	r, s := runRepo(t)
	a := rawOf(`{"claims":["a"]}`, cacheKey("a"))
	finishedRun(t, r, verify.RunCompleted, extractionOutcome(t, refs(1)[0], a, rawOf("second", cacheKey("b"))))

	sha, body, ok, err := r.CachedRaw(ctx, a.Key)
	if err != nil || !ok || sha != a.SHA256 || body != a.Body {
		t.Fatalf("hit = %q, %q, %v, %v", sha, body, ok, err)
	}
	if _, _, ok, err := r.CachedRaw(ctx, cacheKey("never")); err != nil || ok {
		t.Errorf("miss = %v, %v", ok, err)
	}
	for _, key := range []string{"", "nope", strings.ToUpper(a.Key)} {
		if _, _, ok, err := r.CachedRaw(ctx, key); err != nil || ok {
			t.Errorf("invalid key %q = %v, %v", key, ok, err)
		}
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE extraction_raw SET body='altered' WHERE sha256=$1`, a.SHA256); err != nil {
		t.Fatal(err)
	}
	if _, body, ok, err := r.CachedRaw(ctx, a.Key); err != nil || ok || body != "" {
		t.Errorf("altered body = %q, %v, %v; want a miss", body, ok, err)
	}
}

func TestRawReadChecksHash(t *testing.T) {
	ctx := context.Background()
	r, s := runRepo(t)
	rep := NewReportRepo(s)
	a := rawOf("the raw answer", "")
	finishedRun(t, r, verify.RunCompleted, extractionOutcome(t, refs(1)[0], a))

	if body, err := rep.Raw(ctx, a.SHA256); err != nil || body != a.Body {
		t.Fatalf("Raw = %q, %v", body, err)
	}
	if _, err := rep.Raw(ctx, cacheKey("never stored")); !errors.Is(err, ErrRawMissing) {
		t.Errorf("missing = %v", err)
	}
	if _, err := rep.Raw(ctx, "not a hash"); !errors.Is(err, ErrRawMissing) {
		t.Errorf("malformed = %v", err)
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE extraction_raw SET body='tampered answer' WHERE sha256=$1`, a.SHA256); err != nil {
		t.Fatal(err)
	}
	body, err := rep.Raw(ctx, a.SHA256)
	if !errors.Is(err, ErrRawCorrupt) || body != "" {
		t.Fatalf("tampered = %q, %v", body, err)
	}
	if strings.Contains(err.Error(), "tampered") || strings.Contains(err.Error(), "raw answer") {
		t.Errorf("the error carries the body: %v", err)
	}
}

func TestPruneSweepsRaws(t *testing.T) {
	ctx := context.Background()
	r, s := runRepo(t)
	book := refs(1)[0]
	gone := rawOf("only the oldest run", cacheKey("gone"))
	kept := rawOf("shared with a kept run", cacheKey("kept"))
	finishedRun(t, r, verify.RunCompleted, extractionOutcome(t, book, gone, kept))
	finishedRun(t, r, verify.RunCompleted, extractionOutcome(t, book, kept))
	finishedRun(t, r, verify.RunCompleted, sealed(t, book.ID, book.UUID))

	res, err := r.Prune(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if res.Runs != 1 || res.Raws != 1 {
		t.Errorf("Prune() = %+v; want 1 run and 1 raw", res)
	}
	if n := rawCount(t, s, "extraction_raw"); n != 1 {
		t.Errorf("%d raws left, want 1", n)
	}
	if n := rawCount(t, s, "extraction_cache"); n != 1 {
		t.Errorf("%d cache rows left, want 1 (the swept raw's goes by cascade)", n)
	}
	if _, _, ok, _ := r.CachedRaw(ctx, kept.Key); !ok {
		t.Error("the kept raw's cache row is gone")
	}
	if _, _, ok, _ := r.CachedRaw(ctx, gone.Key); ok {
		t.Error("the swept raw's cache row survived")
	}
}

func TestExtractionsVisitsWhatReverifyNeeds(t *testing.T) {
	ctx := context.Background()
	ref := refs(1)[0]
	a := rawOf(`{"claims":[]}`, cacheKey("v"))
	r, s := runRepo(t)
	run := finishedRun(t, r, verify.RunCompleted, extractionOutcome(t, ref, a))
	rep := NewReportRepo(s)
	var got []ExtractionRef
	visit := func(x ExtractionRef) error { got = append(got, x); return nil }
	if err := rep.Extractions(ctx, run, visit); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].BookID != ref.ID || got[0].Body != "extraction excerpt text" || got[0].Raws[a.SHA256] != a.Body ||
		got[0].Missing || got[0].Extraction.ExcerptSHA256 != got[0].Excerpt.SHA256 {
		t.Fatalf("visited %+v", got)
	}
	// An altered answer is reported, not handed over.
	if _, err := s.Pool.Exec(ctx, `UPDATE extraction_raw SET body = 'altered' WHERE sha256 = $1`, a.SHA256); err != nil {
		t.Fatal(err)
	}
	got = nil
	if err := rep.Extractions(ctx, run, visit); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got[0].Missing || len(got[0].Raws) != 0 {
		t.Fatalf("after altering the raw: %+v", got)
	}
	if err := rep.Extractions(ctx, run+999, visit); err == nil {
		t.Error("an unknown run must fail")
	}
}

func TestMigration0020IsRepeatable(t *testing.T) {
	_, s := runRepo(t)
	raw, err := migrationFiles.ReadFile("migrations/0020_extraction.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(context.Background(), string(raw)); err != nil {
		t.Fatalf("a second run of 0020: %v", err)
	}
}
