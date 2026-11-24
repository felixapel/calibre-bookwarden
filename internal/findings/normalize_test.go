package findings

import (
	"reflect"
	"strings"
	"testing"
)

func TestCompareFields(t *testing.T) {
	t.Run("unicode", func(t *testing.T) {
		a, state := normalizeField("  Cafe\u0301\tBOOK  ")
		b, _ := normalizeField("CAFÉ book")
		if state != fieldValid || a != b {
			t.Fatal("NFC, case and whitespace did not agree")
		}
	})
	t.Run("accents", func(t *testing.T) {
		a, _ := normalizeField("García")
		b, _ := normalizeField("Garcia")
		if a == b {
			t.Fatal("accent was erased")
		}
	})
	t.Run("authors", func(t *testing.T) {
		x, sx := normalizeStrings([]string{" Ada ", "Bob"})
		y, _ := normalizeStrings([]string{"Bob", "Ada"})
		if sx != fieldValid || reflect.DeepEqual(x, y) {
			t.Fatal("author order was erased")
		}
	})
	t.Run("size", func(t *testing.T) {
		if _, s := normalizeField(strings.Repeat("é", 513)); s != fieldTooLong {
			t.Fatal("oversized field compared")
		}
	})
	t.Run("missing", func(t *testing.T) {
		if _, s := normalizeField(" \t "); s != fieldMissing {
			t.Fatal("blank field not missing")
		}
	})
	for _, tt := range []struct {
		in    []string
		want  []string
		state fieldState
	}{
		{[]string{"eng", "en", "de-DE"}, []string{"de", "en"}, fieldValid},
		{[]string{"en-US"}, []string{"en"}, fieldValid},
		{[]string{"en?"}, nil, fieldInvalid}, {[]string{"und"}, nil, fieldUndetermined},
		{nil, nil, fieldMissing}, {[]string{"en", strings.Repeat("a", 513)}, nil, fieldTooLong},
	} {
		t.Run("language/"+strings.Join(tt.in, ","), func(t *testing.T) {
			got, s := normalizeLanguages(tt.in)
			if !reflect.DeepEqual(got, tt.want) || s != tt.state {
				t.Fatalf("languages %v = %v/%v; want %v/%v", tt.in, got, s, tt.want, tt.state)
			}
		})
	}
}
