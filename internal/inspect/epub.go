package inspect

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"slices"
	"strings"
	"unicode"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

// Bounds on the text check: enough chapters to find text in any real book.
const (
	maxSpineDocs = 50
	maxSpineText = 32 << 20
)

// fontObfuscation are the encryption.xml algorithms that only scramble
// embedded fonts; every reader opens such books.
var fontObfuscation = []string{"http://www.idpf.org/2008/embedding", "http://ns.adobe.com/pdf/enc#RC"}

// drmFiles mark books locked to a reader: Adobe ADEPT, Apple FairPlay, Readium LCP.
var drmFiles = []string{"META-INF/rights.xml", "META-INF/sinf.xml", "META-INF/license.lcpl"}

func inspectEPUB(ctx context.Context, r io.ReaderAt, size int64, lim Limits) Result {
	a, err := openZip(ctx, r, size, lim)
	if err == nil {
		err = a.verifyAll()
	}
	if err != nil {
		return failure(err)
	}
	opfPath, err := rootfile(a)
	if err != nil {
		return failure(err)
	}
	raw, err := a.read(opfPath, maxXML)
	if err != nil {
		return failure(err)
	}
	pkg, err := parseOPF(raw)
	if err != nil {
		return failure(err)
	}
	meta := pkg.metadata()
	drm, err := epubDRM(a)
	if err != nil {
		res := failure(err)
		res.Metadata = meta
		return res
	}
	if drm {
		return Result{State: evidence.StateDRM, Text: evidence.TextUnverified, Metadata: meta, FailReason: FailDRM}
	}
	text, err := epubText(a, pkg, path.Dir(opfPath))
	if err != nil {
		res := failure(err)
		res.Metadata = meta
		return res
	}
	hints, excerpt, why, docs := scanFrontMatter(a, pkg, path.Dir(opfPath))
	return Result{State: evidence.StateOK, Text: text, Metadata: meta, ISBNHints: hints, Excerpt: excerpt, FailReason: why, ScanDocs: docs, FileCover: fileCover(a, pkg, path.Dir(opfPath))}
}

// rootfile returns the OPF's path from META-INF/container.xml.
func rootfile(a *zipArchive) (string, error) {
	raw, err := a.read("META-INF/container.xml", maxXML)
	if err != nil {
		return "", err
	}
	d := newXMLDecoder(raw, false)
	fallback := ""
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "rootfile" {
			continue
		}
		p := strings.TrimPrefix(path.Clean(attr(se, "full-path")), "/")
		if attr(se, "media-type") == "application/oebps-package+xml" {
			return p, nil
		}
		if fallback == "" && strings.HasSuffix(strings.ToLower(p), ".opf") {
			fallback = p
		}
	}
	if fallback == "" {
		return "", fmt.Errorf("%w: no OPF in container.xml", errCorrupt)
	}
	return fallback, nil
}

type opfCreator struct{ id, role, name string }

type opfIdentifier struct{ scheme, value string }

type opfDate struct{ event, value string }

type opfItem struct{ href, mediaType, properties string }

// opfCollection is an EPUB 3 belongs-to-collection. Collections that refine
// another collection are nested ones and are not kept.
type opfCollection struct{ id, name string }

// opfReference is an EPUB 2 <guide> reference, or an EPUB 3 landmark.
type opfReference struct{ typ, href string }

// opfPackage is what bookwarden reads from an OPF.
type opfPackage struct {
	titles, languages, publishers []string
	creators                      []opfCreator
	roles                         map[string]string // EPUB 3: creator id → role
	identifiers                   []opfIdentifier
	dates                         []opfDate
	manifest                      map[string]opfItem
	manifestOrder                 []string // manifest ids in document order
	coverMeta                     string   // EPUB 2 <meta name="cover" content="id">
	spine                         []string
	guide                         []opfReference
	// Series: Calibre's EPUB 2 meta elements, and the EPUB 3 collections
	// with the types and positions that refine them, by the collection's id.
	calibreSeries, calibreIndex string
	collections                 []opfCollection
	collectionTypes, positions  map[string]string
}

const (
	dcNamespace   = "http://purl.org/dc/elements/1.1/"
	dc10Namespace = "http://purl.org/dc/elements/1.0/"
)

func parseOPF(raw []byte) (*opfPackage, error) {
	p := &opfPackage{roles: map[string]string{}, manifest: map[string]opfItem{}, collectionTypes: map[string]string{}, positions: map[string]string{}}
	d := newXMLDecoder(raw, false)
	sawPackage := false
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %w: OPF: %w", errCorrupt, errXML, err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		local := strings.ToLower(se.Name.Local)
		if se.Name.Space == dcNamespace || se.Name.Space == dc10Namespace || se.Name.Space == "dc" {
			var v struct {
				Text string `xml:",chardata"`
			}
			if err := d.DecodeElement(&v, &se); err != nil {
				return nil, fmt.Errorf("%w: %w: OPF: %w", errCorrupt, errXML, err)
			}
			text := strings.TrimSpace(v.Text)
			switch local {
			case "title":
				p.titles = append(p.titles, text)
			case "creator":
				p.creators = append(p.creators, opfCreator{id: attr(se, "id"), role: attr(se, "role"), name: text})
			case "language":
				p.languages = append(p.languages, text)
			case "identifier":
				p.identifiers = append(p.identifiers, opfIdentifier{scheme: attr(se, "scheme"), value: text})
			case "publisher":
				p.publishers = append(p.publishers, text)
			case "date":
				p.dates = append(p.dates, opfDate{event: attr(se, "event"), value: text})
			}
			continue
		}
		switch local {
		case "package":
			sawPackage = true
		case "meta":
			name, property, refines := attr(se, "name"), attr(se, "property"), strings.TrimPrefix(attr(se, "refines"), "#")
			switch {
			case name == "cover":
				if p.coverMeta == "" {
					p.coverMeta = attr(se, "content")
				}
			case name == "calibre:series":
				if p.calibreSeries == "" {
					p.calibreSeries = attr(se, "content")
				}
			case name == "calibre:series_index":
				if p.calibreIndex == "" {
					p.calibreIndex = attr(se, "content")
				}
			case refines != "" && (property == "role" || property == "collection-type" || property == "group-position"),
				property == "belongs-to-collection":
				var v struct {
					Text string `xml:",chardata"`
				}
				if err := d.DecodeElement(&v, &se); err != nil {
					return nil, fmt.Errorf("%w: %w: OPF: %w", errCorrupt, errXML, err)
				}
				text := strings.TrimSpace(v.Text)
				switch property {
				case "role":
					p.roles[refines] = text
				case "collection-type":
					p.collectionTypes[refines] = text
				case "group-position":
					p.positions[refines] = text
				case "belongs-to-collection":
					if refines == "" {
						p.collections = append(p.collections, opfCollection{id: attr(se, "id"), name: text})
					}
				}
			}
		case "item":
			if _, dup := p.manifest[attr(se, "id")]; !dup {
				p.manifestOrder = append(p.manifestOrder, attr(se, "id"))
			}
			p.manifest[attr(se, "id")] = opfItem{href: attr(se, "href"), mediaType: attr(se, "media-type"), properties: attr(se, "properties")}
		case "reference":
			p.guide = append(p.guide, opfReference{typ: attr(se, "type"), href: attr(se, "href")})
		case "itemref":
			p.spine = append(p.spine, attr(se, "idref"))
		}
	}
	if !sawPackage {
		return nil, fmt.Errorf("%w: OPF has no package element", errCorrupt)
	}
	return p, nil
}

// isbnScheme reports whether an opf:scheme names an ISBN: "ISBN", "ISBN-10"
// or "ISBN13" in any case, with hyphens, underscores or spaces ignored. Other
// schemes that merely start with isbn, such as ISBNdb, are not.
func isbnScheme(scheme string) bool {
	norm := strings.ToLower(strings.NewReplacer("-", "", "_", "", " ", "").Replace(scheme))
	return norm == "isbn" || norm == "isbn10" || norm == "isbn13"
}

func (p *opfPackage) metadata() *evidence.FileMetadata {
	var m evidence.FileMetadata
	if len(p.titles) > 0 {
		m.Title = p.titles[0]
	}
	for _, c := range p.creators {
		role := c.role
		if role == "" {
			role = p.roles[c.id]
		}
		if role == "" || strings.EqualFold(role, "aut") {
			m.Authors = append(m.Authors, c.name)
		}
	}
	m.Languages = p.languages
	for _, id := range p.identifiers {
		if id.scheme == "" || isbnScheme(id.scheme) || isbnPrefixed(id.value) {
			m.ISBNs = append(m.ISBNs, id.value)
		}
	}
	if len(p.publishers) > 0 {
		m.Publisher = p.publishers[0]
	}
	for _, d := range p.dates {
		if d.event == "publication" {
			m.Date = d.value
			break
		}
		if d.event == "" && m.Date == "" {
			m.Date = d.value
		}
	}
	m.Series, m.SeriesIndex = p.series()
	return newMeta(m)
}

// series returns the series name and raw index the OPF names, in the order of
// Calibre's own reader (read_series): the first EPUB 3 collection that has an
// id and a refining collection-type of exactly "series", with its
// group-position; else Calibre's calibre:series meta with its index. A name
// and an index are never taken from different places.
func (p *opfPackage) series() (name, index string) {
	for _, c := range p.collections {
		if c.id != "" && strings.TrimSpace(c.name) != "" && p.collectionTypes[c.id] == "series" {
			return c.name, p.positions[c.id]
		}
	}
	if strings.TrimSpace(p.calibreSeries) != "" {
		return p.calibreSeries, p.calibreIndex
	}
	return "", ""
}

// epubDRM reports whether the book is locked to a reader or account. Font
// obfuscation alone isn't DRM.
func epubDRM(a *zipArchive) (bool, error) {
	for _, name := range drmFiles {
		if a.file(name) != nil {
			return true, nil
		}
	}
	f := a.file("META-INF/encryption.xml")
	if f == nil {
		return false, nil
	}
	raw, err := a.readFile(f, maxXML)
	if err != nil {
		return false, err
	}
	d := newXMLDecoder(raw, false)
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("%w: %w: encryption.xml: %w", errCorrupt, errXML, err)
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "EncryptionMethod" &&
			!slices.Contains(fontObfuscation, attr(se, "Algorithm")) {
			return true, nil
		}
	}
}

// epubText checks that every spine document exists, and reads the first ones
// for text inside their body. Only a scan that read every HTML document to
// its end and found none reports TextAbsent; one that hit a bound or a chapter
// it couldn't parse is TextUnverified, since it may have missed the text.
func epubText(a *zipArchive, p *opfPackage, dir string) (string, error) {
	if len(p.spine) == 0 {
		return "", fmt.Errorf("%w: empty spine", errCorrupt)
	}
	type doc struct {
		name string
		html bool
	}
	docs := make([]doc, 0, len(p.spine))
	for _, idref := range p.spine {
		if err := a.ctx.Err(); err != nil {
			return "", err
		}
		item, ok := p.manifest[idref]
		if !ok {
			return "", fmt.Errorf("%w: spine item %q isn't in the manifest", errCorrupt, idref)
		}
		name := resolveHref(dir, item.href)
		if a.file(name) == nil {
			return "", fmt.Errorf("%w: spine document %q is missing", errCorrupt, name)
		}
		docs = append(docs, doc{name, isHTML(item.mediaType)})
	}
	budget := int64(maxSpineText)
	scanned := 0
	complete := true
	for _, d := range docs {
		if !d.html {
			continue
		}
		if scanned == maxSpineDocs || budget <= 0 {
			complete = false
			break
		}
		scanned++
		f := a.file(d.name)
		b, err := a.head(f, min(maxXML, budget))
		if err != nil {
			return "", err
		}
		budget -= int64(len(b))
		found, whole := bodyText(b)
		if found {
			return evidence.TextPresent, nil
		}
		complete = complete && whole && uint64(len(b)) >= f.UncompressedSize64
	}
	if !complete {
		return evidence.TextUnverified, nil
	}
	return evidence.TextAbsent, nil
}

func isHTML(mediaType string) bool {
	return strings.HasSuffix(mediaType, "html") || mediaType == "application/xhtml+xml"
}

// resolveHref resolves a manifest href against the OPF's directory.
func resolveHref(dir, href string) string {
	if i := strings.IndexByte(href, '#'); i >= 0 {
		href = href[:i]
	}
	if u, err := url.PathUnescape(href); err == nil {
		href = u
	}
	return strings.TrimPrefix(path.Join(dir, href), "/")
}

// bodyText reports whether an (X)HTML document has character data in its
// body, outside scripts and styles, and whether it was parsed to its end: a
// document that didn't parse may have text after the point where it broke.
func bodyText(b []byte) (found, whole bool) {
	d := newXMLDecoder(b, true)
	body, hidden := 0, 0
	for {
		tok, err := d.Token()
		if err != nil {
			return false, errors.Is(err, io.EOF)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch strings.ToLower(t.Name.Local) {
			case "body":
				body++
			case "script", "style":
				hidden++
			}
		case xml.EndElement:
			switch strings.ToLower(t.Name.Local) {
			case "body":
				body = max(body-1, 0)
			case "script", "style":
				hidden = max(hidden-1, 0)
			}
		case xml.CharData:
			if body > 0 && hidden == 0 && strings.IndexFunc(string(t), func(r rune) bool { return !unicode.IsSpace(r) }) >= 0 {
				return true, true
			}
		}
	}
}
