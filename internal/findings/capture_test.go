package findings

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

func markedPackage() evidence.Package {
	p := goodPackage()
	p.Formats[0].Metadata.Limits = &evidence.MetadataLimits{Version: 1}
	p.Formats[0].Metadata.ISBNs = []string{"9780306406157"}
	return p
}

func TestIncompleteFieldsExcluded(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(fmt.Sprint(empty), func(t *testing.T) {
			p := markedPackage()
			other := markedPackage().Formats[0]
			other.Format, other.Detected = "PDF", "pdf"
			p.Formats = append(p.Formats, other)
			m := p.Formats[0].Metadata
			m.Limits = &evidence.MetadataLimits{Version: 1, Title: true, Authors: true, Languages: true, ISBNs: true, Publisher: true, Date: true}
			m.Title, m.Authors, m.Languages = "Other", []string{"Other"}, []string{"en?"}
			m.ISBNs, m.Publisher, m.Date = []string{"9780804429573", "9780306406158"}, "Other", "2020-02-30"
			if empty {
				m.Title, m.Authors, m.Languages, m.ISBNs, m.Date = "", nil, nil, nil, ""
			}
			var want []evidence.Finding
			for _, field := range []string{"authors", "isbn", "languages", "pubdate", "publisher", "title"} {
				want = append(want, evidence.Finding{Code: "metadata_unverified", Format: "EPUB", Field: field, Reason: "field_too_long"})
			}
			before, _ := json.Marshal(p)
			got, err := Derive(context.Background(), p)
			if err != nil || !slices.Equal(got, want) {
				t.Fatalf("incomplete fields established missing/invalid/mismatch/conflict: %+v/%v", got, err)
			}
			after, _ := json.Marshal(p)
			if !bytes.Equal(before, after) {
				t.Fatal("capture derivation mutated input")
			}
			slices.Reverse(p.Formats)
			again, err := Derive(context.Background(), p)
			if err != nil || !slices.Equal(again, want) {
				t.Fatalf("capture derivation depends on format order: %+v/%v", again, err)
			}
		})
	}
}

func TestCaptureBoundaryCompatibility(t *testing.T) {
	authors := make([]string, 32)
	languages := make([]string, 32)
	isbns := make([]string, 16)
	for i := range authors {
		authors[i] = fmt.Sprintf("a%d", i)
		languages[i] = fmt.Sprintf("en-x-%d", i)
	}
	for i := range isbns {
		base := fmt.Sprintf("978000000%03d", i)
		sum := 0
		for j, c := range base {
			sum += int(c-'0') * (1 + 2*(j%2))
		}
		isbns[i] = fmt.Sprintf("%s%d", base, (10-sum%10)%10)
	}
	for _, tt := range []struct {
		name, field string
		edit        func(*evidence.FileMetadata)
	}{
		{"title511", "title", func(m *evidence.FileMetadata) { m.Title = strings.Repeat("a", 511) }},
		{"title512", "title", func(m *evidence.FileMetadata) { m.Title = strings.Repeat("a", 512) }},
		{"authors32", "authors", func(m *evidence.FileMetadata) { m.Authors = authors }},
		{"author511", "authors", func(m *evidence.FileMetadata) { m.Authors = []string{strings.Repeat("a", 511)} }},
		{"languages32", "languages", func(m *evidence.FileMetadata) { m.Languages = languages }},
		{"isbn16", "isbn", func(m *evidence.FileMetadata) { m.ISBNs = isbns }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := markedPackage()
			tt.edit(p.Formats[0].Metadata)
			got, err := Derive(context.Background(), p)
			if err != nil {
				t.Fatal(err)
			}
			want := evidence.Finding{Code: "metadata_unverified", Format: "EPUB", Field: tt.field, Reason: "capture_limit_unverified"}
			for _, f := range got {
				if f.Code == "metadata_unverified" && f.Format == "EPUB" && f.Field == tt.field {
					t.Fatalf("complete capture was qualified: %+v", got)
				}
			}
			p.Formats[0].Metadata.Limits = nil
			got, err = Derive(context.Background(), p)
			if err != nil || !slices.Contains(got, want) {
				t.Fatalf("historical cap uncertainty missing: %+v/%v", got, err)
			}
		})
	}
	p := markedPackage()
	p.Formats[0].Metadata.Authors = []string{strings.Repeat("a", 256), strings.Repeat("b", 256)}
	got, err := Derive(context.Background(), p)
	want := evidence.Finding{Code: "metadata_unverified", Format: "EPUB", Field: "authors", Reason: "field_too_long"}
	if err != nil || !slices.Contains(got, want) {
		t.Fatalf("whole-field separator allowance lost: %+v/%v", got, err)
	}
}

func TestCaptureSealValidation(t *testing.T) {
	ctx := context.Background()
	p := markedPackage()
	p.Formats[0].Metadata.Limits.Title = true
	raw, seal, err := Seal(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := Open(ctx, raw, seal)
	if err != nil || !opened.Formats[0].Metadata.Limits.Title {
		t.Fatalf("capture qualification lost in seal: %+v/%v", opened, err)
	}
	tampered := bytes.Replace(raw, []byte(`"title":true`), []byte(`"title":false`), 1)
	if _, err := Open(ctx, tampered, seal); !errors.Is(err, evidence.ErrSealBroken) {
		t.Fatalf("modified capture marker accepted: %v", err)
	}
	for _, edit := range []func(*evidence.FileMetadata){
		func(m *evidence.FileMetadata) { m.Limits.Version = 99 },
		func(m *evidence.FileMetadata) { m.Title = strings.Repeat("a", 513) },
		func(m *evidence.FileMetadata) { m.Authors = make([]string, 33) },
		func(m *evidence.FileMetadata) { m.Languages = []string{strings.Repeat("a", 513)} },
		func(m *evidence.FileMetadata) { m.ISBNs = make([]string, 17) },
		func(m *evidence.FileMetadata) { m.ISBNs = []string{"97803064061570"} },
	} {
		for _, state := range []evidence.State{evidence.StateOK, evidence.StateDRM} {
			p = markedPackage()
			p.Formats[0].State = state
			edit(p.Formats[0].Metadata)
			if _, _, err := Seal(ctx, p); !errors.Is(err, ErrInvalidEvidence) {
				t.Fatalf("bad capture marker accepted in %s: %v", state, err)
			}
		}
	}
}
