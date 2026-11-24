package inspect

import (
	"bytes"
	"context"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/inspect/inspecttest"
)

func TestEPUBOwnCoverTooLarge(t *testing.T) {
	old := fileCoverCap
	fileCoverCap = 100
	t.Cleanup(func() { fileCoverCap = old })
	img := pngBytes(t, gradient(200, 300))
	if len(img) <= 100 {
		t.Fatalf("test image is only %d bytes", len(img))
	}
	b := inspecttest.EPUB(inspecttest.EPUBOptions{Title: "T", Text: "x", CoverImage: img, ExtraMeta: `<meta name="cover" content="cover"/>`})
	res := Inspect(context.Background(), "EPUB", bytes.NewReader(b), int64(len(b)), DefaultLimits)
	c := res.FileCover
	if res.State != evidence.StateOK || c == nil || c.State != evidence.StateTooLarge || c.SHA256 != "" || c.Flatness != -1 {
		t.Errorf("%s: candidate %+v", res.State, c)
	}
}

func TestCoverCapIsADR023s(t *testing.T) {
	if CoverCap != 20<<20 {
		t.Errorf("CoverCap = %d, ADR-023 says 20 MiB", CoverCap)
	}
}

func TestMeasureCover(t *testing.T) {
	good := pngBytes(t, gradient(800, 1200))
	for _, tc := range []struct {
		name  string
		data  []byte
		lim   Limits
		state evidence.State
	}{
		{"ok", good, DefaultLimits, evidence.StateOK},
		{"corrupt", []byte("junk"), DefaultLimits, evidence.StateCorrupt},
		{"too many pixels", good, Limits{MaxPixels: 1000}, evidence.StateNotInspected},
	} {
		c := MeasureCover(tc.data, tc.lim)
		if c.State != tc.state || c.SHA256 == "" {
			t.Errorf("%s: %+v", tc.name, c)
		}
		if (tc.state == evidence.StateOK) != (c.DHash != "") || (tc.state == evidence.StateOK) != (c.Flatness >= 0) {
			t.Errorf("%s: measures %+v", tc.name, c)
		}
	}
	if c := MeasureCover(good, DefaultLimits); c.MediaType != "image/png" || c.Width != 800 || c.Height != 1200 {
		t.Errorf("%+v", c)
	}
}
