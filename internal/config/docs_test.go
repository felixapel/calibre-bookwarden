package config

import (
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The README states what happens to book text: excerpts go to the extractor
// endpoint when extraction is configured (docs/threat-model.md, "Privacy").
func TestReadmePrivacyIsTrue(t *testing.T) {
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(readme), "not sent anywhere") {
		t.Error(`README.md says excerpts are "not sent anywhere"; they go to the extractor endpoint when extraction is configured`)
	}
}

var adrRef = regexp.MustCompile(`ADR-0\d\d`)

// Every ADR cited in the README, AGENTS.md or docs/ has a file in
// docs/decisions and a row in its index. The numbers AGENTS.md reserves are
// exempt; its next number only on its own line there, so a document can't
// cite a record that isn't written yet.
func TestCitedADRsExist(t *testing.T) {
	agents, err := os.ReadFile("../../AGENTS.md")
	if err != nil {
		t.Fatal(err)
	}
	reserved := map[string]bool{}
	for _, m := range regexp.MustCompile(`(ADR-0\d\d) is reserved`).FindAllStringSubmatch(string(agents), -1) {
		reserved[m[1]] = true
	}
	next := regexp.MustCompile(`next number is (ADR-0\d\d)`)
	if !next.Match(agents) {
		t.Fatal("AGENTS.md names no next ADR number")
	}
	cited := map[string]string{}
	cite := func(path string) {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(path, "AGENTS.md") {
			b = next.ReplaceAll(b, nil)
		}
		for _, n := range adrRef.FindAllString(string(b), -1) {
			if _, ok := cited[n]; !ok {
				cited[n] = path
			}
		}
	}
	cite("../../README.md")
	cite("../../AGENTS.md")
	if err := filepath.WalkDir("../../docs", func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(path, ".md") {
			cite(path)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir("../../docs/decisions")
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, e := range entries {
		if n := adrRef.FindString(e.Name()); n != "" && strings.HasPrefix(e.Name(), n) {
			files = append(files, n)
		}
	}
	index, err := os.ReadFile("../../docs/decisions/README.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range slices.Sorted(maps.Keys(cited)) {
		if reserved[n] {
			continue
		}
		if !slices.Contains(files, n) {
			t.Errorf("%s is cited (first in %s) but has no file in docs/decisions", n, cited[n])
		}
		if !strings.Contains(string(index), "| ["+n+"](") {
			t.Errorf("%s is cited but has no row in docs/decisions/README.md", n)
		}
	}
}
