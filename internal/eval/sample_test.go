package eval

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/store"
)

// library builds n candidates spread over formats, languages, tiers and fixes
// in uneven shares.
func library(n int) []store.RecallCandidate {
	formats := [][]string{{"EPUB"}, {"EPUB", "PDF"}, {"PDF"}, {"MOBI"}, {}}
	langs := []string{"eng", "eng", "eng", "deu", "deu", "spa", "fra", "ita", "por", "pol", ""}
	tiers := []string{"A", "A", "B", "C"}
	out := make([]store.RecallCandidate, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, store.RecallCandidate{
			BookID: i, UUID: fmt.Sprintf("uuid-%d", i), Tier: tiers[i*7%len(tiers)], HasFix: i%3 == 0,
			Language: langs[i*3%len(langs)], Formats: formats[i%len(formats)],
		})
	}
	return out
}

func ids(d Draw) []int {
	out := make([]int, len(d.Books))
	for i, b := range d.Books {
		out[i] = b.BookID
	}
	return out
}

func TestDrawSampleIsDeterministic(t *testing.T) {
	cands := library(1500)
	a := DrawSample(cands, 42, SampleSize)
	b := DrawSample(cands, 42, SampleSize)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("the same seed and library gave different samples")
	}
	// The order the rows were read in is not part of the draw.
	shuffled := slices.Clone(cands)
	rand.New(rand.NewPCG(1, 2)).Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
	if c := DrawSample(shuffled, 42, SampleSize); !reflect.DeepEqual(a, c) {
		t.Error("the candidates' order changed the sample")
	}
	if other := DrawSample(cands, 43, SampleSize); reflect.DeepEqual(ids(a), ids(other)) {
		t.Error("another seed gave the same books")
	}
	if len(a.Books) != SampleSize {
		t.Errorf("%d books, want %d", len(a.Books), SampleSize)
	}
	seen := map[int]bool{}
	for i, b := range a.Books {
		if seen[b.BookID] || b.Position != i+1 {
			t.Fatalf("book %d at position %d: duplicate or wrong position", b.BookID, b.Position)
		}
		seen[b.BookID] = true
	}
}

func TestDrawSampleAllocation(t *testing.T) {
	cands := library(1500)
	d := DrawSample(cands, 7, SampleSize)
	sampled, lib := 0, 0
	perStratum := map[string]int{}
	for _, b := range d.Books {
		perStratum[b.Stratum]++
	}
	for k, sc := range d.Strata {
		sampled += sc.Sampled
		lib += sc.Library
		if perStratum[k] != sc.Sampled {
			t.Errorf("stratum %s: %d books drawn, %d allocated", k, perStratum[k], sc.Sampled)
		}
		// Largest remainder: within one of the exact quota.
		quota := float64(SampleSize) * float64(sc.Library) / float64(len(cands))
		if diff := float64(sc.Sampled) - quota; diff <= -1 || diff >= 1 {
			t.Errorf("stratum %s: %d sampled, quota %.2f", k, sc.Sampled, quota)
		}
	}
	if sampled != SampleSize || lib != len(cands) {
		t.Errorf("sampled %d of %d, library %d of %d", sampled, SampleSize, lib, len(cands))
	}
	// The strata are the four dimensions, with at most the top languages
	// bucketed on their own.
	langs := map[string]bool{}
	for k := range d.Strata {
		if strings.HasPrefix(k, "pooled|") {
			continue
		}
		var f, l, tier, fix string
		if n, _ := fmt.Sscanf(replaceBars(k), "%s %s %s %s", &f, &l, &tier, &fix); n != 4 {
			t.Fatalf("stratum %q", k)
		}
		langs[l] = true
		if !slices.Contains([]string{"EPUB", "PDF", "other"}, f) || !slices.Contains([]string{"A", "B", "C"}, tier) || !slices.Contains([]string{"fix", "nofix"}, fix) {
			t.Errorf("stratum %q", k)
		}
	}
	if len(langs) > topLanguages+1 {
		t.Errorf("%d language buckets, want at most %d", len(langs), topLanguages+1)
	}
}

func replaceBars(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] == '|' {
			b[i] = ' '
		}
	}
	return string(b)
}

func TestDrawSampleSmallLibraryGivesEveryone(t *testing.T) {
	for _, n := range []int{1, 57, SampleSize} {
		d := DrawSample(library(n), 9, SampleSize)
		if len(d.Books) != n {
			t.Fatalf("library of %d gave %d books", n, len(d.Books))
		}
		got := ids(d)
		slices.Sort(got)
		for i, id := range got {
			if id != i+1 {
				t.Fatalf("library of %d: book %d missing", n, i+1)
			}
		}
	}
}

func TestAllocate(t *testing.T) {
	tests := []struct {
		name  string
		sizes map[string]int
		size  int
		want  map[string]int
	}{
		{"exact", map[string]int{"a": 50, "b": 50}, 10, map[string]int{"a": 5, "b": 5}},
		{"largest remainders win", map[string]int{"a": 5, "b": 3, "c": 2}, 4, map[string]int{"a": 2, "b": 1, "c": 1}},
		{"ties go to the first key", map[string]int{"a": 1, "b": 1, "c": 1}, 2, map[string]int{"a": 1, "b": 1, "c": 0}},
		{"tiny strata may get nothing", map[string]int{"a": 990, "b": 10}, 5, map[string]int{"a": 5, "b": 0}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := allocate(tt.sizes, tt.size)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("allocate = %v, want %v", got, tt.want)
			}
			sum := 0
			for _, v := range got {
				sum += v
			}
			if sum != tt.size {
				t.Errorf("sum %d, want %d", sum, tt.size)
			}
		})
	}
}

func TestLanguageAndFormatBuckets(t *testing.T) {
	var cands []store.RecallCandidate
	for i, l := range []string{"eng", "eng", "eng", "deu", "deu", "spa", "fra", "ita", "por", "pol", ""} {
		cands = append(cands, store.RecallCandidate{BookID: i, Language: l})
	}
	b := languageBuckets(cands)
	want := map[string]string{"eng": "eng", "deu": "deu", "fra": "fra", "ita": "ita", "pol": "pol", "por": "other", "spa": "other"}
	if !reflect.DeepEqual(b, want) {
		t.Errorf("buckets %v, want %v", b, want)
	}
	for _, tt := range []struct {
		formats []string
		want    string
	}{{[]string{"pdf", "EPUB"}, "EPUB"}, {[]string{"PDF", "MOBI"}, "PDF"}, {[]string{"MOBI"}, "other"}, {nil, "other"}} {
		if got := formatBucket(tt.formats); got != tt.want {
			t.Errorf("formatBucket(%v) = %s, want %s", tt.formats, got, tt.want)
		}
	}
}

// tailLibrary is 1200 books in a few big strata and a long tail of 60 tiny
// ones (a language and tier each), none worth a book of the 200.
func tailLibrary() []store.RecallCandidate {
	var out []store.RecallCandidate
	add := func(n int, lang, tier string, fix bool, formats ...string) {
		for range n {
			id := len(out) + 1
			out = append(out, store.RecallCandidate{BookID: id, UUID: fmt.Sprintf("t-%d", id), Tier: tier, HasFix: fix, Language: lang, Formats: formats})
		}
	}
	add(700, "eng", "A", false, "EPUB")
	add(300, "eng", "B", true, "EPUB")
	add(100, "deu", "A", false, "PDF")
	// One book each in 60 other strata: a rare language, format or tier.
	for i := range 60 {
		add(1, []string{"fra", "ita", "spa", "pol", "por", "nld"}[i%6], []string{"A", "B", "C"}[i%3], i%2 == 0, []string{"EPUB", "PDF", "MOBI", "AZW3"}[i%4])
	}
	return out
}

func TestDrawSamplePoolsTheLongTail(t *testing.T) {
	cands := tailLibrary()
	d := DrawSample(cands, 3, SampleSize)
	if len(d.Books) != SampleSize {
		t.Fatalf("%d books", len(d.Books))
	}
	pooled, lib, sampled := 0, 0, 0
	for k, sc := range d.Strata {
		lib += sc.Library
		sampled += sc.Sampled
		if strings.HasPrefix(k, "pooled|") {
			pooled += sc.Library
		}
		if sc.Sampled == 0 {
			t.Errorf("stratum %s has %d library books and none sampled", k, sc.Library)
		}
	}
	if lib != len(cands) || sampled != SampleSize || pooled == 0 {
		t.Errorf("library %d of %d, sampled %d, pooled %d", lib, len(cands), sampled, pooled)
	}
	// The weights stay exact: through the sample they give the library back.
	res, err := Recall(RecallInput{Sample: store.RecallSample{Size: SampleSize, Strata: d.Strata}, Books: d.Books})
	if err != nil || res.Unrepresented != 0 || res.Library != len(cands) {
		t.Fatalf("recall of the draw: %+v, %v; want nothing unrepresented", res, err)
	}
}

func TestPool(t *testing.T) {
	mk := func(n int, fix bool) []store.RecallCandidate {
		out := make([]store.RecallCandidate, n)
		for i := range out {
			out[i].HasFix = fix
		}
		return out
	}
	// total 1000, size 200: a stratum under 5 books is small.
	in := map[string][]store.RecallCandidate{"big": mk(900, false), "a": mk(4, true), "b": mk(3, true), "c": mk(2, false), "d": mk(5, false), "x": mk(86, false)}
	got := pool(in, 1000, 200)
	sizes := map[string]int{}
	for k, m := range got {
		sizes[k] = len(m)
	}
	// The fix pool has 7 books and stays; the other has 2, under 5, and
	// joins the "any" pool.
	want := map[string]int{"big": 900, "pooled|fix": 7, "pooled|any": 2, "d": 5, "x": 86}
	if !reflect.DeepEqual(sizes, want) {
		t.Errorf("pooled sizes %v, want %v", sizes, want)
	}
}

func TestDrawSampleGivesTheLoneFixAPlace(t *testing.T) {
	var cands []store.RecallCandidate
	for i := 1; i <= 999; i++ {
		cands = append(cands, store.RecallCandidate{BookID: i, UUID: fmt.Sprintf("b-%d", i), Tier: "A", Language: "eng", Formats: []string{"EPUB"}})
	}
	cands = append(cands, store.RecallCandidate{BookID: 1000, UUID: "b-1000", Tier: "C", HasFix: true, Language: "fra", Formats: []string{"PDF"}})
	d := DrawSample(cands, 1, SampleSize)
	res, err := Recall(RecallInput{Sample: store.RecallSample{Size: SampleSize, Strata: d.Strata}, Books: d.Books})
	if err != nil || res.Unrepresented != 0 || len(d.Books) != SampleSize {
		t.Fatalf("unrepresented %d, %d books, %v; strata %v", res.Unrepresented, len(d.Books), err, d.Strata)
	}
	if !slices.ContainsFunc(d.Books, func(b store.SampleBook) bool { return b.BookID == 1000 }) {
		t.Error("the only book with a fix is not sampled")
	}
}
