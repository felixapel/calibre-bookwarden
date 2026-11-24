package findings_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/findings"
)

func TestScanISBNs(t *testing.T) {
	const a, b, c = "9780000000040", "9780000000064", "9780000000026"
	m := func(isbn, q string) findings.ISBNMatch { return findings.ISBNMatch{ISBN: isbn, Qualifier: q} }
	tests := []struct {
		name, in string
		want     []findings.ISBNMatch
	}{
		{"bare", "ISBN 978-0-00-000004-0", []findings.ISBNMatch{m(a, "")}},
		{"ebook before", "ISBN (eBook): 978-0-00-000004-0", []findings.ISBNMatch{m(a, "ebook")}},
		{"ebook after in parentheses", "ISBN 978-0-00-000004-0 (ebook)", []findings.ISBNMatch{m(a, "ebook")}},
		{"e-book spelled with a hyphen", "E-Book ISBN 978-0-00-000004-0", []findings.ISBNMatch{m(a, "ebook")}},
		{"epub", "ePub ISBN 978-0-00-000004-0", []findings.ISBNMatch{m(a, "ebook")}},
		{"kindle", "Kindle Edition ISBN 978-0-00-000004-0", []findings.ISBNMatch{m(a, "ebook")}},
		{"electronic", "Electronic ISBN: 978-0-00-000004-0", []findings.ISBNMatch{m(a, "ebook")}},
		{"digital", "digital ISBN 978-0-00-000004-0", []findings.ISBNMatch{m(a, "ebook")}},
		{"spanish ebook", "Libro electrónico ISBN 978-0-00-000004-0", []findings.ISBNMatch{m(a, "ebook")}},
		{"german ebook", "E-Book-ISBN 978-0-00-000004-0", []findings.ISBNMatch{m(a, "ebook")}},
		{"pdf", "PDF ISBN 978-0-00-000004-0", []findings.ISBNMatch{m(a, "pdf")}},
		{"hardcover", "Hardcover ISBN 978-0-00-000004-0", []findings.ISBNMatch{m(a, "hardcover")}},
		{"hardback", "ISBN 978-0-00-000004-0 (hardback)", []findings.ISBNMatch{m(a, "hardcover")}},
		{"cloth", "ISBN 978-0-00-000004-0 [cloth]", []findings.ISBNMatch{m(a, "hardcover")}},
		{"german hardcover", "Gebunden: ISBN 978-0-00-000004-0", []findings.ISBNMatch{m(a, "hardcover")}},
		{"spanish hardcover", "Tapa dura ISBN 978-0-00-000004-0", []findings.ISBNMatch{m(a, "hardcover")}},
		{"paperback", "Paperback ISBN 978-0-00-000004-0", []findings.ISBNMatch{m(a, "paperback")}},
		{"pbk", "ISBN 978-0-00-000004-0 (pbk.)", []findings.ISBNMatch{m(a, "paperback")}},
		{"softcover", "Softcover ISBN 978-0-00-000004-0", []findings.ISBNMatch{m(a, "paperback")}},
		{"taschenbuch", "Taschenbuch ISBN 978-0-00-000004-0", []findings.ISBNMatch{m(a, "paperback")}},
		{"broschiert", "ISBN 978-0-00-000004-0 (broschiert)", []findings.ISBNMatch{m(a, "paperback")}},
		{"tapa blanda", "Tapa blanda ISBN 978-0-00-000004-0", []findings.ISBNMatch{m(a, "paperback")}},
		{"rustica", "Rústica ISBN 978-0-00-000004-0", []findings.ISBNMatch{m(a, "paperback")}},
		{"print", "Print ISBN 978-0-00-000004-0", []findings.ISBNMatch{m(a, "print")}},
		{"a word merely containing a term", "Printed in Spain ISBN 978-0-00-000004-0 clothes", []findings.ISBNMatch{m(a, "")}},
		{"a label too far before", "ebook" + strings.Repeat(" ", 45) + "ISBN 978-0-00-000004-0", []findings.ISBNMatch{m(a, "")}},
		{"the nearest label wins", "Paperback ... eBook ISBN 978-0-00-000004-0", []findings.ISBNMatch{m(a, "ebook")}},
		{"a label after beats one before", "Hardcover ISBN 978-0-00-000004-0 (ebook)", []findings.ISBNMatch{m(a, "ebook")}},
		{"two on a line, labelled after", "ISBN 978-0-00-000004-0 (hardcover) ISBN 978-0-00-000006-4 (ebook)", []findings.ISBNMatch{m(a, "hardcover"), m(b, "ebook")}},
		{"two on a line, labelled before", "Paperback ISBN 978-0-00-000004-0 eBook ISBN 978-0-00-000006-4", []findings.ISBNMatch{m(a, "paperback"), m(b, "ebook")}},
		{"the second doesn't borrow the first's label", "Hardcover ISBN 978-0-00-000004-0 ISBN 978-0-00-000006-4", []findings.ISBNMatch{m(a, "hardcover"), m(b, "")}},
		{"an ad page", "Also by the author\nThe Lost Ledger, ISBN 978-0-00-000004-0\nMore Ledgers, ISBN 978-0-00-000006-4", []findings.ISBNMatch{m(a, ""), m(b, "")}},
		{"duplicates keep the first sighting", "ebook ISBN 978-0-00-000004-0 and again 9780000000040 and " + c, []findings.ISBNMatch{m(a, "ebook"), m(c, "")}},
		{"labels on separate lines", "ISBN 978-0-00-000004-0 hardcover\nISBN 978-0-00-000006-4 ebook", []findings.ISBNMatch{m(a, "hardcover"), m(b, "ebook")}},
		{"a label on the previous line is not this ISBN's", "Also available as an ebook.\nISBN 978-0-00-000004-0", []findings.ISBNMatch{m(a, "")}},
		{"a bare label after the ISBN", "ISBN 978-0-00-000004-0 ebook", []findings.ISBNMatch{m(a, "ebook")}},
		{"a bare label after is the first ISBN's, not the second's", "ISBN 978-0-00-000004-0 hardcover ISBN 978-0-00-000006-4", []findings.ISBNMatch{m(a, "hardcover"), m(b, "")}},
		{"a bare label before the next ISBN's own label", "Paperback ISBN 978-0-00-000004-0 eBook ISBN 978-0-00-000006-4", []findings.ISBNMatch{m(a, "paperback"), m(b, "ebook")}},
		{"a label after a line break isn't the ISBN's", "ISBN 978-0-00-000004-0\nhardcover edition", []findings.ISBNMatch{m(a, "")}},
		{"a non-breaking hyphen", "E\u2011book ISBN 978-0-00-000004-0", []findings.ISBNMatch{m(a, "ebook")}},
		{"an en dash", "E\u2013book ISBN 978-0-00-000004-0", []findings.ISBNMatch{m(a, "ebook")}},
		{"a decomposed accent", "Ru\u0301stica ISBN 978-0-00-000004-0", []findings.ISBNMatch{m(a, "paperback")}},
		{"a later sighting's label fills an empty one", "ISBN-10: 0-00-000004-3\nISBN-13: 978-0-00-000004-0 (paperback)", []findings.ISBNMatch{m(a, "paperback")}},
		{"a first label is kept", "Hardcover ISBN 978-0-00-000004-0\nISBN 978-0-00-000004-0 (ebook)", []findings.ISBNMatch{m(a, "hardcover")}},
		{"empty", "", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := findings.ScanISBNs(tc.in)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("ScanISBNs(%q) = %+v, want %+v", tc.in, got, tc.want)
			}
			var isbns []string
			for _, g := range got {
				isbns = append(isbns, g.ISBN)
			}
			if want := findings.ExtractISBNs(tc.in); !slices.Equal(isbns, want) {
				t.Fatalf("ScanISBNs ISBNs %v differ from ExtractISBNs %v", isbns, want)
			}
		})
	}
}

func FuzzScanISBNs(f *testing.F) {
	f.Add("ISBN 978-0-00-000004-0 (ebook) Paperback 978 0 00 000006 4")
	f.Add("(((")
	f.Fuzz(func(t *testing.T, s string) {
		got := findings.ScanISBNs(s)
		want := findings.ExtractISBNs(s)
		if len(got) != len(want) {
			t.Fatalf("%d matches, %d ISBNs", len(got), len(want))
		}
		for i, g := range got {
			if g.ISBN != want[i] {
				t.Fatalf("match %d is %s, want %s", i, g.ISBN, want[i])
			}
		}
	})
}

// TestScanISBNsOverlappingLabels: a label can reach past the next ISBN, as
// when the parentheses after one ISBN hold the next; the scan must not cut a
// window backwards (found by FuzzScanISBNs in the nightly run).
func TestScanISBNsOverlappingLabels(t *testing.T) {
	for _, text := range []string{
		"9780000000040(9780000000040 eBook )",
		"ISBN 9780000000040 (ebook 9780306406157) hardcover",
		"9780000000040(9780306406157(9780000000040 pdf) print)",
		"(9780306406157 ebook 9780000000040)",
	} {
		t.Run(text, func(t *testing.T) {
			got := findings.ScanISBNs(text)
			want := findings.ExtractISBNs(text)
			if len(got) != len(want) {
				t.Fatalf("ScanISBNs = %+v, want the ISBNs %v", got, want)
			}
			for i := range got {
				if got[i].ISBN != want[i] {
					t.Errorf("ISBN %d = %s, want %s", i, got[i].ISBN, want[i])
				}
			}
		})
	}
}
