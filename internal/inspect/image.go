package inspect

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/gif"  // registers the GIF decoder
	_ "image/jpeg" // registers the JPEG decoder
	_ "image/png"  // registers the PNG decoder
	"io"

	_ "golang.org/x/image/webp" // registers the WebP decoder

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

// maxImageDecodeBytes bounds one decode: the pixels at the format's bytes per
// pixel (decodeBytesPerPixel) plus two PNG row buffers for wide rows.
const maxImageDecodeBytes = 256 << 20

// decodeHead is how much of an image is kept to find a JPEG frame header.
const decodeHead = 1 << 20

// imageConfig reads an image's header from one open: its config, format and
// the decoded bytes per pixel to budget for. A failed open is returned as
// is; a header that doesn't parse wraps errCorrupt.
func imageConfig(open func() (io.ReadCloser, error)) (image.Config, string, int64, error) {
	rc, err := open()
	if err != nil {
		return image.Config{}, "", 0, err
	}
	defer func() { _ = rc.Close() }()
	head, err := io.ReadAll(io.LimitReader(rc, decodeHead))
	if err != nil {
		return image.Config{}, "", 0, err
	}
	cfg, format, err := image.DecodeConfig(io.MultiReader(bytes.NewReader(head), rc))
	if err != nil {
		return cfg, format, 0, fmt.Errorf("%w: image header: %w", errCorrupt, err)
	}
	return cfg, format, decodeBytesPerPixel(format, head), nil
}

// decodeBytesPerPixel is the most memory a decode of format takes per pixel.
// RGBA16 PNGs take 8. Go's progressive JPEG decoder also keeps a 4-byte
// coefficient per sample, so it takes up to 8 per component; a JPEG whose
// frame header isn't found in head is budgeted as progressive CMYK.
func decodeBytesPerPixel(format string, head []byte) int64 {
	if format != "jpeg" {
		return 8
	}
	const unknown = 32
	if len(head) < 2 || head[0] != 0xff || head[1] != 0xd8 {
		return unknown
	}
	for i := 2; i+4 <= len(head); {
		if head[i] != 0xff {
			return unknown
		}
		m := head[i+1]
		switch {
		case m == 0xff: // fill byte
			i++
			continue
		case m == 0x01 || (m >= 0xd0 && m <= 0xd8): // markers without a length
			i += 2
			continue
		case m == 0xda || m == 0xd9: // a scan or the end before any frame
			return unknown
		}
		if (m >= 0xc0 && m <= 0xcf) && m != 0xc4 && m != 0xc8 && m != 0xcc { // a frame header
			if i+9 >= len(head) {
				return unknown
			}
			if m == 0xc2 || m == 0xc6 || m == 0xca || m == 0xce { // progressive
				return max(8, 8*int64(head[i+9]))
			}
			return 8
		}
		i += 2 + (int(head[i+2])<<8 | int(head[i+3]))
	}
	return unknown
}

// decodeFits reports whether decoding w×h pixels at bpp bytes each stays
// within lim.MaxPixels and maxImageDecodeBytes.
func decodeFits(w, h, bpp int64, lim Limits) bool {
	if w > lim.MaxPixels/h || w > (maxImageDecodeBytes-32)/bpp/h {
		return false
	}
	return w <= (int64(maxImageDecodeBytes)-32-bpp*w*h)/16
}

func inspectImage(_ context.Context, r io.ReaderAt, size int64, lim Limits) Result {
	headerOnly, err := decodeImage(func() (io.ReadCloser, error) {
		return io.NopCloser(io.NewSectionReader(r, 0, size)), nil
	}, lim)
	if err != nil {
		return failure(err)
	}
	if headerOnly {
		return Result{State: evidence.StateOK, Reason: evidence.ReasonHeaderOnly}
	}
	return Result{State: evidence.StateOK}
}

// decodeImage reads an image's header, then decodes every pixel unless it has
// more than lim.MaxPixels or the decoded-memory allowance, in which case only
// the header is checked.
func decodeImage(open func() (io.ReadCloser, error), lim Limits) (headerOnly bool, err error) {
	cfg, _, bpp, err := imageConfig(open)
	if err != nil {
		return false, err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return false, fmt.Errorf("%w: image dimensions", errCorrupt)
	}
	if !decodeFits(int64(cfg.Width), int64(cfg.Height), bpp, lim) {
		return true, nil
	}
	rc, err := open()
	if err != nil {
		return false, err
	}
	_, _, err = image.Decode(rc)
	_ = rc.Close()
	if err != nil {
		return false, fmt.Errorf("%w: image: %w", errCorrupt, err)
	}
	return false, nil
}
