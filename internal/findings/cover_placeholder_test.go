package findings_test

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"slices"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/findings"
	"github.com/felixapel/calibre-bookwarden/internal/inspect"
)

// measured encodes a w×h image and measures it with the real cover code.
func measured(t *testing.T, w, h int, f func(x, y int) uint8) evidence.CoverCandidate {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetGray(x, y, color.Gray{Y: f(x, y)})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	c := inspect.MeasureCover(b.Bytes(), inspect.DefaultLimits)
	if c.State != evidence.StateOK {
		t.Fatalf("measure: %+v", c)
	}
	return c
}

func calibreCover(c evidence.CoverEvidence) *evidence.CoverEvidence { return &c }

func coverPackage(cal evidence.CoverCandidate, file evidence.CoverCandidate) evidence.Package {
	file.Source = evidence.CandidateEPUB
	return evidence.Package{
		SchemaVersion: evidence.SchemaVersion, LibraryID: "library", BookID: 1, UUID: "u",
		Calibre: evidence.Record{Title: "Synthetic", Authors: []string{"Ada Author"}, AuthorSort: "Author, Ada", Languages: []string{"en"}, HasCover: true},
		Cover: calibreCover(evidence.CoverEvidence{State: evidence.StateOK, Bytes: 1000, SHA256: cal.SHA256, Detected: "png",
			Width: cal.Width, Height: cal.Height, DHash: cal.DHash, Flatness: cal.Flatness}),
		Formats: []evidence.FormatEvidence{{Format: "EPUB", State: evidence.StateOK, Detected: "epub", FileCover: &file}},
	}
}

func hasBetter(t *testing.T, p evidence.Package) bool {
	t.Helper()
	fs, err := findings.Derive(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	return slices.ContainsFunc(fs, func(f evidence.Finding) bool { return f.Code == "cover_better_available" })
}

// A real solid-grey placeholder, measured by the real code, is treated like a
// missing cover: whatever the candidate's artwork, the size and hash
// conditions don't apply (ADR-043).
func TestRealPlaceholderIsLikeMissing(t *testing.T) {
	placeholder := measured(t, 400, 600, func(int, int) uint8 { return 128 })
	if placeholder.Flatness != 0 {
		t.Fatalf("a solid grey cover has flatness %d", placeholder.Flatness)
	}
	artwork := measured(t, 900, 1400, func(x, _ int) uint8 { return uint8(x * 255 / 900) })
	if !hasBetter(t, coverPackage(placeholder, artwork)) {
		t.Error("a real placeholder with a 900 px EPUB cover gives no finding")
	}
	// Even a candidate under 1.5x the placeholder's side: it is as if missing.
	near := measured(t, 620, 900, func(x, _ int) uint8 { return uint8(x * 255 / 900) })
	if !hasBetter(t, coverPackage(placeholder, near)) {
		t.Error("a 620 px candidate against a 400 px placeholder gives no finding")
	}
	// A real, non-flat cover of other artwork that is just small still needs
	// the same artwork.
	other := measured(t, 400, 600, func(x, _ int) uint8 { return uint8(255 - x*255/400) })
	if other.Flatness < 30 {
		t.Fatalf("the other cover is flat: %d", other.Flatness)
	}
	if hasBetter(t, coverPackage(other, artwork)) {
		t.Error("a small cover of different artwork was replaced")
	}
}
