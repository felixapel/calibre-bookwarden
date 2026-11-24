package findings

import (
	"slices"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"
	"golang.org/x/text/unicode/norm"
)

type fieldState int

const (
	fieldMissing fieldState = iota
	fieldValid
	fieldInvalid
	fieldTooLong
	fieldUndetermined
)
const maxFieldRunes = 512

func normalizeField(s string) (string, fieldState) {
	if utf8.RuneCountInString(s) > maxFieldRunes {
		return "", fieldTooLong
	}
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return "", fieldMissing
	}
	return norm.NFC.String(cases.Fold().String(norm.NFC.String(s))), fieldValid
}

func normalizeStrings(in []string) ([]string, fieldState) {
	var out []string
	for _, s := range in {
		n, state := normalizeField(s)
		if state == fieldTooLong {
			return nil, state
		}
		if state == fieldValid {
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		return nil, fieldMissing
	}
	return out, fieldValid
}

func normalizeLanguages(in []string) ([]string, fieldState) {
	var out []string
	state := fieldMissing
	for _, s := range in {
		if utf8.RuneCountInString(s) > maxFieldRunes {
			return nil, fieldTooLong
		}
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		tag, err := language.Parse(s)
		if err != nil {
			state = fieldInvalid
			continue
		}
		// Base infers a likely language for undetermined tags. Raw preserves
		// the explicit primary language, so uncertainty cannot become agreement.
		base, _, _ := tag.Raw()
		if base.String() == "und" {
			if state != fieldInvalid {
				state = fieldUndetermined
			}
			continue
		}
		out = append(out, base.String())
	}
	if state == fieldInvalid || state == fieldUndetermined {
		return nil, state
	}
	if len(out) == 0 {
		return nil, fieldMissing
	}
	slices.Sort(out)
	return slices.Compact(out), fieldValid
}
