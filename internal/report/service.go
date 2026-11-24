package report

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/findings"
)

type Service struct{ store Store }

func New(st Store) *Service { return &Service{store: st} }

func (s *Service) Book(ctx context.Context, runID int64, bookID int) (Book, error) {
	snap, err := s.store.Snapshot(ctx, runID)
	if err != nil {
		return Book{}, err
	}
	defer func() { _ = snap.Close(ctx) }()
	return snap.Book(ctx, bookID)
}

func Display(value string) string {
	r := []rune(value)
	if len(r) > 512 {
		r = r[:512]
	}
	return string(r)
}

// EachBook visits every frozen book of a run in id order, in one snapshot,
// loading each once. An unverifiable book stops the walk with its error.
func (s *Service) EachBook(ctx context.Context, runID int64, visit func(Book) error) error {
	snap, err := s.store.Snapshot(ctx, runID)
	if err != nil {
		return err
	}
	defer func() { _ = snap.Close(ctx) }()
	for after := 0; ; {
		if err := ctx.Err(); err != nil {
			return err
		}
		b, ok, err := snap.NextBook(ctx, after)
		if err != nil || !ok {
			return err
		}
		if err := visit(b); err != nil {
			return err
		}
		after = b.Ref.ID
	}
}

func (s *Service) Books(ctx context.Context, runID int64, after int) (BooksPage, error) {
	if after < 0 {
		return BooksPage{}, ErrInvalidCursor
	}
	snap, pr, err := s.open(ctx, runID, 0)
	if err != nil {
		return BooksPage{}, err
	}
	defer func() { _ = snap.Close(ctx) }()
	if pr != nil {
		rows, err := pr.BookRows(ctx, after, booksPageSize+1)
		if err != nil {
			return BooksPage{}, err
		}
		var page BooksPage
		if len(rows) > booksPageSize {
			rows = rows[:booksPageSize]
			page.NextBookID = rows[len(rows)-1].Ref.ID
		}
		page.Rows = rows
		return page, nil
	}
	var page BooksPage
	for {
		if err := ctx.Err(); err != nil {
			return BooksPage{}, err
		}
		b, ok, err := snap.NextBook(ctx, after)
		if err != nil {
			return BooksPage{}, err
		}
		if !ok {
			break
		}
		if len(page.Rows) == booksPageSize {
			page.NextBookID = after
			break
		}
		summary := BookSummary{Ref: b.Ref, State: b.State, Reason: Display(b.Reason)}
		if b.Package != nil {
			summary.Title = Display(b.Package.Calibre.Title)
		}
		page.Rows = append(page.Rows, summary)
		after = b.Ref.ID
	}
	return page, nil
}

const booksPageSize = 50

var formatPattern = regexp.MustCompile(`^[A-Z0-9_]{1,32}$`)

func ValidateFilter(f Filter) error {
	if f.BookID < 0 || (f.Format != "" && !formatPattern.MatchString(f.Format)) {
		return ErrInvalidFilter
	}
	if f.Code != "" {
		for _, d := range findings.Catalog() {
			if f.Code == d.Code {
				return nil
			}
		}
		return ErrInvalidFilter
	}
	return nil
}

// walk verifies every selected book of the run, in book order, and visits
// each of the book's items after the cursor; items counts them. bookID 0
// selects every book. The walk reaches the end of the run even when the
// caller keeps only one page, so damage anywhere fails the page.
func walk(ctx context.Context, snap Snapshot, bookID int, cursor *Cursor, items func(Book) int, visit func(Book, int) error) error {
	cursorSeen := cursor == nil
	after := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var b Book
		var ok bool
		var err error
		if bookID > 0 {
			if after != 0 {
				break
			}
			b, err = snap.Book(ctx, bookID)
			ok = err == nil
		} else {
			b, ok, err = snap.NextBook(ctx, after)
		}
		if err != nil {
			return err
		}
		if !ok {
			break
		}
		after = b.Ref.ID
		if b.Package == nil {
			continue
		}
		n := items(b)
		if cursor != nil && b.Ref.ID == cursor.BookID {
			if cursor.Ordinal >= n {
				return ErrInvalidCursor
			}
			cursorSeen = true
		}
		for ordinal := range n {
			if err := ctx.Err(); err != nil {
				return err
			}
			if cursor != nil && (b.Ref.ID < cursor.BookID || (b.Ref.ID == cursor.BookID && ordinal <= cursor.Ordinal)) {
				continue
			}
			if err := visit(b, ordinal); err != nil {
				return err
			}
		}
	}
	if !cursorSeen {
		return ErrInvalidCursor
	}
	return nil
}

// checkCursor rejects a cursor that can't belong to this run and filter.
func checkCursor(runID int64, bookID int, cursor *Cursor) error {
	if cursor != nil && (cursor.RunID != runID || cursor.BookID <= 0 || cursor.Ordinal < 0 || cursor.Offset < 0 || (bookID > 0 && cursor.BookID != bookID)) {
		return ErrInvalidCursor
	}
	return nil
}

// scan visits the findings that match f, from the projection when the run's
// check allows it and by walking the run otherwise. limit bounds the rows the
// projection returns (0 is no bound); the visit sees them all either way.
func (s *Service) scan(ctx context.Context, runID int64, f Filter, cursor *Cursor, limit int, visit func(Row, int) error) error {
	if err := ValidateFilter(f); err != nil {
		return err
	}
	if err := checkCursor(runID, f.BookID, cursor); err != nil {
		return err
	}
	snap, pr, err := s.open(ctx, runID, f.BookID)
	if err != nil {
		return err
	}
	defer func() { _ = snap.Close(ctx) }()
	if pr != nil {
		if err := checkProjectedCursor(ctx, pr, f.BookID, cursor, func(b ProjectedBook) int { return b.Findings }); err != nil {
			return err
		}
		return pr.FindingRows(ctx, f, cursor, limit, visit)
	}
	return walk(ctx, snap, f.BookID, cursor, func(b Book) int { return len(b.Findings) }, func(b Book, ordinal int) error {
		finding := b.Findings[ordinal]
		if (f.Code != "" && f.Code != finding.Code) || (f.Format != "" && f.Format != finding.Format) {
			return nil
		}
		return visit(Row{RunID: runID, BookID: b.Ref.ID, Title: Display(b.Package.Calibre.Title), Finding: finding}, ordinal)
	})
}

const findingsPageSize = 50

func (s *Service) Findings(ctx context.Context, runID int64, f Filter, cursor *Cursor) (Page, error) {
	var page Page
	var last Cursor
	more := false
	err := s.scan(ctx, runID, f, cursor, findingsPageSize+1, func(row Row, ordinal int) error {
		if len(page.Rows) < findingsPageSize {
			page.Rows = append(page.Rows, row)
			last = Cursor{RunID: runID, BookID: row.BookID, Ordinal: ordinal}
		} else {
			more = true
		}
		return nil
	})
	if err != nil {
		return Page{}, err
	}
	if more {
		page.Next = &last
	}
	return page, nil
}

func (s *Service) Walk(ctx context.Context, runID int64, f Filter, visit func(Row) error) error {
	return s.scan(ctx, runID, f, nil, 0, func(row Row, _ int) error { return visit(row) })
}

// ValidateFixFilter accepts a proposal field, a tier and a book id, each optional.
func ValidateFixFilter(f FixFilter) error {
	if f.BookID < 0 || (f.Field != "" && !slices.Contains(decide.Fields(), f.Field)) ||
		(f.Tier != "" && f.Tier != evidence.TierA && f.Tier != evidence.TierB && f.Tier != evidence.TierC) ||
		(f.Decided != "" && f.Decided != DecidedAgent && f.Decided != DecidedPerson && f.Decided != DecidedDisagree) ||
		utf8.RuneCountInString(f.Query) > MaxFixQuery || f.Skip < 0 {
		return ErrInvalidFilter
	}
	return nil
}

const fixesPageSize = 50

// Fold lowers a string and drops its combining marks, so a search ignores
// case and diacritics. Letters that don't decompose are mapped by hand. Both
// search paths fold with this function: a check stores the folded text of
// each book (SearchText) and a walk folds the sealed package.
func Fold(s string) string {
	var b strings.Builder
	for _, r := range norm.NFD.String(s) {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		switch r = unicode.ToLower(r); r {
		case 'ø':
			r = 'o'
		case 'ł':
			r = 'l'
		case 'đ':
			r = 'd'
		case 'ß':
			b.WriteString("ss")
			continue
		case 'æ':
			b.WriteString("ae")
			continue
		case 'œ':
			b.WriteString("oe")
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// SearchText is what a search of a book looks at: its folded title and
// authors, one per line, so a query without a line break never matches
// across two of them.
func SearchText(title string, authors []string) string {
	parts := make([]string, 0, 1+len(authors))
	parts = append(parts, Fold(title))
	for _, a := range authors {
		parts = append(parts, Fold(a))
	}
	return strings.Join(parts, "\n")
}

// normalizeQuery collapses every run of white space, line breaks included,
// to one space.
func normalizeQuery(q string) string { return strings.Join(strings.Fields(q), " ") }

// sortedFixes is whether a Fixes list is in an order the book cursor can't
// page, so it pages by offset.
func sortedFixes(f FixFilter) bool {
	return f.Sort == SortField || f.Sort == SortTier || f.Sort == SortDecided
}

// Fixes lists the current proposals of a run's verified books, 50 per page.
func (s *Service) Fixes(ctx context.Context, runID int64, f FixFilter, cursor *Cursor) (FixPage, error) {
	if err := ValidateFixFilter(f); err != nil {
		return FixPage{}, err
	}
	if err := checkCursor(runID, f.BookID, cursor); err != nil {
		return FixPage{}, err
	}
	snap, pr, err := s.open(ctx, runID, f.BookID)
	if err != nil {
		return FixPage{}, err
	}
	defer func() { _ = snap.Close(ctx) }()
	f.Query = normalizeQuery(f.Query)
	if pr == nil {
		f.Sort = ""
	}
	var page FixPage
	page.Walked = pr == nil
	var last Cursor
	more := false
	// The default order pages by book and ordinal; the others by offset.
	offset := 0
	if sortedFixes(f) {
		if cursor != nil {
			offset = cursor.Offset
		}
		f.Skip = offset
	}
	add := func(row FixRow, ordinal int) {
		if len(page.Rows) == fixesPageSize {
			more = true
			return
		}
		page.Rows = append(page.Rows, row)
		last = Cursor{RunID: runID, BookID: row.BookID, Ordinal: ordinal}
		if offset > 0 || sortedFixes(f) {
			last.Offset = offset + len(page.Rows)
		}
	}
	if pr != nil {
		if err := checkProjectedCursor(ctx, pr, f.BookID, cursor, func(b ProjectedBook) int { return b.Proposals }); err != nil {
			return FixPage{}, err
		}
		after := cursor
		if sortedFixes(f) {
			after = nil
		}
		err = pr.FixRows(ctx, f, after, fixesPageSize+1, func(row FixRow, ordinal int) error {
			add(row, ordinal)
			return nil
		})
		if err == nil && !f.Muted && len(f.MutedTypes) > 0 {
			page.MutedHidden, err = pr.MutedFixes(ctx, f)
		}
	} else {
		err = walk(ctx, snap, f.BookID, cursor, func(b Book) int { return len(b.Decision.Proposals) }, func(b Book, ordinal int) error {
			p := b.Decision.Proposals[ordinal]
			if (f.Field != "" && f.Field != p.Field) || (f.Tier != "" && f.Tier != b.Decision.Tier) {
				return nil
			}
			// Only the projection sorts and filters by decision, in SQL; an
			// unchecked run is listed in book order and the page applies
			// the Decided filter to what it lists.
			if f.Query != "" && !b.Matches(f.Query) {
				return nil
			}
			if f.Muted || len(f.MutedTypes) > 0 {
				if muted := slices.Contains(f.MutedTypes, TypeOf(p)); muted != f.Muted {
					if muted {
						page.MutedHidden++
					}
					return nil
				}
			}
			add(FixRow{RunID: runID, BookID: b.Ref.ID, Title: Display(b.Package.Calibre.Title), Tier: b.Decision.Tier, Proposal: p}, ordinal)
			return nil
		})
	}
	if err != nil {
		return FixPage{}, err
	}
	if more {
		page.Next = &last
	}
	return page, nil
}
