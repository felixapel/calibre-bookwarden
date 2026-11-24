package evidence_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/library"
)

func book() library.Book {
	return library.Book{
		ID: 3, UUID: "0c57e75c-16aa-4d88-ae0e-916c8f0e8b8e", Title: "Binary Tides",
		Authors: []string{"Cy Placeholder"}, AuthorSort: "Placeholder, Cy", Languages: []string{"eng"},
		Identifiers:  map[string]string{"isbn": "9780000000026"},
		LastModified: time.Date(2026, 10, 12, 21, 5, 55, 0, time.UTC),
		Formats: []library.Format{
			{Name: "AZW3", Size: 11075, MTime: time.Date(2026, 10, 12, 21, 5, 55, 498462000, time.UTC)},
			{Name: "EPUB", Size: 2326, MTime: time.Date(2026, 10, 12, 21, 5, 55, 24520000, time.UTC)},
		},
	}
}

func pkg(t *testing.T) evidence.Package {
	t.Helper()
	b := book()
	snap, err := evidence.Snapshot(b)
	if err != nil {
		t.Fatal(err)
	}
	return evidence.Package{
		SchemaVersion: evidence.SchemaVersionV1, LibraryID: "library", BookID: b.ID, UUID: b.UUID,
		Snapshot: snap, Calibre: evidence.RecordFrom(b),
		Formats: []evidence.FormatEvidence{
			{Format: "AZW3", Size: 11075, SHA256: strings.Repeat("a", 64), State: evidence.StateNotInspected, Reason: evidence.ReasonNoInspector},
			{Format: "EPUB", Size: 2326, State: evidence.StateMissing, Reason: evidence.ReasonMissingOnDisk},
		},
	}
}

func TestSealOpenRoundTrip(t *testing.T) {
	p := pkg(t)
	raw, seal, err := evidence.Seal(p)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(raw)
	if seal != hex.EncodeToString(h[:]) {
		t.Errorf("seal %s isn't the SHA-256 of raw", seal)
	}
	canon, err := evidence.Canonical(p)
	if err != nil || !bytes.Equal(raw, canon) {
		t.Errorf("raw isn't Canonical(p): %v", err)
	}
	got, err := evidence.Open(raw, seal)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, p) {
		t.Errorf("Open(Seal(p)):\n got %+v\nwant %+v", got, p)
	}
}

func TestOpenAcceptsReformattedJSON(t *testing.T) {
	p := pkg(t)
	raw, seal, err := evidence.Seal(p)
	if err != nil {
		t.Fatal(err)
	}
	// Re-indent and reverse the top-level keys, the way jsonb hands it back.
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatal(err)
	}
	keys := []string{"uuid", "snapshot", "schema_version", "library_id", "formats", "calibre_record", "book_id"}
	var b strings.Builder
	b.WriteString("{\n")
	for i, k := range keys {
		var v bytes.Buffer
		if err := json.Indent(&v, top[k], "  ", "    "); err != nil {
			t.Fatal(err)
		}
		b.WriteString(`  "` + k + `": ` + v.String())
		if i < len(keys)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString("}")
	if len(keys) != len(top) {
		t.Fatalf("package has %d keys, test lists %d", len(top), len(keys))
	}
	got, err := evidence.Open([]byte(b.String()), seal)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, p) {
		t.Errorf("reformatted package opened as %+v", got)
	}
}

func TestOpenRejects(t *testing.T) {
	p := pkg(t)
	raw, seal, err := evidence.Seal(p)
	if err != nil {
		t.Fatal(err)
	}
	sealAs := func(v any) ([]byte, string) {
		b, err := evidence.Canonical(v)
		if err != nil {
			t.Fatal(err)
		}
		h := sha256.Sum256(b)
		return b, hex.EncodeToString(h[:])
	}
	future := p
	future.SchemaVersion = evidence.SchemaVersion + 1
	futureRaw, futureSeal := sealAs(future)

	var extra map[string]any
	if err := json.Unmarshal(raw, &extra); err != nil {
		t.Fatal(err)
	}
	extra["comment"] = "added later"
	extraRaw, extraSeal := sealAs(extra)

	tests := []struct {
		name    string
		raw     []byte
		seal    string
		wantErr error
	}{
		{name: "title changed after sealing", raw: bytes.Replace(raw, []byte("Binary Tides"), []byte("Binary Tidez"), 1), seal: seal, wantErr: evidence.ErrSealBroken},
		{name: "a later schema version", raw: futureRaw, seal: futureSeal, wantErr: evidence.ErrUnknownSchema},
		{name: "unknown field", raw: extraRaw, seal: extraSeal},
		{name: "not JSON", raw: []byte("{"), seal: seal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := evidence.Open(tt.raw, tt.seal)
			if err == nil {
				t.Fatal("Open() succeeded")
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Errorf("err = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestSnapshot(t *testing.T) {
	base, err := evidence.Snapshot(book())
	if err != nil {
		t.Fatal(err)
	}
	if len(base) != 64 {
		t.Fatalf("snapshot %q isn't hex SHA-256", base)
	}
	tests := []struct {
		name    string
		edit    func(b *library.Book)
		changes bool
	}{
		{"last modified", func(b *library.Book) { b.LastModified = b.LastModified.Add(time.Second) }, true},
		{"a format's size", func(b *library.Book) { b.Formats[1].Size++ }, true},
		{"a format's mtime", func(b *library.Book) { b.Formats[0].MTime = b.Formats[0].MTime.Add(time.Microsecond) }, true},
		{"identifiers", func(b *library.Book) { b.Identifiers = map[string]string{"isbn": "9780000000019"} }, true},
		{"uuid", func(b *library.Book) { b.UUID = "another" }, true},
		{"a format added", func(b *library.Book) {
			b.Formats = append(b.Formats, library.Format{Name: "PDF", Size: 1, MTime: b.LastModified})
		}, true},
		{"title only", func(b *library.Book) { b.Title = "Another Title" }, false},
		{"formats order", func(b *library.Book) { b.Formats[0], b.Formats[1] = b.Formats[1], b.Formats[0] }, false},
		{"same instant in another zone", func(b *library.Book) {
			b.LastModified = b.LastModified.In(time.FixedZone("CEST", 2*3600))
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := book()
			tt.edit(&b)
			got, err := evidence.Snapshot(b)
			if err != nil {
				t.Fatal(err)
			}
			if (got != base) != tt.changes {
				t.Errorf("snapshot changed = %v, want %v", got != base, tt.changes)
			}
		})
	}
}

func TestRecordFrom(t *testing.T) {
	r := evidence.RecordFrom(library.Book{LastModified: time.Date(2026, 10, 12, 21, 5, 55, 0, time.UTC)})
	got, err := evidence.Canonical(r)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"author_sort":"","authors":[],"has_cover":false,"identifiers":{},"languages":[],"last_modified":"2026-10-12T21:05:55Z","pubdate":"","publisher":"","title":""}`
	if string(got) != want {
		t.Errorf("RecordFrom(empty book)\n got %s\nwant %s", got, want)
	}
}

func TestFormatEvidenceRoundTrip(t *testing.T) {
	// Formats without the M2.2 fields seal to exactly the M2.1 bytes.
	if _, seal, err := evidence.Seal(pkg(t)); err != nil || seal != "8509f231af0478e8552b7422a1ca9fc59344c413fc9bfa91e085bf2743627e81" {
		t.Errorf("M2.1 package sealed to %s, %v", seal, err)
	}

	p := pkg(t)
	p.Formats[0].State, p.Formats[0].Reason = evidence.StateOK, ""
	p.Formats[0].Detected = "mobi"
	p.Formats[0].Text = evidence.TextPresent
	p.Formats[0].Metadata = &evidence.FileMetadata{
		Title: "Binary Tides", Authors: []string{"Cy Placeholder"}, Languages: []string{"en"},
		ISBNs: []string{"9780000000026"}, Publisher: "Nobody Press", Date: "2019-05-04",
	}
	raw, seal, err := evidence.Seal(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"detected":"mobi"`, `"text":"present"`, `"metadata":{"authors":["Cy Placeholder"]`} {
		if !bytes.Contains(raw, []byte(key)) {
			t.Errorf("sealed package lacks %s:\n%s", key, raw)
		}
	}
	got, err := evidence.Open(raw, seal)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, p) {
		t.Errorf("Open(Seal(p)):\n got %+v\nwant %+v", got.Formats[0], p.Formats[0])
	}
}

func TestLegacyFindingFields(t *testing.T) {
	raw := []byte(`{"book_id":3,"calibre_record":{"author_sort":"Doe, Jane","authors":["Jane Doe"],"has_cover":false,"identifiers":{"isbn":"9780306406157"},"languages":["en"],"last_modified":"2026-10-12T00:00:00Z","pubdate":"","publisher":"","title":"Synthetic"},"formats":[],"library_id":"library","schema_version":1,"snapshot":"s","uuid":"u"}`)
	const seal = "5537b9b644a65dae629829fb300f794ee74e101701fe8e1884b4c01f75422ca1"
	p, err := evidence.Open(raw, seal)
	if err != nil {
		t.Fatal(err)
	}
	if p.FindingsVersion != 0 || len(p.Findings) != 0 {
		t.Fatal("legacy evidence acquired sealed findings")
	}
	got, h, err := evidence.Seal(p)
	if err != nil || h != seal || !bytes.Equal(got, raw) {
		t.Fatalf("legacy bytes/seal changed: %v", err)
	}
	p.FindingsVersion = 1
	p.Findings = []evidence.Finding{{Code: "isbn_missing", Field: "isbn"}}
	encoded, hash, err := evidence.Seal(p)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := evidence.Open(encoded, hash)
	if err != nil || opened.FindingsVersion != 1 || len(opened.Findings) != 1 || opened.Findings[0].Code != "isbn_missing" {
		t.Fatalf("new carrier lost: %v", err)
	}
}

// legacySeal is pkg's seal before Record gained TitleSort (M2.4). A package
// without a title sort must keep sealing to exactly these bytes.
const legacySeal = "8509f231af0478e8552b7422a1ca9fc59344c413fc9bfa91e085bf2743627e81"

func TestRecordTitleSortSeal(t *testing.T) {
	p := pkg(t)
	if _, seal, err := evidence.Seal(p); err != nil || seal != legacySeal {
		t.Fatalf("a package without title sort sealed to %s (%v), want %s", seal, err, legacySeal)
	}
	sort := "Binary Tides"
	b := book()
	b.TitleSort = &sort
	p.Calibre = evidence.RecordFrom(b)
	sort = "changed after RecordFrom"
	raw, _, err := evidence.Seal(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"title_sort":"Binary Tides"`)) {
		t.Fatalf("canonical JSON lacks the title sort: %s", raw)
	}
	empty := ""
	b.TitleSort = &empty
	p.Calibre = evidence.RecordFrom(b)
	if raw, _, _ = evidence.Seal(p); !bytes.Contains(raw, []byte(`"title_sort":""`)) {
		t.Fatalf("an empty title sort was dropped: %s", raw)
	}
}

func TestLookupsSealLegacy(t *testing.T) {
	p := pkg(t)
	if _, seal, err := evidence.Seal(p); err != nil || seal != legacySeal {
		t.Fatalf("a package without lookups sealed to %s (%v), want %s", seal, err, legacySeal)
	}
	p.Lookups = []evidence.Lookup{{ISBN: "9783551551672", Seeds: []string{"file"}, Results: []evidence.SourceResult{
		{Source: evidence.SourceDNB, Outcome: evidence.SourceOK, Records: []evidence.SourceRecord{{ID: "958577617", Title: "T", ISBNs: []string{"9783551551672"}}}},
		{Source: evidence.SourceGoogleBooks, Outcome: evidence.SourceNotConfigured},
	}}}
	raw, seal, err := evidence.Seal(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"lookups":[{"isbn":"9783551551672"`)) {
		t.Fatalf("canonical JSON lacks lookups: %s", raw)
	}
	got, err := evidence.Open(raw, seal)
	if err != nil || len(got.Lookups) != 1 || got.Lookups[0].Results[0].Records[0].ID != "958577617" {
		t.Fatalf("round trip: %+v/%v", got.Lookups, err)
	}
}

func TestDecisionSealLegacy(t *testing.T) {
	p := pkg(t)
	if _, seal, err := evidence.Seal(p); err != nil || seal != legacySeal {
		t.Fatalf("a package without a decision sealed to %s (%v), want %s", seal, err, legacySeal)
	}
	p.DecisionVersion = 1
	p.Tier = evidence.TierB
	p.TierReasons = []string{"no_record"}
	p.Proposals = []evidence.Proposal{{Field: "languages", Current: []string{}, Proposed: []string{"deu"}, Roots: []string{evidence.SourceDNB, evidence.RootFile}}}
	raw, seal, err := evidence.Seal(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"decision_version":1`, `"proposals":[`, `"tier":"B"`, `"tier_reasons":["no_record"]`} {
		if !bytes.Contains(raw, []byte(want)) {
			t.Fatalf("canonical JSON lacks %s: %s", want, raw)
		}
	}
	got, err := evidence.Open(raw, seal)
	if err != nil || got.Tier != evidence.TierB || len(got.Proposals) != 1 || got.Proposals[0].Roots[1] != "file" {
		t.Fatalf("round trip: %+v/%v", got, err)
	}
}

// TestRecordAuthorSortMap: an uncaptured table leaves the record's bytes as
// before M3.x, so older packages keep their seals; a captured one is copied.
func TestRecordAuthorSortMap(t *testing.T) {
	raw, err := evidence.Canonical(evidence.RecordFrom(library.Book{Title: "T"}))
	if err != nil || strings.Contains(string(raw), "author_sort_map") {
		t.Fatalf("uncaptured table in %s, %v", raw, err)
	}
	table := map[string]string{"Ada Quill": "Quill, Ada"}
	r := evidence.RecordFrom(library.Book{Title: "T", AuthorSortMap: table})
	table["Ada Quill"] = "changed"
	if r.AuthorSortMap["Ada Quill"] != "Quill, Ada" {
		t.Fatalf("AuthorSortMap = %v, want a copy", r.AuthorSortMap)
	}
	raw, err = evidence.Canonical(r)
	if err != nil || !strings.Contains(string(raw), `"author_sort_map":{"Ada Quill":"Quill, Ada"}`) {
		t.Fatalf("captured table in %s, %v", raw, err)
	}
}

func withHints(p evidence.Package, h *evidence.ISBNHints) evidence.Package {
	p.Formats = append([]evidence.FormatEvidence(nil), p.Formats...)
	p.Formats[1].ISBNHints = h
	return p
}

func TestOpenSchemaVersions(t *testing.T) {
	const isbn = "9780000000026"
	v2 := &evidence.ISBNHints{
		Method: evidence.HintEPUBText, State: evidence.HintOK, Pages: []int{1, 2}, ISBNs: []string{isbn},
		Context: []evidence.ISBNContext{{ISBN: isbn, Landmark: evidence.LandmarkCopyright, Qualifier: evidence.QualifierEbook}},
	}
	old := &evidence.ISBNHints{Method: evidence.HintOCR, State: evidence.HintOK, Pages: []int{1}, ISBNs: []string{isbn}}
	ocr2 := &evidence.ISBNHints{Method: evidence.HintOCR, State: evidence.HintOK, Pages: []int{1}, Lang: "deu"}
	tests := []struct {
		name    string
		version int
		hints   *evidence.ISBNHints
		wantErr error
	}{
		{"schema 1 without schema 2 fields", 1, old, nil},
		{"schema 1 with no hints", 1, nil, nil},
		{"schema 2 with context", 2, v2, nil},
		{"schema 2 with the old shape", 2, old, nil},
		{"schema 1 with an epub_text method", 1, &evidence.ISBNHints{Method: evidence.HintEPUBText, State: evidence.HintOK, Pages: []int{1}}, evidence.ErrInvalidEvidence},
		{"schema 1 with a context", 1, v2, evidence.ErrInvalidEvidence},
		{"schema 1 with a lang", 1, ocr2, evidence.ErrInvalidEvidence},
		{"schema 2 with a lang", 2, ocr2, nil},
		{"schema 2 with each tessdata lang", 2, &evidence.ISBNHints{Method: evidence.HintOCR, State: evidence.HintOK, Pages: []int{1}, Lang: "spa"}, nil},
		{"schema 2 with an unknown lang", 2, &evidence.ISBNHints{Method: evidence.HintOCR, State: evidence.HintOK, Pages: []int{1}, Lang: "fra"}, evidence.ErrInvalidEvidence},
		{"schema 2 with a lang on a text layer", 2, &evidence.ISBNHints{Method: evidence.HintTextLayer, State: evidence.HintOK, Pages: []int{1}, Lang: "eng"}, evidence.ErrInvalidEvidence},
		{"schema 2 with a lang on epub_text", 2, &evidence.ISBNHints{Method: evidence.HintEPUBText, State: evidence.HintOK, Pages: []int{1}, Lang: "eng"}, evidence.ErrInvalidEvidence},
		{"schema 2 with too few contexts", 2, &evidence.ISBNHints{Method: evidence.HintOCR, State: evidence.HintOK, Pages: []int{1}, ISBNs: []string{isbn, "9780000000040"}, Context: v2.Context}, evidence.ErrInvalidEvidence},
		{"schema 2 with another ISBN's context", 2, &evidence.ISBNHints{Method: evidence.HintOCR, State: evidence.HintOK, Pages: []int{1}, ISBNs: []string{"9780000000040"}, Context: v2.Context}, evidence.ErrInvalidEvidence},
		{"schema 2 with an unknown landmark", 2, &evidence.ISBNHints{Method: evidence.HintEPUBText, State: evidence.HintOK, Pages: []int{1}, ISBNs: []string{isbn}, Context: []evidence.ISBNContext{{ISBN: isbn, Landmark: "toc"}}}, evidence.ErrInvalidEvidence},
		{"schema 2 with an unknown qualifier", 2, &evidence.ISBNHints{Method: evidence.HintEPUBText, State: evidence.HintOK, Pages: []int{1}, ISBNs: []string{isbn}, Context: []evidence.ISBNContext{{ISBN: isbn, Qualifier: "audio"}}}, evidence.ErrInvalidEvidence},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := pkg(t)
			if tt.hints != nil {
				p = withHints(p, tt.hints)
			}
			p.SchemaVersion = tt.version
			raw, seal, err := evidence.Seal(p)
			if err != nil {
				t.Fatal(err)
			}
			got, err := evidence.Open(raw, seal)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Open = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, p) {
				t.Errorf("Open(Seal(p)) = %+v, want %+v", got, p)
			}
		})
	}
}

func TestSchemaVersionIsWritten(t *testing.T) {
	if evidence.SchemaVersion != 5 || evidence.SchemaVersionV4 != 4 || evidence.SchemaVersionV3 != 3 || evidence.SchemaVersionV2 != 2 || evidence.SchemaVersionV1 != 1 {
		t.Fatalf("SchemaVersion %d, V4 %d, V3 %d, V2 %d, V1 %d", evidence.SchemaVersion, evidence.SchemaVersionV4, evidence.SchemaVersionV3, evidence.SchemaVersionV2, evidence.SchemaVersionV1)
	}
}
