package extraction

import (
	"context"
	"errors"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/extract"
)

func TestReverify(t *testing.T) {
	a := &fakeSeat{text: titleAnswer, model: "m-a"}
	b := &fakeSeat{text: "not json", model: "m-b"}
	s := service(t, mapCache{}, map[string]int{"a": 5, "b": 5}, ext("a", "fa", a), ext("b", "fb", b))
	x, raws, err := s.Extract(context.Background(), excerpt(), body)
	if err != nil {
		t.Fatal(err)
	}
	stored := map[string]string{}
	for _, r := range raws {
		stored[r.SHA256] = r.Body
	}
	if err := Reverify(excerpt(), body, *x, stored); err != nil {
		t.Fatalf("Reverify of what was sealed: %v", err)
	}
}

func TestReverifyDetectsAlteredRaw(t *testing.T) {
	a := &fakeSeat{text: titleAnswer, model: "m-a"}
	s := service(t, mapCache{}, map[string]int{"a": 5}, ext("a", "fa", a))
	x, raws, err := s.Extract(context.Background(), excerpt(), body)
	if err != nil {
		t.Fatal(err)
	}
	for name, stored := range map[string]map[string]string{
		"missing": {},
		"altered": {raws[0].SHA256: `{"claims":[]}`},
	} {
		if err := Reverify(excerpt(), body, *x, stored); !errors.Is(err, ErrReverify) {
			t.Errorf("%s raw: err = %v, want ErrReverify", name, err)
		}
	}
	// A claim edited after sealing is caught too.
	x.Claims[0].Start++
	if err := Reverify(excerpt(), body, *x, map[string]string{raws[0].SHA256: raws[0].Body}); !errors.Is(err, ErrReverify) {
		t.Errorf("edited claim: err = %v", err)
	}
}

func TestReverifyLeavesOtherPromptsAlone(t *testing.T) {
	a := &fakeSeat{text: titleAnswer, model: "m-a"}
	s := service(t, mapCache{}, map[string]int{"a": 5}, ext("a", "fa", a))
	x, raws, err := s.Extract(context.Background(), excerpt(), body)
	if err != nil {
		t.Fatal(err)
	}
	x.PromptVersion = "extract-1" // parsed by older rules
	if err := Reverify(excerpt(), body, *x, map[string]string{raws[0].SHA256: raws[0].Body}); !errors.Is(err, ErrOtherVersion) {
		t.Fatalf("err = %v, want ErrOtherVersion", err)
	}
}

func TestReverifyLeavesOtherVerifierVersionsAlone(t *testing.T) {
	a := &fakeSeat{text: titleAnswer, model: "m-a"}
	s := service(t, mapCache{}, map[string]int{"a": 5}, ext("a", "fa", a))
	x, raws, err := s.Extract(context.Background(), excerpt(), body)
	if err != nil {
		t.Fatal(err)
	}
	x.ExtractVersion = extract.Version - 1 // judged by the previous rules
	if err := Reverify(excerpt(), body, *x, map[string]string{raws[0].SHA256: raws[0].Body}); !errors.Is(err, ErrOtherVersion) {
		t.Fatalf("err = %v, want ErrOtherVersion", err)
	}
}
