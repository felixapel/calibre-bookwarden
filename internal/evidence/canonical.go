// Package evidence turns what a check read from Calibre into a sealed,
// versioned package: canonical JSON (RFC 8785) and its SHA-256.
package evidence

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// ErrNotCanonical: the value can't be written as the canonical JSON subset
// evidence uses (integers within ±(2^53−1), valid UTF-8 without NUL).
var ErrNotCanonical = errors.New("evidence: value has no canonical form")

const maxSafeInteger = 1<<53 - 1

// Canonical returns v as RFC 8785 canonical JSON: v is marshalled with
// encoding/json, then written back with keys sorted by UTF-16 code units,
// strings escaped as ECMAScript's JSON.stringify does, and integers only.
func Canonical(v any) ([]byte, error) {
	// encoding/json silently replaces invalid UTF-8, so check first.
	if err := validUTF8(reflect.ValueOf(v)); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("evidence: marshal: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var tree any
	if err := dec.Decode(&tree); err != nil {
		return nil, fmt.Errorf("evidence: decode: %w", err)
	}
	var out bytes.Buffer
	if err := writeValue(&out, tree); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

var rawMessage = reflect.TypeFor[json.RawMessage]()

// validUTF8 checks every string, map key and json.RawMessage in v.
func validUTF8(v reflect.Value) error {
	switch v.Kind() {
	case reflect.String:
		if !utf8.ValidString(v.String()) {
			return fmt.Errorf("%w: invalid UTF-8", ErrNotCanonical)
		}
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			return validUTF8(v.Elem())
		}
	case reflect.Struct:
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				if err := validUTF8(v.Field(i)); err != nil {
					return err
				}
			}
		}
	case reflect.Slice, reflect.Array:
		if v.Type() == rawMessage {
			if !utf8.Valid(v.Bytes()) {
				return fmt.Errorf("%w: invalid UTF-8", ErrNotCanonical)
			}
			return nil
		}
		for i := range v.Len() {
			if err := validUTF8(v.Index(i)); err != nil {
				return err
			}
		}
	case reflect.Map:
		for it := v.MapRange(); it.Next(); {
			if err := validUTF8(it.Key()); err != nil {
				return err
			}
			if err := validUTF8(it.Value()); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeValue(out *bytes.Buffer, v any) error {
	switch v := v.(type) {
	case nil:
		out.WriteString("null")
	case bool:
		out.WriteString(strconv.FormatBool(v))
	case json.Number:
		n, err := strconv.ParseInt(string(v), 10, 64)
		if err != nil || n > maxSafeInteger || n < -maxSafeInteger {
			return fmt.Errorf("%w: number %s", ErrNotCanonical, v)
		}
		out.WriteString(strconv.FormatInt(n, 10))
	case string:
		return writeString(out, v)
	case []any:
		out.WriteByte('[')
		for i, e := range v {
			if i > 0 {
				out.WriteByte(',')
			}
			if err := writeValue(out, e); err != nil {
				return err
			}
		}
		out.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		slices.SortFunc(keys, compareUTF16)
		out.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				out.WriteByte(',')
			}
			if err := writeString(out, k); err != nil {
				return err
			}
			out.WriteByte(':')
			if err := writeValue(out, v[k]); err != nil {
				return err
			}
		}
		out.WriteByte('}')
	default:
		return fmt.Errorf("%w: type %T", ErrNotCanonical, v)
	}
	return nil
}

func compareUTF16(a, b string) int {
	return slices.Compare(utf16.Encode([]rune(a)), utf16.Encode([]rune(b)))
}

func writeString(out *bytes.Buffer, s string) error {
	if strings.IndexByte(s, 0) >= 0 {
		return fmt.Errorf("%w: NUL in a string", ErrNotCanonical)
	}
	out.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			out.WriteString(`\"`)
		case '\\':
			out.WriteString(`\\`)
		case '\b':
			out.WriteString(`\b`)
		case '\f':
			out.WriteString(`\f`)
		case '\n':
			out.WriteString(`\n`)
		case '\r':
			out.WriteString(`\r`)
		case '\t':
			out.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(out, `\u%04x`, r)
				continue
			}
			out.WriteRune(r)
		}
	}
	out.WriteByte('"')
	return nil
}
