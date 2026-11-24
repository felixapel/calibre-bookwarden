package inspect_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/inspect"
	"github.com/felixapel/calibre-bookwarden/internal/inspect/inspecttest"
)

// stripes is a w×h greyscale image with vertical stripes, so it is neither flat
// nor all one dHash.
func stripes(w, h int) *image.Gray {
	img := image.NewGray(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetGray(x, y, color.Gray{Y: uint8((x / 8 % 2) * 200)})
		}
	}
	return img
}

func pngOf(t testing.TB, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func jpegOf(t testing.TB, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func inspectOPF(t *testing.T, o inspecttest.EPUBOptions) inspect.Result {
	t.Helper()
	b := inspecttest.EPUB(o)
	return inspect.Inspect(context.Background(), "EPUB", bytes.NewReader(b), int64(len(b)), inspect.DefaultLimits)
}

func TestEPUBOwnCover(t *testing.T) {
	img := pngOf(t, stripes(640, 960))
	jpg := jpegOf(t, stripes(300, 400))
	base := inspecttest.EPUBOptions{Title: "Binary Tides", Text: "Hello", CoverImage: img}
	with := func(f func(o *inspecttest.EPUBOptions)) inspecttest.EPUBOptions {
		o := base
		f(&o)
		return o
	}
	tests := []struct {
		name      string
		o         inspecttest.EPUBOptions
		want      bool // a candidate is sealed
		state     evidence.State
		mediaType string
		w, h      int
		sha       string
	}{
		{"none named", base, false, "", "", 0, 0, ""},
		{"EPUB 3 properties", with(func(o *inspecttest.EPUBOptions) {
			o.EPUB3 = true
			o.ManifestXML = `<item id="img1" href="images/c3.jpg" media-type="image/jpeg" properties="cover-image"/>`
			o.Files = map[string][]byte{"OEBPS/images/c3.jpg": jpg}
		}), true, evidence.StateOK, "image/jpeg", 300, 400, sum(jpg)},
		{"EPUB 3 properties among others", with(func(o *inspecttest.EPUBOptions) {
			o.EPUB3 = true
			o.ManifestXML = `<item id="img1" href="images/c3.jpg" media-type="image/jpeg" properties="svg cover-image"/>`
			o.Files = map[string][]byte{"OEBPS/images/c3.jpg": jpg}
		}), true, evidence.StateOK, "image/jpeg", 300, 400, sum(jpg)},
		{"EPUB 2 meta", with(func(o *inspecttest.EPUBOptions) {
			o.ExtraMeta = `<meta name="cover" content="cover"/>`
		}), true, evidence.StateOK, "image/png", 640, 960, sum(img)},
		{"EPUB 2 meta with the content first", with(func(o *inspecttest.EPUBOptions) {
			o.ExtraMeta = `<meta content="cover" name="cover"/>`
		}), true, evidence.StateOK, "image/png", 640, 960, sum(img)},
		{"guide fallback", with(func(o *inspecttest.EPUBOptions) {
			o.Guide = `<guide><reference type="cover" title="Cover" href="images/cover.png"/></guide>`
		}), true, evidence.StateOK, "image/png", 640, 960, sum(img)},
		{"guide fallback with a fragment", with(func(o *inspecttest.EPUBOptions) {
			o.Guide = `<guide><reference type="cover" href="images/cover.png#x"/></guide>`
		}), true, evidence.StateOK, "image/png", 640, 960, sum(img)},
		{"guide to an XHTML page is skipped", with(func(o *inspecttest.EPUBOptions) {
			o.Guide = `<guide><reference type="cover" title="Cover" href="text/chapter%201.xhtml"/></guide>`
		}), false, "", "", 0, 0, ""},
		{"guide to a page outside the manifest is skipped", with(func(o *inspecttest.EPUBOptions) {
			o.Guide = `<guide><reference type="cover" href="cover.xhtml"/></guide>`
		}), false, "", "", 0, 0, ""},
		{"meta to an XHTML page is skipped", with(func(o *inspecttest.EPUBOptions) {
			o.ExtraMeta = `<meta name="cover" content="ch1"/>`
		}), false, "", "", 0, 0, ""},
		{"a non image media type is skipped", with(func(o *inspecttest.EPUBOptions) {
			o.EPUB3 = true
			o.ManifestXML = `<item id="f" href="images/c3.bin" media-type="application/octet-stream" properties="cover-image"/>`
			o.Files = map[string][]byte{"OEBPS/images/c3.bin": jpg}
		}), false, "", "", 0, 0, ""},
		{"an SVG is skipped", with(func(o *inspecttest.EPUBOptions) {
			o.EPUB3 = true
			o.ManifestXML = `<item id="f" href="images/c3.svg" media-type="image/svg+xml" properties="cover-image"/>`
			o.Files = map[string][]byte{"OEBPS/images/c3.svg": []byte("<svg/>")}
		}), false, "", "", 0, 0, ""},
		{"a missing manifest item", with(func(o *inspecttest.EPUBOptions) {
			o.ExtraMeta = `<meta name="cover" content="nope"/>`
		}), true, evidence.StateMissing, "", 0, 0, ""},
		{"a manifest item whose file is gone", with(func(o *inspecttest.EPUBOptions) {
			o.EPUB3 = true
			o.ManifestXML = `<item id="img1" href="images/gone.jpg" media-type="image/jpeg" properties="cover-image"/>`
		}), true, evidence.StateMissing, "", 0, 0, ""},
		{"EPUB 3 item whose file is gone falls through to the EPUB 2 meta", with(func(o *inspecttest.EPUBOptions) {
			o.EPUB3 = true
			o.ManifestXML = `<item id="img1" href="images/gone.jpg" media-type="image/jpeg" properties="cover-image"/>`
			o.ExtraMeta = `<meta name="cover" content="cover"/>`
		}), true, evidence.StateOK, "image/png", 640, 960, sum(img)},
		{"EPUB 3 item whose file is gone falls through to the guide", with(func(o *inspecttest.EPUBOptions) {
			o.EPUB3 = true
			o.ManifestXML = `<item id="img1" href="images/gone.jpg" media-type="image/jpeg" properties="cover-image"/>`
			o.Guide = `<guide><reference type="cover" href="images/cover.png"/></guide>`
		}), true, evidence.StateOK, "image/png", 640, 960, sum(img)},
		{"a corrupt image", with(func(o *inspecttest.EPUBOptions) {
			o.ExtraMeta = `<meta name="cover" content="cover"/>`
			o.CoverImage = []byte("not really a png")
		}), true, evidence.StateCorrupt, "", 0, 0, sum([]byte("not really a png"))},
		{"EPUB 3 beats EPUB 2 and the guide", with(func(o *inspecttest.EPUBOptions) {
			o.EPUB3 = true
			o.ManifestXML = `<item id="img1" href="images/c3.jpg" media-type="image/jpeg" properties="cover-image"/>`
			o.Files = map[string][]byte{"OEBPS/images/c3.jpg": jpg}
			o.ExtraMeta = `<meta name="cover" content="cover"/>`
			o.Guide = `<guide><reference type="cover" href="images/cover.png"/></guide>`
		}), true, evidence.StateOK, "image/jpeg", 300, 400, sum(jpg)},
		{"a page named first falls through to the next method", with(func(o *inspecttest.EPUBOptions) {
			o.ExtraMeta = `<meta name="cover" content="ch1"/>`
			o.Guide = `<guide><reference type="cover" href="images/cover.png"/></guide>`
		}), true, evidence.StateOK, "image/png", 640, 960, sum(img)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := inspectOPF(t, tc.o)
			if res.State != evidence.StateOK {
				t.Fatalf("EPUB state %s/%s", res.State, res.Reason)
			}
			c := res.FileCover
			if !tc.want {
				if c != nil {
					t.Fatalf("candidate %+v, want none", c)
				}
				return
			}
			if c == nil {
				t.Fatal("no candidate")
			}
			if c.Source != evidence.CandidateEPUB || c.State != tc.state || c.MediaType != tc.mediaType ||
				c.Width != tc.w || c.Height != tc.h || c.SHA256 != tc.sha {
				t.Errorf("candidate %+v", c)
			}
			if tc.state == evidence.StateOK && (len(c.DHash) != 16 || c.Flatness <= 0) {
				t.Errorf("measures %q %d", c.DHash, c.Flatness)
			}
			if tc.state != evidence.StateOK && (c.DHash != "" || c.Flatness != -1) {
				t.Errorf("measures on a %s candidate: %q %d", c.State, c.DHash, c.Flatness)
			}
		})
	}
}

// The candidate is exactly what the Calibre cover measure would say for the
// same bytes.
func TestEPUBOwnCoverMatchesCoverMeasures(t *testing.T) {
	img := pngOf(t, stripes(640, 960))
	res := inspectOPF(t, inspecttest.EPUBOptions{Title: "T", Text: "x", CoverImage: img, ExtraMeta: `<meta name="cover" content="cover"/>`})
	want := inspect.Cover(func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(img)), nil }, inspect.DefaultLimits)
	c := res.FileCover
	if c == nil || c.DHash != want.DHash || c.Flatness != want.Flatness || c.Width != want.Width || c.Height != want.Height {
		t.Errorf("candidate %+v, cover %+v", c, want)
	}
}

func TestNoCoverCandidateForOtherStates(t *testing.T) {
	res := inspectOPF(t, inspecttest.EPUBOptions{Title: "T", Text: "x", Rights: true, ExtraMeta: `<meta name="cover" content="cover"/>`})
	if res.State != evidence.StateDRM || res.FileCover != nil {
		t.Errorf("%s, candidate %+v", res.State, res.FileCover)
	}
}
