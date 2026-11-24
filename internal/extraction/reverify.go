package extraction

import (
	"errors"
	"fmt"
	"reflect"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/extract"
	"github.com/felixapel/calibre-bookwarden/internal/extractor"
)

// ErrReverify means a sealed extraction is not what its stored raw answers
// and excerpt derive. The message names what differs, never text.
var ErrReverify = errors.New("extraction: does not re-verify")

// ErrOtherVersion means the extraction was sealed under another prompt or
// other verifier rules; only extractions of this build's
// extractor.PromptVersion and extract.Version are re-verified.
var ErrOtherVersion = errors.New("extraction: sealed under other verifier rules")

// Reverify re-derives a sealed extraction from the excerpt's body and the
// raw answers by hash, and requires the same claims and agreement (design
// section 8).
func Reverify(ex evidence.Excerpt, body string, sealed evidence.Extraction, raws map[string]string) error {
	// The answers are parsed and verified by today's rules: a block sealed
	// under another prompt's parsing or other verifier rules is left alone.
	if sealed.ExtractVersion != extract.Version || sealed.PromptVersion != extractor.PromptVersion {
		return ErrOtherVersion
	}
	if hashOf(body) != ex.SHA256 || sealed.ExcerptSHA256 != ex.SHA256 {
		return fmt.Errorf("%w: the excerpt", ErrReverify)
	}
	ex.Text = body
	x := extract.NewExcerpt(ex).Prepared()
	byName := map[string][]extract.Claim{}
	for _, r := range sealed.Extractors {
		if r.RawSHA256 == "" {
			continue
		}
		raw, ok := raws[r.RawSHA256]
		if !ok || hashOf(raw) != r.RawSHA256 {
			return fmt.Errorf("%w: %s's raw answer is missing or altered", ErrReverify, r.Name)
		}
		claims, err := extractor.ParseOutput(raw)
		switch {
		case r.State == evidence.ExtractOK && err != nil:
			return fmt.Errorf("%w: %s's raw answer no longer parses", ErrReverify, r.Name)
		case r.State == evidence.ExtractInvalidOutput && err == nil:
			return fmt.Errorf("%w: %s's raw answer parses now", ErrReverify, r.Name)
		case r.State == evidence.ExtractOK:
			byName[r.Name] = withSource(claims, r.Name)
		}
	}
	claims, agreed := judge(x, byName, names(sealed.Extractors))
	if !reflect.DeepEqual(claims, sealed.Claims) {
		return fmt.Errorf("%w: the claims", ErrReverify)
	}
	if !reflect.DeepEqual(agreed, sealed.Agreed) {
		return fmt.Errorf("%w: the agreement", ErrReverify)
	}
	return nil
}

// Rederived is what today's rules make of a stored extraction: the sealable
// claims and agreed facts, and the verifier's verdicts, which keep the role
// and label a claim asked for even when it was not grounded.
type Rederived struct {
	Claims   []evidence.ExtractedClaim
	Agreed   []evidence.AgreedFact
	Verified []extract.Verified
	// StateChanged counts the extractors whose raw answer parses now
	// differently from the state sealed (ok or invalid_output).
	StateChanged int
	// Unsealable says Extract would have emptied the block: it fails
	// evidence.Extraction.Check when built from today's states and claims.
	Unsealable bool
}

// Rederive runs today's parsers, verifier and agreement over the excerpt's
// body and the stored raw answers, as Extract does after the models answer
// (including its Check that the block can be sealed), and does not compare
// with what was sealed. Unlike Reverify it accepts a block sealed under other
// rules, which is the point: to measure them. An answer that parses now is ok
// and one that doesn't is invalid_output, whatever state it was sealed in;
// the other states are kept. A body or raw answer that doesn't match its hash
// is ErrReverify.
func Rederive(ex evidence.Excerpt, body string, sealed evidence.Extraction, raws map[string]string) (Rederived, error) {
	if hashOf(body) != ex.SHA256 || sealed.ExcerptSHA256 != ex.SHA256 {
		return Rederived{}, fmt.Errorf("%w: the excerpt", ErrReverify)
	}
	ex.Text = body
	x := extract.NewExcerpt(ex).Prepared()
	byName := map[string][]extract.Claim{}
	now := evidence.Extraction{PromptVersion: extractor.PromptVersion, ExtractVersion: extract.Version, ExcerptSHA256: ex.SHA256}
	changed := 0
	for _, r := range sealed.Extractors {
		if r.RawSHA256 != "" {
			raw, ok := raws[r.RawSHA256]
			if !ok || hashOf(raw) != r.RawSHA256 {
				return Rederived{}, fmt.Errorf("%w: %s's raw answer is missing or altered", ErrReverify, r.Name)
			}
			state := evidence.ExtractOK
			if claims, err := extractor.ParseOutput(raw); err != nil {
				state = evidence.ExtractInvalidOutput
			} else {
				byName[r.Name] = withSource(claims, r.Name)
			}
			if state != r.State {
				changed++
			}
			r.State = state
		}
		now.Extractors = append(now.Extractors, r)
	}
	claims, agreed, vs := judgeVerified(x, byName, names(sealed.Extractors))
	now.Claims, now.Agreed = claims, agreed
	return Rederived{Claims: claims, Agreed: agreed, Verified: vs, StateChanged: changed, Unsealable: now.Check(&ex) != nil}, nil
}
