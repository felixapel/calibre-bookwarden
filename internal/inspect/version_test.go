package inspect

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The version the pinned hash was last decided for, and the hash of the
// sources that make file evidence at that version. Update both together; see
// the failure message.
const (
	pinnedVersion    = 2
	pinnedSourceHash = "b6395108540db7c9599b56725087f3a78fc2eb5d6cf60a5b20f548c2e0923ab8"
)

// evidenceSources are the non-test sources whose behaviour shapes file
// evidence (see Version), relative to the repository root.
func evidenceSources(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	add := func(dir string, keep func(rel string) bool) {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			if d.IsDir() {
				if strings.Contains(rel, "/testdata") || strings.HasSuffix(rel, "/inspecttest") {
					return fs.SkipDir
				}
				return nil
			}
			if keep(rel) {
				files = append(files, rel)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	goSource := func(rel string) bool { return strings.HasSuffix(rel, ".go") && !strings.HasSuffix(rel, "_test.go") }
	add("internal/inspect", func(rel string) bool { return goSource(rel) && rel != "internal/inspect/version.go" })
	add("internal/ocr", func(rel string) bool {
		return goSource(rel) && rel != "internal/ocr/versions.go" && !strings.Contains(strings.TrimPrefix(rel, "internal/ocr/"), "/")
	})
	files = append(files, "internal/findings/isbn.go", "internal/findings/isbn_scan.go", "internal/findings/isbn_qualifier.go", "internal/verify/fileevidence.go")
	add("internal/findings/isbnranges", func(rel string) bool { return goSource(rel) || strings.HasSuffix(rel, ".xml") })
	slices.Sort(files)
	return files
}

func sourcesHash(t *testing.T) (string, int) {
	t.Helper()
	root := filepath.Join("..", "..")
	files := evidenceSources(t, root)
	h := sha256.New()
	for _, rel := range files {
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		h.Write([]byte(rel + "\n"))
		h.Write(b)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), len(files)
}

// TestSourcesMatchTheVersion fails when anything that shapes file evidence
// changed since someone last decided whether Version needs a bump.
func TestSourcesMatchTheVersion(t *testing.T) {
	got, n := sourcesHash(t)
	if n < 20 {
		t.Fatalf("only %d source files found: the walk is broken", n)
	}
	if got == pinnedSourceHash && Version == pinnedVersion {
		return
	}
	t.Errorf(`the sources that make file evidence (%d files: internal/inspect, internal/ocr, the ISBN scanner in internal/findings, internal/verify/fileevidence.go) are not what version %d was pinned for.
  now:    version %d, hash %s
  pinned: version %d, hash %s
Decide whether the change can make a fresh check of an unchanged file seal different file evidence.
If it can, bump Version in internal/inspect/version.go (incremental runs then re-measure every book once).
Either way, then set pinnedVersion = %d and pinnedSourceHash = %q in this file.`,
		n, pinnedVersion, Version, got, pinnedVersion, pinnedSourceHash, Version, got)
}

// A refactor that doesn't touch the listed files can't trip the test, and a
// listed file can't escape it by being added: the list is the directory walk.
func TestEvidenceSourcesIncludeTheScannerAndFileEvidence(t *testing.T) {
	files := evidenceSources(t, filepath.Join("..", ".."))
	for _, want := range []string{
		"internal/inspect/epub_frontmatter.go", "internal/inspect/pdf/xref.go", "internal/ocr/engine.go",
		"internal/findings/isbn_scan.go", "internal/verify/fileevidence.go", "internal/findings/isbnranges/isbnranges.go",
	} {
		if !slices.Contains(files, want) {
			t.Errorf("%s is not hashed", want)
		}
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || strings.Contains(f, "inspecttest") {
			t.Errorf("%s is hashed but is test support", f)
		}
	}
}
