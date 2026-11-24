package verify

import (
	"context"
	"fmt"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/inspect"
	"github.com/felixapel/calibre-bookwarden/internal/library"
)

// PriorSource finds a book's newest sealed package; RunStore implements it.
type PriorSource interface {
	PriorEvidence(ctx context.Context, libraryID string, ref library.BookRef) (Prior, bool, error)
}

// inspectKey names the inspector build and settings a package's file
// evidence was made under (ADR-039). Two keys are equal only when a fresh
// check of an unchanged file would seal the same file evidence: the
// behaviour version (inspect.Version), the limits that decide what is
// inspected and how far, the OCR time, and whether OCR hints can be read
// at all. The stored column holds this text, so it must stay stable and
// readable.
func inspectKey(lim Limits, hasHinter bool, tools string) string {
	return fmt.Sprintf("inspect=%d;file=%d;entries=%d;decompressed=%d;ratio=%d;pixels=%d;hints=%s;ocr=%t;build=%s;tools=%s",
		inspect.Version, lim.FileSize, lim.Inspect.MaxEntries, lim.Inspect.MaxDecompressed, lim.Inspect.MaxRatio, lim.Inspect.MaxPixels, lim.HintTime, hasHinter, buildStamp(), tools)
}

// ReuseMaxAge is how long file evidence may be reused after it was measured
// (ADR-039). Reuse does not renew it, so every book is measured again within
// this long of its last measurement, whatever the schedule. It bounds what the
// Calibre snapshot can't see: a file whose bytes changed in place with the
// same size and mtime (bit rot, a restore with cp -p). A weekly full run, with
// a day's slack, always lands inside it.
const ReuseMaxAge = 8 * 24 * time.Hour

// buildStamp is replaced by tests.
var buildStamp = defaultBuildStamp

// defaultBuildStamp names the Go toolchain and the modules whose decoders
// shape file evidence (images for covers, text for the encodings), as this
// binary was built.
func defaultBuildStamp() string {
	mods := map[string]string{"golang.org/x/image": "unknown", "golang.org/x/text": "unknown"}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, d := range bi.Deps {
			if _, want := mods[d.Path]; want {
				mods[d.Path] = d.Version
			}
		}
	}
	return runtime.Version() + ";golang.org/x/image=" + mods["golang.org/x/image"] + ";golang.org/x/text=" + mods["golang.org/x/text"]
}

// reusable reports whether prior's file evidence is what a fresh check of b
// would seal (ADR-039). Every condition below is one way it might not be:
//   - the package is not of the current schema (5 adds the file's series, which
//     an earlier package never read), or was made by another
//     inspector build or under other limits (key);
//   - the Calibre snapshot differs: last_modified, identifiers, or a
//     format's name, size or mtime;
//   - it is another book, or another library;
//   - the formats or the cover Calibre lists now are not the ones sealed
//     (a format that was added, removed, resized, or that went missing);
//   - a result that depends on timing and not on the file: a timeout, a
//     size mismatch, an inspector failure, hints that timed out, failed or
//     were unavailable, a format Calibre said it did not have;
//   - an excerpt the package refers to has no body here, or one that does not
//     hash back to what the package seals.
func reusable(p Prior, libraryID string, b library.Book, snapshot, key string, now time.Time) bool {
	pkg := p.Package
	switch {
	case key == "" || p.InspectKey != key:
		return false
	case p.MeasuredAt.IsZero() || p.MeasuredAt.After(now) || now.Sub(p.MeasuredAt) > ReuseMaxAge:
		return false
	case !slices.Equal(pkg.Calibre.Languages, evidence.RecordFrom(b).Languages):
		return false
	case pkg.SchemaVersion != evidence.SchemaVersion || pkg.Snapshot != snapshot:
		return false
	case pkg.LibraryID != libraryID || pkg.BookID != b.ID || pkg.UUID != b.UUID:
		return false
	case pkg.Cover == nil || pkg.Calibre.HasCover != b.HasCover || !coverSettled(pkg.Cover, b.HasCover):
		return false
	case len(pkg.Formats) != len(b.Formats):
		return false
	}
	listed := slices.SortedFunc(slices.Values(b.Formats), func(x, y library.Format) int { return strings.Compare(x.Name, y.Name) })
	for i, f := range listed {
		fe := pkg.Formats[i]
		if fe.Format != f.Name || fe.Size != f.Size || !formatSettled(fe, f.Missing) {
			return false
		}
	}
	refs, err := evidence.ExcerptRefs(pkg)
	if err != nil || len(p.Excerpts) != len(refs) {
		return false
	}
	for hash, chars := range refs {
		body, ok := p.Excerpts[hash]
		if !ok || utf8.RuneCountInString(body) != chars || evidence.ExcerptHash(body) != hash {
			return false
		}
	}
	return true
}

// formatSettled reports whether a sealed format result depends on the file
// and Calibre's listing alone. listedMissing is whether Calibre lists the
// format as missing on disk now.
func formatSettled(fe evidence.FormatEvidence, listedMissing bool) bool {
	switch fe.Reason {
	case evidence.ReasonTimeout, evidence.ReasonSizeMismatch, evidence.ReasonInspectorError:
		return false
	}
	if fe.State == evidence.StateMissing {
		// A format listed as missing is sealed as missing on disk without a
		// request. Any other missing result came from the export itself.
		return listedMissing && fe.Reason == evidence.ReasonMissingOnDisk
	}
	if listedMissing {
		return false
	}
	if h := fe.ISBNHints; h != nil {
		switch h.State {
		case evidence.HintTimeout, evidence.HintFailed, evidence.HintUnavailable:
			return false
		}
	}
	return true
}

// coverSettled is formatSettled for the cover.
func coverSettled(c *evidence.CoverEvidence, listed bool) bool {
	switch c.Reason {
	case evidence.ReasonTimeout, evidence.ReasonSizeMismatch, evidence.ReasonInspectorError:
		return false
	}
	// Calibre gives no sign when a cover file it lists comes back, so a cover
	// that was listed and not found is measured again.
	return !listed || c.State != evidence.StateMissing
}
