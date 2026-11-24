package inspect

import (
	"context"
	"errors"
	"io"
	"maps"
	"math"
	"slices"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/inspect/pdf"
)

// Bounds on the text layer check.
const (
	maxPDFPages     = 50
	maxPageNodes    = 100_000
	maxPageDepth    = 64
	maxContent      = 16 << 20 // decoded bytes of one content stream
	maxContentTotal = 64 << 20 // decoded bytes of all of them
)

func inspectPDF(ctx context.Context, r io.ReaderAt, size int64, _ Limits) (res Result) {
	d, err := pdf.Open(ctx, r, size)
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return Result{State: evidence.StateNotInspected, Reason: evidence.ReasonTimeout}
	case errors.Is(err, pdf.ErrTooLarge):
		return Result{State: evidence.StateNotInspected, Reason: evidence.ReasonDecompressedTooLarge}
	case err != nil:
		return Result{State: evidence.StateCorrupt}
	}
	defer func() {
		switch err := d.Err(); {
		case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
			res = Result{State: evidence.StateNotInspected, Reason: evidence.ReasonTimeout}
		case errors.Is(err, pdf.ErrTooLarge):
			res = Result{State: evidence.StateNotInspected, Reason: evidence.ReasonDecompressedTooLarge}
		}
	}()
	tr := d.Trailer()
	if _, ok := tr["Encrypt"]; ok {
		enc, _ := d.Resolve(tr["Encrypt"])
		if e, _ := enc.(pdf.Dict); e["Filter"] == pdf.Name("Standard") {
			return Result{State: evidence.StateOK, Reason: evidence.ReasonEncrypted, Text: evidence.TextUnverified}
		}
		return Result{State: evidence.StateDRM, Text: evidence.TextUnverified}
	}
	root, _ := d.Resolve(tr["Root"])
	catalog, ok := root.(pdf.Dict)
	if !ok {
		return Result{State: evidence.StateCorrupt}
	}
	pagesObj, _ := d.Resolve(catalog["Pages"])
	pages, ok := pagesObj.(pdf.Dict)
	if !ok {
		return Result{State: evidence.StateCorrupt}
	}
	res = Result{State: evidence.StateOK, Text: pdfText(ctx, d, pages), Metadata: pdfMetadata(d, catalog, tr), Pages: pageCount(d, pages)}
	if d.Repaired() {
		res.Reason = evidence.ReasonXrefRepaired
	}
	return res
}

// pdfMetadata reads the Info dictionary, then lets XMP override it field by
// field, then takes the catalog's /Lang when nothing named a language.
func pdfMetadata(d *pdf.Doc, catalog, trailer pdf.Dict) *evidence.FileMetadata {
	text := func(o pdf.Object) string {
		v, _ := d.Resolve(o)
		s, _ := v.(pdf.String)
		return pdf.TextString(s)
	}
	var m evidence.FileMetadata
	if v, _ := d.Resolve(trailer["Info"]); v != nil {
		if info, ok := v.(pdf.Dict); ok {
			m.Title = text(info["Title"])
			if a := text(info["Author"]); a != "" {
				m.Authors = []string{a}
			}
			if isbn := text(info["ISBN"]); isbn != "" {
				m.ISBNs = []string{isbn}
			}
		}
	}
	if v, _ := d.Resolve(catalog["Metadata"]); v != nil {
		if s, ok := v.(pdf.Stream); ok {
			if b, err := d.Decode(s, maxXML); err == nil {
				x := parseXMP(b)
				if x.Title != "" {
					m.Title = x.Title
				}
				if x.Authors != nil {
					m.Authors = x.Authors
				}
				if x.ISBNs != nil {
					m.ISBNs = x.ISBNs
				}
				m.Languages, m.Publisher, m.Date = x.Languages, x.Publisher, x.Date
			}
		}
	}
	if m.Languages == nil {
		if lang := text(catalog["Lang"]); lang != "" {
			m.Languages = []string{lang}
		}
	}
	return newMeta(m)
}

// pdfText walks the page tree in order through the first maxPDFPages pages:
// present when a page's content, or a form XObject in its resources, shows
// text; unverified when a stream that could hold text couldn't be decoded.
func pdfText(ctx context.Context, d *pdf.Doc, root pdf.Dict) string {
	return pdfTextWithBudget(ctx, d, root, maxContentTotal)
}

func pdfTextWithBudget(ctx context.Context, d *pdf.Doc, root pdf.Dict, budget int64) string {
	type node struct {
		dict      pdf.Dict
		resources pdf.Object
		depth     int
	}
	stack := []node{{dict: root, resources: root["Resources"]}}
	seen := map[int]bool{}
	pages, nodes := 0, 0
	unverified := false
	decodes := func(o pdf.Object) (bool, bool) { // has text, decoded
		v, _ := d.Resolve(o)
		s, ok := v.(pdf.Stream)
		if !ok || budget <= 0 {
			return false, false
		}
		b, err := d.Decode(s, min(maxContent, budget))
		if err != nil {
			return false, false
		}
		budget -= int64(len(b))
		return pdf.ContentHasText(b), true
	}
	for len(stack) > 0 && pages < maxPDFPages && nodes < maxPageNodes && ctx.Err() == nil {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		nodes++
		resources := n.resources
		if r, ok := n.dict["Resources"]; ok {
			resources = r
		}
		if kids, ok := resolveArray(d, n.dict["Kids"]); ok {
			if n.depth == maxPageDepth {
				continue
			}
			for i := len(kids) - 1; i >= 0; i-- { // first kid on top
				if ref, ok := kids[i].(pdf.Ref); ok {
					if seen[ref.Num] {
						continue
					}
					seen[ref.Num] = true
				}
				if v, _ := d.Resolve(kids[i]); v != nil {
					if kid, ok := v.(pdf.Dict); ok {
						stack = append(stack, node{dict: kid, resources: resources, depth: n.depth + 1})
					}
				}
			}
			continue
		}
		pages++
		contents, _ := d.Resolve(n.dict["Contents"])
		streams, ok := contents.(pdf.Array)
		if !ok {
			streams = pdf.Array{contents}
		}
		for _, c := range streams {
			has, decoded := decodes(c)
			if has {
				return evidence.TextPresent
			}
			unverified = unverified || !decoded
		}
		res, _ := d.Resolve(resources)
		rd, _ := res.(pdf.Dict)
		xo, _ := d.Resolve(rd["XObject"])
		xobjects, _ := xo.(pdf.Dict)
		for _, name := range slices.Sorted(maps.Keys(xobjects)) {
			x := xobjects[name]
			v, _ := d.Resolve(x)
			if s, ok := v.(pdf.Stream); ok && s.Dict["Subtype"] == pdf.Name("Form") {
				has, decoded := decodes(s)
				if has {
					return evidence.TextPresent
				}
				unverified = unverified || !decoded
			}
		}
	}
	if unverified {
		return evidence.TextUnverified
	}
	return evidence.TextAbsent
}

func resolveArray(d *pdf.Doc, o pdf.Object) (pdf.Array, bool) {
	if o == nil {
		return nil, false
	}
	v, _ := d.Resolve(o)
	a, ok := v.(pdf.Array)
	return a, ok
}

// pageCount is the page tree's /Count, or 0 when it is missing, negative or
// beyond any real document.
func pageCount(d *pdf.Doc, pages pdf.Dict) int {
	v, _ := d.Resolve(pages["Count"])
	if n, ok := v.(int64); ok && n >= 0 && n <= math.MaxInt32 {
		return int(n)
	}
	return 0
}
