package findings

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

func TestFindingsSealOpen(t *testing.T) {
	ctx := context.Background()
	p := goodPackage()
	raw, hash, err := Seal(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Open(ctx, raw, hash)
	if err != nil || got.FindingsVersion != Version || len(got.Findings) != 0 {
		t.Fatalf("clean version: %+v/%v", got, err)
	}
	p.Formats[0].State = evidence.StateMissing
	p.Formats[0].Reason = evidence.ReasonMissingOnDisk
	raw, hash, err = Seal(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	got, err = Open(ctx, raw, hash)
	if err != nil || len(got.Findings) != 1 || got.Findings[0].Code != "file_missing" {
		t.Fatalf("sealed findings: %+v/%v", got, err)
	}
	legacy, legacyHash, err := evidence.Seal(p)
	if err != nil {
		t.Fatal(err)
	}
	before := bytes.Clone(legacy)
	got, err = Open(ctx, legacy, legacyHash)
	if err != nil || got.FindingsVersion != 0 || len(got.Findings) != 1 || !bytes.Equal(legacy, before) {
		t.Fatalf("legacy: %+v/%v", got, err)
	}
	opened, err := evidence.Open(legacy, legacyHash)
	if err != nil {
		t.Fatal(err)
	}
	again, againHash, err := evidence.Seal(opened)
	if err != nil || againHash != legacyHash || !bytes.Equal(again, legacy) {
		t.Fatal("legacy seal changed")
	}
	for _, tt := range []struct {
		version int
		fs      []evidence.Finding
		want    error
	}{
		{0, []evidence.Finding{{Code: "file_missing"}}, ErrInvalidEvidence},
		{1, []evidence.Finding{{Code: "isbn_missing", Field: "isbn"}}, ErrMismatch},
		{Version, []evidence.Finding{{Code: "isbn_missing", Field: "isbn"}}, ErrMismatch},
		{Version + 1, nil, ErrUnknownVersion}, {-1, nil, ErrUnknownVersion},
	} {
		p.FindingsVersion = tt.version
		p.Findings = tt.fs
		raw, hash, err = evidence.Seal(p)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = Open(ctx, raw, hash); !errors.Is(err, tt.want) {
			t.Fatalf("version %d: %v, want %v", tt.version, err, tt.want)
		}
	}
	if _, err = Open(ctx, raw, strings.Repeat("0", 64)); !errors.Is(err, evidence.ErrSealBroken) {
		t.Fatalf("seal not checked first: %v", err)
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err = Seal(ctx, goodPackage()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled seal: %v", err)
	}
}
