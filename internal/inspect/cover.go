package inspect

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"io"
	"runtime/debug"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

// CoverResult is what inspecting a cover found (ADR-023). DHash and Flatness
// are set only for a decoded cover; Flatness is -1 otherwise.
type CoverResult struct {
	State         evidence.State
	Reason        string
	Detected      string
	Width, Height int
	DHash         string // 64-bit difference hash, 16 lower-case hex digits
	Flatness      int    // luminance standard deviation of a 32×32 thumbnail, in tenths
	Panic         string // a recovered panic for the caller's log; never sealed
}

// Cover decodes a cover within the image bounds and measures it. open is
// called at most twice: for the header and for the pixels. It never panics.
func Cover(open func() (io.ReadCloser, error), lim Limits) (res CoverResult) {
	lim = lim.withDefaults()
	res = CoverResult{Flatness: -1}
	defer func() {
		if v := recover(); v != nil {
			res = CoverResult{State: evidence.StateNotInspected, Reason: evidence.ReasonInspectorError, Flatness: -1,
				Detected: res.Detected, Width: res.Width, Height: res.Height, Panic: fmt.Sprintf("%v\n%s", v, debug.Stack())}
		}
	}()
	cfg, format, bpp, err := imageConfig(open)
	if err != nil && !errors.Is(err, errCorrupt) {
		return CoverResult{State: evidence.StateNotInspected, Reason: evidence.ReasonInspectorError, Flatness: -1}
	}
	if err != nil {
		res.State = evidence.StateCorrupt
		return res
	}
	res.Detected = format
	if cfg.Width <= 0 || cfg.Height <= 0 {
		res.State = evidence.StateCorrupt
		return res
	}
	res.Width, res.Height = cfg.Width, cfg.Height
	if !decodeFits(int64(cfg.Width), int64(cfg.Height), bpp, lim) {
		res.State, res.Reason = evidence.StateNotInspected, evidence.ReasonTooLarge
		return res
	}
	rc, err := open()
	if err != nil {
		return CoverResult{State: evidence.StateNotInspected, Reason: evidence.ReasonInspectorError, Flatness: -1}
	}
	img, _, err := image.Decode(rc)
	_ = rc.Close()
	if err != nil {
		res.State = evidence.StateCorrupt
		return res
	}
	b := img.Bounds()
	if b.Dx() != cfg.Width || b.Dy() != cfg.Height {
		res.State = evidence.StateCorrupt
		return res
	}
	res.State = evidence.StateOK
	res.DHash = fmt.Sprintf("%016x", dhash(thumbnail(img, 9, 8)))
	res.Flatness = flatness(thumbnail(img, 32, 32))
	return res
}

var errEmptyImage = errors.New("inspect: empty image")

// luma is an 8-bit luminance with integer weights.
func luma(c interface{ RGBA() (r, g, b, a uint32) }) int64 {
	r, g, b, _ := c.RGBA()
	return (299*int64(r>>8) + 587*int64(g>>8) + 114*int64(b>>8)) / 1000
}

// thumbnail averages img's luminance over a tw×th grid of cells, each at
// least one pixel, using integers only.
func thumbnail(img image.Image, tw, th int) [][]int64 {
	b := img.Bounds()
	W, H := b.Dx(), b.Dy()
	if W <= 0 || H <= 0 {
		panic(errEmptyImage)
	}
	cells := make([][]int64, th)
	for cy := range th {
		cells[cy] = make([]int64, tw)
		y0 := cy * H / th
		y1 := max((cy+1)*H/th, y0+1)
		for cx := range tw {
			x0 := cx * W / tw
			x1 := max((cx+1)*W/tw, x0+1)
			var sum, n int64
			for y := y0; y < y1; y++ {
				for x := x0; x < x1; x++ {
					sum += luma(img.At(b.Min.X+x, b.Min.Y+y))
					n++
				}
			}
			cells[cy][cx] = sum / n
		}
	}
	return cells
}

// dhash sets bit y*8+x, from the most significant end, where a cell is
// brighter than its right-hand neighbour.
func dhash(cells [][]int64) uint64 {
	var h uint64
	for y := range 8 {
		for x := range 8 {
			if cells[y][x] > cells[y][x+1] {
				h |= 1 << (63 - (y*8 + x))
			}
		}
	}
	return h
}

// flatness is the cells' standard deviation in tenths, rounded down.
func flatness(cells [][]int64) int {
	var n, sum, sq int64
	for _, row := range cells {
		for _, v := range row {
			n++
			sum += v
			sq += v * v
		}
	}
	return int(isqrt((n*sq - sum*sum) * 100 / (n * n)))
}

// isqrt is the integer square root of v ≥ 0, rounded down; 0 for v < 0.
func isqrt(v int64) int64 {
	var r int64
	for bit := int64(1) << 62; bit != 0; bit >>= 2 {
		if v >= r+bit {
			v -= r + bit
			r = r>>1 + bit
		} else {
			r >>= 1
		}
	}
	return r
}

// MeasureCover measures an image held in memory as a cover candidate
// (ADR-043): the same dimensions, difference hash and flatness as Cover, plus
// the SHA-256 of the bytes and the media type the bytes show. Source and ISBN
// are the caller's. State is ok, corrupt or not_inspected; the caller decides
// missing and too_large before it has bytes. It never keeps data.
func MeasureCover(data []byte, lim Limits) evidence.CoverCandidate {
	open := func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil }
	r := Cover(open, lim)
	sum := sha256.Sum256(data)
	c := evidence.CoverCandidate{State: r.State, SHA256: hex.EncodeToString(sum[:]), Flatness: -1}
	switch r.State {
	case evidence.StateOK:
		c.Width, c.Height, c.DHash, c.Flatness = r.Width, r.Height, r.DHash, r.Flatness
		c.MediaType = "image/" + r.Detected
	case evidence.StateNotInspected:
		c.Width, c.Height = r.Width, r.Height
		if r.Detected != "" {
			c.MediaType = "image/" + r.Detected
		}
	}
	return c
}
