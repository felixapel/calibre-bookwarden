package eval

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strconv"
	"strings"

	"github.com/felixapel/calibre-bookwarden/internal/store"
)

// SampleSize is the number of books in a recall sample (design section 6.4).
const SampleSize = 200

// topLanguages is how many languages keep a bucket of their own; every other
// language, and a book with none, falls in "other".
const topLanguages = 5

// Draw is a drawn sample: its books in the order they are asked in, and each
// stratum's size in the library and in the sample.
type Draw struct {
	Books  []store.SampleBook
	Strata map[string]store.StratumCount
}

// formatBucket is EPUB when any format is one, else PDF when any is, else
// "other".
func formatBucket(formats []string) string {
	has := func(want string) bool {
		return slices.ContainsFunc(formats, func(f string) bool { return strings.EqualFold(f, want) })
	}
	switch {
	case has("EPUB"):
		return "EPUB"
	case has("PDF"):
		return "PDF"
	}
	return "other"
}

// languageBuckets maps each candidate's first Calibre language to its bucket:
// the topLanguages most common codes (ties by code), else "other".
func languageBuckets(cands []store.RecallCandidate) map[string]string {
	counts := map[string]int{}
	for _, c := range cands {
		if l := strings.ToLower(c.Language); l != "" {
			counts[l]++
		}
	}
	codes := make([]string, 0, len(counts))
	for l := range counts {
		codes = append(codes, l)
	}
	slices.SortFunc(codes, func(a, b string) int {
		if counts[a] != counts[b] {
			return counts[b] - counts[a]
		}
		return strings.Compare(a, b)
	})
	out := map[string]string{}
	for i, l := range codes {
		if i < topLanguages {
			out[l] = l
		} else {
			out[l] = "other"
		}
	}
	return out
}

// StratumKey names a candidate's stratum: format, language bucket, tier and
// whether the verified projection has a fix for the book.
func stratumKey(c store.RecallCandidate, langs map[string]string) string {
	lang := "other"
	if b, ok := langs[strings.ToLower(c.Language)]; ok {
		lang = b
	}
	fix := "nofix"
	if c.HasFix {
		fix = "fix"
	}
	return formatBucket(c.Formats) + "|" + lang + "|" + c.Tier + "|" + fix
}

// rank orders books reproducibly: by the hash of the seed, a purpose and the
// book's uuid, so the draw depends on the seed and the library state and on
// nothing else, such as the order the rows were read in.
func rank(seed int64, purpose, uuid string) string {
	sum := sha256.Sum256([]byte(strconv.FormatInt(seed, 10) + "|" + purpose + "|" + uuid))
	return hex.EncodeToString(sum[:])
}

// allocate splits size among the strata in proportion to their library
// counts, by the largest-remainder method: each stratum gets the floor of its
// quota and the leftover places go to the largest remainders, ties by key.
// The shares sum to size, and no stratum gets more than it has.
func allocate(sizes map[string]int, size int) map[string]int {
	total := 0
	for _, n := range sizes {
		total += n
	}
	out := make(map[string]int, len(sizes))
	if total == 0 {
		return out
	}
	type rem struct {
		key string
		r   int
	}
	var rems []rem
	left := size
	for k, n := range sizes {
		out[k] = size * n / total
		left -= out[k]
		rems = append(rems, rem{k, size * n % total})
	}
	slices.SortFunc(rems, func(a, b rem) int {
		if a.r != b.r {
			return b.r - a.r
		}
		return strings.Compare(a.key, b.key)
	})
	for i := 0; left > 0 && i < len(rems); i++ {
		out[rems[i].key]++
		left--
	}
	return out
}

// pool merges the strata too small to earn a place of their own. A stratum
// whose exact quota is below one book (n*size < total) joins a pooled stratum
// of its fix state; a pooled stratum that is still that small joins the other
// pooled one. Every library book keeps exactly one stratum, so the weights
// (stratum size over sampled) stay exact.
func pool(byStratum map[string][]store.RecallCandidate, total, size int) map[string][]store.RecallCandidate {
	small := func(n int) bool { return n*size < total }
	out := map[string][]store.RecallCandidate{}
	for k, members := range byStratum {
		if small(len(members)) {
			k = "pooled|nofix"
			if members[0].HasFix {
				k = "pooled|fix"
			}
		}
		out[k] = append(out[k], members...)
	}
	for _, k := range []string{"pooled|fix", "pooled|nofix"} {
		if m, ok := out[k]; ok && small(len(m)) {
			delete(out, k)
			out["pooled|any"] = append(out["pooled|any"], m...)
		}
	}
	return out
}

// guarantee gives every stratum that rounding left without a place one, taken
// from the stratum with the largest surplus over its exact quota (among those
// keeping at least one). Weights stay Library/Sampled, so they stay exact.
func guarantee(want, sizes map[string]int, size int) map[string]int {
	total := 0
	for _, n := range sizes {
		total += n
	}
	keys := make([]string, 0, len(sizes))
	for k := range sizes {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if sizes[k] == 0 || want[k] > 0 {
			continue
		}
		donor, best := "", -1.0
		for _, d := range keys {
			if want[d] < 2 {
				continue
			}
			if surplus := float64(want[d]) - float64(size*sizes[d])/float64(total); surplus > best {
				donor, best = d, surplus
			}
		}
		if donor == "" {
			continue
		}
		want[donor]--
		want[k]++
	}
	return want
}

// DrawSample draws size books from the candidates: stratified by format,
// language, tier and fix, with each stratum's share of the sample equal to
// its share of the library (largest-remainder rounding), and seeded, so the
// same seed and library give the same books in the same order. Strata with a
// quota under one book are pooled first (see pool). A library of size books or
// fewer gives every book.
func DrawSample(cands []store.RecallCandidate, seed int64, size int) Draw {
	langs := languageBuckets(cands)
	byStratum := map[string][]store.RecallCandidate{}
	for _, c := range cands {
		k := stratumKey(c, langs)
		byStratum[k] = append(byStratum[k], c)
	}
	if len(cands) > size {
		byStratum = pool(byStratum, len(cands), size)
	}
	sizes := map[string]int{}
	for k, m := range byStratum {
		sizes[k] = len(m)
	}
	want := sizes
	if len(cands) > size {
		want = guarantee(allocate(sizes, size), sizes, size)
	}
	d := Draw{Strata: map[string]store.StratumCount{}}
	type pick struct {
		c       store.RecallCandidate
		stratum string
	}
	var picked []pick
	for k, members := range byStratum {
		slices.SortFunc(members, func(a, b store.RecallCandidate) int {
			if c := strings.Compare(rank(seed, "stratum", a.UUID), rank(seed, "stratum", b.UUID)); c != 0 {
				return c
			}
			return a.BookID - b.BookID
		})
		d.Strata[k] = store.StratumCount{Library: len(members), Sampled: want[k]}
		for _, c := range members[:want[k]] {
			picked = append(picked, pick{c, k})
		}
	}
	slices.SortFunc(picked, func(a, b pick) int {
		if c := strings.Compare(rank(seed, "order", a.c.UUID), rank(seed, "order", b.c.UUID)); c != 0 {
			return c
		}
		return a.c.BookID - b.c.BookID
	})
	for i, p := range picked {
		d.Books = append(d.Books, store.SampleBook{BookID: p.c.BookID, UUID: p.c.UUID, Stratum: p.stratum, Language: p.c.Language, Position: i + 1})
	}
	return d
}
