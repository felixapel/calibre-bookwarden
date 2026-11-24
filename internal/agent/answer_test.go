package agent

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestParseAnswer(t *testing.T) {
	fields := []string{"isbn", "publisher"}
	both := `{"verdicts":[{"field":"publisher","verdict":"reject","reason":"imprint swap"},{"field":"isbn","verdict":"approve","reason":"file carries it"}]}`
	long := strings.Repeat("é", 200)
	for _, tc := range []struct {
		name string
		text string
		want map[string]Judgement
		bad  bool
	}{
		{"plain", both, map[string]Judgement{"publisher": {"reject", "imprint swap"}, "isbn": {"approve", "file carries it"}}, false},
		{"fenced", "```json\n" + both + "\n```", map[string]Judgement{"publisher": {"reject", "imprint swap"}, "isbn": {"approve", "file carries it"}}, false},
		{"prose around", "Here is my review.\n" + both + "\nDone.", map[string]Judgement{"publisher": {"reject", "imprint swap"}, "isbn": {"approve", "file carries it"}}, false},
		{"uppercase", `{"verdicts":[{"field":"publisher","verdict":"REJECT","reason":"x"},{"field":"isbn","verdict":"Unsure","reason":"y"}]}`,
			map[string]Judgement{"publisher": {"reject", "x"}, "isbn": {"unsure", "y"}}, false},
		{"two objects", both + "\n" + both, nil, true},
		{"missing field", `{"verdicts":[{"field":"isbn","verdict":"approve","reason":"x"}]}`, nil, true},
		{"extra field", `{"verdicts":[{"field":"isbn","verdict":"approve","reason":"x"},{"field":"publisher","verdict":"reject","reason":"x"},{"field":"title","verdict":"approve","reason":"x"}]}`, nil, true},
		{"duplicate field", `{"verdicts":[{"field":"isbn","verdict":"approve","reason":"x"},{"field":"isbn","verdict":"reject","reason":"x"}]}`, nil, true},
		{"unknown verdict", `{"verdicts":[{"field":"isbn","verdict":"maybe","reason":"x"},{"field":"publisher","verdict":"reject","reason":"x"}]}`, nil, true},
		{"empty reason", `{"verdicts":[{"field":"isbn","verdict":"approve","reason":" "},{"field":"publisher","verdict":"reject","reason":"x"}]}`, nil, true},
		{"not JSON", "approve everything", nil, true},
		{"empty", "", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseAnswer(tc.text, fields)
			if tc.bad {
				if !errors.Is(err, ErrBadAnswer) {
					t.Fatalf("ParseAnswer = %v, %v; want ErrBadAnswer", got, err)
				}
				return
			}
			if err != nil || len(got) != len(tc.want) {
				t.Fatalf("ParseAnswer = %v, %v", got, err)
			}
			for f, j := range tc.want {
				if got[f] != j {
					t.Errorf("%s = %+v, want %+v", f, got[f], j)
				}
			}
		})
	}
	t.Run("long reason", func(t *testing.T) {
		got, err := ParseAnswer(`{"verdicts":[{"field":"isbn","verdict":"approve","reason":"`+long+`"},{"field":"publisher","verdict":"reject","reason":"x"}]}`, fields)
		if err != nil {
			t.Fatal(err)
		}
		if r := got["isbn"].Reason; len(r) > 300 || !utf8.ValidString(r) || !strings.HasPrefix(long, r) {
			t.Fatalf("reason %d bytes, valid %v", len(r), utf8.ValidString(r))
		}
	})
}
