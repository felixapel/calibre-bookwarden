package eval

import (
	"errors"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/store"
)

func TestCleanAnswers(t *testing.T) {
	tests := []struct {
		name    string
		in      []store.Confirmation
		wantErr bool
		typed   string // the typed value left on the first answer, "" for none
	}{
		{"plain", []store.Confirmation{ans(0, "title", store.AnswerRight, nil)}, false, ""},
		{"typed is trimmed", []store.Confirmation{ans(0, "title", store.AnswerWrong, typed("  Dune \n"))}, false, "Dune"},
		{"blank typed is none", []store.Confirmation{ans(0, "title", store.AnswerWrong, typed("   "))}, false, ""},
		{"blank typed with right is none", []store.Confirmation{ans(0, "title", store.AnswerRight, typed(""))}, false, ""},
		{"typed with right is wrong", []store.Confirmation{ans(0, "title", store.AnswerRight, typed("Dune"))}, false, "Dune"},
		{"typed with skip is wrong", []store.Confirmation{ans(0, "title", store.AnswerSkip, typed("Dune"))}, false, "Dune"},
		{"unknown field", []store.Confirmation{ans(0, "series", store.AnswerRight, nil)}, true, ""},
		{"unknown answer", []store.Confirmation{ans(0, "title", "maybe", nil)}, true, ""},
		{"field twice", []store.Confirmation{ans(0, "title", store.AnswerRight, nil), ans(0, "title", store.AnswerSkip, nil)}, true, ""},
		{"too long", []store.Confirmation{ans(0, "title", store.AnswerWrong, typed(strings.Repeat("x", maxTyped+1)))}, true, ""},
		{"control character", []store.Confirmation{ans(0, "title", store.AnswerWrong, typed("a\x00b"))}, true, ""},
		{"invalid utf-8", []store.Confirmation{ans(0, "title", store.AnswerWrong, typed("a\xffb"))}, true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := cleanAnswers(tt.in, "admin")
			if tt.wantErr {
				if !errors.Is(err, ErrInvalidAnswer) {
					t.Fatalf("err = %v, want ErrInvalidAnswer", err)
				}
				return
			}
			if err != nil || len(got) != len(tt.in) || got[0].By != "admin" {
				t.Fatalf("got %+v, %v", got, err)
			}
			if tt.typed != "" && got[0].Answer != store.AnswerWrong {
				t.Errorf("answer %s, want wrong: a typed value implies it", got[0].Answer)
			}
			if tt.typed == "" && got[0].Typed != nil || tt.typed != "" && (got[0].Typed == nil || *got[0].Typed != tt.typed) {
				t.Errorf("typed = %v, want %q", got[0].Typed, tt.typed)
			}
		})
	}
}
