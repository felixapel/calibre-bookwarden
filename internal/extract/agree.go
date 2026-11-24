package extract

import (
	"cmp"
	"slices"
)

// Outcomes of agreement for one key.
const (
	OutcomeAgreed   = "agreed"
	OutcomeSingle   = "single"
	OutcomeDisagree = "disagree"
)

// Fact is what the voices say about one key. Values is nil for a disagreement.
type Fact struct {
	Key     string
	Outcome string
	Values  []FactValue
	Voices  []string
}

// FactValue is one value of a fact. Role is set for contributors and
// translators, Label for ISBNs; a year's label is in its key.
type FactValue struct{ Value, Role, Label string }

// vote is one grounded claim of one source for one key.
type vote struct {
	v                FactValue
	unit, start, end int
}

const keyTranslator = "translator"

// Agree decides, per key, whether the grounded claims agree (M4 §7). Only
// grounded claims count; a claim whose source is neither the parser nor one of
// extractors is ignored. The result is sorted by key and does not depend on
// the order of vs.
func Agree(vs []Verified, extractors []string) []Fact {
	isExtractor := make(map[string]bool, len(extractors))
	for _, e := range extractors {
		if e != SourceParser {
			isExtractor[e] = true
		}
	}
	byKey := map[string]map[string][]vote{} // key -> source -> votes
	add := func(key, source string, v vote) {
		if byKey[key] == nil {
			byKey[key] = map[string][]vote{}
		}
		byKey[key][source] = append(byKey[key][source], v)
	}
	for _, c := range vs {
		if c.Status != StatusGrounded {
			continue
		}
		parser := c.Source == SourceParser
		if !parser && !isExtractor[c.Source] {
			continue
		}
		v := vote{v: FactValue{Value: c.Value}, unit: c.Unit, start: c.Start, end: c.End}
		switch c.Field {
		case FieldTitle, FieldSubtitle, FieldImprint, FieldEdition, FieldSeries:
			if !parser {
				add(c.Field, c.Source, v)
			}
		case FieldContributor:
			v.v.Role = c.Role
			if !parser {
				add(FieldContributor, c.Source, v)
			}
			if c.Role == RoleTranslator {
				add(keyTranslator, c.Source, v)
			}
		case FieldSeriesIndex:
			add(FieldSeriesIndex, c.Source, v)
		case FieldISBN:
			v.v.Label = c.Label
			add(FieldISBN, c.Source, v)
		case FieldYear:
			add(FieldYear+"/"+c.Label, c.Source, v)
		}
	}

	var facts []Fact
	for key, sources := range byKey {
		facts = append(facts, decideKey(key, sources))
	}
	slices.SortFunc(facts, func(a, b Fact) int { return cmp.Compare(a.Key, b.Key) })
	return facts
}

func orderedKey(key string) bool { return key == FieldContributor || key == keyTranslator }

func freeFormKey(key string) bool {
	switch key {
	case FieldTitle, FieldSubtitle, FieldImprint, FieldEdition, FieldSeries, FieldContributor:
		return true
	}
	return false
}

func compareValues(a, b FactValue) int {
	return cmp.Or(cmp.Compare(a.Value, b.Value), cmp.Compare(a.Role, b.Role), cmp.Compare(a.Label, b.Label))
}

// valueOf is a source's value for a key: for contributors and translators
// the list in text order, each person once, else the sorted set of distinct
// values.
func valueOf(key string, votes []vote) []FactValue {
	if orderedKey(key) {
		slices.SortFunc(votes, func(a, b vote) int {
			return cmp.Or(cmp.Compare(a.unit, b.unit), cmp.Compare(a.start, b.start), cmp.Compare(a.end, b.end), compareValues(a.v, b.v))
		})
		// One person quoted twice (the cover and the title page) is one
		// contributor, at the first place (extract v4, audit Q3-B).
		seen := map[FactValue]bool{}
		votes = slices.DeleteFunc(votes, func(v vote) bool {
			if seen[v.v] {
				return true
			}
			seen[v.v] = true
			return false
		})
	} else {
		slices.SortFunc(votes, func(a, b vote) int { return compareValues(a.v, b.v) })
		votes = slices.CompactFunc(votes, func(a, b vote) bool { return a.v == b.v })
	}
	out := make([]FactValue, len(votes))
	for i, v := range votes {
		out[i] = v.v
	}
	return out
}

func decideKey(key string, sources map[string][]vote) Fact {
	f := Fact{Key: key}
	for s := range sources {
		f.Voices = append(f.Voices, s)
	}
	slices.Sort(f.Voices)
	first := valueOf(key, sources[f.Voices[0]])
	for _, s := range f.Voices[1:] {
		if !slices.Equal(first, valueOf(key, sources[s])) {
			f.Outcome = OutcomeDisagree
			return f
		}
	}
	f.Values = first
	f.Outcome = OutcomeSingle
	switch {
	case len(f.Voices) < 2:
	case freeFormKey(key):
		f.Outcome = OutcomeAgreed
	case slices.Contains(f.Voices, SourceParser):
		f.Outcome = OutcomeAgreed
	}
	return f
}
