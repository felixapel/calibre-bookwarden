// Package isbnranges hyphenates ISBNs from the International ISBN Agency's
// range table (README.md). Wikidata stores ISBNs hyphenated and matches them
// as exact strings, so a lookup needs the hyphenated form (ADR-035).
package isbnranges

import (
	_ "embed"
	"encoding/xml"
	"strconv"
	"strings"
	"sync"
)

//go:embed RangeMessage.xml
var rangeMessage []byte

type rule struct {
	lo, hi int // 7-digit bounds
	length int // digits of the next part; 0 = unassigned
}

type table struct {
	ean    map[string][]rule // "978": rules for the group
	groups map[string][]rule // "978-0": rules for the registrant
}

type xmlRule struct {
	Range  string `xml:"Range"`
	Length int    `xml:"Length"`
}

type xmlEntry struct {
	Prefix string    `xml:"Prefix"`
	Rules  []xmlRule `xml:"Rules>Rule"`
}

type xmlMessage struct {
	EAN    []xmlEntry `xml:"EAN.UCCPrefixes>EAN.UCC"`
	Groups []xmlEntry `xml:"RegistrationGroups>Group"`
}

// load parses the embedded table. It panics on a corrupt file, which the
// tests would catch: the file is vendored, not user input.
var load = sync.OnceValue(func() *table {
	var m xmlMessage
	if err := xml.Unmarshal(rangeMessage, &m); err != nil {
		panic("isbnranges: " + err.Error())
	}
	t := &table{ean: map[string][]rule{}, groups: map[string][]rule{}}
	conv := func(dst map[string][]rule, entries []xmlEntry) {
		for _, e := range entries {
			for _, r := range e.Rules {
				lo, hi, ok := strings.Cut(r.Range, "-")
				l, err1 := strconv.Atoi(lo)
				h, err2 := strconv.Atoi(hi)
				if !ok || err1 != nil || err2 != nil {
					panic("isbnranges: bad range " + r.Range)
				}
				dst[e.Prefix] = append(dst[e.Prefix], rule{l, h, r.Length})
			}
		}
	}
	conv(t.ean, m.EAN)
	conv(t.groups, m.Groups)
	return t
})

func lengthFor(rules []rule, digits string) (int, bool) {
	n, err := strconv.Atoi(digits)
	if err != nil {
		return 0, false
	}
	for _, r := range rules {
		if n >= r.lo && n <= r.hi {
			return r.length, r.length > 0
		}
	}
	return 0, false
}

func checksum13(s string) bool {
	sum := 0
	for i := range 13 {
		d := int(s[i] - '0')
		if i%2 == 1 {
			d *= 3
		}
		sum += d
	}
	return sum%10 == 0
}

// parts splits a valid ISBN-13 into prefix, group, registrant, publication
// and check digit. ok is false for a malformed ISBN or an unassigned range.
func parts(isbn13 string) (p [5]string, ok bool) {
	if len(isbn13) != 13 {
		return p, false
	}
	for i := range 13 {
		if isbn13[i] < '0' || isbn13[i] > '9' {
			return p, false
		}
	}
	prefix := isbn13[:3]
	if (prefix != "978" && prefix != "979") || !checksum13(isbn13) {
		return p, false
	}
	t := load()
	body := isbn13[3:12]
	gl, ok := lengthFor(t.ean[prefix], body[:7])
	if !ok {
		return p, false
	}
	group := body[:gl]
	rest := body[gl:]
	rules := t.groups[prefix+"-"+group]
	if len(rest) < 7 {
		rest += strings.Repeat("0", 7-len(rest))
	}
	rl, ok := lengthFor(rules, rest[:7])
	if !ok || gl+rl > 8 {
		return p, false
	}
	return [5]string{prefix, group, body[gl : gl+rl], body[gl+rl:], isbn13[12:]}, true
}

// Hyphenate writes a checksum-valid ISBN-13 with hyphens, for example
// 978-0-7653-2635-5. It returns false for malformed input and for a number
// in a range the table leaves unassigned.
func Hyphenate(isbn13 string) (string, bool) {
	p, ok := parts(isbn13)
	if !ok {
		return "", false
	}
	return strings.Join(p[:], "-"), true
}

// Hyphenate10 writes the ISBN-10 of a 978 ISBN-13 with hyphens, for example
// 0-7653-2635-3. It returns false for a 979 number, which has no ISBN-10, and
// wherever Hyphenate does.
func Hyphenate10(isbn13 string) (string, bool) {
	p, ok := parts(isbn13)
	if !ok || p[0] != "978" {
		return "", false
	}
	body := p[1] + p[2] + p[3]
	sum := 0
	for i := range 9 {
		sum += int(body[i]-'0') * (10 - i)
	}
	check := (11 - sum%11) % 11
	c := strconv.Itoa(check)
	if check == 10 {
		c = "X"
	}
	return p[1] + "-" + p[2] + "-" + p[3] + "-" + c, true
}
