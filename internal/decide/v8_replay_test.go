package decide_test

import (
	"encoding/json"
	"os"
	"strconv"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/decide"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

// v8GoldenFixtures are the version 8 rows, which carry an extraction on every
// format, plus the version 7 rows.
func v8GoldenFixtures() map[string]evidence.Package {
	fx := map[string]evidence.Package{}
	for _, c := range v8Cases() {
		fx[c.name] = c.p
	}
	for name, p := range v7GoldenFixtures() {
		fx["v7: "+name] = p
	}
	return fx
}

// TestReplayGoldenV8Fixtures: the version 8 fixtures derive at versions 1 to 7
// the bytes the code before version 8 derived. testdata/replay_v8_golden.json
// is generated with UPDATE_GOLDEN_V8 on main's decide code from before
// version 8 plus these fixtures, never on code that has v8, so a v8 change
// that leaks into an earlier version fails here.
func TestReplayGoldenV8Fixtures(t *testing.T) {
	const file = "testdata/replay_v8_golden.json"
	got := map[string]json.RawMessage{}
	for name, p := range v8GoldenFixtures() {
		for version := 1; version <= 7; version++ {
			d, err := decide.AtVersion(t.Context(), p, version)
			if err != nil {
				t.Fatalf("%s v%d: %v", name, version, err)
			}
			raw, _ := json.Marshal(d)
			got[name+"/v"+strconv.Itoa(version)] = raw
		}
	}
	if os.Getenv("UPDATE_GOLDEN_V8") != "" {
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
			t.Errorf("%s changed:\n got %s\nwant %s", k, g, wantMap[k])
		}
	}
}
