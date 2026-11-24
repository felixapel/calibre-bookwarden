package evidence_test

import (
	"errors"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

func TestCanonical(t *testing.T) {
	tests := []struct {
		name    string
		in      any
		want    string
		wantErr error
	}{
		{
			// RFC 8785 section 3.2.3: keys sort by UTF-16 code units, so the
			// emoji (a surrogate pair, D83D…) sorts before U+FB33.
			name: "RFC 8785 key order",
			in: map[string]string{
				"€": "Euro Sign", "\r": "Carriage Return", "\uFB33": "Hebrew Letter Dalet With Dagesh",
				"1": "One", "\U0001F600": "Emoji: Grinning Face", "\u0080": "Control",
				"ö": "Latin Small Letter O With Diaeresis",
			},
			want: `{"\r":"Carriage Return","1":"One","` + "\u0080" + `":"Control","ö":"Latin Small Letter O With Diaeresis","€":"Euro Sign","` + "\U0001F600" + `":"Emoji: Grinning Face","` + "\uFB33" + `":"Hebrew Letter Dalet With Dagesh"}`,
		},
		{name: "escapes", in: "q\"b\\c\b\f\n\r\t\u001f", want: `"q\"b\\c\b\f\n\r\t\u001f"`},
		{name: "no HTML or U+2028 escaping", in: "</script>& ", want: "\"</script>& \""},
		{name: "zero", in: 0, want: `0`},
		{name: "negative", in: -1, want: `-1`},
		{name: "largest safe integer", in: int64(9007199254740991), want: `9007199254740991`},
		{name: "smallest safe integer", in: int64(-9007199254740991), want: `-9007199254740991`},
		{name: "bools and null", in: []any{true, false, nil}, want: `[true,false,null]`},
		{name: "fraction", in: 1.5, wantErr: evidence.ErrNotCanonical},
		{name: "beyond 2^53", in: int64(9007199254740992), wantErr: evidence.ErrNotCanonical},
		{name: "NUL", in: "a\x00b", wantErr: evidence.ErrNotCanonical},
		{name: "NUL in a key", in: map[string]int{"a\x00": 1}, wantErr: evidence.ErrNotCanonical},
		{name: "invalid UTF-8", in: "\xff", wantErr: evidence.ErrNotCanonical},
		{name: "invalid UTF-8 in a key", in: map[string]int{"\xff": 1}, wantErr: evidence.ErrNotCanonical},
		{name: "a real U+FFFD is fine", in: "�", want: "\"�\""},
		{name: "an escaped backslash before ufffd is fine", in: "\\ufffd", want: `"\\ufffd"`},
		{name: "nested", in: []any{map[string]any{"b": 1, "a": []any{}}}, want: `[{"a":[],"b":1}]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := evidence.Canonical(tt.in)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Canonical() = %s, %v; want %v", got, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Errorf("Canonical()\n got %s\nwant %s", got, tt.want)
			}
		})
	}
}
