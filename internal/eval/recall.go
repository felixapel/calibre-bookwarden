package eval

import (
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"strings"

	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/store"
)

// RecallInput is everything the recall of a sample is computed from: the
// sample, the newest answer of each (book, field), and what the sample's run
// says about each sampled book.
type RecallInput struct {
	Sample  store.RecallSample
	Books   []store.SampleBook
	Answers []store.Confirmation
	Facts   map[int]store.SampleFacts
}

// FieldRecall is the recall of one field. Wrong counts the sampled books
// Felix called wrong, Found those among them that have a fix for the field
// (with a matching value when he typed one), and Recall is their share with
// each book weighted by its stratum's library share. Lower and Upper are the
// 95 % Clopper-Pearson interval of that share at the effective sample size.
type FieldRecall struct {
	Field string
	// Right, Wrong and Skipped count answered books, unweighted. A skip
	// counts nowhere else.
	Right, Wrong, Skipped int
	// Found are the wrong books with a fix for the field that matches what
	// he typed; WrongValue those with a fix whose value didn't match.
	Found, WrongValue int
	Recall            float64
	// EffectiveN is the weighted sample size, (sum w)^2 / sum w^2 over the
	// wrong books; it is what the interval is computed at.
	EffectiveN   float64
	Lower, Upper float64
	HasInterval  bool
	// FalseAlarms are the book ids Felix called right that have a fix for the
	// field.
	FalseAlarms []int
}

// RecallResult is the measurement: one row per field, in store.RecallFields
// order, and how much of the library the sample could speak for.
type RecallResult struct {
	Sample int64
	Run    int64
	Seed   int64
	Size   int
	// FindingsVersion and DecisionVersion are the rules the sampled run was
	// checked under; the fixes the recall counts are those rules' fixes.
	FindingsVersion, DecisionVersion int
	Answered                         int // books with at least one answer
	Fields                           []FieldRecall
	// Unrepresented counts library books in strata that got no sampled book:
	// the weights can't speak for them.
	Unrepresented int
	Library       int
}

// ErrNoWeight means a sampled book's stratum is missing from the sample's
// weights, so its share of the library is unknown.
var ErrNoWeight = errors.New("eval: stratum without a weight")

// Recall computes recall per field from a sample's confirmations (design
// section 6.4). Strata are reweighted by their library share: a sampled book
// stands for Library/Sampled books of its stratum. Skips count nowhere. A
// typed value must match the fix's proposed value under ADR-036's
// equivalences (decide.SameValue). The answers are confirmations, not labels:
// they certify nothing.
func Recall(in RecallInput) (RecallResult, error) {
	res := RecallResult{Sample: in.Sample.ID, Run: in.Sample.RunID, Seed: in.Sample.Seed, Size: in.Sample.Size,
		FindingsVersion: in.Sample.FindingsVersion, DecisionVersion: in.Sample.DecisionVersion}
	for _, sc := range in.Sample.Strata {
		res.Library += sc.Library
		if sc.Sampled == 0 {
			res.Unrepresented += sc.Library
		}
	}
	weight := map[int]float64{}
	for _, b := range in.Books {
		sc, ok := in.Sample.Strata[b.Stratum]
		if !ok || sc.Sampled == 0 || sc.Library == 0 {
			return RecallResult{}, fmt.Errorf("%w: book %d is in stratum %q, which has no weight", ErrNoWeight, b.BookID, b.Stratum)
		}
		weight[b.BookID] = float64(sc.Library) / float64(sc.Sampled)
	}
	type key struct {
		book  int
		field string
	}
	latest := map[key]store.Confirmation{}
	books := map[int]bool{}
	for _, a := range in.Answers {
		latest[key{a.BookID, a.Field}] = a
		books[a.BookID] = true
	}
	res.Answered = len(books)
	for _, field := range store.RecallFields {
		fr := FieldRecall{Field: field}
		var sumW, sumW2, foundW float64
		for _, b := range in.Books {
			a, ok := latest[key{b.BookID, field}]
			if !ok {
				continue
			}
			facts := in.Facts[b.BookID]
			proposed, hasFix := facts.Fixes[field]
			switch a.Answer {
			case store.AnswerSkip:
				fr.Skipped++
			case store.AnswerRight:
				fr.Right++
				if hasFix {
					fr.FalseAlarms = append(fr.FalseAlarms, b.BookID)
				}
			case store.AnswerWrong:
				fr.Wrong++
				w := weight[b.BookID]
				sumW += w
				sumW2 += w * w
				found := hasFix
				if hasFix && a.Typed != nil && *a.Typed != "" {
					found = decide.SameValue(field, *a.Typed, proposed, facts.Language)
					if !found {
						fr.WrongValue++
					}
				}
				if found {
					fr.Found++
					foundW += w
				}
			}
		}
		slices.Sort(fr.FalseAlarms)
		if sumW > 0 {
			fr.Recall = foundW / sumW
			// The interval treats the weighted share as a binomial share at
			// the effective sample size: unequal weights act like a smaller
			// sample (a design effect of n over EffectiveN), so the interval
			// widens with them. It is approximate, and a few books in a
			// heavy stratum can move it a lot.
			fr.EffectiveN = sumW * sumW / sumW2
			n := int(math.Round(fr.EffectiveN))
			k := int(math.Round(fr.Recall * float64(n)))
			fr.Lower, fr.Upper, fr.HasInterval = store.ClopperPearson(n, k)
		}
		res.Fields = append(res.Fields, fr)
	}
	return res, nil
}

func sharePercent(v float64) string { return fmt.Sprintf("%.1f %%", v*100) }

// Interval is the recall's 95 % interval as the table shows it.
func (f FieldRecall) Interval() string {
	if !f.HasInterval {
		return "n/a"
	}
	return sharePercent(f.Lower) + " to " + sharePercent(f.Upper)
}

// RecallText is the recall as the table shows it, "n/a" with no wrong book.
func (f FieldRecall) RecallText() string {
	if f.Wrong == 0 {
		return "n/a"
	}
	return sharePercent(f.Recall)
}

// WriteText prints the result as a table, and lists the false alarms by book
// id; like the rest of eval it never prints library text.
func (r RecallResult) WriteText(w io.Writer) {
	fmt.Fprintf(w, "Recall sample %d (run %d, seed %d, findings v%d, decision v%d): %d of %d books answered.\n", r.Sample, r.Run, r.Seed, r.FindingsVersion, r.DecisionVersion, r.Answered, r.Size)
	fmt.Fprintln(w, "Answers are confirmations, not labels: they measure recall and certify nothing.")
	fmt.Fprintf(w, "%-10s %6s %6s %7s %7s %-9s %-30s %6s %12s\n", "field", "wrong", "found", "n_eff", "right", "recall", "95 % interval (approx.)", "skips", "false alarms")
	for _, f := range r.Fields {
		fmt.Fprintf(w, "%-10s %6d %6d %7.1f %7d %-9s %-30s %6d %12d\n", f.Field, f.Wrong, f.Found, f.EffectiveN, f.Right, f.RecallText(), f.Interval(), f.Skipped, len(f.FalseAlarms))
	}
	for _, f := range r.Fields {
		if len(f.FalseAlarms) > 0 {
			ids := make([]string, len(f.FalseAlarms))
			for i, id := range f.FalseAlarms {
				ids[i] = fmt.Sprint(id)
			}
			fmt.Fprintf(w, "false alarms, %s: books %s\n", f.Field, strings.Join(ids, " "))
		}
	}
	if r.Unrepresented > 0 {
		fmt.Fprintf(w, "%d of %d library books are in strata with no sampled book; the weights can't speak for them.\n", r.Unrepresented, r.Library)
	}
	fmt.Fprintln(w, "Recall is weighted by each stratum's library share. The interval is approximate: Clopper-Pearson at the effective sample size n_eff = (sum w)^2 / sum w^2 over the wrong books, so unequal weights (a design effect of n / n_eff) widen it.")
}
