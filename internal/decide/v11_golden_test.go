package decide_test

import (
	"encoding/json"
	"os"
	"strconv"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

// v11GoldenFixtures are v10's golden fixtures and v11's own cases.
func v11GoldenFixtures() map[string]evidence.Package {
	fx := map[string]evidence.Package{
		"pubdate over the 1st":           v9Dated("2007-01-01T00:00:00+00:00", "2007-02-13"),
		"pubdate over a shifted 31st":    v9Dated("1991-07-31T22:00:00+00:00", "2007-04-17"),
		"pubdate over mid-month":         v9Dated("2010-05-05T12:00:00+00:00", "2007-02-13"),
		"title sort with a double space": sorted(str("Stein  der Weisen"), rowlingSort, map[string]string{A: rowlingSort})(),
	}
	title := sorted(str("Stein  der Weisen"), rowlingSort, map[string]string{A: rowlingSort})()
	title.Calibre.Title = "Harry Potter"
	fx["title over a double-spaced sort"] = title
	for name, p := range v10GoldenFixtures() {
		fx["v10: "+name] = p
	}
	return fx
}

// TestReplayGoldenV11Fixtures: every fixture derives at versions 1 to 10 the
// bytes the code before version 11 derived. testdata/replay_v11_golden.json
// was generated once with UPDATE_GOLDEN_V11 on code without any v11 rule;
// never regenerate it: a difference here is a change to a sealed version.
func TestReplayGoldenV11Fixtures(t *testing.T) {
	const file = "testdata/replay_v11_golden.json"
	got := map[string]json.RawMessage{}
	for name, p := range v11GoldenFixtures() {
		for version := 1; version <= 10; version++ {
			d, err := decide.AtVersion(t.Context(), p, version)
			if err != nil {
				t.Fatalf("%s v%d: %v", name, version, err)
			}
			raw, _ := json.Marshal(d)
			got[name+"/v"+strconv.Itoa(version)] = raw
		}
	}
	if os.Getenv("UPDATE_GOLDEN_V11") != "" {
		out, _ := json.MarshalIndent(got, "", " ")
		if err := os.WriteFile(file, append(out, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var wantMap map[string]json.RawMessage
	if err := json.Unmarshal(want, &wantMap); err != nil {
		t.Fatal(err)
	}
	if len(wantMap) != len(got) {
		t.Fatalf("golden has %d decisions, derived %d", len(wantMap), len(got))
	}
	for k, g := range got {
		var a, b any
		_ = json.Unmarshal(g, &a)
		_ = json.Unmarshal(wantMap[k], &b)
		ga, _ := json.Marshal(a)
		gb, _ := json.Marshal(b)
		if string(ga) != string(gb) {
			t.Errorf("%s:\n got  %s\n want %s", k, ga, gb)
		}
	}
}
