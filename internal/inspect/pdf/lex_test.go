package pdf_test

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/inspect/pdf"
)

func TestLex(t *testing.T) {
	tests := []struct {
		in   string
		want pdf.Object
	}{
		{`(a\(b\)c)`, pdf.String("a(b)c")},
		{`(nested (paren) ok)`, pdf.String("nested (paren) ok")},
		{`(\101\n)`, pdf.String("A\n")},
		{`(\0533)`, pdf.String("+3")},
		{"(ab\\\ncd)", pdf.String("abcd")},
		{"(ab\\\r\ncd)", pdf.String("abcd")},
		{"(a\r\nb)", pdf.String("a\nb")},
		{`(\q)`, pdf.String("q")},
		{`<48 65 6C6C6F>`, pdf.String("Hello")},
		{`<4>`, pdf.String("@")},
		{`/A#20B`, pdf.Name("A B")},
		{`/`, pdf.Name("")},
		{`-.5`, -0.5},
		{`+3`, int64(3)},
		{`1.`, 1.0},
		{`99999999999999999999`, 1e20},
		{"% comment\n42", int64(42)},
		{`12 0 R`, pdf.Ref{Num: 12, Gen: 0}},
		{`true`, true},
		{`null`, nil},
		{`[1 2 (x) /N]`, pdf.Array{int64(1), int64(2), pdf.String("x"), pdf.Name("N")}},
		{`[1 2 R 3]`, pdf.Array{pdf.Ref{Num: 1, Gen: 2}, int64(3)}},
		{`<< /A [1 (x)] /B << /C true >> /D 5 0 R >>`, pdf.Dict{
			"A": pdf.Array{int64(1), pdf.String("x")}, "B": pdf.Dict{"C": true}, "D": pdf.Ref{Num: 5, Gen: 0},
		}},
		{strings.Repeat("[", 64) + strings.Repeat("]", 64), nested(64)},
	}
	for _, tt := range tests {
		got, err := pdf.ParseObject([]byte(tt.in))
		if err != nil || !reflect.DeepEqual(got, tt.want) {
			t.Errorf("ParseObject(%.40q) = %#v, %v; want %#v", tt.in, got, err, tt.want)
		}
	}
	for _, in := range []string{
		strings.Repeat("[", 65) + strings.Repeat("]", 65),
		"<< /A >",
		"<4G>",
		"(unterminated",
		")",
	} {
		if got, err := pdf.ParseObject([]byte(in)); err == nil {
			t.Errorf("ParseObject(%.40q) = %#v, want an error", in, got)
		}
	}
	big := append(append([]byte("("), bytes.Repeat([]byte("a"), 17<<20)...), ')')
	if _, err := pdf.ParseObject(big); !errors.Is(err, pdf.ErrTooLarge) {
		t.Errorf("a 17 MiB string: %v, want ErrTooLarge", err)
	}
	many := "[" + strings.Repeat("1 ", 100_001) + "]"
	if _, err := pdf.ParseObject([]byte(many)); !errors.Is(err, pdf.ErrTooLarge) {
		t.Errorf("an array of 100,001 items: %v, want ErrTooLarge", err)
	}
}

func nested(n int) pdf.Object {
	var o pdf.Object = pdf.Array{}
	for range n - 1 {
		o = pdf.Array{o}
	}
	return o
}
