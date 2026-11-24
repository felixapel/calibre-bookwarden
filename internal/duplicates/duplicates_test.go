package duplicates_test

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/bits"
	"slices"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/duplicates"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/findings"
)

const (
	isbnA = "9780000000002"
	isbnB = "9780000000019"
)

var (
	shaA = strings.Repeat("a", 64)
	shaB = strings.Repeat("b", 64)
)

func epub(sha string, isbns ...string) evidence.FormatEvidence {
	return evidence.FormatEvidence{Format: "EPUB", Size: 10, SHA256: sha, State: evidence.StateOK, Detected: "epub",
		Metadata: &evidence.FileMetadata{ISBNs: isbns, Limits: &evidence.MetadataLimits{Version: 1}}}
}

func titleAuthor(title string, authors ...string) string {
	sum := sha256.Sum256([]byte(findings.TitleKey(title).Value + "\x1f" + findings.AuthorsKey(authors).Value))
	return hex.EncodeToString(sum[:])
}

func TestKeys(t *testing.T) {
	hinted := epub("")
	hinted.Format, hinted.Detected = "PDF", "pdf"
	hinted.ISBNHints = &evidence.ISBNHints{ISBNs: []string{isbnB}}
	limited := epub("", isbnB)
	limited.Metadata.Limits.ISBNs = true
	empty := epub(shaB)
	empty.Size = 0
	azw3, mobi := epub(shaB), epub(shaB)
	azw3.Format, azw3.Detected, mobi.Format, mobi.Detected = "AZW3", "mobi", "MOBI", "mobi"
	pdf := epub(shaB)
	pdf.Format, pdf.Detected = "PDF", "pdf"

	for _, tt := range []struct {
		name    string
		title   string
		authors []string
		ids     map[string]string
		formats []evidence.FormatEvidence
		want    []duplicates.Key
	}{
		{name: "isbn from Calibre and the file", ids: map[string]string{"ISBN": "978-0-00-000000-2"}, formats: []evidence.FormatEvidence{epub("", isbnA)},
			want: []duplicates.Key{{Kind: duplicates.KindISBN, Value: isbnA, Detail: "Calibre, EPUB"}}},
		{name: "invalid checksum", ids: map[string]string{"isbn": "9780000000003"}},
		{name: "hints never count", formats: []evidence.FormatEvidence{hinted}},
		{name: "capture-limited isbns", formats: []evidence.FormatEvidence{limited}},
		{name: "two files", formats: []evidence.FormatEvidence{epub(shaA), pdf},
			want: []duplicates.Key{{Kind: duplicates.KindFile, Value: shaA, Detail: "EPUB"}, {Kind: duplicates.KindFile, Value: shaB, Detail: "PDF"}}},
		{name: "empty or unread files", formats: []evidence.FormatEvidence{empty, epub("")}},
		{name: "one file in two formats", formats: []evidence.FormatEvidence{azw3, mobi},
			want: []duplicates.Key{{Kind: duplicates.KindFile, Value: shaB, Detail: "AZW3, MOBI"}}},
		{name: "title and authors", title: "The  Clockwork Garden", authors: []string{"Ada Example"},
			want: []duplicates.Key{{Kind: duplicates.KindTitleAuthor, Value: titleAuthor("The  Clockwork Garden", "Ada Example")}}},
		{name: "no authors", title: "The Clockwork Garden"},
		{name: "no title", authors: []string{"Ada Example"}},
		{name: "sorted", title: "T", authors: []string{"A"}, ids: map[string]string{"isbn": isbnB}, formats: []evidence.FormatEvidence{epub(shaA, isbnA, isbnB)},
			want: []duplicates.Key{
				{Kind: duplicates.KindFile, Value: shaA, Detail: "EPUB"},
				{Kind: duplicates.KindISBN, Value: isbnA, Detail: "EPUB"},
				{Kind: duplicates.KindISBN, Value: isbnB, Detail: "Calibre, EPUB"},
				{Kind: duplicates.KindTitleAuthor, Value: titleAuthor("T", "A")},
			}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := evidence.Package{Calibre: evidence.Record{Title: tt.title, Authors: tt.authors, Identifiers: tt.ids}, Formats: tt.formats}
			if got := duplicates.Keys(p); !slices.Equal(got, tt.want) {
				t.Errorf("Keys\n got %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func cover(h uint64) *evidence.CoverEvidence {
	return &evidence.CoverEvidence{State: evidence.StateOK, Width: 600, Height: 900, DHash: fmt.Sprintf("%016x", h), Flatness: 400}
}

func TestCoverHashComparable(t *testing.T) {
	flat := cover(0xff)
	flat.Flatness = 29
	corrupt := &evidence.CoverEvidence{State: evidence.StateCorrupt, Flatness: -1}
	bad := cover(0xff)
	bad.DHash = "zz"
	for _, tt := range []struct {
		name           string
		c              *evidence.CoverEvidence
		comparable, ok bool
	}{
		{"0 bits", cover(0), false, true},
		{"7 bits", cover(0x7f), false, true},
		{"8 bits", cover(0xff), true, true},
		{"56 bits", cover(1<<56 - 1), true, true},
		{"57 bits", cover(1<<57 - 1), false, true},
		{"placeholder", flat, false, true},
		{"corrupt", corrupt, false, false},
		{"bad hash", bad, false, false},
		{"none", nil, false, false},
	} {
		h, compared, ok := duplicates.CoverHash(tt.c)
		if compared != tt.comparable || ok != tt.ok {
			t.Errorf("%s: comparable %v ok %v, want %v %v", tt.name, compared, ok, tt.comparable, tt.ok)
		}
		if tt.ok && fmt.Sprintf("%016x", h) != tt.c.DHash {
			t.Errorf("%s: hash %016x", tt.name, h)
		}
	}
}

func members(ids ...int) []duplicates.Member {
	out := make([]duplicates.Member, 0, len(ids))
	for _, id := range ids {
		out = append(out, duplicates.Member{BookID: id})
	}
	return out
}

func TestGroups(t *testing.T) {
	const h = 0x0f0f_0f0f_0f0f_0f0f
	isbn := func(book int, v string) duplicates.KeyRow {
		return duplicates.KeyRow{BookID: book, Key: duplicates.Key{Kind: duplicates.KindISBN, Value: v}}
	}
	rows := []duplicates.KeyRow{
		{BookID: 4, Key: duplicates.Key{Kind: duplicates.KindTitleAuthor, Value: shaA}},
		isbn(9, isbnA), isbn(1, isbnA), isbn(3, isbnB),
		{BookID: 2, Key: duplicates.Key{Kind: duplicates.KindFile, Value: shaB, Detail: "EPUB"}},
		{BookID: 5, Key: duplicates.Key{Kind: duplicates.KindFile, Value: shaB, Detail: "PDF"}},
		{BookID: 6, Key: duplicates.Key{Kind: duplicates.KindTitleAuthor, Value: shaA}},
		isbn(7, "9780000000026"), isbn(7, "9780000000026"), // one book twice is no group
	}
	covers := []duplicates.Cover{{BookID: 9, Hash: h}, {BookID: 10, Hash: h ^ 0xf}, {BookID: 11, Hash: h ^ (0x1f << 20)},
		// a chain at distance 4 + 4
		{BookID: 20, Hash: ^uint64(h)}, {BookID: 21, Hash: ^uint64(h) ^ 0xf}, {BookID: 22, Hash: ^uint64(h) ^ 0xff}}
	want := []duplicates.Group{
		{Kind: duplicates.KindISBN, Value: isbnA, Members: members(1, 9)},
		{Kind: duplicates.KindFile, Value: shaB, Members: []duplicates.Member{{BookID: 2, Detail: "EPUB"}, {BookID: 5, Detail: "PDF"}}},
		{Kind: duplicates.KindTitleAuthor, Value: shaA, Members: members(4, 6)},
		{Kind: duplicates.KindCover, Members: members(9, 10)},
		{Kind: duplicates.KindCover, Members: members(20, 21, 22)},
	}
	if got := duplicates.Groups(rows, covers); !equalGroups(got, want) {
		t.Errorf("Groups\n got %+v\nwant %+v", got, want)
	}
}

func equalGroups(a, b []duplicates.Group) bool {
	return slices.EqualFunc(a, b, func(x, y duplicates.Group) bool {
		return x.Kind == y.Kind && x.Value == y.Value && slices.Equal(x.Members, y.Members)
	})
}

// bruteCoverGroups is the O(n²) reference for FuzzCoverGroups.
func bruteCoverGroups(covers []duplicates.Cover) [][]int {
	parent := make([]int, len(covers))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		if parent[i] != i {
			parent[i] = find(parent[i])
		}
		return parent[i]
	}
	for i := range covers {
		for j := i + 1; j < len(covers); j++ {
			if bits.OnesCount64(covers[i].Hash^covers[j].Hash) <= duplicates.MaxCoverDistance {
				parent[find(i)] = find(j)
			}
		}
	}
	byRoot := map[int][]int{}
	for i, c := range covers {
		byRoot[find(i)] = append(byRoot[find(i)], c.BookID)
	}
	var out [][]int
	for _, g := range byRoot {
		if len(g) > 1 {
			slices.Sort(g)
			out = append(out, g)
		}
	}
	slices.SortFunc(out, func(a, b []int) int { return a[0] - b[0] })
	return out
}

func FuzzCoverGroups(f *testing.F) {
	f.Add(uint64(0x0f0f0f0f0f0f0f0f), []byte{0, 4, 5, 63, 12, 13})
	f.Add(uint64(0), []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12})
	f.Fuzz(func(t *testing.T, seed uint64, flips []byte) {
		if len(flips) > 256 {
			return
		}
		// Each byte either flips one bit of the previous hash (a near
		// neighbour) or, with its top bit set, starts afresh from the seed
		// rotated, so the set mixes close and distant hashes.
		var covers []duplicates.Cover
		h := seed
		for i, b := range flips {
			if b&0x80 != 0 {
				h = bits.RotateLeft64(seed, int(b))
			}
			h ^= 1 << (b & 63)
			covers = append(covers, duplicates.Cover{BookID: i + 1, Hash: h})
		}
		var got [][]int
		for _, g := range duplicates.Groups(nil, covers) {
			ids := make([]int, 0, len(g.Members))
			for _, m := range g.Members {
				ids = append(ids, m.BookID)
			}
			got = append(got, ids)
		}
		if want := bruteCoverGroups(covers); !slices.EqualFunc(got, want, slices.Equal) {
			t.Fatalf("Groups %v, brute force %v", got, want)
		}
	})
}

// BenchmarkGroups groups 50 000 covers, 30 % of them sharing their lowest
// band (a flat strip of the cover), as the Duplicates page does on a large
// library.
func BenchmarkGroups(b *testing.B) {
	covers := make([]duplicates.Cover, 50000)
	state := uint64(0x9e3779b97f4a7c15)
	for i := range covers {
		state ^= state << 13
		state ^= state >> 7
		state ^= state << 17
		h := state
		if i%10 < 3 {
			h &^= 1<<13 - 1
		}
		covers[i] = duplicates.Cover{BookID: i + 1, Hash: h}
	}
	for b.Loop() {
		duplicates.Groups(nil, covers)
	}
}
