package config

import (
	"os"
	"slices"
	"strings"
	"testing"
)

// SECURITY.md names a real contact, and THIRD_PARTY.md has a table row for
// every direct Go requirement.
func TestPublicHygiene(t *testing.T) {
	security, err := os.ReadFile("../../SECURITY.md")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(security), "SECURITY_CONTACT") {
		t.Error("SECURITY.md still has the SECURITY_CONTACT placeholder")
	}
	gomod, err := os.ReadFile("../../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	third, err := os.ReadFile("../../THIRD_PARTY.md")
	if err != nil {
		t.Fatal(err)
	}
	mods := directRequirements(string(gomod))
	if len(mods) == 0 {
		t.Fatal("no direct requirements found in go.mod")
	}
	for _, m := range unlisted(mods, string(third)) {
		t.Errorf("THIRD_PARTY.md has no table row for %s", m)
	}
}

func TestDirectRequirements(t *testing.T) {
	tests := []struct {
		name, gomod string
		want        []string
	}{
		{"block", "module x\n\nrequire (\n\ta.example/one v1.0.0\n\tb.example/two v0.2.0 // indirect\n)\n", []string{"a.example/one"}},
		{"single line", "module x\n\nrequire a.example/one v1.0.0\n", []string{"a.example/one"}},
		{"single line indirect", "module x\n\nrequire a.example/one v1.0.0 // indirect\n", nil},
		{"exclude block is not a requirement", "module x\n\nexclude (\n\tc.example/bad v1.0.0\n)\n\nrequire a.example/one v1.0.0\n", []string{"a.example/one"}},
		{"CRLF", "module x\r\n\r\nrequire (\r\n\ta.example/one v1.0.0\r\n)\r\n", []string{"a.example/one"}},
		{"two blocks", "require (\n\ta.example/one v1.0.0\n)\n\nrequire (\n\tc.example/three v3.0.0\n)\n", []string{"a.example/one", "c.example/three"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := directRequirements(tt.gomod); !slices.Equal(got, tt.want) {
				t.Errorf("directRequirements = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestUnlisted(t *testing.T) {
	third := "| [a.example/one](https://a.example) | MIT |\n\nThe indirect requirement `b.example/two` is MIT.\n"
	got := unlisted([]string{"a.example/one", "b.example/two"}, third)
	if want := []string{"b.example/two"}; !slices.Equal(got, want) {
		t.Errorf("unlisted = %q, want %q (prose alone is not a table row)", got, want)
	}
}

// directRequirements returns the modules go.mod requires directly, from
// require blocks and single-line requires, leaving out "// indirect" ones.
func directRequirements(gomod string) []string {
	var mods []string
	inRequire, inOther := false, false
	for _, line := range strings.Split(gomod, "\n") {
		line = strings.TrimSpace(line)
		var spec string
		switch {
		case inRequire || inOther:
			if line == ")" {
				inRequire, inOther = false, false
				continue
			}
			if !inRequire {
				continue
			}
			spec = line
		case line == "require (":
			inRequire = true
			continue
		case strings.HasSuffix(line, "("):
			inOther = true
			continue
		case strings.HasPrefix(line, "require "):
			spec = strings.TrimPrefix(line, "require ")
		default:
			continue
		}
		if f := strings.Fields(spec); len(f) >= 2 && !strings.Contains(spec, "// indirect") {
			mods = append(mods, f[0])
		}
	}
	return mods
}

// unlisted returns the modules that have no "| [module](" table row.
func unlisted(mods []string, third string) []string {
	var out []string
	for _, m := range mods {
		if !strings.Contains(third, "| ["+m+"](") {
			out = append(out, m)
		}
	}
	return out
}
