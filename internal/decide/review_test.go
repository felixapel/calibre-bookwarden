package decide_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

// isbn13 returns a checksum-valid ISBN-13 for n.
func isbn13(n int) string {
	body := fmt.Sprintf("978%09d", n)
	sum := 0
	for i, c := range body {
		d := int(c - '0')
		if i%2 == 1 {
			d *= 3
		}
		sum += d
	}
	return body + fmt.Sprint((10-sum%10)%10)
}

// TestFrontMatterReview pins the review findings of PR #86: the front-matter
// ISBN only selects records, never becomes a proposal, and a disputed or
// partly scanned identity fails closed.
func TestFrontMatterReview(t *testing.T) {
	ebook := evidence.QualifierEbook
	dnb := ok(evidence.SourceDNB, dnbT())
	unconfirmed := []string{"frontmatter_isbn_unconfirmed", "isbn_missing", "no_record"}
	many := func(n int) []evidence.ISBNContext {
		ctx := []evidence.ISBNContext{copyrightISBN(I, "")}
		for i := 1; i < n; i++ {
			ctx = append(ctx, evidence.ISBNContext{ISBN: isbn13(i), Landmark: evidence.LandmarkBack})
		}
		return ctx
	}
	for _, tt := range []struct {
		name    string
		p       func() evidence.Package
		reasons []string
		fields  []string
	}{
		{"C1: Calibre's correct e-book ISBN is not replaced by the page's print ISBN", func() evidence.Package {
			p := frontPkg([]evidence.ISBNContext{copyrightISBN(I, "")}, dnb)
			p.Calibre.Identifiers = map[string]string{"isbn": I2}
			p.Calibre.Languages = []string{"deu"}
			return p
		}, []string{"identity_frontmatter", "isbn_missing"}, nil},
		{"I1: a record by another author doesn't confirm", func() evidence.Package {
			r := rec("x", T, []string{"Someone Else"}, []string{"deu"}, I)
			r.Date, r.Publisher = "2001-03-04", "Other Verlag"
			g := r
			g.ID = "g"
			return frontPkg([]evidence.ISBNContext{copyrightISBN(I, "")}, ok(evidence.SourceDNB, r), ok(evidence.SourceGoogleBooks, g))
		}, unconfirmed, nil},
		{"I1: the original-language edition of a translation doesn't confirm", func() evidence.Package {
			r := rec("x", T, []string{A}, []string{"eng"}, I)
			r.Date, r.Publisher = "1997-06-26", "Bloomsbury"
			g := r
			g.ID = "g"
			return frontPkg([]evidence.ISBNContext{copyrightISBN(I, "")}, ok(evidence.SourceDNB, r), ok(evidence.SourceGoogleBooks, g))
		}, unconfirmed, nil},
		{"I1: an omnibus isn't confirmed by a volume's record", func() evidence.Package {
			p := frontPkg([]evidence.ISBNContext{copyrightISBN(I, "")}, dnb)
			p.Formats[0].Metadata.Title = T + " (Sammelband)"
			p.Calibre.Title = T + " (Sammelband)"
			return p
		}, unconfirmed, nil},
		{"I1: a record conflict after confirmation drops the identity", func() evidence.Package {
			other := rec("g", "A Different Title", []string{A}, []string{"deu"}, I)
			return frontPkg([]evidence.ISBNContext{copyrightISBN(I, "")}, dnb, ok(evidence.SourceGoogleBooks, other))
		}, unconfirmed, nil},
		{"M1: a garbled metadata ISBN blocks the front-matter path", func() evidence.Package {
			p := frontPkg([]evidence.ISBNContext{copyrightISBN(I, "")}, dnb)
			p.Formats[0].Metadata.ISBNs = []string{"9783551551670"} // bad checksum
			return p
		}, []string{"isbn_missing", "no_record"}, nil},
		{"M2: a hint list at the 16 ISBN cap is ambiguous", func() evidence.Package {
			return frontPkg(many(16), dnb)
		}, []string{"frontmatter_isbn_ambiguous", "isbn_missing", "no_record"}, nil},
		{"M2: 15 ISBNs are below the cap", func() evidence.Package {
			return frontPkg(many(15), dnb)
		}, []string{"identity_frontmatter", "isbn_missing"}, []string{"languages"}},
		{"M3: a failed scan of another EPUB fails closed", func() evidence.Package {
			p := frontPkg([]evidence.ISBNContext{copyrightISBN(I, "")}, dnb)
			second := hinted(T, copyrightISBN(I2, ebook))
			second.Format = "KEPUB"
			second.ISBNHints.State = evidence.HintFailed
			p.Formats = append(p.Formats, second)
			return p
		}, []string{"isbn_missing", "no_record"}, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d, err := decide.Decide(t.Context(), tt.p())
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(d.Reasons, tt.reasons) || !slices.Equal(fields(d.Proposals), tt.fields) {
				t.Fatalf("reasons %v proposals %v, want %v and %v", d.Reasons, fields(d.Proposals), tt.reasons, tt.fields)
			}
		})
	}
}

// TestISBNProposalOnV5PathIsNeverLooserThanV5: at version 6 the record's title
// key must equal the files' exactly as well as match in full.
func TestISBNProposalOnV5PathIsNeverLooserThanV5(t *testing.T) {
	p := tierA()
	p.Calibre.Identifiers = map[string]string{"isbn": "978-0-306-40615-7"}
	p.Lookups[0].Results[0].Records[0].Title = T + " (Sammelband)"
	for version, want := range map[int]bool{5: false, 6: false} {
		d, err := decide.AtVersion(t.Context(), p, version)
		if err != nil {
			t.Fatal(err)
		}
		if got := slices.Contains(fields(d.Proposals), "isbn"); got != want {
			t.Errorf("v%d: isbn proposal = %v, want %v", version, got, want)
		}
	}
	// A leading article is a full ADR-030 match but not an exact title.
	p = tierA()
	p.Calibre.Identifiers = map[string]string{"isbn": "978-0-306-40615-7"}
	p.Formats[0].Metadata.Title = "The Road"
	p.Calibre.Title = "The Road"
	p.Lookups[0].Results[0].Records[0].Title = "Road"
	d, err := decide.AtVersion(t.Context(), p, 6)
	if err != nil || slices.Contains(fields(d.Proposals), "isbn") || strings.Contains(fmt.Sprint(d.Reasons), "other_work") {
		t.Fatalf("v6 = %+v, %v", d, err)
	}
}
