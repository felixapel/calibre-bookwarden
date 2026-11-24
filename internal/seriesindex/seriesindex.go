// Package seriesindex turns a series position into the one decimal string
// evidence seals. Calibre keeps the index as a float and files write it as
// text; the seal's canonical JSON has no non-integer numbers (RFC 8785
// subset), so the index is a string, and the same position must always give
// the same string.
package seriesindex

import (
	"regexp"
	"strconv"
	"strings"
)

// Caps on a position: enough for any real series, short enough that the
// string can't carry text.
const (
	maxIntDigits  = 6
	maxFracDigits = 6
)

var exponent = regexp.MustCompile(`^[0-9]+(\.[0-9]*)?[eE][+-]?[0-9]+$`)

// Normalize returns the canonical form of s: digits with an optional fraction,
// no sign, an exponent written out in full, no leading zeros and no trailing fraction zeros ("1.0"
// is "1", "2.50" is "2.5", "007" is "7", ".5" is "0.5", "0" is "0"). An empty
// or blank s is "" and valid. Anything else that isn't a plain non-negative
// decimal within the caps reports false.
func Normalize(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", true
	}
	if exponent.MatchString(s) {
		// Python can write a float as "1e-05": read it and write it out in
		// full first, so Calibre's list and an OPF give the same string.
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return "", false
		}
		s = strconv.FormatFloat(f, 'f', -1, 64)
	}
	whole, frac, _ := strings.Cut(s, ".")
	if s == "." || !digits(whole) || !digits(frac) {
		return "", false
	}
	whole = strings.TrimLeft(whole, "0")
	frac = strings.TrimRight(frac, "0")
	if len(whole) > maxIntDigits || len(frac) > maxFracDigits {
		return "", false
	}
	if whole == "" {
		whole = "0"
	}
	if frac == "" {
		return whole, true
	}
	return whole + "." + frac, true
}

func digits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// Compare orders two normalized positions numerically: -1, 0 or 1. Both must
// be Normalize's output.
func Compare(a, b string) int {
	aw, af, _ := strings.Cut(a, ".")
	bw, bf, _ := strings.Cut(b, ".")
	if len(aw) != len(bw) {
		if len(aw) < len(bw) {
			return -1
		}
		return 1
	}
	if c := strings.Compare(aw, bw); c != 0 {
		return c
	}
	return strings.Compare(af, bf)
}
