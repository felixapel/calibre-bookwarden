// Package duplicates finds books of one run that may be the same book
// (ADR-024): the same ISBN, the same file, the same title and authors, or a
// near-identical cover. They are hints for Felix; they never authorize a
// merge, a deletion or a write.
package duplicates

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"math/bits"
	"slices"
	"strconv"
	"strings"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/findings"
)

// Version 1's rules. Another kind, threshold or key format needs version 2.
const Version = 1

const (
	KindISBN        = "isbn"
	KindFile        = "file"
	KindTitleAuthor = "title_author"
	KindCover       = "cover" // a hint only
)

// Covers within MaxCoverDistance differing bits are near-identical. A hash
// with fewer than MinDetailBits or more than MaxDetailBits bits set comes from
// a near-blank page or a smooth gradient, and isn't compared.
const (
	MaxCoverDistance = 4
	MinDetailBits    = 8
	MaxDetailBits    = 56
)

// Kinds lists the kinds in display order.
func Kinds() []string { return []string{KindISBN, KindFile, KindTitleAuthor, KindCover} }

// Key is one exact duplicate key of a book. Detail says where it came from:
// "Calibre" and format names for an ISBN, format names for a file.
type Key struct{ Kind, Value, Detail string }

// KeyRow is a stored key of one book.
type KeyRow struct {
	BookID int
	Key
}

// Cover is a comparable cover of one book.
type Cover struct {
	BookID int
	Hash   uint64
}

type Member struct {
	BookID int
	Detail string
}

// Group is two or more books sharing a key, or with near-identical covers.
// Value is "" for a cover group; Members ascend by book id.
type Group struct {
	Kind, Value string
	Members     []Member
}

// Keys returns a verified package's exact keys, sorted by kind and value.
func Keys(p evidence.Package) []Key {
	type origins struct{ names []string }
	isbns, files := map[string]*origins{}, map[string]*origins{}
	add := func(m map[string]*origins, value, name string) {
		o := m[value]
		if o == nil {
			o = &origins{}
			m[value] = o
		}
		if !slices.Contains(o.names, name) {
			o.names = append(o.names, name)
		}
	}
	for k, v := range p.Calibre.Identifiers {
		if isbn, ok := findings.CanonicalISBN(v); ok && strings.EqualFold(k, "isbn") {
			add(isbns, isbn, "Calibre")
		}
	}
	for _, f := range p.Formats {
		if f.SHA256 != "" && f.Size > 0 {
			add(files, f.SHA256, f.Format)
		}
		// The package was verified, so the capture markers are well formed;
		// a format whose fields can't be read gives no ISBN.
		if ff, err := findings.Fields(f); err == nil {
			for _, isbn := range ff.ISBNs {
				add(isbns, isbn, f.Format)
			}
		}
	}
	var out []Key
	for v, o := range isbns {
		out = append(out, Key{Kind: KindISBN, Value: v, Detail: strings.Join(o.names, ", ")})
	}
	for v, o := range files {
		out = append(out, Key{Kind: KindFile, Value: v, Detail: strings.Join(o.names, ", ")})
	}
	title, authors := findings.TitleKey(p.Calibre.Title), findings.AuthorsKey(p.Calibre.Authors)
	if title.Valid && authors.Valid {
		sum := sha256.Sum256([]byte(title.Value + "\x1f" + authors.Value))
		out = append(out, Key{Kind: KindTitleAuthor, Value: hex.EncodeToString(sum[:])})
	}
	slices.SortFunc(out, func(a, b Key) int { return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Value, b.Value)) })
	return out
}

// CoverHash returns a decoded cover's difference hash, and whether it is
// compared for duplicates: not a version 4 placeholder, and with enough
// detail. ok is false when there is no decoded cover with a valid hash.
func CoverHash(c *evidence.CoverEvidence) (h uint64, compared, ok bool) {
	if c == nil || c.State != evidence.StateOK || len(c.DHash) != 16 {
		return 0, false, false
	}
	h, err := strconv.ParseUint(c.DHash, 16, 64)
	if err != nil {
		return 0, false, false
	}
	n := bits.OnesCount64(h)
	return h, n >= MinDetailBits && n <= MaxDetailBits && !findings.CoverPlaceholder(*c), true
}

// Groups builds the groups of a run from its stored keys and comparable
// covers, ordered by kind (Kinds), then smallest book id, then value.
func Groups(rows []KeyRow, covers []Cover) []Group {
	byKey := map[Key][]Member{}
	for _, r := range rows {
		k := Key{Kind: r.Kind, Value: r.Value}
		if !slices.ContainsFunc(byKey[k], func(m Member) bool { return m.BookID == r.BookID }) {
			byKey[k] = append(byKey[k], Member{BookID: r.BookID, Detail: r.Detail})
		}
	}
	var out []Group
	for k, ms := range byKey {
		if len(ms) > 1 {
			out = append(out, Group{Kind: k.Kind, Value: k.Value, Members: ms})
		}
	}
	out = append(out, coverGroups(covers)...)
	for _, g := range out {
		slices.SortFunc(g.Members, func(a, b Member) int { return cmp.Compare(a.BookID, b.BookID) })
	}
	rank := func(kind string) int { return slices.Index(Kinds(), kind) }
	slices.SortFunc(out, func(a, b Group) int {
		return cmp.Or(cmp.Compare(rank(a.Kind), rank(b.Kind)), cmp.Compare(a.Members[0].BookID, b.Members[0].BookID), cmp.Compare(a.Value, b.Value))
	})
	return out
}

// bands split a hash into five disjoint parts: two hashes within distance 4
// agree on at least one of them.
var bands = [5]struct{ shift, width uint }{{0, 13}, {13, 13}, {26, 13}, {39, 13}, {52, 12}}

// coverGroups links covers within MaxCoverDistance and returns the connected
// components of two or more books.
func coverGroups(covers []Cover) []Group {
	parent := make([]int, len(covers))
	for i := range parent {
		parent[i] = i
	}
	find := func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	for b, band := range bands {
		buckets := map[uint64][]int{}
		for i, c := range covers {
			v := c.Hash >> band.shift & (1<<band.width - 1)
			buckets[v] = append(buckets[v], i)
		}
		for _, idx := range buckets {
			for x, i := range idx {
				for _, j := range idx[x+1:] {
					if find(i) == find(j) || bits.OnesCount64(covers[i].Hash^covers[j].Hash) > MaxCoverDistance {
						continue
					}
					// A pair agreeing on an earlier band was seen there.
					if earlierBandAgrees(covers[i].Hash, covers[j].Hash, b) {
						continue
					}
					parent[find(i)] = find(j)
				}
			}
		}
	}
	byRoot := map[int][]Member{}
	for i, c := range covers {
		r := find(i)
		byRoot[r] = append(byRoot[r], Member{BookID: c.BookID})
	}
	var out []Group
	for _, ms := range byRoot {
		if len(ms) > 1 {
			out = append(out, Group{Kind: KindCover, Members: ms})
		}
	}
	return out
}

func earlierBandAgrees(a, b uint64, band int) bool {
	for _, e := range bands[:band] {
		mask := uint64(1)<<e.width - 1
		if a>>e.shift&mask == b>>e.shift&mask {
			return true
		}
	}
	return false
}
