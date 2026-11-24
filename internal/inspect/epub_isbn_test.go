package inspect_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/inspect"
	"github.com/felixapel/calibre-bookwarden/internal/inspect/inspecttest"
)

func TestEPUBISBNSchemes(t *testing.T) {
	for _, scheme := range []string{"ISBN", "isbn", "ISBN-13", "ISBN13", "ISBN-10", "isbn-10", "ISBN_13", "ISBN 13", "Isbn-10"} {
		t.Run(scheme, func(t *testing.T) {
			isbn := "9780000000026"
			if strings.HasSuffix(scheme, "10") {
				isbn = "0000000000"
			}
			o := with(calibreEPUB(), func(o *inspecttest.EPUBOptions) { o.ISBN, o.ISBNScheme = isbn, scheme })
			data := inspecttest.EPUB(o)
			res := inspect.Inspect(context.Background(), "EPUB", bytes.NewReader(data), int64(len(data)), inspect.Limits{})
			if res.Metadata == nil || strings.Join(res.Metadata.ISBNs, "|") != isbn {
				t.Errorf("ISBNs %+v, want %s", res.Metadata, isbn)
			}
		})
	}
	for _, scheme := range []string{"DOI", "ISBNdb", "isbn-registry-id", "isbn-a", "ISBN 1313"} {
		t.Run("scheme "+scheme+" stays out", func(t *testing.T) {
			o := with(calibreEPUB(), func(o *inspecttest.EPUBOptions) { o.ISBNScheme = scheme })
			data := inspecttest.EPUB(o)
			res := inspect.Inspect(context.Background(), "EPUB", bytes.NewReader(data), int64(len(data)), inspect.Limits{})
			if res.Metadata != nil && len(res.Metadata.ISBNs) != 0 {
				t.Errorf("ISBNs %+v, want none", res.Metadata.ISBNs)
			}
		})
	}
}
