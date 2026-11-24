package inspect

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

// cbzPages is how many pages, in name order, are fully decoded.
const cbzPages = 3

var imageExts = []string{".jpg", ".jpeg", ".png", ".gif", ".webp"}

func inspectCBZ(ctx context.Context, r io.ReaderAt, size int64, lim Limits) Result {
	a, err := openZip(ctx, r, size, lim)
	if err == nil {
		err = a.verifyAll()
	}
	if err != nil {
		return failure(err)
	}
	var pages []*zip.File
	for _, f := range a.zr.File {
		if strings.HasPrefix(f.Name, "__MACOSX/") || strings.HasPrefix(path.Base(f.Name), ".") || f.FileInfo().IsDir() {
			continue
		}
		if slices.Contains(imageExts, strings.ToLower(path.Ext(f.Name))) {
			pages = append(pages, f)
		}
	}
	if len(pages) == 0 {
		return failure(fmt.Errorf("%w: no images", errCorrupt))
	}
	slices.SortFunc(pages, func(x, y *zip.File) int { return strings.Compare(x.Name, y.Name) })
	res := Result{State: evidence.StateOK}
	for _, f := range pages[:min(cbzPages, len(pages))] {
		headerOnly, err := decodeImage(func() (io.ReadCloser, error) { return a.open(f) }, a.lim)
		if err != nil {
			return failure(a.cause(err))
		}
		if headerOnly {
			res.Reason = evidence.ReasonHeaderOnly
		}
	}
	return res
}
