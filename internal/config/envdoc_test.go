package config

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// repoRoot is the directory with go.mod, above the test's own directory.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the test directory")
		}
		dir = parent
	}
}

// perProvider matches the overrides every agent seat and extractor reads under
// its own prefix. The README documents them once, as
// BOOKWARDEN_AGENT_<SEAT>_X and BOOKWARDEN_EXTRACTOR_<EXTRACTOR>_X.
var perProvider = regexp.MustCompile(`^BOOKWARDEN_(AGENT|EXTRACTOR)_([A-Z0-9]+)_(URL|MODEL|EFFORT|FAMILY|ALLOW_CIDR|MAX_CALLS)$`)

// template turns a per-seat or per-extractor variable into its README form.
func template(name string) string {
	m := perProvider.FindStringSubmatch(name)
	if m == nil {
		return name
	}
	switch m[1] {
	case "AGENT":
		for _, s := range agentSeats {
			if strings.ToUpper(s.name) == m[2] {
				return "BOOKWARDEN_AGENT_<SEAT>_" + m[3]
			}
		}
	case "EXTRACTOR":
		for _, e := range extractors {
			if strings.ToUpper(e.name) == m[2] {
				return "BOOKWARDEN_EXTRACTOR_<EXTRACTOR>_" + m[3]
			}
		}
	}
	return name
}

// variableLiteral matches a whole string literal that is one variable name.
var variableLiteral = regexp.MustCompile(`^BOOKWARDEN_[A-Z0-9_]+$`)

// literalVariables returns the variable names that appear in src as a whole Go
// string literal. A name inside a longer string (an error message) or a
// comment doesn't count, and neither does a prefix ending in _ or a
// BOOKWARDEN_TEST_ name, which is the test harness's.
func literalVariables(t *testing.T, filename string, src []byte) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), filename, src, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", filename, err)
	}
	var names []string
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		v, err := strconv.Unquote(lit.Value)
		if err == nil && variableLiteral.MatchString(v) && !strings.HasSuffix(v, "_") && !strings.HasPrefix(v, "BOOKWARDEN_TEST_") {
			names = append(names, v)
		}
		return true
	})
	return names
}

// plausible is a value that lets a read go on to the code it guards.
func plausible(name string) string {
	switch {
	case strings.HasSuffix(name, "_ALLOW_CIDR"):
		return "203.0.113.5/32"
	case strings.HasSuffix(name, "_DATABASE_URL"):
		return "postgres://u:p@db:5432/d"
	case strings.HasSuffix(name, "BIFROST_URL"):
		return "http://gateway:8080"
	case strings.HasSuffix(name, "_URL"):
		return "https://example.com/x"
	case strings.HasSuffix(name, "_FULL"):
		return "sun 04:00"
	case strings.HasSuffix(name, "_INCREMENTAL"):
		return "03:00"
	case strings.HasSuffix(name, "_STOP_GRACE"):
		return "5s"
	case strings.HasSuffix(name, "_RETAIN_RUNS"), strings.HasSuffix(name, "_MAX_CALLS"), strings.HasSuffix(name, "_MAX_TOKENS_PER_RUN"):
		return "5"
	case strings.HasSuffix(name, "_LISTEN"):
		return ":8080"
	case strings.HasSuffix(name, "_FAMILY"):
		return "qwen"
	case strings.HasSuffix(name, "_TRUSTED_PROXIES"):
		return "10.0.0.1"
	case strings.HasSuffix(name, "_METRICS_TOKEN"):
		return strings.Repeat("t", 32)
	}
	return "x"
}

// codeVariables are the BOOKWARDEN_* variables the program reads: every one
// that config asks its getenv for, found by running it twice, once with an
// empty environment and once with every variable seen so far set (to catch a
// read that only happens when another variable is set); plus every variable
// name written as a whole string literal in any other non-test source file,
// such as cmd/bookwarden's own reads.
func codeVariables(t *testing.T) map[string]bool {
	t.Helper()
	seen := map[string]bool{}
	run := func(get func(string) string) {
		_, _ = FromEnv(get)
		_, _ = CalibreFromEnv(get)
		_, _ = ExtractorsFromEnv(get)
		_, _ = LogLevelFrom(get)
	}
	run(func(name string) string {
		if strings.HasPrefix(name, "BOOKWARDEN_") {
			seen[name] = true
		}
		return ""
	})
	first := maps.Clone(seen)
	run(func(name string) string {
		if !strings.HasPrefix(name, "BOOKWARDEN_") {
			return ""
		}
		seen[name] = true
		if first[name] {
			return plausible(name)
		}
		return ""
	})
	found := map[string]bool{}
	for n := range seen {
		found[template(n)] = true
	}

	root := repoRoot(t)
	for _, dir := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || strings.Contains(path, "testutil") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, n := range literalVariables(t, path, src) {
				found[template(n)] = true
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return found
}

// readmeVariables are the rows of the README's "Environment reference" table.
func readmeVariables(t *testing.T) map[string]bool {
	t.Helper()
	src, err := os.ReadFile(filepath.Join(repoRoot(t), "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	row := regexp.MustCompile("^\\| `(BOOKWARDEN_[A-Za-z0-9_<>]+)` \\|")
	rows := map[string]bool{}
	in := false
	for _, line := range strings.Split(string(src), "\n") {
		switch {
		case strings.HasPrefix(line, "## "):
			in = strings.TrimSpace(strings.TrimPrefix(line, "## ")) == "Environment reference"
		case in:
			if m := row.FindStringSubmatch(line); m != nil {
				rows[m[1]] = true
			}
		}
	}
	return rows
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestEnvironmentReferenceIsComplete keeps the README's table and the code in
// step both ways: a variable the code reads needs a row, and a row needs a
// variable the code still reads.
func TestEnvironmentReferenceIsComplete(t *testing.T) {
	code, doc := codeVariables(t), readmeVariables(t)
	if len(doc) == 0 {
		t.Fatal("README.md has no \"Environment reference\" table with rows like | `BOOKWARDEN_X` |")
	}
	for _, name := range sortedKeys(code) {
		if !doc[name] {
			t.Errorf("%s is read by the code but has no row in the README's Environment reference", name)
		}
	}
	for _, name := range sortedKeys(doc) {
		if !code[name] {
			t.Errorf("%s has a row in the README's Environment reference but the code doesn't read it", name)
		}
	}
}

func TestEnvironmentTemplate(t *testing.T) {
	tests := []struct{ in, want string }{
		{"BOOKWARDEN_AGENT_GEMINI_URL", "BOOKWARDEN_AGENT_<SEAT>_URL"},
		{"BOOKWARDEN_AGENT_GEMINI_KEY", "BOOKWARDEN_AGENT_GEMINI_KEY"},
		{"BOOKWARDEN_AGENT_MAX_TOKENS_PER_RUN", "BOOKWARDEN_AGENT_MAX_TOKENS_PER_RUN"},
		{"BOOKWARDEN_EXTRACTOR_GLM_MAX_CALLS", "BOOKWARDEN_EXTRACTOR_<EXTRACTOR>_MAX_CALLS"},
		{"BOOKWARDEN_EXTRACTOR_ALLOW_CIDR", "BOOKWARDEN_EXTRACTOR_ALLOW_CIDR"},
		{"BOOKWARDEN_LISTEN", "BOOKWARDEN_LISTEN"},
	}
	for _, tt := range tests {
		if got := template(tt.in); got != tt.want {
			t.Errorf("template(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestLiteralVariables(t *testing.T) {
	src := `package x

// BOOKWARDEN_IN_COMMENT is not read.
func f(getenv func(string) string) error {
	_ = getenv("BOOKWARDEN_READ")
	_ = getenv("BOOKWARDEN_PREFIX_" + "X")
	_ = getenv("BOOKWARDEN_TEST_DATABASE_URL")
	_ = ` + "`BOOKWARDEN_RAW`" + `
	return errors.New("BOOKWARDEN_ONLY_IN_AN_ERROR is required")
}
`
	got := literalVariables(t, "x.go", []byte(src))
	want := []string{"BOOKWARDEN_READ", "BOOKWARDEN_RAW"}
	if !slices.Equal(got, want) {
		t.Errorf("literalVariables = %v, want %v", got, want)
	}
}
