package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Answers a judge may give for a proposal.
const (
	Approve = "approve"
	Reject  = "reject"
	Unsure  = "unsure"
)

// maxReason is how many bytes of a judge's reason are kept.
const maxReason = 300

// ErrBadAnswer: a judge's answer isn't exactly one verdict per asked field.
var ErrBadAnswer = errors.New("agent: unusable answer")

// Judgement is a judge's verdict on one proposal.
type Judgement struct{ Answer, Reason string }

// ParseAnswer reads a judge's answer: one JSON object, possibly in a code
// fence or between prose, with exactly one verdict per field asked about.
// Anything else is ErrBadAnswer, which leaves the proposals for a person.
func ParseAnswer(text string, fields []string) (map[string]Judgement, error) {
	obj, err := onlyObject(text)
	if err != nil {
		return nil, err
	}
	var a struct {
		Verdicts []struct {
			Field   string `json:"field"`
			Verdict string `json:"verdict"`
			Reason  string `json:"reason"`
		} `json:"verdicts"`
	}
	if err := json.Unmarshal([]byte(obj), &a); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBadAnswer, err)
	}
	asked := map[string]bool{}
	for _, f := range fields {
		asked[f] = true
	}
	out := map[string]Judgement{}
	for _, v := range a.Verdicts {
		if !asked[v.Field] {
			return nil, fmt.Errorf("%w: unexpected field %q", ErrBadAnswer, v.Field)
		}
		if _, dup := out[v.Field]; dup {
			return nil, fmt.Errorf("%w: field %q twice", ErrBadAnswer, v.Field)
		}
		answer := strings.ToLower(strings.TrimSpace(v.Verdict))
		if answer != Approve && answer != Reject && answer != Unsure {
			return nil, fmt.Errorf("%w: verdict %q", ErrBadAnswer, v.Verdict)
		}
		reason := strings.TrimSpace(v.Reason)
		if reason == "" {
			return nil, fmt.Errorf("%w: no reason for %q", ErrBadAnswer, v.Field)
		}
		out[v.Field] = Judgement{Answer: answer, Reason: truncate(reason, maxReason)}
	}
	if len(out) != len(asked) {
		return nil, fmt.Errorf("%w: %d of %d fields answered", ErrBadAnswer, len(out), len(asked))
	}
	return out, nil
}

// onlyObject returns the one top-level JSON object in text.
func onlyObject(text string) (string, error) {
	start := strings.Index(text, "{")
	if start < 0 {
		return "", fmt.Errorf("%w: no JSON object", ErrBadAnswer)
	}
	var found string
	for i := start; i >= 0 && i < len(text); {
		d := json.NewDecoder(strings.NewReader(text[i:]))
		var raw json.RawMessage
		if err := d.Decode(&raw); err != nil || len(raw) == 0 || raw[0] != '{' {
			return "", fmt.Errorf("%w: no JSON object", ErrBadAnswer)
		}
		if found != "" {
			return "", fmt.Errorf("%w: more than one JSON object", ErrBadAnswer)
		}
		found = string(raw)
		rest := i + int(d.InputOffset())
		next := strings.Index(text[rest:], "{")
		if next < 0 {
			break
		}
		i = rest + next
	}
	return found, nil
}

// truncate cuts s to at most n bytes at a rune boundary.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
