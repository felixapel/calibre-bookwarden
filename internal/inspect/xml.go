package inspect

import (
	"bytes"
	"encoding/xml"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/unicode"
)

// maxXML bounds one XML document read from a file.
const maxXML = 4 << 20

var xmlEncoding = regexp.MustCompile(`^\s*<\?xml[^>]*encoding\s*=\s*["']([A-Za-z0-9._-]+)["']`)

// xml11 matches an XML 1.1 declaration's version.
var xml11 = regexp.MustCompile(`^(\s*<\?xml\s+version\s*=\s*["'])1\.1(["'])`)

// xmlText turns an XML document into valid UTF-8 holding only characters XML
// allows. UTF-16 (by its BOM) and a declared Latin-1 or Windows-1252 are
// decoded first; anything else is read as UTF-8, and broken bytes become U+FFFD.
// An XML 1.1 declaration becomes 1.0, since encoding/xml reads only 1.0 and
// readers open such books; the versions differ in allowed characters and line
// ends, which no check here depends on.
func xmlText(b []byte) []byte {
	switch {
	case bytes.HasPrefix(b, []byte{0xFF, 0xFE}), bytes.HasPrefix(b, []byte{0xFE, 0xFF}):
		if u, err := unicode.UTF16(unicode.LittleEndian, unicode.UseBOM).NewDecoder().Bytes(b); err == nil {
			b = u
		}
	case bytes.HasPrefix(b, []byte{0xEF, 0xBB, 0xBF}):
		b = b[3:]
	default:
		if m := xmlEncoding.FindSubmatch(b[:min(len(b), 256)]); m != nil {
			switch strings.ToLower(string(m[1])) {
			case "iso-8859-1", "latin1", "latin-1", "iso_8859-1", "windows-1252", "cp1252":
				if u, err := charmap.Windows1252.NewDecoder().Bytes(b); err == nil {
					b = u
				}
			}
		}
	}
	out := make([]byte, 0, len(b))
	for len(b) > 0 {
		r, n := utf8.DecodeRune(b)
		b = b[n:]
		if r == '\t' || r == '\n' || r == '\r' || r >= 0x20 && r != 0xFFFE && r != 0xFFFF {
			out = utf8.AppendRune(out, r)
		}
	}
	return xml11.ReplaceAll(out, []byte("${1}1.0${2}"))
}

// newXMLDecoder is a lenient decoder over xmlText(b): HTML entities, no
// strictness, and any declared encoding read as the UTF-8 it now is. html
// also closes HTML's void elements. encoding/xml never expands DTD entities.
func newXMLDecoder(b []byte, html bool) *xml.Decoder {
	d := xml.NewDecoder(bytes.NewReader(xmlText(b)))
	d.Strict = false
	d.Entity = xml.HTMLEntity
	d.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
	if html {
		d.AutoClose = xml.HTMLAutoClose
	}
	return d
}

// attr returns the value of the attribute with the local name, in any namespace.
func attr(se xml.StartElement, local string) string {
	for _, a := range se.Attr {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}
