package inspect_test

import (
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/inspect"
)

func TestXMLText(t *testing.T) {
	utf16le := []byte{0xFF, 0xFE}
	for _, r := range `<t>Café</t>` {
		utf16le = append(utf16le, byte(r), byte(r>>8))
	}
	tests := []struct {
		name string
		in   []byte
		want string
	}{
		{"UTF-8", []byte(`<t>Café</t>`), `<t>Café</t>`},
		{"UTF-8 with a BOM", []byte("\xef\xbb\xbf<t>Café</t>"), `<t>Café</t>`},
		{"declared Latin-1", []byte(`<?xml version="1.0" encoding="ISO-8859-1"?><t>Caf` + "\xe9</t>"), `<?xml version="1.0" encoding="ISO-8859-1"?><t>Café</t>`},
		{"declared Windows-1252", []byte(`<?xml version='1.0' encoding='windows-1252'?><t>` + "\x93q\x94</t>"), `<?xml version='1.0' encoding='windows-1252'?><t>“q”</t>`},
		{"UTF-16LE with a BOM", utf16le, `<t>Café</t>`},
		{"an XML 1.1 declaration", []byte(`<?xml version="1.1" encoding="UTF-8"?><t/>`), `<?xml version="1.0" encoding="UTF-8"?><t/>`},
		{"an XML 1.1 declaration in single quotes", []byte("\xef\xbb\xbf<?xml version = '1.1'?><t/>"), `<?xml version = '1.0'?><t/>`},
		{"a version attribute that isn't the declaration's", []byte(`<package version="1.1"/>`), `<package version="1.1"/>`},
		{"broken UTF-8 and characters XML forbids", []byte("<t>a\x00b\x01c\xffd\te</t>"), "<t>abc�d\te</t>"},
	}
	for _, tt := range tests {
		if got := string(inspect.XMLText(tt.in)); got != tt.want {
			t.Errorf("%s: XMLText = %q, want %q", tt.name, got, tt.want)
		}
	}
}
