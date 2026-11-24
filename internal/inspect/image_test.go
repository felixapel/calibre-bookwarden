package inspect_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/inspect"
	"github.com/felixapel/calibre-bookwarden/internal/inspect/inspecttest"
)

// pngHeader is a PNG that announces w×h pixels and holds no image data.
func pngHeader(w, h uint32) []byte {
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr, w)
	binary.BigEndian.PutUint32(ihdr[4:], h)
	ihdr[8] = 8 // bit depth; colour type 0, greyscale
	chunk := append([]byte("IHDR"), ihdr...)
	out := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\x0d")
	out = append(out, chunk...)
	return binary.BigEndian.AppendUint32(out, crc32.ChecksumIEEE(chunk))
}

// brokenPNG has an image data chunk whose checksum doesn't match.
func brokenPNG() []byte {
	b := inspecttest.PNG(40, 60)
	i := bytes.Index(b, []byte("IDAT"))
	b[i+6] ^= 0xFF
	return b
}

// A header is sufficient to exercise the memory guard without large pixels.
func wideRGBA16PNG() []byte {
	b := pngHeader(24_000_000, 1)
	b[24], b[25] = 16, 6
	binary.BigEndian.PutUint32(b[len(b)-4:], crc32.ChecksumIEEE(b[12:len(b)-4]))
	return b
}

func truncatedGIF() []byte {
	var buf bytes.Buffer
	if err := gif.Encode(&buf, image.NewPaletted(image.Rect(0, 0, 40, 60), color.Palette{color.Black, color.White}), nil); err != nil {
		panic(err)
	}
	return buf.Bytes()[:buf.Len()/2]
}

// webp1x1 is a valid 1×1 lossless WebP.
var webp1x1, _ = hex.DecodeString("524946461a000000574542505650384c0d0000002f00000010071011118888fe0700")

func TestImage(t *testing.T) {
	tests := []struct {
		name, declared string
		data           []byte
		state          evidence.State
		reason         string
	}{
		{"PNG", "PNG", inspecttest.PNG(40, 60), evidence.StateOK, ""},
		{"JPEG", "JPG", inspecttest.JPEG(40, 60), evidence.StateOK, ""},
		{"WebP", "WEBP", webp1x1, evidence.StateOK, ""},
		{"30 megapixels", "PNG", pngHeader(6000, 5000), evidence.StateOK, evidence.ReasonHeaderOnly},
		{"24 megapixels", "PNG", pngHeader(6000, 4000), evidence.StateCorrupt, ""},
		{"24 megapixels in one RGBA16 row", "PNG", wideRGBA16PNG(), evidence.StateOK, evidence.ReasonHeaderOnly},
		{"a broken image data chunk", "PNG", brokenPNG(), evidence.StateCorrupt, ""},
		{"a truncated GIF", "GIF", truncatedGIF(), evidence.StateCorrupt, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := inspect.Inspect(context.Background(), tt.declared, bytes.NewReader(tt.data), int64(len(tt.data)), inspect.Limits{})
			checkInvariants(t, res)
			if res.State != tt.state || res.Reason != tt.reason {
				t.Errorf("got %s/%s, want %s/%s", res.State, res.Reason, tt.state, tt.reason)
			}
		})
	}
}
