package inspect

import (
	"archive/zip"
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync/atomic"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

// ratioSlack is how much an entry may decompress before the ratio limit
// applies: a small XHTML file legitimately compresses better than 100:1.
const ratioSlack = 1 << 20

// limitError is a section 10 limit being hit; the format is not_inspected.
type limitError struct{ reason string }

func (e *limitError) Error() string { return "inspect: limit hit: " + e.reason }

var errCorrupt = errors.New("inspect: corrupt")

// errTooLarge marks an entry past the size bound its reader was given, and
// errXML a document that didn't parse; both are corrupt, and tell
// Result.FailReason apart.
var (
	errTooLarge = errors.New("inspect: entry too large")
	errXML      = errors.New("inspect: XML error")
)

// newZipReader is zip.NewReader; tests count the calls.
var newZipReader = zip.NewReader

// zipArchive is a zip opened under the section 10 limits. Its entries are
// read one at a time.
type zipArchive struct {
	ctx     context.Context
	zr      *zip.Reader
	byName  map[string]*zip.File
	lim     Limits
	counter *countingReaderAt
	total   int64 // bytes decompressed from all entries so far
	limit   error // the first limit an entry hit
}

// countingReaderAt counts the bytes read through it: the compressed bytes of
// the entry being read.
type countingReaderAt struct {
	r io.ReaderAt
	n atomic.Int64
}

func (c *countingReaderAt) ReadAt(p []byte, off int64) (int, error) {
	n, err := c.r.ReadAt(p, off)
	c.n.Add(int64(n))
	return n, err
}

// openZip refuses archives with more than lim.MaxEntries entries before
// archive/zip allocates its directory, then opens the archive.
func openZip(ctx context.Context, r io.ReaderAt, size int64, lim Limits) (*zipArchive, error) {
	lim = lim.withDefaults()
	end, err := readEnd(r, size)
	if err != nil {
		return nil, err
	}
	if end.records > uint64(lim.MaxEntries) { //nolint:gosec // withDefaults makes it positive
		return nil, &limitError{evidence.ReasonTooManyEntries}
	}
	// archive/zip walks the directory until a header doesn't parse, only
	// comparing the low 16 bits of the count, and may start at either offset.
	base := end.offset - end.dirSize - end.dirOffset
	for _, start := range []int64{end.dirOffset, base + end.dirOffset} {
		if countHeaders(r, start, size, lim.MaxEntries) > lim.MaxEntries {
			return nil, &limitError{evidence.ReasonTooManyEntries}
		}
	}
	counter := &countingReaderAt{r: r}
	zr, err := newZipReader(counter, size)
	if err != nil && !errors.Is(err, zip.ErrInsecurePath) { // names are never used as paths
		return nil, fmt.Errorf("%w: %w", errCorrupt, err)
	}
	if len(zr.File) > lim.MaxEntries {
		return nil, &limitError{evidence.ReasonTooManyEntries}
	}
	// The first entry of a name wins, as in a linear scan.
	byName := make(map[string]*zip.File, len(zr.File))
	for _, f := range zr.File {
		if _, ok := byName[f.Name]; !ok {
			byName[f.Name] = f
		}
	}
	return &zipArchive{ctx: ctx, zr: zr, lim: lim, counter: counter, byName: byName}, nil
}

type zipEnd struct {
	offset    int64 // of the end record archive/zip uses (zip64 if present)
	records   uint64
	dirSize   int64
	dirOffset int64
}

// readEnd finds the end of central directory record, and the zip64 one when
// the archive has it.
func readEnd(r io.ReaderAt, size int64) (zipEnd, error) {
	tail := make([]byte, min(size, 22+65535))
	if _, err := r.ReadAt(tail, size-int64(len(tail))); err != nil && !errors.Is(err, io.EOF) {
		return zipEnd{}, fmt.Errorf("%w: %w", errCorrupt, err)
	}
	i := len(tail) - 22
	for ; i >= 0; i-- {
		if string(tail[i:i+4]) == "PK\x05\x06" && i+22+int(binary.LittleEndian.Uint16(tail[i+20:])) <= len(tail) {
			break
		}
	}
	if i < 0 {
		return zipEnd{}, fmt.Errorf("%w: no end of central directory", errCorrupt)
	}
	rec := tail[i:]
	end := zipEnd{
		offset:    size - int64(len(tail)) + int64(i),
		records:   uint64(binary.LittleEndian.Uint16(rec[10:])),
		dirSize:   int64(binary.LittleEndian.Uint32(rec[12:])),
		dirOffset: int64(binary.LittleEndian.Uint32(rec[16:])),
	}
	if i < 20 || string(tail[i-20:i-16]) != "PK\x06\x07" {
		return end, nil
	}
	off := binary.LittleEndian.Uint64(tail[i-12:])
	z := make([]byte, 56)
	if size < 56 || off > uint64(size-56) { //nolint:gosec // size-56 is not negative here
		return zipEnd{}, fmt.Errorf("%w: zip64 end record out of range", errCorrupt)
	}
	if _, err := r.ReadAt(z, int64(off)); err != nil || string(z[:4]) != "PK\x06\x06" { //nolint:gosec // off < size
		return zipEnd{}, fmt.Errorf("%w: no zip64 end record", errCorrupt)
	}
	dirSize, dirOffset := binary.LittleEndian.Uint64(z[40:]), binary.LittleEndian.Uint64(z[48:])
	if dirSize > uint64(size) || dirOffset > uint64(size) { //nolint:gosec // size is not negative
		return zipEnd{}, fmt.Errorf("%w: zip64 directory out of range", errCorrupt)
	}
	return zipEnd{
		offset:    int64(off), //nolint:gosec // off < size
		records:   binary.LittleEndian.Uint64(z[32:]),
		dirSize:   int64(dirSize),   //nolint:gosec // dirSize <= size
		dirOffset: int64(dirOffset), //nolint:gosec // dirOffset <= size
	}, nil
}

// countHeaders counts the central directory headers from off, stopping past limit.
func countHeaders(r io.ReaderAt, off, size int64, limit int) int {
	if off < 0 || off >= size {
		return 0
	}
	br := bufio.NewReader(io.NewSectionReader(r, off, size-off))
	var h [46]byte
	n := 0
	for n <= limit {
		if _, err := io.ReadFull(br, h[:]); err != nil || string(h[:4]) != "PK\x01\x02" {
			break
		}
		skip := int(binary.LittleEndian.Uint16(h[28:])) + int(binary.LittleEndian.Uint16(h[30:])) + int(binary.LittleEndian.Uint16(h[32:]))
		if _, err := br.Discard(skip); err != nil {
			break
		}
		n++
	}
	return n
}

// verifyAll reads every entry to its end, so archive/zip checks every CRC.
func (a *zipArchive) verifyAll() error {
	for _, f := range a.zr.File {
		rc, err := a.open(f)
		if err != nil {
			return err
		}
		_, err = io.Copy(io.Discard, rc)
		_ = rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// file returns the entry called name, or nil.
func (a *zipArchive) file(name string) *zip.File {
	return a.byName[name]
}

var errNoEntry = errors.New("inspect: no such entry")

// read returns the whole entry called name; an entry past limit bytes is corrupt.
func (a *zipArchive) read(name string, limit int64) ([]byte, error) {
	f := a.file(name)
	if f == nil {
		return nil, fmt.Errorf("%w: %w: %q", errCorrupt, errNoEntry, name)
	}
	return a.readFile(f, limit)
}

// head returns at most the first limit bytes of an entry.
func (a *zipArchive) head(f *zip.File, limit int64) ([]byte, error) {
	rc, err := a.open(f)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	return io.ReadAll(io.LimitReader(rc, limit))
}

func (a *zipArchive) readFile(f *zip.File, limit int64) ([]byte, error) {
	rc, err := a.open(f)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%w: %w: entry %q is larger than %d bytes", errCorrupt, errTooLarge, f.Name, limit)
	}
	return b, nil
}

// open opens one entry under the limits.
func (a *zipArchive) open(f *zip.File) (io.ReadCloser, error) {
	if err := a.ctx.Err(); err != nil {
		return nil, err
	}
	start := a.counter.n.Load()
	rc, err := f.Open()
	if err != nil {
		return nil, entryError(err)
	}
	return &entryReader{a: a, rc: rc, start: start}, nil
}

type entryReader struct {
	a     *zipArchive
	rc    io.ReadCloser
	start int64 // compressed bytes read before this entry
	n     int64 // bytes decompressed from this entry
}

func (e *entryReader) Read(p []byte) (int, error) {
	if err := e.a.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := e.rc.Read(p)
	e.n += int64(n)
	e.a.total += int64(n)
	if e.a.total > e.a.lim.MaxDecompressed {
		return n, e.a.hit(evidence.ReasonDecompressedTooLarge)
	}
	if e.n > ratioSlack && e.n > e.a.lim.MaxRatio*max(e.a.counter.n.Load()-e.start, 1) {
		return n, e.a.hit(evidence.ReasonCompressionRatio)
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return n, entryError(err)
	}
	return n, err
}

func (e *entryReader) Close() error { return e.rc.Close() }

func (a *zipArchive) hit(reason string) error {
	if a.limit == nil {
		a.limit = &limitError{reason}
	}
	return a.limit
}

// cause is the reason an entry's consumer failed: a limit or the deadline,
// which decoders may not pass on, or else err itself.
func (a *zipArchive) cause(err error) error {
	switch {
	case a.limit != nil:
		return a.limit
	case a.ctx.Err() != nil:
		return a.ctx.Err()
	}
	return err
}

func entryError(err error) error {
	if errors.Is(err, zip.ErrAlgorithm) {
		return err
	}
	return fmt.Errorf("%w: %w", errCorrupt, err)
}

// failure turns an error from reading a file into its Result: a limit, a
// timeout, an unsupported zip method, or a corrupt file.
func failure(err error) Result {
	var limit *limitError
	switch {
	case errors.As(err, &limit):
		return Result{State: evidence.StateNotInspected, Reason: limit.reason, FailReason: FailZipLimit}
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return Result{State: evidence.StateNotInspected, Reason: evidence.ReasonTimeout, FailReason: FailDeadline}
	case errors.Is(err, zip.ErrAlgorithm):
		return Result{State: evidence.StateUnsupported, Reason: evidence.ReasonZipMethod, FailReason: FailOther}
	}
	return Result{State: evidence.StateCorrupt, FailReason: failReason(err)}
}

// failReason classifies an error from reading a file for Result.FailReason.
func failReason(err error) string {
	var limit *limitError
	switch {
	case errors.As(err, &limit):
		return FailZipLimit
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return FailDeadline
	case errors.Is(err, errTooLarge):
		return FailXMLLimit
	case errors.Is(err, errXML):
		return FailXMLError
	}
	return FailOther
}
