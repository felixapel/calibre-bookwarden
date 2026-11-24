package inspecttest

import (
	"archive/zip"
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"slices"
)

func gradient(w, h int) *image.Gray {
	img := image.NewGray(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetGray(x, y, color.Gray{Y: uint8((x + y) % 256)}) //nolint:gosec // modulo 256
		}
	}
	return img
}

// PNG is a w×h greyscale PNG.
func PNG(w, h int) []byte {
	var buf bytes.Buffer
	if err := png.Encode(&buf, gradient(w, h)); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// JPEG is a w×h greyscale JPEG.
func JPEG(w, h int) []byte {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, gradient(w, h), nil); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// CBZ is a comic archive holding pages, written in name order.
func CBZ(pages map[string][]byte) []byte {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	names := make([]string, 0, len(pages))
	for name := range pages {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		f, err := w.Create(name)
		if err == nil {
			_, err = f.Write(pages[name])
		}
		if err != nil {
			panic(err)
		}
	}
	if err := w.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}
