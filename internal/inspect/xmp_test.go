package inspect_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/inspect"
)

func xmpPacket(description string) string {
	return `<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?>
<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">
` + description + `
</rdf:RDF></x:xmpmeta><?xpacket end="w"?>`
}

const xmpNamespaces = `xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:prism="http://prismstandard.org/namespaces/basic/3.0/" xmlns:pdfx="http://ns.adobe.com/pdfx/1.3/"`

func TestXMP(t *testing.T) {
	full := xmpPacket(`<rdf:Description rdf:about="" ` + xmpNamespaces + `>
  <dc:title><rdf:Alt><rdf:li xml:lang="fr">Le Titre</rdf:li><rdf:li xml:lang="x-default">The Title</rdf:li></rdf:Alt></dc:title>
  <dc:creator><rdf:Seq><rdf:li>Ada Example</rdf:li><rdf:li>Bea Sample</rdf:li></rdf:Seq></dc:creator>
  <dc:language><rdf:Bag><rdf:li>en</rdf:li><rdf:li>de</rdf:li></rdf:Bag></dc:language>
  <dc:publisher><rdf:Bag><rdf:li>Nobody Press</rdf:li></rdf:Bag></dc:publisher>
  <dc:date><rdf:Seq><rdf:li>2001-01-01</rdf:li></rdf:Seq></dc:date>
  <prism:publicationDate>2019-05-04</prism:publicationDate>
  <prism:isbn>978-0-00-000002-6</prism:isbn>
  <dc:identifier>urn:isbn:9780000000019</dc:identifier>
  <dc:identifier>doi:10.1000/182</dc:identifier>
</rdf:Description>
<rdf:Description rdf:about="" ` + xmpNamespaces + ` pdfx:ISBN="9780000000040"/>`)
	tests := []struct {
		name string
		in   string
		want evidence.FileMetadata
	}{
		{"every field", full, evidence.FileMetadata{
			Title: "The Title", Authors: []string{"Ada Example", "Bea Sample"}, Languages: []string{"en", "de"},
			ISBNs:     []string{"978-0-00-000002-6", "urn:isbn:9780000000019", "doi:10.1000/182", "9780000000040"},
			Publisher: "Nobody Press", Date: "2019-05-04",
		}},
		{"dc:date without a PRISM date", xmpPacket(`<rdf:Description ` + xmpNamespaces + `><dc:date><rdf:Seq><rdf:li>2001-01-01</rdf:li></rdf:Seq></dc:date></rdf:Description>`),
			evidence.FileMetadata{Date: "2001-01-01"}},
		{"properties as attributes", xmpPacket(`<rdf:Description ` + xmpNamespaces + ` dc:publisher="Attr Press" prism:publicationDate="2020"/>`),
			evidence.FileMetadata{Publisher: "Attr Press", Date: "2020"}},
		{"a title without rdf:Alt", xmpPacket(`<rdf:Description ` + xmpNamespaces + `><dc:title>Plain</dc:title></rdf:Description>`),
			evidence.FileMetadata{Title: "Plain"}},
		{"a DOCTYPE", `<!DOCTYPE x [<!ENTITY a "aaaa">]>` + full, evidence.FileMetadata{}},
		{"past 4 MiB", full + strings.Repeat(" ", 4<<20), evidence.FileMetadata{}},
		{"not XML", "\x00\x01 not xml at all", evidence.FileMetadata{}},
	}
	for _, tt := range tests {
		if got := inspect.ParseXMP([]byte(tt.in)); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s: ParseXMP =\n%+v\nwant\n%+v", tt.name, got, tt.want)
		}
	}
}
