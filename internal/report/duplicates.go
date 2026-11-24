package report

import (
	"context"
	"math/bits"
	"slices"
	"strings"

	"github.com/felixapel/calibre-bookwarden/internal/duplicates"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

// Duplicates pages (ADR-024): 25 groups per page, 10 members shown per group.
const (
	duplicateGroupsPerPage = 25
	duplicateMembersShown  = 10
	maxDuplicatePage       = 10000
)

// Why a run's duplicates aren't available.
const (
	DuplicatesSingleBook     = "single_book"
	DuplicatesPredate        = "predates"
	DuplicatesUnknownVersion = "unknown_version"
)

// DuplicateFilter selects one kind ("" for all) and a page from 1.
type DuplicateFilter struct {
	Kind string
	Page int
}

type DuplicateMember struct {
	BookID        int
	Title, Detail string
}

// DuplicateGroup is a group's first members, re-verified; More counts the
// rest.
type DuplicateGroup struct {
	Kind, Value, Label string
	Members            []DuplicateMember
	More               int
}

type DuplicatePage struct {
	Unavailable string         // "", or one of the Duplicates* reasons
	Partial     bool           // the run didn't complete
	Counts      map[string]int // groups per kind, before the filter
	Groups      []DuplicateGroup
	Page        int
	More        bool
}

func ValidateDuplicateFilter(f DuplicateFilter) error {
	if f.Page < 1 || f.Page > maxDuplicatePage || (f.Kind != "" && !slices.Contains(duplicates.Kinds(), f.Kind)) {
		return ErrInvalidFilter
	}
	return nil
}

// duplicatesOf opens a snapshot and says whether its run has duplicates.
func (s *Service) duplicatesOf(ctx context.Context, runID int64) (Snapshot, DuplicatePage, error) {
	snap, err := s.store.Snapshot(ctx, runID)
	if err != nil {
		return nil, DuplicatePage{}, err
	}
	run := snap.Run()
	page := DuplicatePage{Partial: run.Status != verify.RunCompleted}
	switch {
	case run.Scope.BookID != 0:
		page.Unavailable, page.Partial = DuplicatesSingleBook, false
	case run.DuplicatesVersion == 0:
		page.Unavailable, page.Partial = DuplicatesPredate, false
	case run.DuplicatesVersion != duplicates.Version:
		page.Unavailable, page.Partial = DuplicatesUnknownVersion, false
	}
	return snap, page, nil
}

// Duplicates lists a run's groups, re-verifying every member shown.
func (s *Service) Duplicates(ctx context.Context, runID int64, f DuplicateFilter) (DuplicatePage, error) {
	if err := ValidateDuplicateFilter(f); err != nil {
		return DuplicatePage{}, err
	}
	snap, page, err := s.duplicatesOf(ctx, runID)
	if err != nil {
		return DuplicatePage{}, err
	}
	defer func() { _ = snap.Close(ctx) }()
	page.Page = f.Page
	if page.Unavailable != "" {
		return page, nil
	}
	rows, covers, err := snap.DuplicateRows(ctx)
	if err != nil {
		return DuplicatePage{}, err
	}
	page.Counts = map[string]int{}
	var groups []duplicates.Group
	for _, g := range duplicates.Groups(rows, covers) {
		page.Counts[g.Kind]++
		if f.Kind == "" || f.Kind == g.Kind {
			groups = append(groups, g)
		}
	}
	start := (f.Page - 1) * duplicateGroupsPerPage
	if start >= len(groups) {
		return page, nil
	}
	end := min(start+duplicateGroupsPerPage, len(groups))
	page.More = end < len(groups)
	hashes := coverHashes(covers)
	for _, g := range groups[start:end] {
		dg, err := resolveGroup(ctx, snap, g, hashes, 0)
		if err != nil {
			return DuplicatePage{}, err
		}
		page.Groups = append(page.Groups, dg)
	}
	return page, nil
}

// BookDuplicates lists the groups one book belongs to, with its other
// members, after checking the book's whole stored projection. It reads only
// the book's own groups: its exact keys' rows, and the covers within
// MaxCoverDistance of its own (direct neighbours, not the run's chains).
func (s *Service) BookDuplicates(ctx context.Context, runID int64, bookID int) (DuplicatePage, error) {
	snap, page, err := s.duplicatesOf(ctx, runID)
	if err != nil {
		return DuplicatePage{}, err
	}
	defer func() { _ = snap.Close(ctx) }()
	if page.Unavailable != "" {
		return page, nil
	}
	b, err := snap.Book(ctx, bookID)
	if err != nil {
		return DuplicatePage{}, err
	}
	if b.Package == nil {
		return page, nil
	}
	keys, cover, err := snap.BookKeys(ctx, bookID)
	if err != nil {
		return DuplicatePage{}, err
	}
	sortKeys(keys)
	h, compared, _ := duplicates.CoverHash(b.Package.Cover)
	if !slices.Equal(keys, duplicates.Keys(*b.Package)) || compared != (cover != nil) || (cover != nil && (cover.Hash != h || cover.BookID != bookID)) {
		return DuplicatePage{}, ErrIntegrity
	}
	rows, err := snap.BookGroupRows(ctx, bookID)
	if err != nil {
		return DuplicatePage{}, err
	}
	groups := duplicates.Groups(rows, nil)
	var near []duplicates.Cover
	if cover != nil {
		if near, err = snap.NearCovers(ctx, bookID, cover.Hash); err != nil {
			return DuplicatePage{}, err
		}
		g := duplicates.Group{Kind: duplicates.KindCover, Members: []duplicates.Member{{BookID: bookID}}}
		for _, c := range near {
			if c.BookID == bookID || bits.OnesCount64(c.Hash^cover.Hash) > duplicates.MaxCoverDistance {
				return DuplicatePage{}, ErrIntegrity
			}
			g.Members = append(g.Members, duplicates.Member{BookID: c.BookID})
		}
		slices.SortFunc(g.Members, func(a, b duplicates.Member) int { return a.BookID - b.BookID })
		if len(g.Members) > 1 {
			groups = append(groups, g)
		}
	}
	hashes := coverHashes(near)
	for _, g := range groups {
		if !slices.ContainsFunc(g.Members, func(m duplicates.Member) bool { return m.BookID == bookID }) {
			continue
		}
		dg, err := resolveGroup(ctx, snap, g, hashes, bookID)
		if err != nil {
			return DuplicatePage{}, err
		}
		page.Groups = append(page.Groups, dg)
	}
	return page, nil
}

func sortKeys(keys []duplicates.Key) {
	slices.SortFunc(keys, func(a, b duplicates.Key) int {
		if c := strings.Compare(a.Kind, b.Kind); c != 0 {
			return c
		}
		return strings.Compare(a.Value, b.Value)
	})
}

func coverHashes(covers []duplicates.Cover) map[int]uint64 {
	out := make(map[int]uint64, len(covers))
	for _, c := range covers {
		out[c.BookID] = c.Hash
	}
	return out
}

// resolveGroup loads the first members (other than skip), checks that each
// sealed package still produces the group's key or cover hash, and labels
// the group.
func resolveGroup(ctx context.Context, snap Snapshot, g duplicates.Group, hashes map[int]uint64, skip int) (DuplicateGroup, error) {
	out := DuplicateGroup{Kind: g.Kind, Value: g.Value}
	members := slices.DeleteFunc(slices.Clone(g.Members), func(m duplicates.Member) bool { return m.BookID == skip })
	if len(members) > duplicateMembersShown {
		out.More = len(members) - duplicateMembersShown
		members = members[:duplicateMembersShown]
	}
	var formats []string
	for _, m := range members {
		b, err := snap.Book(ctx, m.BookID)
		if err != nil {
			return DuplicateGroup{}, err
		}
		if b.Package == nil {
			return DuplicateGroup{}, ErrIntegrity
		}
		if g.Kind == duplicates.KindCover {
			h, compared, ok := duplicates.CoverHash(b.Package.Cover)
			stored, has := hashes[m.BookID]
			if !ok || !compared || !has || h != stored {
				return DuplicateGroup{}, ErrIntegrity
			}
		} else if !slices.Contains(duplicates.Keys(*b.Package), duplicates.Key{Kind: g.Kind, Value: g.Value, Detail: m.Detail}) {
			return DuplicateGroup{}, ErrIntegrity
		}
		if len(out.Members) == 0 && g.Kind == duplicates.KindTitleAuthor {
			out.Label = Display(b.Package.Calibre.Title) + " — " + Display(strings.Join(b.Package.Calibre.Authors, " & "))
		}
		for _, f := range strings.Split(m.Detail, ", ") {
			if f != "" && !slices.Contains(formats, f) {
				formats = append(formats, f)
			}
		}
		out.Members = append(out.Members, DuplicateMember{BookID: m.BookID, Title: Display(b.Package.Calibre.Title), Detail: m.Detail})
	}
	switch g.Kind {
	case duplicates.KindISBN:
		out.Label = g.Value
	case duplicates.KindFile:
		out.Label = strings.Join(formats, ", ") + " · " + g.Value[:12]
	}
	return out, nil
}
