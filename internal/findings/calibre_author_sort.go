package findings

import "strings"

// Calibre 9.16's default author tweaks (author_sort_copy_method "comma", no
// surname prefixes), verbatim.
var (
	authorNameSuffixes  = []string{"Jr", "Sr", "Inc", "Ph.D", "Phd", "MD", "M.D", "I", "II", "III", "IV", "Junior", "Senior"}
	authorNamePrefixes  = []string{"Mr", "Mrs", "Ms", "Dr", "Prof"}
	authorNameCopywords = []string{"Agency", "Corporation", "Company", "Co.", "Council", "Committee", "Inc.", "Institute",
		"National", "Society", "Club", "Team", "Software", "Games", "Entertainment", "Media", "Studios"}
)

// lowerSet holds words lower-cased, and with a trailing "." too when dotted.
func lowerSet(words []string, dotted bool) map[string]bool {
	m := map[string]bool{}
	for _, w := range words {
		m[strings.ToLower(w)] = true
		if dotted {
			m[strings.ToLower(w)+"."] = true
		}
	}
	return m
}

var (
	nameSuffixes  = lowerSet(authorNameSuffixes, true)
	namePrefixes  = lowerSet(authorNamePrefixes, true)
	nameCopywords = lowerSet(authorNameCopywords, false)
)

// removeBracketedText is Calibre's remove_bracketed_text: text inside (), []
// or {} goes, nesting counted; a closer without an opener is dropped.
func removeBracketedText(s string) string {
	closers := map[rune]rune{')': '(', ']': '[', '}': '{'}
	open := map[rune]int{}
	depth := 0
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '(' || r == '[' || r == '{':
			open[r]++
			depth++
		case closers[r] != 0:
			if o := closers[r]; open[o] > 0 {
				open[o]--
				depth--
			}
		case depth < 1:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// CalibreAuthorSort is Calibre 9.16's author_to_author_sort(name) with the
// default tweaks: what Calibre stores as a new author's sort. The golden test
// (scripts/author-sort-golden.sh) is its authority.
func CalibreAuthorSort(name string) string {
	if name == "" {
		return ""
	}
	stripped := strings.TrimFunc(removeBracketedText(name), pySpace)
	if strings.Contains(stripped, ",") {
		return name
	}
	tokens := strings.FieldsFunc(stripped, pySpace)
	if len(tokens) < 2 {
		return name
	}
	for _, t := range tokens {
		if nameCopywords[strings.ToLower(t)] {
			return name
		}
	}
	first := 0
	for first < len(tokens) && namePrefixes[strings.ToLower(tokens[first])] {
		first++
	}
	if first == len(tokens) {
		return name
	}
	last := len(tokens) - 1
	for last >= first && nameSuffixes[strings.ToLower(tokens[last])] {
		last--
	}
	if last < first {
		return name
	}
	out := append([]string{tokens[last]}, tokens[first:last]...)
	if last > first {
		out[0] += ","
	}
	if suffix := strings.Join(tokens[last+1:], " "); suffix != "" {
		out = append(out, suffix)
	}
	return strings.Join(out, " ")
}
