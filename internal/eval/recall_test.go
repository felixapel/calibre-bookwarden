package eval

import (
	"bytes"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/store"
)

func typed(s string) *string { return &s }

func ans(book int, field, answer string, t *string) store.Confirmation {
	return store.Confirmation{SampleID: 1, BookID: book, Field: field, Answer: answer, Typed: t}
}

// recallFixture is a hand-built sample: stratum A stands for 100 library
// books through 2 sampled ones (weight 50), B for 20 through 4 (weight 5), C
// for 10 through 2 (weight 5), and D's 40 books have no sampled book.
func recallFixture() RecallInput {
	sample := store.RecallSample{ID: 1, RunID: 9, Seed: 5, Size: 8, FindingsVersion: 2, DecisionVersion: 3, Strata: map[string]store.StratumCount{
		"A": {Library: 100, Sampled: 2}, "B": {Library: 20, Sampled: 4}, "C": {Library: 10, Sampled: 2}, "D": {Library: 40, Sampled: 0},
	}}
	books := []store.SampleBook{
		{BookID: 1, Stratum: "A", Position: 1}, {BookID: 2, Stratum: "A", Position: 2},
		{BookID: 3, Stratum: "B", Position: 3}, {BookID: 4, Stratum: "B", Position: 4}, {BookID: 7, Stratum: "B", Position: 5}, {BookID: 8, Stratum: "B", Position: 6},
		{BookID: 5, Stratum: "C", Position: 7}, {BookID: 6, Stratum: "C", Position: 8},
	}
	fix := func(field string, v ...string) map[string][]string { return map[string][]string{field: v} }
	facts := map[int]store.SampleFacts{
		1: {Language: "eng", Fixes: fix("title", "Dune")},
		2: {Language: "eng", Fixes: fix("title", "Dune Messiah")},
		3: {Language: "eng"},
		4: {Language: "eng"},
		7: {Language: "eng", Fixes: fix("title", "Hobbit")},
		8: {Language: "eng"},
		5: {Language: "eng", Fixes: fix("title", "Emma")},
		6: {Language: "eng", Fixes: fix("title", "Emma")},
	}
	answers := []store.Confirmation{
		ans(1, "title", store.AnswerWrong, nil),                 // a fix: found
		ans(2, "title", store.AnswerWrong, typed("Emma")),       // a fix, but not what he typed
		ans(3, "title", store.AnswerWrong, nil),                 // no fix: missed
		ans(4, "title", store.AnswerWrong, typed("The Hobbit")), // no fix: missed
		ans(7, "title", store.AnswerWrong, typed("the Hobbit")), // equivalent to the fix: found
		ans(8, "title", store.AnswerRight, nil),                 // right, no fix: fine
		ans(5, "title", store.AnswerSkip, nil),                  // skips count nowhere
		ans(6, "title", store.AnswerRight, nil),                 // right but a fix: a false alarm
		ans(1, "authors", store.AnswerWrong, typed("Ada Reed")), // authors: the fix says Ada Reed
		ans(2, "authors", store.AnswerRight, nil),
	}
	facts[1].Fixes["authors"] = []string{"Ada Reed"}
	return RecallInput{Sample: sample, Books: books, Answers: answers, Facts: facts}
}

func mustRecall(t *testing.T, in RecallInput) RecallResult {
	t.Helper()
	r, err := Recall(in)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func field(t *testing.T, r RecallResult, name string) FieldRecall {
	t.Helper()
	for _, f := range r.Fields {
		if f.Field == name {
			return f
		}
	}
	t.Fatalf("no field %s", name)
	return FieldRecall{}
}

func TestRecall(t *testing.T) {
	r := mustRecall(t, recallFixture())
	if len(r.Fields) != len(store.RecallFields) {
		t.Fatalf("%d fields", len(r.Fields))
	}
	title := field(t, r, "title")
	// Wrong: books 1, 2 (weight 50 each), 3, 4, 7 (weight 5 each). Found: 1
	// and 7. Unweighted that is 2 of 5; reweighted (50 + 5) / 115.
	if title.Wrong != 5 || title.Found != 2 || title.WrongValue != 1 || title.Right != 2 || title.Skipped != 1 {
		t.Errorf("title counts %+v", title)
	}
	if want := 55.0 / 115.0; math.Abs(title.Recall-want) > 1e-9 {
		t.Errorf("title recall %.6f, want %.6f (a plain share would be 0.4)", title.Recall, want)
	}
	if want := 115.0 * 115.0 / (2*50*50 + 3*5*5); math.Abs(title.EffectiveN-want) > 1e-9 {
		t.Errorf("effective n %.4f, want %.4f", title.EffectiveN, want)
	}
	if !title.HasInterval || title.Lower >= title.Recall || title.Upper <= title.Recall || title.Lower < 0 || title.Upper > 1 {
		t.Errorf("interval %.3f to %.3f around %.3f", title.Lower, title.Upper, title.Recall)
	}
	// Only a book called right that has a fix is a false alarm; a skipped one
	// with a fix is not.
	if !reflect.DeepEqual(title.FalseAlarms, []int{6}) {
		t.Errorf("false alarms %v, want [6]", title.FalseAlarms)
	}

	authors := field(t, r, "authors")
	if authors.Wrong != 1 || authors.Found != 1 || authors.Recall != 1 || authors.Right != 1 || len(authors.FalseAlarms) != 0 {
		t.Errorf("authors %+v", authors)
	}
	if authors.Upper != 1 || authors.Lower <= 0 {
		t.Errorf("authors interval %.3f to %.3f", authors.Lower, authors.Upper)
	}

	// A field nobody answered has no recall and no interval.
	pub := field(t, r, "publisher")
	if pub.Wrong != 0 || pub.HasInterval || pub.RecallText() != "n/a" || pub.Interval() != "n/a" {
		t.Errorf("publisher %+v", pub)
	}
	if r.Answered != 8 || r.Unrepresented != 40 || r.Library != 170 {
		t.Errorf("answered %d, unrepresented %d of %d", r.Answered, r.Unrepresented, r.Library)
	}
}

func TestRecallTypedValueNeedsTheFix(t *testing.T) {
	in := recallFixture()
	// With nothing typed, any fix for the field is found; with a typed value
	// it must be the same value under ADR-036.
	in.Answers = []store.Confirmation{
		ans(1, "title", store.AnswerWrong, typed("Dune")),
		ans(2, "title", store.AnswerWrong, typed("Dune: Messiah")),
	}
	title := field(t, mustRecall(t, in), "title")
	if title.Found != 2 || title.WrongValue != 0 {
		t.Errorf("equivalent typed values: %+v", title)
	}
	in.Answers = []store.Confirmation{ans(1, "title", store.AnswerWrong, typed("Children of Dune"))}
	if title := field(t, mustRecall(t, in), "title"); title.Found != 0 || title.WrongValue != 1 || title.Recall != 0 {
		t.Errorf("a different typed value: %+v", title)
	}
}

func TestRecallSkipsCountNowhere(t *testing.T) {
	in := recallFixture()
	base := field(t, mustRecall(t, in), "title")
	in.Answers = append(in.Answers, ans(3, "title", store.AnswerSkip, nil))
	// The same book answered skip later replaces its wrong answer.
	skipped := field(t, mustRecall(t, in), "title")
	if skipped.Wrong != base.Wrong-1 || skipped.Skipped != base.Skipped+1 {
		t.Errorf("a skip over a wrong answer: wrong %d, skipped %d", skipped.Wrong, skipped.Skipped)
	}
}

func TestRecallWithoutAnswers(t *testing.T) {
	in := recallFixture()
	in.Answers = nil
	r := mustRecall(t, in)
	for _, f := range r.Fields {
		if f.Wrong+f.Right+f.Skipped != 0 || f.HasInterval {
			t.Errorf("%s: %+v", f.Field, f)
		}
	}
	if r.Answered != 0 {
		t.Errorf("answered %d", r.Answered)
	}
}

func TestRecallWriteText(t *testing.T) {
	var out bytes.Buffer
	mustRecall(t, recallFixture()).WriteText(&out)
	for _, want := range []string{"Recall sample 1", "decision v3", "approx", "not labels", "title", "47.8 %", "false alarms, title: books 6", "40 of 170 library books"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("text lacks %q:\n%s", want, out.String())
		}
	}
}

func TestRecallNeedsEveryStratumsWeight(t *testing.T) {
	for name, mutate := range map[string]func(*RecallInput){
		"stratum missing":    func(in *RecallInput) { delete(in.Sample.Strata, "A") },
		"nothing sampled":    func(in *RecallInput) { in.Sample.Strata["A"] = store.StratumCount{Library: 100} },
		"empty in library":   func(in *RecallInput) { in.Sample.Strata["A"] = store.StratumCount{Sampled: 2} },
		"book without class": func(in *RecallInput) { in.Books[0].Stratum = "Z" },
	} {
		t.Run(name, func(t *testing.T) {
			in := recallFixture()
			mutate(&in)
			if _, err := Recall(in); !errors.Is(err, ErrNoWeight) {
				t.Errorf("err = %v, want ErrNoWeight", err)
			}
		})
	}
}
