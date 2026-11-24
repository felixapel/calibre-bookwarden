package pdf_test

import (
	"bytes"
	"compress/flate"
	"compress/zlib"
	"encoding/ascii85"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/inspect/pdf"
)

func zlibBytes(b []byte) []byte {
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	_, _ = w.Write(b)
	_ = w.Close()
	return buf.Bytes()
}

func rawDeflate(b []byte) []byte {
	var buf bytes.Buffer
	w, _ := flate.NewWriter(&buf, flate.BestCompression)
	_, _ = w.Write(b)
	_ = w.Close()
	return buf.Bytes()
}

// pngRows encodes rows of width bytes with one PNG filter, for 1 byte per pixel.
func pngRows(data []byte, width int, filter byte) []byte {
	var out []byte
	prev := make([]byte, width)
	for r := 0; r < len(data); r += width {
		row := data[r : r+width]
		out = append(out, filter)
		for i, x := range row {
			var left, up, upLeft byte
			if i > 0 {
				left, upLeft = row[i-1], prev[i-1]
			}
			up = prev[i]
			switch filter {
			case 0:
				out = append(out, x)
			case 1:
				out = append(out, x-left)
			case 2:
				out = append(out, x-up)
			case 3:
				out = append(out, x-byte((int(left)+int(up))/2))
			case 4:
				out = append(out, x-paeth(left, up, upLeft))
			}
		}
		prev = row
	}
	return out
}

func paeth(a, b, c byte) byte {
	p := int(a) + int(b) - int(c)
	pa, pb, pc := abs(p-int(a)), abs(p-int(b)), abs(p-int(c))
	switch {
	case pa <= pb && pa <= pc:
		return a
	case pb <= pc:
		return b
	}
	return c
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func TestDecode(t *testing.T) {
	text := []byte("BT /F1 12 Tf (Hello) Tj ET")
	for _, size := range []int{0, 4096, 8192, 10_000} {
		plain := bytes.Repeat([]byte{'p'}, size)
		decoded, err := pdf.DecodeBytes(zlibBytes(plain), pdf.Dict{"Filter": pdf.Name("FlateDecode")}, int64(size))
		if err != nil || !bytes.Equal(decoded, plain) || cap(decoded) > size {
			t.Fatalf("exact limit %d: got len %d/cap %d, error %v", size, len(decoded), cap(decoded), err)
		}
	}
	a85 := make([]byte, ascii85.MaxEncodedLen(len(text)))
	a85 = a85[:ascii85.Encode(a85, text)]
	tests := []struct {
		name string
		raw  []byte
		dict pdf.Dict
		want []byte
	}{
		{"no filter", text, pdf.Dict{}, text},
		{"Flate", zlibBytes(text), pdf.Dict{"Filter": pdf.Name("FlateDecode")}, text},
		{"raw deflate without a zlib header", rawDeflate(text), pdf.Dict{"Filter": pdf.Name("FlateDecode")}, text},
		{"a truncated Flate stream", zlibBytes(text)[:len(zlibBytes(text))-4], pdf.Dict{"Filter": pdf.Name("FlateDecode")}, text},
		{"ASCIIHex then Flate", []byte(hex.EncodeToString(zlibBytes(text)) + ">"),
			pdf.Dict{"Filter": pdf.Array{pdf.Name("ASCIIHexDecode"), pdf.Name("FlateDecode")}}, text},
		{"ASCII85", append(a85, "~>"...), pdf.Dict{"Filter": pdf.Name("ASCII85Decode")}, text},
		{"abbreviated names", []byte(hex.EncodeToString(text)), pdf.Dict{"Filter": pdf.Name("AHx")}, text},
	}
	rows := bytes.Repeat([]byte{10, 20, 30, 40, 50, 60, 70, 80}, 4)
	for filter := range byte(5) {
		tests = append(tests, struct {
			name string
			raw  []byte
			dict pdf.Dict
			want []byte
		}{"PNG predictor " + string('0'+filter), zlibBytes(pngRows(rows, 8, filter)), pdf.Dict{
			"Filter": pdf.Name("FlateDecode"), "DecodeParms": pdf.Dict{"Predictor": int64(15), "Columns": int64(8)},
		}, rows})
	}
	for _, tt := range tests {
		got, err := pdf.DecodeBytes(tt.raw, tt.dict, 1<<20)
		if err != nil || !bytes.Equal(got, tt.want) {
			t.Errorf("%s: got %q, %v; want %q", tt.name, got, err, tt.want)
		}
	}

	if _, err := pdf.DecodeBytes([]byte{0x80}, pdf.Dict{"Filter": pdf.Name("LZWDecode")}, 1<<20); !errors.Is(err, pdf.ErrUnsupportedFilter) {
		t.Errorf("LZW: %v, want ErrUnsupportedFilter", err)
	}
	bomb := zlibBytes(make([]byte, 4<<20))
	if _, err := pdf.DecodeBytes(bomb, pdf.Dict{"Filter": pdf.Name("FlateDecode")}, 1<<20); !errors.Is(err, pdf.ErrTooLarge) {
		t.Errorf("a Flate bomb: %v, want ErrTooLarge", err)
	}
	if _, err := pdf.DecodeBytes(make([]byte, 2<<20), pdf.Dict{}, 1<<20); !errors.Is(err, pdf.ErrTooLarge) {
		t.Errorf("an unfiltered stream past the limit: %v, want ErrTooLarge", err)
	}
}
