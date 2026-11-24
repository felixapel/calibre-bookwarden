// Package inspecttest builds small synthetic files for the inspectors' tests.
// Every title, name and ISBN in them is invented.
package inspecttest

import (
	"archive/zip"
	"bytes"
	"cmp"
	"fmt"
	"hash/crc32"
	"maps"
	"slices"
	"strings"
)

// EPUBOptions describe a generated EPUB. Strings are written as given, with
// only the XML metacharacters escaped, so tests can put hostile bytes in them.
type EPUBOptions struct {
	Title        string
	Authors      []string
	Illustrators []string
	Language     string
	ISBN         string // EPUB 2 marks it opf:scheme="ISBN"
	ISBNScheme   string // replaces "ISBN" as the EPUB 2 opf:scheme
	Publisher    string
	Date         string // the publication date
	ModifiedDate string // a modification date next to it
	Text         string // the chapter's body text; "" leaves only an image
	ContentDoc   string // raw XHTML that replaces the generated chapter
	EPUB3        bool
	ExtraMeta    string // raw OPF metadata elements, written after the generated ones
	Encryption   string // raw META-INF/encryption.xml; "" for none
	Rights       bool   // adds META-INF/rights.xml, as Adobe ADEPT does
	NoContainer  bool
	NoOPF        bool     // the container points at an OPF that isn't there
	NoChapter    bool     // the spine points at a chapter that isn't there
	Extra        []string // raw XHTML for more spine documents after the first
	Nav          string   // raw EPUB 3 navigation document, OEBPS/nav.xhtml, listed with properties="nav"
	Guide        string   // raw <guide> element of the OPF
	ExtraNavs    []string // more navigation documents, OEBPS/nav1.xhtml and so on, each listed with properties="nav"
	MissingNav   bool     // lists a navigation document, OEBPS/gone.xhtml, that isn't in the archive
	MimetypeLast bool
	XML11        bool              // every generated XML document declares version 1.1
	CoverImage   []byte            // the bytes of OEBPS/images/cover.png, manifest id "cover"; "" keeps a placeholder text
	ManifestXML  string            // raw manifest items, after the generated ones
	Files        map[string][]byte // more archive entries, by full name
}

var escape = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")

// EncryptionXML is a META-INF/encryption.xml that encrypts uri with algorithm.
func EncryptionXML(algorithm, uri string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<encryption xmlns="urn:oasis:names:tc:opendocument:xmlns:container" xmlns:enc="http://www.w3.org/2001/04/xmlenc#">
  <enc:EncryptedData>
    <enc:EncryptionMethod Algorithm="` + algorithm + `"/>
    <enc:CipherData><enc:CipherReference URI="` + uri + `"/></enc:CipherData>
  </enc:EncryptedData>
</encryption>`
}

// EPUB builds an EPUB whose OPF is OEBPS/content.opf and whose one chapter is
// "OEBPS/text/chapter 1.xhtml", referenced URL-escaped.
func EPUB(o EPUBOptions) []byte {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	add := func(name, data string) {
		if o.XML11 {
			data = strings.Replace(data, `<?xml version="1.0"`, `<?xml version="1.1"`, 1)
		}
		f, err := w.Create(name)
		if err == nil {
			_, err = f.Write([]byte(data))
		}
		if err != nil {
			panic(err)
		}
	}
	mimetype := func() {
		data := []byte("application/epub+zip")
		f, err := w.CreateRaw(&zip.FileHeader{
			Name: "mimetype", Method: zip.Store, CRC32: crc32.ChecksumIEEE(data),
			CompressedSize64: uint64(len(data)), UncompressedSize64: uint64(len(data)),
		})
		if err == nil {
			_, err = f.Write(data)
		}
		if err != nil {
			panic(err)
		}
	}
	if !o.MimetypeLast {
		mimetype()
	}
	if !o.NoContainer {
		add("META-INF/container.xml", `<?xml version="1.0"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles>
</container>`)
	}
	if o.Nav != "" {
		add("OEBPS/nav.xhtml", o.Nav)
	}
	for i, nav := range o.ExtraNavs {
		add(fmt.Sprintf("OEBPS/nav%d.xhtml", i+1), nav)
	}
	if o.Encryption != "" {
		add("META-INF/encryption.xml", o.Encryption)
	}
	if o.Rights {
		add("META-INF/rights.xml", `<?xml version="1.0"?><adept:rights xmlns:adept="http://ns.adobe.com/adept"/>`)
	}
	if !o.NoOPF {
		add("OEBPS/content.opf", opf(o))
	}
	if !o.NoChapter {
		add("OEBPS/text/chapter 1.xhtml", chapter(o))
	}
	for i, doc := range o.Extra {
		add(fmt.Sprintf("OEBPS/text/extra%d.xhtml", i), doc)
	}
	if o.CoverImage != nil {
		add("OEBPS/images/cover.png", string(o.CoverImage))
	} else {
		add("OEBPS/images/cover.png", "not really a png")
	}
	for _, name := range slices.Sorted(maps.Keys(o.Files)) {
		add(name, string(o.Files[name]))
	}
	if o.MimetypeLast {
		mimetype()
	}
	if err := w.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func opf(o EPUBOptions) string {
	var m strings.Builder
	el := func(format string, args ...any) { fmt.Fprintf(&m, "    "+format+"\n", args...) }
	version := "2.0"
	if o.EPUB3 {
		version = "3.0"
	}
	if o.Title != "" {
		el("<dc:title>%s</dc:title>", escape.Replace(o.Title))
	}
	creators := 0
	creator := func(name, role string) {
		creators++
		if o.EPUB3 {
			el(`<dc:creator id="c%d">%s</dc:creator>`, creators, escape.Replace(name))
			el(`<meta refines="#c%d" property="role" scheme="marc:relators">%s</meta>`, creators, role)
		} else {
			el(`<dc:creator opf:role="%s">%s</dc:creator>`, role, escape.Replace(name))
		}
	}
	for _, a := range o.Authors {
		creator(a, "aut")
	}
	for _, a := range o.Illustrators {
		creator(a, "ill")
	}
	if o.Language != "" {
		el("<dc:language>%s</dc:language>", escape.Replace(o.Language))
	}
	el(`<dc:identifier id="uid" opf:scheme="uuid">urn:uuid:0c57e75c-16aa-4d88-ae0e-916c8f0e8b8e</dc:identifier>`)
	if o.ISBN != "" {
		if o.EPUB3 {
			el("<dc:identifier>%s</dc:identifier>", escape.Replace(o.ISBN))
		} else {
			scheme := cmp.Or(o.ISBNScheme, "ISBN")
			el(`<dc:identifier opf:scheme="%s">%s</dc:identifier>`, escape.Replace(scheme), escape.Replace(o.ISBN))
		}
	}
	if o.Publisher != "" {
		el("<dc:publisher>%s</dc:publisher>", escape.Replace(o.Publisher))
	}
	if o.ModifiedDate != "" {
		if o.EPUB3 {
			el(`<meta property="dcterms:modified">%s</meta>`, escape.Replace(o.ModifiedDate))
		} else {
			el(`<dc:date opf:event="modification">%s</dc:date>`, escape.Replace(o.ModifiedDate))
		}
	}
	if o.Date != "" {
		if o.EPUB3 {
			el("<dc:date>%s</dc:date>", escape.Replace(o.Date))
		} else {
			el(`<dc:date opf:event="publication">%s</dc:date>`, escape.Replace(o.Date))
		}
	}
	if o.ExtraMeta != "" {
		m.WriteString(o.ExtraMeta + "\n")
	}
	var extraItems, extraRefs strings.Builder
	for i := range o.Extra {
		fmt.Fprintf(&extraItems, "    <item id=\"x%d\" href=\"text/extra%d.xhtml\" media-type=\"application/xhtml+xml\"/>\n", i, i)
		fmt.Fprintf(&extraRefs, `<itemref idref="x%d"/>`, i)
	}
	nav := ""
	if o.Nav != "" {
		nav = `    <item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>` + "\n"
	}
	for i := range o.ExtraNavs {
		nav += fmt.Sprintf("    <item id=\"nav%d\" href=\"nav%d.xhtml\" media-type=\"application/xhtml+xml\" properties=\"nav\"/>\n", i+1, i+1)
	}
	if o.MissingNav {
		nav += `    <item id="gone" href="gone.xhtml" media-type="application/xhtml+xml" properties="nav"/>` + "\n"
	}
	return `<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="` + version + `" unique-identifier="uid">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:opf="http://www.idpf.org/2007/opf">
` + m.String() + `  </metadata>
  <manifest>
    <item id="ch1" href="text/chapter%201.xhtml" media-type="application/xhtml+xml"/>
    <item id="cover" href="images/cover.png" media-type="image/png"/>
` + extraItems.String() + nav + o.ManifestXML + `  </manifest>
  <spine><itemref idref="ch1"/>` + extraRefs.String() + `</spine>
` + o.Guide + `
</package>`
}

func chapter(o EPUBOptions) string {
	if o.ContentDoc != "" {
		return o.ContentDoc
	}
	body := `<div><img src="../images/cover.png" alt="cover"/></div>`
	if o.Text != "" {
		body = "<p>" + escape.Replace(o.Text) + "</p>"
	}
	return `<?xml version="1.0" encoding="utf-8"?>
<!DOCTYPE html>
<html xmlns="http://www.w3.org/1999/xhtml"><head><title>Chapter</title><style>p { margin: 0 }</style></head>
<body>` + body + `</body></html>`
}
