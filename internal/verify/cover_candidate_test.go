package verify_test

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/inspect/inspecttest"
	"github.com/felixapel/calibre-bookwarden/internal/verify/verifytest"
)

// A real EPUB whose OPF names a cover: its measures reach the sealed format
// evidence, and with no cover in Calibre the finding follows.
func TestEPUBOwnCoverIsSealed(t *testing.T) {
	img := coverPNG(t, 900, 1400)
	epub := inspecttest.EPUB(inspecttest.EPUBOptions{
		Title: "Binary Tides", Authors: []string{"Cy Placeholder"}, Language: "en", ISBN: "9780000000026",
		Text: "Generated text.", CoverImage: img, ExtraMeta: `<meta name="cover" content="cover"/>`,
	})
	b := fileBook(6, "EPUB", epub) // Calibre has no cover
	lib := verifytest.NewLibrary("library", b)
	_, p, err := checkCover(t, lib, b.Book.ID, b.Book.UUID)
	if err != nil {
		t.Fatal(err)
	}
	c := p.Formats[0].FileCover
	sum := sha256.Sum256(img)
	if p.SchemaVersion != evidence.SchemaVersion || c == nil || c.Source != evidence.CandidateEPUB || c.State != evidence.StateOK ||
		c.MediaType != "image/png" || c.Width != 900 || c.Height != 1400 || c.SHA256 != hex.EncodeToString(sum[:]) || len(c.DHash) != 16 || c.Flatness < 0 {
		t.Fatalf("sealed candidate %+v (schema %d)", c, p.SchemaVersion)
	}
	if !hasFinding(p, "cover_better_available", "epub") {
		t.Errorf("findings %+v", p.Findings)
	}

	// A book whose EPUB names no cover seals no candidate.
	plain := fileBook(7, "EPUB", tidesEPUB)
	_, p, err = checkCover(t, verifytest.NewLibrary("library", plain), plain.Book.ID, plain.Book.UUID)
	if err != nil || p.Formats[0].FileCover != nil {
		t.Errorf("candidate %+v, err %v", p.Formats[0].FileCover, err)
	}
}
