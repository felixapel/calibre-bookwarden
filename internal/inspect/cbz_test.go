package inspect_test

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/inspect"
	"github.com/felixapel/calibre-bookwarden/internal/inspect/inspecttest"
)

var cbzCases = []struct {
	name   string
	pages  map[string][]byte
	state  evidence.State
	reason string
}{
	{"three PNG pages", map[string][]byte{
		"page-1.png": inspecttest.PNG(40, 60), "page-2.png": inspecttest.PNG(40, 60), "page-3.png": inspecttest.PNG(40, 60),
	}, evidence.StateOK, ""},
	{"the first three in name order include a broken page", map[string][]byte{
		"p1.jpg": inspecttest.JPEG(40, 60), "p10.png": inspecttest.PNG(40, 60), "p2.png": brokenPNG(), "p3.png": inspecttest.PNG(40, 60),
	}, evidence.StateCorrupt, ""},
	{"a broken fourth page isn't decoded", map[string][]byte{
		"p1.png": inspecttest.PNG(40, 60), "p2.png": inspecttest.PNG(40, 60), "p3.png": inspecttest.PNG(40, 60), "p4.png": brokenPNG(),
	}, evidence.StateOK, ""},
	{"no images", map[string][]byte{"ComicInfo.xml": []byte("<ComicInfo/>")}, evidence.StateCorrupt, ""},
	{"macOS and hidden files are skipped", map[string][]byte{
		"__MACOSX/._p1.png": []byte("resource fork"), ".hidden.png": []byte("junk"), "p1.PNG": inspecttest.PNG(40, 60),
	}, evidence.StateOK, ""},
	{"a page past the pixel cap", map[string][]byte{
		"p1.png": pngHeader(6000, 5000), "p2.png": inspecttest.PNG(40, 60),
	}, evidence.StateOK, evidence.ReasonHeaderOnly},
	{"a WebP page", map[string][]byte{"p1.webp": webp1x1}, evidence.StateOK, ""},
	{"a page with oversized decoder rows", map[string][]byte{"p1.png": wideRGBA16PNG()}, evidence.StateOK, evidence.ReasonHeaderOnly},
}

func TestCBZ(t *testing.T) {
	for _, tt := range cbzCases {
		t.Run(tt.name, func(t *testing.T) {
			data := inspecttest.CBZ(tt.pages)
			res := inspect.Inspect(context.Background(), "CBZ", bytes.NewReader(data), int64(len(data)), inspect.Limits{})
			checkInvariants(t, res)
			if res.State != tt.state || res.Reason != tt.reason || res.Detected != "zip" {
				t.Errorf("got %s/%s detected %q, want %s/%s detected zip", res.State, res.Reason, res.Detected, tt.state, tt.reason)
			}
		})
	}
}

func FuzzCBZ(f *testing.F) {
	for _, tt := range cbzCases {
		f.Add(inspecttest.CBZ(tt.pages))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		checkInvariants(t, inspect.Inspect(context.Background(), "CBZ", bytes.NewReader(data), int64(len(data)), inspect.Limits{}))
	})
}

// A limit hit while a decoder reads a page is a limit, whatever error the
// decoder makes of it.
func TestCBZLimitInsideADecoder(t *testing.T) {
	// Incompressible pages, so the decoder reads past the limit. The GIF decoder
	// wraps read errors with %v, losing the limit.
	for name, page := range map[string][]byte{"p1.png": noisePNG(200, 200), "p1.gif": noiseGIF(200, 200)} {
		data := inspecttest.CBZ(map[string][]byte{name: page})
		lim := inspect.Limits{MaxDecompressed: int64(len(page)) + 100} // verifying passes; decoding crosses it
		res := inspect.Inspect(context.Background(), "CBZ", bytes.NewReader(data), int64(len(data)), lim)
		if res.State != evidence.StateNotInspected || res.Reason != evidence.ReasonDecompressedTooLarge {
			t.Errorf("%s: got %s/%s, want not_inspected/decompressed_too_large", name, res.State, res.Reason)
		}
	}
}

func noisePNG(w, h int) []byte {
	img := image.NewGray(image.Rect(0, 0, w, h))
	copy(img.Pix, randomBytes(len(img.Pix), 7))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func noiseGIF(w, h int) []byte {
	palette := make(color.Palette, 256)
	for i := range palette {
		palette[i] = color.Gray{Y: uint8(i)} //nolint:gosec // i < 256
	}
	img := image.NewPaletted(image.Rect(0, 0, w, h), palette)
	copy(img.Pix, randomBytes(len(img.Pix), 8))
	var buf bytes.Buffer
	if err := gif.Encode(&buf, img, nil); err != nil {
		panic(err)
	}
	return buf.Bytes()
}
