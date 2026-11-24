package inspect

import (
	"cmp"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/findings"
)

// Bounds on the front-matter scan (ADR-032).
const (
	defaultFrontDocs = 6        // front matter when no landmark says where the body starts
	maxFrontDocs     = 12       // front documents read
	backDocs         = 3        // last spine documents read
	docScanWindow    = 32 << 10 // bytes of text one document contributes to the ISBN scan
	// maxScanText is the bytes of text across the whole scan: every document
	// the scan can read, each cut at the window, fits. Only a bug can reach
	// it, so reaching it fails the scan.
	maxScanText = (maxFrontDocs + backDocs) * docScanWindow
	// excerptTextMax bounds a whole-document read for an excerpt's text in
	// excerptText (the fuzz and unit entry point), as maxScanText once did.
	excerptTextMax = 256 << 10
)

// scanBudget is the text the whole scan may read; tests replace it.
var scanBudget = maxScanText

// scanDoc is a spine document to read: its 1-based spine position, entry
// name and landmark.
type scanDoc struct {
	pos      int
	name     string
	landmark string
}

// scanFrontMatter reads an EPUB's front matter and its last spine documents
// for ISBNs, with the landmark of the document and the qualifier of the label
// of each (ADR-032). The hints are never authority, and no text is kept.
//
// Front matter is every spine document before the first body landmark (the
// EPUB 3 nav's landmarks, or the EPUB 2 guide), or the first 6 if there is
// none, at most 12. Each document is read only to its first 32 KiB of text
// (docScanWindow): that is not a failure, since front matter is short and a
// chapter's first 32 KiB is all the scan means to read. A scan that hit the
// total bound, a read limit or a document it couldn't parse is failed, not
// ok, since it may have missed an ISBN.
//
// The same pass builds the bounded text excerpt of the documents it reads
// (ADR-032 part 2), nil if none had text. The excerpt follows the scan's
// order and costs no second read of the archive.
//
// why is Result.FailReason: the first thing that kept the scan from ending ok,
// or "" if nothing did. read is Result.ScanDocs.
func scanFrontMatter(a *zipArchive, p *opfPackage, dir string) (h *evidence.ISBNHints, ex *evidence.Excerpt, why string, read []ScanDoc) {
	h = &evidence.ISBNHints{Method: evidence.HintEPUBText, State: evidence.HintOK, Pages: []int{}}
	if len(p.spine) == 0 {
		return h, nil, "", nil
	}
	fail := func(reason string) {
		// The first cause wins, except that a deadline explains where the
		// scan ended whatever came before it.
		if why == "" || reason == FailDeadline {
			why = reason
		}
	}
	if dl, ok := a.ctx.Deadline(); ok {
		// Stop short of the file's own deadline, so that a slow scan costs
		// its hints and not the whole format's result.
		if time.Until(dl) <= scanMargin {
			h.State = evidence.HintTimeout
			return h, nil, FailDeadline, nil
		}
		ctx, cancel := scanContext(a.ctx, dl)
		defer cancel()
		sub := *a
		sub.ctx = ctx
		a = &sub
	}
	landmarks, body, state, navWhy := epubLandmarks(a, p, dir)
	if state != "" {
		h.State = state
		fail(navWhy)
		if state == evidence.HintTimeout {
			return h, nil, why, read
		}
	}
	docs := make([]string, len(p.spine))
	html := make([]bool, len(p.spine))
	for i, idref := range p.spine {
		item := p.manifest[idref]
		docs[i], html[i] = resolveHref(dir, item.href), isHTML(item.mediaType)
	}
	frontEnd := defaultFrontDocs
	if i := slices.IndexFunc(docs, func(name string) bool { return body[name] }); i > 0 {
		// A body landmark on the very first document tells nothing about
		// where the front matter ends.
		frontEnd = i
	}
	frontEnd = min(frontEnd, maxFrontDocs, len(docs))
	var scan []scanDoc
	for i, name := range docs {
		switch {
		case i < frontEnd:
			scan = append(scan, scanDoc{i + 1, name, cmpLandmark(landmarks[name], evidence.LandmarkFront)})
		case i >= len(docs)-backDocs:
			scan = append(scan, scanDoc{i + 1, name, cmpLandmark(landmarks[name], evidence.LandmarkBack)})
		}
	}
	budget := scanBudget
	excerpt := evidence.NewExcerptBuilder(evidence.ExcerptEPUBText)
	// A document the excerpt no longer needs, because it is full, can't be
	// missing from it.
	missed := func() {
		if !excerpt.Full() {
			excerpt.MarkIncomplete()
		}
	}
	for _, d := range scan {
		if !html[d.pos-1] {
			continue
		}
		if budget <= 0 {
			h.State = evidence.HintFailed
			fail(FailTextBudget)
			missed()
			break
		}
		f := a.file(d.name)
		b, err := a.head(f, maxXML)
		if err != nil {
			// The archive may still hand out other entries; this one is lost.
			if h.State = failState(a); h.State == evidence.HintTimeout {
				fail(FailDeadline)
				break
			}
			fail(failReason(err))
			missed()
			continue
		}
		// Only the start of a document is read for ISBNs: front matter is
		// short, and a chapter or a whole book in one file is not. A copyright
		// or title landmark can label a whole file (a #fragment is dropped), and
		// its ISBN labels can be anywhere in it, so those documents are never
		// windowed: they are read whole or the scan fails.
		windowed := d.landmark != evidence.LandmarkCopyright && d.landmark != evidence.LandmarkTitle
		limit := budget
		if windowed {
			limit = min(docScanWindow, budget)
		}
		// After a window cut the rest of the read bytes are still parsed
		// without collecting text, so that a later error fails the scan.
		text, parsed, textCut, cutEOF := htmlTextDrain(a.ctx, b, limit, false, windowed)
		windowCut := windowed && textCut && limit == docScanWindow
		budget -= len(text)
		if textCut {
			// A cut inside a number must not leave a valid shorter ISBN.
			text = strings.TrimRight(text, "0123456789Xx -\u2010\u2013")
		}
		readCut := uint64(len(b)) < f.UncompressedSize64
		addFrontISBNs(h, text, d.landmark)
		doc := ScanDoc{Pos: d.pos, Landmark: d.landmark, Whole: parsed && !textCut && !readCut}
		switch {
		case parsed && !textCut && !readCut:
			h.Pages = append(h.Pages, d.pos)
		case textCut && !windowCut:
			// The total bound, not the window, cut the text.
			h.State = evidence.HintFailed
			fail(FailTextBudget)
		case windowCut && (parsed || readCut && cutEOF):
			// The window cut it, as meant: the document counts as read and
			// the scan stays as it was. A read limit that cut the rest lost
			// nothing the scan needs.
			doc.Reason = FailDocWindow
			h.Pages = append(h.Pages, d.pos)
		case readCut:
			// The read limit cut it before the window was filled, or the
			// bytes it did read don't parse.
			h.State = evidence.HintFailed
			fail(FailDocReadLimit)
		default:
			h.State = evidence.HintFailed
			fail(FailXMLError)
		}
		read = append(read, doc)
		if scanTextHook != nil {
			scanTextHook()
		}
		if a.ctx.Err() != nil {
			// The second read below would run past the deadline too.
			h.State = evidence.HintTimeout
			fail(FailDeadline)
			break
		}
		if !excerpt.Full() {
			// A second parse of the bytes already read, with white space as
			// a reader sees it; the archive is not read again. Text cut by a
			// bound still counts, marked truncated. A document that doesn't
			// parse adds none of its text.
			etext, eWhole, cut := htmlText(b, excerptRead, true)
			switch {
			case cut || readCut:
				excerpt.Add(d.pos, d.landmark, etext)
				excerpt.MarkTruncated()
			case !eWhole:
				excerpt.MarkIncomplete()
			default:
				excerpt.Add(d.pos, d.landmark, etext)
			}
		}
		if scanDocHook != nil {
			scanDocHook()
		}
	}
	if h.State == evidence.HintTimeout {
		// How far a deadline got must not decide which text is sealed.
		return h, nil, why, read
	}
	return h, excerpt.Excerpt(), why, read
}

// excerptRead bounds the text read from one document for the excerpt: four
// bytes a rune is the most the excerpt could keep of it.
const excerptRead = 4 * evidence.MaxExcerptRunes

// excerptText is the text of an (X)HTML document's body as an excerpt stores
// it (ADR-032 part 2).
func excerptText(b []byte) string {
	text, _, _ := htmlText(b, excerptTextMax, true)
	return evidence.NormalizeExcerptText(text)
}

// collapseSpace replaces each run of white space in character data with one
// space.
func collapseSpace(t []byte) []byte {
	out := make([]byte, 0, len(t))
	space := false
	for _, r := range string(t) {
		if unicode.IsSpace(r) {
			space = true
			continue
		}
		if space {
			out = append(out, ' ')
			space = false
		}
		out = utf8.AppendRune(out, r)
	}
	if space {
		out = append(out, ' ')
	}
	return out
}

// failState is HintTimeout once the scan's context has ended, else HintFailed.
func failState(a *zipArchive) string {
	if a.ctx.Err() != nil {
		return evidence.HintTimeout
	}
	return evidence.HintFailed
}

func cmpLandmark(found, fallback string) string {
	if found != "" {
		return found
	}
	return fallback
}

// addFrontISBNs records the ISBNs of one document's text. The first sighting
// of an ISBN keeps its place, but a sighting on a copyright page replaces the
// context of an earlier one elsewhere.
func addFrontISBNs(h *evidence.ISBNHints, text, landmark string) {
	for _, m := range findings.ScanISBNs(text) {
		c := evidence.ISBNContext{ISBN: m.ISBN, Landmark: landmark, Qualifier: m.Qualifier}
		if i := slices.Index(h.ISBNs, m.ISBN); i >= 0 {
			switch old := &h.Context[i]; {
			case landmark == evidence.LandmarkCopyright && old.Landmark != evidence.LandmarkCopyright:
				c.Qualifier = cmp.Or(c.Qualifier, old.Qualifier)
				*old = c
			case old.Qualifier == "":
				// A later sighting's label is better than none.
				old.Qualifier = c.Qualifier
			}
			continue
		}
		if len(h.ISBNs) < findings.MaxISBNs {
			h.ISBNs = append(h.ISBNs, m.ISBN)
			h.Context = append(h.Context, c)
		}
	}
}

// epubLandmarks labels spine documents by what the EPUB 3 nav's landmarks and
// the EPUB 2 guide say about them: copyright or title. body holds the
// documents where the body starts. state is "" or, when a nav couldn't be read
// to its end, what the scan's state must become: the labels may be missing, so
// the hints can't claim to be complete, and why is the FailReason.
func epubLandmarks(a *zipArchive, p *opfPackage, dir string) (labels map[string]string, body map[string]bool, state, why string) {
	labels, body = map[string]string{}, map[string]bool{}
	note := func(typ, name string) {
		for _, t := range strings.Fields(strings.ToLower(typ)) {
			switch t {
			case "copyright-page", "copyright":
				if labels[name] == "" {
					labels[name] = evidence.LandmarkCopyright
				}
			case "title-page", "titlepage", "title":
				if labels[name] == "" {
					labels[name] = evidence.LandmarkTitle
				}
			case "bodymatter", "text":
				body[name] = true
			}
		}
	}
	for _, navPath := range navPaths(p, dir) {
		raw, err := a.read(navPath, maxXML)
		if err != nil {
			if state = failState(a); state == evidence.HintTimeout {
				return labels, body, state, FailDeadline
			}
			if why == "" {
				why = FailNavUnreadable
				if r := failReason(err); r == FailXMLLimit || r == FailZipLimit {
					why = r
				}
			}
			continue
		}
		refs, ok := navLandmarks(raw)
		if !ok {
			state = evidence.HintFailed
			if why == "" {
				why = FailNavUnreadable
			}
		}
		for _, ref := range refs {
			note(ref.typ, resolveHref(path0(navPath), ref.href))
		}
	}
	for _, ref := range p.guide {
		note(ref.typ, resolveHref(dir, ref.href))
	}
	return labels, body, state, why
}

// scanMargin is how long before the file's deadline the scan stops.
var scanMargin = 5 * time.Second

// scanContext is the scan's own context: it ends scanMargin before the file's
// deadline dl. Tests replace it.
var scanContext = func(parent context.Context, dl time.Time) (context.Context, context.CancelFunc) {
	return context.WithDeadline(parent, dl.Add(-scanMargin))
}

// scanTextHook is called between a document's two reads; tests set it.
var scanTextHook func()

// scanDocHook is called after each document the scan reads; tests set it.
var scanDocHook func()

// navPaths returns the navigation documents the OPF lists, each once and in
// name order, so that conflicting navs always resolve the same way.
func navPaths(p *opfPackage, dir string) []string {
	var out []string
	for _, item := range p.manifest {
		if slices.Contains(strings.Fields(item.properties), "nav") {
			out = append(out, resolveHref(dir, item.href))
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// path0 is the directory of an archive entry name.
func path0(name string) string {
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		return name[:i]
	}
	return ""
}

// navLandmarks returns the entries of an EPUB 3 navigation document's
// landmarks nav, and whether the document parsed to its end.
func navLandmarks(raw []byte) ([]opfReference, bool) {
	d := newXMLDecoder(raw, true)
	var out []opfReference
	in := false
	for {
		tok, err := d.Token()
		if err != nil {
			return out, errors.Is(err, io.EOF)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch strings.ToLower(t.Name.Local) {
			case "nav":
				in = slices.Contains(strings.Fields(strings.ToLower(attr(t, "type"))), "landmarks")
			case "a":
				if in {
					out = append(out, opfReference{typ: attr(t, "type"), href: attr(t, "href")})
				}
			}
		case xml.EndElement:
			if strings.ToLower(t.Name.Local) == "nav" {
				in = false
			}
		}
	}
}

// blockElements end a line of text, so that a number at the end of one
// paragraph doesn't run into the start of the next.
var blockElements = map[string]bool{
	"p": true, "div": true, "br": true, "li": true, "ul": true, "ol": true, "dl": true, "dt": true, "dd": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true, "tr": true, "td": true, "th": true,
	"table": true, "section": true, "article": true, "aside": true, "blockquote": true, "pre": true,
	"header": true, "footer": true, "hr": true, "figcaption": true,
}

// htmlBodyText returns the character data in an (X)HTML document's body,
// outside scripts and styles, with a line break at each block element, up to
// limit bytes; whole reports whether the document was parsed to its end and
// nothing was cut.
func htmlBodyText(b []byte, limit int) (text string, whole bool) {
	text, whole, _ = htmlText(b, limit, false)
	return text, whole
}

// htmlText is htmlBodyText, or with reading set the text as an excerpt wants
// it: the source's own line breaks and runs of white space are one space, as
// in a browser, and a void br or hr breaks the line once, not twice. cut
// reports that text was left out at limit; a cut never splits a rune.
func htmlText(b []byte, limit int, reading bool) (text string, whole, cut bool) {
	text, whole, cut, _ = htmlTextDrain(context.Background(), b, limit, reading, false)
	return text, whole, cut
}

// htmlTextDrain is htmlText that, with drain set, goes on tokenizing after a
// cut without collecting text, so that parsed reports whether the document
// parses to the end of b (it is false after a cut without drain). cutEOF
// reports that the parse ended on an unexpected EOF, as a document cut short
// by a read limit does. The context is checked as it goes: if it ends, parsed
// is false.
func htmlTextDrain(ctx context.Context, b []byte, limit int, reading, drain bool) (text string, parsed, cut, cutEOF bool) {
	d := newXMLDecoder(b, true)
	var out strings.Builder
	// Every write goes through these, so the text never passes limit. A line
	// break that doesn't fit is dropped: it holds no text.
	newline := func() {
		if !cut && out.Len() < limit {
			out.WriteByte('\n')
		}
	}
	chars := func(t []byte) (over bool) {
		room := max(limit-out.Len(), 0)
		if len(t) > room {
			for room > 0 && !utf8.RuneStart(t[room]) {
				room--
			}
			out.Write(t[:room])
			return true
		}
		out.Write(t)
		return false
	}
	body, hidden := 0, 0
	for n := 0; ; n++ {
		if cut && n%1024 == 0 && ctx.Err() != nil {
			return out.String(), false, true, false
		}
		tok, err := d.Token()
		if err != nil {
			var se *xml.SyntaxError
			eof := errors.As(err, &se) && se.Msg == "unexpected EOF"
			return out.String(), errors.Is(err, io.EOF), cut, eof
		}
		switch t := tok.(type) {
		case xml.StartElement:
			name := strings.ToLower(t.Name.Local)
			switch name {
			case "body":
				body++
			case "script", "style":
				hidden++
			}
			if body > 0 && blockElements[name] {
				newline()
			}
		case xml.EndElement:
			name := strings.ToLower(t.Name.Local)
			switch name {
			case "body":
				body = max(body-1, 0)
			case "script", "style":
				hidden = max(hidden-1, 0)
			}
			// A void element's start already broke the line.
			if body > 0 && blockElements[name] && (!reading || name != "br" && name != "hr") {
				newline()
			}
		case xml.CharData:
			if body > 0 && hidden == 0 && !cut {
				if reading {
					t = collapseSpace(t)
				}
				if chars(t) {
					if !drain {
						return out.String(), false, true, false
					}
					cut = true
				}
			}
		}
	}
}
