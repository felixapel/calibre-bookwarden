package findings

import "strings"

// CanonicalISBN validates an ISBN and returns its ISBN-13 identity.
func CanonicalISBN(raw string) (string, bool) {
	if len(raw) > 2048 {
		return "", false
	}
	s := strings.TrimSpace(raw)
	lower := strings.ToLower(s)
	switch {
	case strings.HasPrefix(lower, "urn:isbn:"):
		s = s[len("urn:isbn:"):]
	case strings.HasPrefix(lower, "isbn"):
		s = strings.TrimPrefix(s[4:], ":")
	}
	s = strings.NewReplacer(" ", "", "-", "").Replace(s)
	if len(s) == 13 {
		if !strings.HasPrefix(s, "978") && !strings.HasPrefix(s, "979") {
			return "", false
		}
		sum := 0
		for i, c := range []byte(s) {
			if c < '0' || c > '9' {
				return "", false
			}
			weight := 1
			if i%2 == 1 {
				weight = 3
			}
			sum += int(c-'0') * weight
		}
		if sum%10 == 0 {
			return s, true
		}
		return "", false
	}
	if len(s) != 10 {
		return "", false
	}
	sum := 0
	for i, c := range []byte(s) {
		n := int(c - '0')
		if i == 9 && (c == 'X' || c == 'x') {
			n = 10
		} else if c < '0' || c > '9' {
			return "", false
		}
		sum += n * (10 - i)
	}
	if sum%11 != 0 {
		return "", false
	}
	base := "978" + s[:9]
	sum = 0
	for i, c := range []byte(base) {
		weight := 1
		if i%2 == 1 {
			weight = 3
		}
		sum += int(c-'0') * weight
	}
	return base + string(rune('0'+(10-sum%10)%10)), true
}
