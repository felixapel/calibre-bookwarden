package extraction

import (
	"context"
	"reflect"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

func sealedBlock(t *testing.T, answerA, answerB string) (*evidence.Extraction, map[string]string) {
	t.Helper()
	a := &fakeSeat{text: answerA, model: "m-a"}
	b := &fakeSeat{text: answerB, model: "m-b"}
	s := service(t, mapCache{}, map[string]int{"a": 5, "b": 5}, ext("a", "fa", a), ext("b", "fb", b))
	x, raws, err := s.Extract(context.Background(), excerpt(), body)
	if err != nil {
		t.Fatal(err)
	}
	stored := map[string]string{}
	for _, r := range raws {
		stored[r.SHA256] = r.Body
	}
	return x, stored
}

func TestRederiveOfACurrentBlockChangesNothing(t *testing.T) {
	x, raws := sealedBlock(t, titleAnswer, titleAnswer)
	d, err := Rederive(excerpt(), body, *x, raws)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(d.Claims, x.Claims) || !reflect.DeepEqual(d.Agreed, x.Agreed) {
		t.Fatalf("rederived %+v / %+v, sealed %+v / %+v", d.Claims, d.Agreed, x.Claims, x.Agreed)
	}
	if d.StateChanged != 0 || d.Unsealable || len(d.Verified) != len(x.Claims) {
		t.Fatalf("rederived = %+v", d)
	}
}

func TestRederiveParseStateTransitions(t *testing.T) {
	x, raws := sealedBlock(t, titleAnswer, "not json")
	if x.Extractors[1].State != evidence.ExtractInvalidOutput {
		t.Fatalf("setup: %+v", x.Extractors)
	}
	for name, tc := range map[string]struct {
		edit        func(x *evidence.Extraction)
		wantChanged int
		wantTitle   int // agreed voices for the title
	}{
		"as sealed": {func(*evidence.Extraction) {}, 0, 1},
		// b's sealed invalid_output raw is replaced by one that parses now.
		"invalid_output now parses": {func(x *evidence.Extraction) { x.Extractors[1].RawSHA256 = x.Extractors[0].RawSHA256 }, 1, 2},
		// a's sealed ok answer is replaced by one that no longer parses.
		"ok no longer parses": {func(x *evidence.Extraction) { x.Extractors[0].RawSHA256 = x.Extractors[1].RawSHA256 }, 1, 0},
	} {
		t.Run(name, func(t *testing.T) {
			c := *x
			c.Extractors = append([]evidence.ExtractorRun(nil), x.Extractors...)
			tc.edit(&c)
			d, err := Rederive(excerpt(), body, c, raws)
			if err != nil {
				t.Fatal(err)
			}
			if d.StateChanged != tc.wantChanged || d.Unsealable {
				t.Fatalf("changed %d, unsealable %v", d.StateChanged, d.Unsealable)
			}
			voices := 0
			for _, f := range d.Agreed {
				if f.Key == "title" {
					voices = len(f.Voices)
				}
			}
			if voices != tc.wantTitle {
				t.Fatalf("title voices = %d, want %d: %+v", voices, tc.wantTitle, d.Agreed)
			}
		})
	}
}

func TestRederiveFlagsABlockThatCouldNotSeal(t *testing.T) {
	x, raws := sealedBlock(t, titleAnswer, titleAnswer)
	// An extractor with no name can't be sealed, whatever its claims say.
	c := *x
	c.Extractors = append([]evidence.ExtractorRun(nil), x.Extractors...)
	c.Extractors[0].Name = ""
	d, err := Rederive(excerpt(), body, c, raws)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Unsealable {
		t.Fatal("a block Check refuses is not flagged")
	}
}
