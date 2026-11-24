package findings

import (
	"slices"
	"strings"
	"unicode"
)

// MaxISBNs is the most ISBNs a scan returns.
const MaxISBNs = 16

const (
	maxISBNs   = MaxISBNs
	labelReach = 30 // runes between "ISBN" and an ISBN-10
)

func isDigit(r rune) bool { return r >= '0' && r <= '9' }

func isDash(r rune) bool { return r == '-' || r == '‐' || r == '‑' || r == '–' }

// gapAt is the length of the separator starting rs: a space, or a dash with at
// most one space on each side, as text layers often set hyphens; 0 for none.
func gapAt(rs []rune) int {
	n := 0
	if n < len(rs) && rs[n] == ' ' {
		n++
	}
	if n < len(rs) && isDash(rs[n]) {
		n++
		if n < len(rs) && rs[n] == ' ' {
			n++
		}
	}
	return n
}

// checkX reports whether rs starts with an ISBN-10's check character X that
// ends its word.
func checkX(rs []rune) bool {
	return len(rs) > 0 && (rs[0] == 'X' || rs[0] == 'x') && (len(rs) == 1 || !unicode.IsLetter(rs[1]) && !isDigit(rs[1]))
}

// ExtractISBNs returns the checksum-valid ISBNs in text as canonical ISBN-13,
// in the order first seen, at most 16. Digits form groups, and groups joined by
// one separator form a run; an ISBN-10's check character X may follow one too.
// Within a run, consecutive groups holding 13 digits are a candidate on their
// own; 10 (the last may be X) only shortly after the word "ISBN", since a bare
// ten-digit number is too often something else. A group is never split, so an
// ISBN inside a longer number is ignored.
func ExtractISBNs(text string) []string {
	var out []string
	for _, m := range scan([]rune(text), false) {
		out = append(out, m.isbn)
	}
	return out
}

// found is an ISBN and where it sits in the text, in runes: [start, end).
type found struct {
	isbn       string
	start, end int
}

// scan is ExtractISBNs with positions: the first sighting of each ISBN, or
// with all every sighting, until MaxISBNs different ISBNs have been seen.
func scan(rs []rune, all bool) []found {
	var out []found
	var seen []string
	add := func(digits string, start, end int) {
		isbn, ok := CanonicalISBN(digits)
		if !ok {
			return
		}
		if slices.Contains(seen, isbn) {
			if all {
				out = append(out, found{isbn, start, end})
			}
		} else if len(seen) < maxISBNs {
			seen = append(seen, isbn)
			out = append(out, found{isbn, start, end})
		}
	}
	for i := 0; i < len(rs) && len(seen) < maxISBNs; {
		if !isDigit(rs[i]) {
			i++
			continue
		}
		var groups []group
		for {
			g := group{at: i}
			for i < len(rs) && isDigit(rs[i]) {
				i++
			}
			if i < len(rs) && (rs[i] == 'X' || rs[i] == 'x') {
				i++
			}
			g.digits = strings.ToUpper(string(rs[g.at:i]))
			groups = append(groups, g)
			gap := gapAt(rs[i:])
			if strings.HasSuffix(g.digits, "X") || gap == 0 || i+gap >= len(rs) || !isDigit(rs[i+gap]) && !checkX(rs[i+gap:]) {
				break
			}
			i += gap
		}
		for s := 0; s < len(groups); {
			n, next := 0, s
			var b strings.Builder
			for next < len(groups) && n < 13 {
				b.WriteString(groups[next].digits)
				n += len(groups[next].digits)
				next++
				if n == 10 && labelled(rs[:groups[s].at]) {
					if _, ok := CanonicalISBN(b.String()); ok {
						break
					}
				}
			}
			if n == 13 || (n == 10 && labelled(rs[:groups[s].at])) {
				if _, ok := CanonicalISBN(b.String()); ok {
					last := groups[next-1]
					add(b.String(), groups[s].at, last.at+len([]rune(last.digits)))
					s = next
					continue
				}
			}
			s++
		}
	}
	return out
}

type group struct {
	at     int // rune index of the first digit
	digits string
}

// labelled reports whether "ISBN" ends at most labelReach runes before the
// end of before.
func labelled(before []rune) bool {
	from := max(len(before)-labelReach-len("isbn"), 0)
	window := strings.ToLower(string(before[from:]))
	at := strings.LastIndex(window, "isbn")
	return at >= 0 && len([]rune(window[at+len("isbn"):])) <= labelReach
}
