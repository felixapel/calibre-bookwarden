package inspect

import (
	"bytes"
	"encoding/xml"
	"strings"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

const (
	rdfNamespace   = "http://www.w3.org/1999/02/22-rdf-syntax-ns#"
	pdfxNamespace  = "http://ns.adobe.com/pdfx/1.3/"
	prismNamespace = "http://prismstandard.org/namespaces/" // any version
)

// xmpKey names the properties bookwarden reads from XMP, or returns "".
func xmpKey(n xml.Name) string {
	switch {
	case n.Space == dcNamespace:
		switch n.Local {
		case "title", "creator", "language", "publisher", "date", "identifier":
			return "dc:" + n.Local
		}
	case strings.HasPrefix(n.Space, prismNamespace) && (n.Local == "publicationDate" || n.Local == "isbn"):
		return "prism:" + n.Local
	case n.Space == pdfxNamespace && n.Local == "ISBN":
		return "pdfx:ISBN"
	}
	return ""
}

// parseXMP reads the raw values of the Dublin Core, PRISM and pdfx properties
// in an XMP packet: as elements, rdf:li lists or rdf:Description attributes.
// A packet with a DTD, past maxXML bytes, or that isn't XML yields nothing.
func parseXMP(b []byte) evidence.FileMetadata {
	var m evidence.FileMetadata
	if len(b) > maxXML || bytes.Contains(b, []byte("<!DOCTYPE")) {
		return m
	}
	var prismDate, dcDate string
	add := func(key string, items, langs []string) {
		var values []string
		for _, v := range items {
			if v = strings.TrimSpace(v); v != "" {
				values = append(values, v)
			}
		}
		if len(values) == 0 {
			return
		}
		switch key {
		case "dc:title":
			if m.Title == "" {
				m.Title = values[0]
				for i, lang := range langs {
					if lang == "x-default" && strings.TrimSpace(items[i]) != "" {
						m.Title = strings.TrimSpace(items[i])
					}
				}
			}
		case "dc:creator":
			m.Authors = append(m.Authors, values...)
		case "dc:language":
			m.Languages = append(m.Languages, values...)
		case "dc:publisher":
			if m.Publisher == "" {
				m.Publisher = values[0]
			}
		case "dc:date":
			if dcDate == "" {
				dcDate = values[0]
			}
		case "prism:publicationDate":
			if prismDate == "" {
				prismDate = values[0]
			}
		case "prism:isbn", "pdfx:ISBN", "dc:identifier":
			m.ISBNs = append(m.ISBNs, values...)
		}
	}

	type property struct {
		key          string
		depth        int
		items, langs []string
		text         strings.Builder
		inItem       bool
		lang         string
	}
	var cur *property
	depth := 0
	d := newXMLDecoder(b, false)
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			if cur == nil {
				for _, a := range t.Attr {
					if key := xmpKey(a.Name); key != "" {
						add(key, []string{a.Value}, nil)
					}
				}
				if key := xmpKey(t.Name); key != "" {
					cur = &property{key: key, depth: depth}
				}
			} else if t.Name.Space == rdfNamespace && t.Name.Local == "li" {
				cur.inItem, cur.lang = true, attr(t, "lang")
				cur.text.Reset()
			}
		case xml.EndElement:
			switch {
			case cur == nil:
			case cur.inItem && t.Name.Space == rdfNamespace && t.Name.Local == "li":
				cur.items, cur.langs = append(cur.items, cur.text.String()), append(cur.langs, cur.lang)
				cur.inItem = false
				cur.text.Reset()
			case depth == cur.depth:
				if len(cur.items) == 0 {
					cur.items = []string{cur.text.String()}
				}
				add(cur.key, cur.items, cur.langs)
				cur = nil
			}
			depth--
		case xml.CharData:
			if cur != nil {
				cur.text.Write(t)
			}
		}
	}
	m.Date = prismDate
	if m.Date == "" {
		m.Date = dcDate
	}
	return m
}
