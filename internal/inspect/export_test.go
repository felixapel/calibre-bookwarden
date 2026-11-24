package inspect

import (
	"archive/zip"
	"context"
	"io"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/inspect/pdf"
)

var (
	Clean          = clean
	CleanList      = cleanList
	ISBNCandidates = isbnCandidates
	NewMeta        = newMeta
)

// Inspector is the signature of one format's inspector.
type Inspector = inspector

// SetInspector replaces the inspector for one kind for the rest of a test.
func SetInspector(t testing.TB, kind string, f Inspector) {
	old, had := inspectors[kind]
	inspectors[kind] = f
	t.Cleanup(func() {
		if had {
			inspectors[kind] = old
		} else {
			delete(inspectors, kind)
		}
	})
}

// VerifyZip opens an archive under the zip guard and reads every entry.
func VerifyZip(ctx context.Context, r io.ReaderAt, size int64, lim Limits) Result {
	a, err := openZip(ctx, r, size, lim)
	if err == nil {
		err = a.verifyAll()
	}
	if err != nil {
		return failure(err)
	}
	return Result{State: evidence.StateOK}
}

// CountZipReaders counts calls to zip.NewReader for the rest of a test.
func CountZipReaders(t testing.TB) *int {
	n := new(int)
	old := newZipReader
	newZipReader = func(r io.ReaderAt, size int64) (*zip.Reader, error) {
		*n++
		return old(r, size)
	}
	t.Cleanup(func() { newZipReader = old })
	return n
}

// ReadZipEntry opens an archive under the zip guard and reads one entry.
func ReadZipEntry(r io.ReaderAt, size int64, name string, limit int64) ([]byte, error) {
	a, err := openZip(context.Background(), r, size, Limits{})
	if err != nil {
		return nil, err
	}
	return a.read(name, limit)
}

var XMLText = xmlText

var PalmDOCDecompress = palmdocDecompress

var ParseXMP = parseXMP

// PDFText checks text with a small budget so resource-order regressions stay small.
func PDFText(ctx context.Context, d *pdf.Doc, pages pdf.Dict, budget int64) string {
	return pdfTextWithBudget(ctx, d, pages, budget)
}

// OPFMetadata is what the inspector records from an OPF's metadata.
func OPFMetadata(raw []byte) (*evidence.FileMetadata, error) {
	p, err := parseOPF(raw)
	if err != nil {
		return nil, err
	}
	return p.metadata(), nil
}
