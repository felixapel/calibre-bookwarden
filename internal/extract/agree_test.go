package extract

import (
	"math/rand"
	"reflect"
	"slices"
	"testing"
)

func gc(source, field, value string) Verified {
	return Verified{Claim: Claim{Source: source, Field: field, Value: value}, Status: StatusGrounded}
}

func withLabel(v Verified, label string) Verified { v.Label = label; return v }

func withRole(v Verified, role string, unit, start int) Verified {
	v.Role, v.Unit, v.Start, v.End = role, unit, start, start+1
	return v
}

func fv(value string) FactValue { return FactValue{Value: value} }

func TestAgree(t *testing.T) {
	ex := []string{"a", "b", "c"}
	nongrounded := gc("b", FieldTitle, "Other")
	nongrounded.Status = StatusNotFound
	tests := []struct {
		name string
		vs   []Verified
		want []Fact
	}{
		{"nothing", nil, nil},
		{"two extractors same title", []Verified{gc("a", FieldTitle, "T"), gc("b", FieldTitle, "T")},
			[]Fact{{Key: "title", Outcome: OutcomeAgreed, Values: []FactValue{fv("T")}, Voices: []string{"a", "b"}}}},
		{"one extractor", []Verified{gc("a", FieldTitle, "T")},
			[]Fact{{Key: "title", Outcome: OutcomeSingle, Values: []FactValue{fv("T")}, Voices: []string{"a"}}}},
		{"third grounds another title", []Verified{gc("a", FieldTitle, "T"), gc("b", FieldTitle, "T"), gc("c", FieldTitle, "U")},
			[]Fact{{Key: "title", Outcome: OutcomeDisagree, Voices: []string{"a", "b", "c"}}}},
		{"parser does not vote on free-form", []Verified{gc(SourceParser, FieldTitle, "T"), gc("a", FieldTitle, "T")},
			[]Fact{{Key: "title", Outcome: OutcomeSingle, Values: []FactValue{fv("T")}, Voices: []string{"a"}}}},
		{"extractor claiming nothing is no dissent", []Verified{gc("a", FieldImprint, "I"), gc("b", FieldTitle, "T"), gc("b", FieldImprint, "I")},
			[]Fact{
				{Key: "imprint", Outcome: OutcomeAgreed, Values: []FactValue{fv("I")}, Voices: []string{"a", "b"}},
				{Key: "title", Outcome: OutcomeSingle, Values: []FactValue{fv("T")}, Voices: []string{"b"}},
			}},
		{"isbn by two extractors, no parser", []Verified{
			withLabel(gc("a", FieldISBN, "9780306406157"), "ebook"), withLabel(gc("b", FieldISBN, "9780306406157"), "ebook")},
			[]Fact{{Key: "isbn", Outcome: OutcomeSingle, Values: []FactValue{{Value: "9780306406157", Label: "ebook"}}, Voices: []string{"a", "b"}}}},
		{"isbn by parser and one extractor", []Verified{
			withLabel(gc(SourceParser, FieldISBN, "9780306406157"), "ebook"), withLabel(gc("a", FieldISBN, "9780306406157"), "ebook")},
			[]Fact{{Key: "isbn", Outcome: OutcomeAgreed, Values: []FactValue{{Value: "9780306406157", Label: "ebook"}}, Voices: []string{"a", SourceParser}}}},
		{"isbn same value, different label", []Verified{
			withLabel(gc(SourceParser, FieldISBN, "9780306406157"), "ebook"), withLabel(gc("a", FieldISBN, "9780306406157"), "paperback")},
			[]Fact{{Key: "isbn", Outcome: OutcomeDisagree, Voices: []string{"a", SourceParser}}}},
		{"isbn: a voice with more values differs", []Verified{
			gc(SourceParser, FieldISBN, "1"), gc("a", FieldISBN, "1"), gc("a", FieldISBN, "2")},
			[]Fact{{Key: "isbn", Outcome: OutcomeDisagree, Voices: []string{"a", SourceParser}}}},
		{"isbn: several values sorted", []Verified{
			gc(SourceParser, FieldISBN, "2"), gc(SourceParser, FieldISBN, "1"), gc("a", FieldISBN, "1"), gc("a", FieldISBN, "2")},
			[]Fact{{Key: "isbn", Outcome: OutcomeAgreed, Values: []FactValue{fv("1"), fv("2")}, Voices: []string{"a", SourceParser}}}},
		{"year labels are separate keys", []Verified{
			withLabel(gc(SourceParser, FieldYear, "2003"), LabelCopyright), withLabel(gc("a", FieldYear, "2003"), LabelCopyright),
			withLabel(gc(SourceParser, FieldYear, "2001"), LabelFirstPublished), withLabel(gc("a", FieldYear, "1999"), LabelFirstPublished)},
			[]Fact{
				{Key: "year/copyright", Outcome: OutcomeAgreed, Values: []FactValue{fv("2003")}, Voices: []string{"a", SourceParser}},
				{Key: "year/first_published", Outcome: OutcomeDisagree, Voices: []string{"a", SourceParser}},
			}},
		{"parser alone on a patterned key", []Verified{gc(SourceParser, FieldSeriesIndex, "2")},
			[]Fact{{Key: "series_index", Outcome: OutcomeSingle, Values: []FactValue{fv("2")}, Voices: []string{SourceParser}}}},
		{"contributor lists in another order", []Verified{
			withRole(gc("a", FieldContributor, "X"), RoleAuthor, 0, 1), withRole(gc("a", FieldContributor, "Y"), RoleEditor, 0, 5),
			withRole(gc("b", FieldContributor, "Y"), RoleEditor, 0, 1), withRole(gc("b", FieldContributor, "X"), RoleAuthor, 0, 5)},
			[]Fact{{Key: "contributor", Outcome: OutcomeDisagree, Voices: []string{"a", "b"}}}},
		{"contributor lists in the same order", []Verified{
			withRole(gc("b", FieldContributor, "Y"), RoleEditor, 0, 5), withRole(gc("b", FieldContributor, "X"), RoleAuthor, 0, 1),
			withRole(gc("a", FieldContributor, "X"), RoleAuthor, 0, 2), withRole(gc("a", FieldContributor, "Y"), RoleEditor, 1, 0)},
			[]Fact{{Key: "contributor", Outcome: OutcomeAgreed,
				Values: []FactValue{{Value: "X", Role: RoleAuthor}, {Value: "Y", Role: RoleEditor}}, Voices: []string{"a", "b"}}}},
		{"parser translator and one extractor's translator", []Verified{
			withRole(gc(SourceParser, FieldContributor, "J"), RoleTranslator, 0, 3), withRole(gc("a", FieldContributor, "J"), RoleTranslator, 0, 4)},
			[]Fact{
				{Key: "contributor", Outcome: OutcomeSingle, Values: []FactValue{{Value: "J", Role: RoleTranslator}}, Voices: []string{"a"}},
				{Key: "translator", Outcome: OutcomeAgreed, Values: []FactValue{{Value: "J", Role: RoleTranslator}}, Voices: []string{"a", SourceParser}},
			}},
		{"translator among other contributors", []Verified{
			withRole(gc(SourceParser, FieldContributor, "J"), RoleTranslator, 0, 3),
			withRole(gc("a", FieldContributor, "X"), RoleAuthor, 0, 1), withRole(gc("a", FieldContributor, "J"), RoleTranslator, 0, 4)},
			[]Fact{
				{Key: "contributor", Outcome: OutcomeSingle, Values: []FactValue{{Value: "X", Role: RoleAuthor}, {Value: "J", Role: RoleTranslator}}, Voices: []string{"a"}},
				{Key: "translator", Outcome: OutcomeAgreed, Values: []FactValue{{Value: "J", Role: RoleTranslator}}, Voices: []string{"a", SourceParser}},
			}},
		{"a non-grounded claim never votes", []Verified{gc("a", FieldTitle, "T"), nongrounded},
			[]Fact{{Key: "title", Outcome: OutcomeSingle, Values: []FactValue{fv("T")}, Voices: []string{"a"}}}},
		{"unknown source ignored", []Verified{gc("z", FieldTitle, "T"), gc("a", FieldTitle, "T")},
			[]Fact{{Key: "title", Outcome: OutcomeSingle, Values: []FactValue{fv("T")}, Voices: []string{"a"}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Agree(tt.vs, ex)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Agree = %+v, want %+v", got, tt.want)
			}
			rev := slices.Clone(tt.vs)
			slices.Reverse(rev)
			if again := Agree(rev, []string{"c", "b", "a"}); !reflect.DeepEqual(again, got) {
				t.Errorf("reversed input: %+v, want %+v", again, got)
			}
		})
	}
}

func TestAgreeInputOrder(t *testing.T) {
	var vs []Verified
	for _, s := range []string{"a", "b", "c", SourceParser} {
		for i, f := range []string{FieldTitle, FieldISBN, FieldContributor, FieldYear, FieldSeriesIndex} {
			v := gc(s, f, string(rune('p'+i%2)))
			v.Role, v.Label, v.Unit, v.Start = RoleTranslator, LabelCopyright, i%2, i
			vs = append(vs, v)
		}
	}
	want := Agree(vs, []string{"a", "b", "c"})
	rng := rand.New(rand.NewSource(1))
	for range 50 {
		rng.Shuffle(len(vs), func(i, j int) { vs[i], vs[j] = vs[j], vs[i] })
		if got := Agree(vs, []string{"c", "a", "b"}); !reflect.DeepEqual(got, want) {
			t.Fatalf("shuffled: %+v, want %+v", got, want)
		}
	}
}

func FuzzAgree(f *testing.F) {
	f.Add([]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11})
	f.Add([]byte{1, 0, 0, 0, 1, 1, 1, 1, 0, 2, 2, 2, 5, 3, 0, 0, 0, 0})
	f.Add([]byte{})
	fields := []string{FieldTitle, FieldSubtitle, FieldContributor, FieldImprint, FieldYear, FieldEdition, FieldSeries, FieldSeriesIndex, FieldISBN, "bogus"}
	sources := []string{SourceParser, "a", "b", "c", "z"}
	roles := []string{"", RoleAuthor, RoleTranslator, RoleEditor}
	labels := []string{"", LabelCopyright, LabelFirstPublished, "ebook"}
	statuses := []string{StatusGrounded, StatusGrounded, StatusGrounded, StatusNotFound}
	f.Fuzz(func(t *testing.T, data []byte) {
		var vs []Verified
		for len(data) >= 6 {
			b := data[:6]
			data = data[6:]
			vs = append(vs, Verified{
				Claim: Claim{
					Source: sources[int(b[0])%len(sources)], Field: fields[int(b[1])%len(fields)],
					Value: string(rune('a' + b[2]%3)), Role: roles[int(b[3])%len(roles)], Label: labels[int(b[4])%len(labels)],
				},
				Unit: int(b[5] % 2), Start: int(b[5] / 2 % 4), End: int(b[5] / 8),
				Status: statuses[int(b[5])%len(statuses)],
			})
		}
		facts := Agree(vs, []string{"a", "b", "c"})
		for i, fact := range facts {
			if i > 0 && facts[i-1].Key >= fact.Key {
				t.Fatalf("keys not sorted and unique: %+v", facts)
			}
			if len(fact.Voices) == 0 {
				t.Fatalf("no voices: %+v", fact)
			}
			for j := 1; j < len(fact.Voices); j++ {
				if fact.Voices[j-1] >= fact.Voices[j] {
					t.Fatalf("voices not sorted and unique: %+v", fact)
				}
			}
			switch fact.Outcome {
			case OutcomeAgreed:
				if len(fact.Voices) < 2 || len(fact.Values) == 0 {
					t.Fatalf("bad agreed fact %+v", fact)
				}
			case OutcomeSingle:
				if len(fact.Values) == 0 {
					t.Fatalf("single fact without values %+v", fact)
				}
			case OutcomeDisagree:
				if fact.Values != nil || len(fact.Voices) < 2 {
					t.Fatalf("bad disagree fact %+v", fact)
				}
			default:
				t.Fatalf("outcome %q", fact.Outcome)
			}
		}
		rev := slices.Clone(vs)
		slices.Reverse(rev)
		if again := Agree(rev, []string{"c", "b", "a"}); !reflect.DeepEqual(again, facts) {
			t.Fatalf("order changed the result: %+v vs %+v", again, facts)
		}
	})
}
