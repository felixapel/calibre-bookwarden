package inspect

import (
	"errors"
	"path"
	"strings"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

// CoverCap is the most bytes of one cover image that is read: ADR-023's 20 MiB
// for the Calibre cover, which also bounds an EPUB's own cover and the Open
// Library cover (ADR-043).
const CoverCap = 20 << 20

// fileCoverCap is CoverCap; a test lowers it.
var fileCoverCap int64 = CoverCap

// imageExtensions are the raster types a guide reference may point at when
// the manifest doesn't list the file.
var imageExtensions = []string{".jpg", ".jpeg", ".png", ".gif", ".webp"}

// coverTarget is a cover the OPF names: the manifest item, or only the path
// of a guide reference that the manifest doesn't list.
type coverTarget struct {
	item    opfItem
	listed  bool
	missing bool // the OPF names a manifest id that isn't there
}

// isRaster reports whether a manifest media type is an image a cover can be.
func isRaster(mediaType string) bool {
	m := strings.ToLower(strings.TrimSpace(mediaType))
	return strings.HasPrefix(m, "image/") && m != "image/svg+xml"
}

// coverTargets lists, in priority order, the covers the OPF names: EPUB 3
// manifest items with the cover-image property, then EPUB 2's
// <meta name="cover"> id, then a guide reference of type cover.
func (p *opfPackage) coverTargets(dir string) []coverTarget {
	var out []coverTarget
	for _, id := range p.manifestOrder {
		it := p.manifest[id]
		for _, prop := range strings.Fields(it.properties) {
			if prop == "cover-image" {
				out = append(out, coverTarget{item: it, listed: true})
				break
			}
		}
	}
	if id := strings.TrimSpace(p.coverMeta); id != "" {
		if it, ok := p.manifest[id]; ok {
			out = append(out, coverTarget{item: it, listed: true})
		} else {
			out = append(out, coverTarget{missing: true})
		}
	}
	for _, r := range p.guide {
		if !strings.EqualFold(r.typ, "cover") || strings.TrimSpace(r.href) == "" {
			continue
		}
		name := resolveHref(dir, r.href)
		found := false
		for _, id := range p.manifestOrder {
			if it := p.manifest[id]; resolveHref(dir, it.href) == name {
				out = append(out, coverTarget{item: it, listed: true})
				found = true
				break
			}
		}
		if !found && isImageName(name) {
			out = append(out, coverTarget{item: opfItem{href: r.href, mediaType: "image/" + strings.TrimPrefix(path.Ext(strings.ToLower(name)), ".")}})
		}
	}
	return out
}

func isImageName(name string) bool {
	ext := strings.ToLower(path.Ext(name))
	for _, e := range imageExtensions {
		if ext == e {
			return true
		}
	}
	return false
}

// fileCover measures the cover image the OPF names, if it names one that is
// an image. A page (XHTML) or another non-image item is skipped, and the next
// method is tried; nil means the OPF names no image cover at all. Only
// measures are returned, never the bytes.
func fileCover(a *zipArchive, p *opfPackage, dir string) *evidence.CoverCandidate {
	missing := false
	for _, t := range p.coverTargets(dir) {
		if t.missing {
			missing = true
			continue
		}
		if !isRaster(t.item.mediaType) {
			continue
		}
		c := &evidence.CoverCandidate{Source: evidence.CandidateEPUB, State: evidence.StateMissing, Flatness: -1}
		f := a.file(resolveHref(dir, t.item.href))
		if f == nil {
			// Named but not in the archive: remember it and try the next
			// method, as for an id the manifest lacks.
			missing = true
			continue
		}
		if f.UncompressedSize64 > uint64(fileCoverCap) { //nolint:gosec // the cap is positive
			c.State = evidence.StateTooLarge
			return c
		}
		data, err := a.readFile(f, fileCoverCap)
		switch {
		case errors.Is(err, errTooLarge):
			c.State = evidence.StateTooLarge
		case err != nil:
			c.State = evidence.StateNotInspected
		default:
			m := MeasureCover(data, a.lim)
			m.Source = evidence.CandidateEPUB
			return &m
		}
		return c
	}
	if missing {
		return &evidence.CoverCandidate{Source: evidence.CandidateEPUB, State: evidence.StateMissing, Flatness: -1}
	}
	return nil
}
