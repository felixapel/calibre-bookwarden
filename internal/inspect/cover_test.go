package inspect

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

func opener(b []byte) func() (io.ReadCloser, error) {
	return func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b)), nil }
}

func grey(w, h int, f func(x, y int) uint8) *image.Gray {
	img := image.NewGray(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetGray(x, y, color.Gray{Y: f(x, y)})
		}
	}
	return img
}

func pngBytes(t testing.TB, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func gradient(w, h int) *image.Gray {
	return grey(w, h, func(x, _ int) uint8 { return uint8(x * 255 / (w - 1)) })
}

func TestCoverHashGolden(t *testing.T) {
	for _, tc := range []struct {
		name      string
		img       image.Image
		dhash     string
		flatLo    int
		flatHi    int
		w, h      int
		wantState evidence.State
	}{
		{"rising gradient", gradient(800, 1200), "0000000000000000", 700, 760, 800, 1200, evidence.StateOK},
		{"falling gradient", grey(800, 1200, func(x, _ int) uint8 { return uint8(255 - x*255/799) }), "ffffffffffffffff", 700, 760, 800, 1200, evidence.StateOK},
		{"flat grey", grey(600, 900, func(int, int) uint8 { return 128 }), "0000000000000000", 0, 0, 600, 900, evidence.StateOK},
		{"tiny", grey(3, 2, func(x, _ int) uint8 { return uint8(x * 100) }), "", 0, 1000, 3, 2, evidence.StateOK},
	} {
		got := Cover(opener(pngBytes(t, tc.img)), DefaultLimits)
		if got.State != tc.wantState || got.Detected != "png" || got.Width != tc.w || got.Height != tc.h {
			t.Errorf("%s: %+v", tc.name, got)
		}
		if tc.dhash != "" && got.DHash != tc.dhash {
			t.Errorf("%s: dhash %s, want %s", tc.name, got.DHash, tc.dhash)
		}
		if len(got.DHash) != 16 || got.Flatness < tc.flatLo || got.Flatness > tc.flatHi {
			t.Errorf("%s: dhash %q, flatness %d, want %d..%d", tc.name, got.DHash, got.Flatness, tc.flatLo, tc.flatHi)
		}
	}
	// The same pixels always give the same measures.
	board := grey(600, 900, func(x, y int) uint8 { return uint8(((x/75 + y/75) % 2) * 255) })
	a, b := Cover(opener(pngBytes(t, board)), DefaultLimits), Cover(opener(pngBytes(t, board)), DefaultLimits)
	if a != b || a.DHash == "0000000000000000" || a.Flatness < 1000 {
		t.Errorf("checkerboard: %+v vs %+v", a, b)
	}
	if a.DHash != checkerboardDHash || a.Flatness != checkerboardFlatness {
		t.Errorf("checkerboard pinned: dhash %s flatness %d", a.DHash, a.Flatness)
	}
}

// Pinned from the implementation of ADR-023's integer area average; a change
// here changes every sealed cover hash.
const (
	checkerboardDHash    = "5a5aa5a55a5aa5a5"
	checkerboardFlatness = 1123
)

func TestCoverFormats(t *testing.T) {
	img := gradient(64, 96)
	var jb, gb bytes.Buffer
	if err := jpeg.Encode(&jb, img, nil); err != nil {
		t.Fatal(err)
	}
	if err := gif.Encode(&gb, img, nil); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		data     []byte
		detected string
	}{
		"jpeg": {jb.Bytes(), "jpeg"}, "png": {pngBytes(t, img), "png"}, "gif": {gb.Bytes(), "gif"}, "webp": {webpSample, "webp"},
	} {
		got := Cover(opener(tc.data), DefaultLimits)
		if got.State != evidence.StateOK || got.Detected != tc.detected || len(got.DHash) != 16 || got.Flatness < 0 {
			t.Errorf("%s: %+v", name, got)
		}
	}
	for name, data := range map[string][]byte{"garbage": []byte("not an image at all"), "empty": nil, "truncated jpeg": jb.Bytes()[:jb.Len()/2]} {
		got := Cover(opener(data), DefaultLimits)
		if got.State != evidence.StateCorrupt || got.DHash != "" || got.Flatness != -1 {
			t.Errorf("%s: %+v", name, got)
		}
	}
}

// pngHeader is a PNG signature and IHDR claiming w×h, with no pixel data.
func pngHeader(w, h uint32) []byte {
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], w)
	binary.BigEndian.PutUint32(ihdr[4:], h)
	ihdr[8], ihdr[9] = 8, 2 // 8-bit RGB
	var b bytes.Buffer
	b.WriteString("\x89PNG\r\n\x1a\n")
	_ = binary.Write(&b, binary.BigEndian, uint32(13))
	chunk := append([]byte("IHDR"), ihdr...)
	b.Write(chunk)
	_ = binary.Write(&b, binary.BigEndian, crc32.ChecksumIEEE(chunk))
	return b.Bytes()
}

func TestCoverLimits(t *testing.T) {
	opened := 0
	data := pngHeader(30000, 30000)
	got := Cover(func() (io.ReadCloser, error) {
		opened++
		return io.NopCloser(bytes.NewReader(data)), nil
	}, DefaultLimits)
	if got.State != evidence.StateNotInspected || got.Reason != evidence.ReasonTooLarge || got.Width != 30000 || got.Height != 30000 ||
		got.DHash != "" || got.Flatness != -1 || opened != 1 {
		t.Fatalf("huge header = %+v, opened %d times", got, opened)
	}
	if got := Cover(opener(pngHeader(0, 10)), DefaultLimits); got.State != evidence.StateCorrupt {
		t.Fatalf("zero width = %+v", got)
	}
}

func FuzzCover(f *testing.F) {
	f.Add(pngBytes(f, gradient(16, 24)))
	var jb bytes.Buffer
	_ = jpeg.Encode(&jb, gradient(16, 24), nil)
	f.Add(jb.Bytes())
	f.Add(webpSample)
	f.Add(pngHeader(30000, 30000))
	f.Fuzz(func(t *testing.T, data []byte) {
		got := Cover(opener(data), Limits{MaxPixels: 1 << 20})
		switch got.State {
		case evidence.StateOK:
			if len(got.DHash) != 16 || got.Flatness < 0 || got.Width <= 0 || got.Height <= 0 {
				t.Fatalf("ok without measures: %+v", got)
			}
		case evidence.StateCorrupt, evidence.StateNotInspected:
			if got.DHash != "" || got.Flatness != -1 {
				t.Fatalf("measures without a decode: %+v", got)
			}
		default:
			t.Fatalf("state %q", got.State)
		}
	})
}

// webpSample is a valid 1×1 lossless WebP (the same bytes as image_test.go's).
var webpSample = []byte("RIFF\x1a\x00\x00\x00WEBPVP8L\x0d\x00\x00\x00\x2f\x00\x00\x00\x10\x07\x10\x11\x11\x88\x88\xfe\x07\x00")

// jpegHeader is an SOI and a frame header (SOF0 baseline, SOF2 progressive)
// for w×h with n components, with no image data.
func jpegHeader(marker byte, w, h uint16, n int) []byte {
	b := []byte{0xff, 0xd8}
	if n == 4 { // Adobe APP14: CMYK
		b = append(b, 0xff, 0xee, 0, 14, 'A', 'd', 'o', 'b', 'e', 0, 100, 0, 0, 0, 0, 0)
	} else { // JFIF APP0
		b = append(b, 0xff, 0xe0, 0, 16, 'J', 'F', 'I', 'F', 0, 1, 1, 0, 0, 1, 0, 1, 0, 0)
	}
	b = append(b, 0xff, marker)
	seg := []byte{0, byte(8 + 3*n), 8, byte(h >> 8), byte(h), byte(w >> 8), byte(w), byte(n)}
	for i := range n {
		seg = append(seg, byte(i+1), 0x11, 0)
	}
	b = append(b, seg...)
	// A scan header: Go reads up to it before it reports a non-JFIF config.
	sos := []byte{0xff, 0xda, 0, byte(6 + 2*n), byte(n)}
	for i := range n {
		sos = append(sos, byte(i+1), 0)
	}
	return append(b, append(sos, 0, 63, 0)...)
}

// Go's progressive JPEG decoder keeps a 4-byte coefficient per sample on top
// of the planes, so a small progressive file can decode to several times the
// baseline estimate.
func TestCoverProgressiveJPEGLimits(t *testing.T) {
	for _, tc := range []struct {
		name  string
		data  []byte
		state evidence.State
		opens int
	}{
		{"progressive 24 MP", jpegHeader(0xc2, 4000, 6000, 3), evidence.StateNotInspected, 1},
		{"progressive CMYK 12 MP", jpegHeader(0xc2, 3000, 4000, 4), evidence.StateNotInspected, 1},
		{"progressive 6 MP", jpegHeader(0xc2, 2000, 3000, 3), evidence.StateCorrupt, 2},
		{"baseline 24 MP", jpegHeader(0xc0, 4000, 6000, 3), evidence.StateCorrupt, 2},
	} {
		opens := 0
		got := Cover(func() (io.ReadCloser, error) {
			opens++
			return io.NopCloser(bytes.NewReader(tc.data)), nil
		}, DefaultLimits)
		if got.State != tc.state || opens != tc.opens {
			t.Errorf("%s: %+v, opened %d times", tc.name, got, opens)
		}
		if tc.state == evidence.StateNotInspected && got.Reason != evidence.ReasonTooLarge {
			t.Errorf("%s: reason %q", tc.name, got.Reason)
		}
	}
	// The format inspector keeps to the same bound.
	if headerOnly, err := decodeImage(opener(jpegHeader(0xc2, 4000, 6000, 3)), DefaultLimits.withDefaults()); err != nil || !headerOnly {
		t.Errorf("decodeImage on a progressive 24 MP header: %v, %v", headerOnly, err)
	}
}
