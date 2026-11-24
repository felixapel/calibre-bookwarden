// Package pdf is a small, lazy PDF reader for inspection: it finds objects
// through the cross-reference data (or by scanning, when that is broken),
// parses them on demand and decodes the common stream filters. Every size,
// count and depth is bounded, and every loop checks the caller's context.
package pdf

import (
	"errors"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Object is a PDF object: nil, bool, int64, float64, Name, String, Array,
// Dict, Ref or Stream.
type Object any

type (
	// Name is a PDF name without its slash.
	Name string
	// String is a PDF string's bytes.
	String []byte
	// Array is a PDF array.
	Array []Object
	// Dict is a PDF dictionary.
	Dict map[Name]Object
	// Ref is an indirect reference.
	Ref struct{ Num, Gen int }
	// Stream is a stream's dictionary and where its data lies in the file.
	Stream struct {
		Dict           Dict
		Offset, Length int64
	}
	// keyword is a bare word: an operator, or obj, stream, R and the like.
	keyword string
)

var (
	ErrNotPDF            = errors.New("pdf: not a PDF file")
	ErrUnsupportedFilter = errors.New("pdf: unsupported stream filter")
	ErrTooLarge          = errors.New("pdf: past a size limit")
	errSyntax            = errors.New("pdf: syntax error")
)

// pdfDocEncoding holds the code points where PDFDocEncoding differs from
// Latin-1; 0 marks an undefined byte.
var pdfDocEncoding = map[byte]rune{
	0x18: '˘', 0x19: 'ˇ', 0x1A: 'ˆ', 0x1B: '˙', 0x1C: '˝', 0x1D: '˛', 0x1E: '˚', 0x1F: '˜',
	0x80: '•', 0x81: '†', 0x82: '‡', 0x83: '…', 0x84: '—', 0x85: '–', 0x86: 'ƒ', 0x87: '⁄',
	0x88: '‹', 0x89: '›', 0x8A: '−', 0x8B: '‰', 0x8C: '„', 0x8D: '“', 0x8E: '”', 0x8F: '‘',
	0x90: '’', 0x91: '‚', 0x92: '™', 0x93: 'ﬁ', 0x94: 'ﬂ', 0x95: 'Ł', 0x96: 'Œ', 0x97: 'Š',
	0x98: 'Ÿ', 0x99: 'Ž', 0x9A: 'ı', 0x9B: 'ł', 0x9C: 'œ', 0x9D: 'š', 0x9E: 'ž', 0x9F: 0,
	0xA0: '€', 0xAD: 0,
}

// TextString decodes a PDF text string: UTF-16BE after FE FF, UTF-8 after
// EF BB BF, and PDFDocEncoding otherwise.
func TextString(b String) string {
	switch {
	case len(b) >= 2 && b[0] == 0xFE && b[1] == 0xFF:
		u := make([]uint16, 0, len(b)/2)
		for i := 2; i+1 < len(b); i += 2 {
			u = append(u, uint16(b[i])<<8|uint16(b[i+1]))
		}
		return string(utf16.Decode(u))
	case len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF:
		return strings.ToValidUTF8(string(b[3:]), string(utf8.RuneError))
	}
	var s strings.Builder
	for _, c := range b {
		r, ok := pdfDocEncoding[c]
		switch {
		case !ok:
			r = rune(c)
		case r == 0:
			r = utf8.RuneError
		}
		s.WriteRune(r)
	}
	return s.String()
}
