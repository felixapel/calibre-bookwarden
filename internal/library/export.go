package library

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// ExportStatus says what Calibre sent for a format.
type ExportStatus int

const (
	// ExportOK: the bytes were written and their size matches the listing.
	ExportOK ExportStatus = iota
	// ExportNotInCalibre: Calibre has no such book or format.
	ExportNotInCalibre
	// ExportMissingOnDisk: Calibre lists the format with a size, but sent
	// zero bytes because the file is gone from the library folder.
	ExportMissingOnDisk
	// ExportSizeMismatch: Calibre announced a size other than the listed
	// one. Nothing was written.
	ExportSizeMismatch
	// ExportTooLarge: Calibre announced more bytes than the cap allows.
	// Nothing was written.
	ExportTooLarge
)

// CoverCap is the most bytes a cover export reads (ADR-023).
const CoverCap = 20 << 20

// ExportResult is the outcome of one export. SHA256 is lower-case hex and set
// only for ExportOK.
type ExportResult struct {
	Status ExportStatus
	Bytes  int64
	SHA256 string
}

var (
	// errMalformed marks an answer that isn't Calibre's export envelope.
	errMalformed = errors.New("malformed export answer")
	// errWrite marks a failure of the caller's writer, such as a full disk.
	errWrite = errors.New("writing the export failed")
)

// ExportFormat streams one format's stored bytes into w. want is the size
// from the listing; an answer of another size is not written. The caller's
// context bounds the whole transfer. A stream cut short counts as Calibre
// being unreachable, so a restart mid-transfer pauses the run instead of
// failing the book.
func (c *Client) ExportFormat(ctx context.Context, bookID int, format string, w io.Writer, want int64) (ExportResult, error) {
	lib, err := c.library(ctx)
	if err != nil {
		return ExportResult{}, err
	}
	return c.ExportFormatInLibrary(ctx, lib, bookID, format, w, want)
}

// ExportFormatInLibrary cannot be redirected by a concurrent default-library probe.
func (c *Client) ExportFormatInLibrary(ctx context.Context, lib string, bookID int, format string, w io.Writer, want int64) (ExportResult, error) {
	if lib == "" {
		return ExportResult{}, ErrNoLibrary
	}
	body, err := json.Marshal([]any{"fmt", bookID, format, nil})
	if err != nil {
		return ExportResult{}, fmt.Errorf("library: encode export arguments: %w", err)
	}
	resp, err := c.do(ctx, "export", http.MethodPost, cdbURL(c.cfg.BaseURL, lib, "export"), body, "application/x-msgpack")
	if err != nil {
		return ExportResult{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	res, err := readExport(resp.Body, w, want)
	var ce *CalibreError
	switch {
	case err == nil:
		return res, nil
	case ctx.Err() != nil:
		return ExportResult{}, fmt.Errorf("library: export: %w", ctx.Err())
	case errors.As(err, &ce), errors.Is(err, errMalformed), errors.Is(err, errWrite):
		return ExportResult{}, fmt.Errorf("library: book %d %s: %w", bookID, format, err)
	default:
		return ExportResult{}, fmt.Errorf("library: export: %w: %w", ErrUnreachable, err)
	}
}

// ExportCoverInLibrary streams a book's stored cover into w. Covers have no
// listed size, so an answer of more than limit bytes is ExportTooLarge and
// isn't written; no cover is ExportNotInCalibre.
func (c *Client) ExportCoverInLibrary(ctx context.Context, lib string, bookID int, w io.Writer, limit int64) (ExportResult, error) {
	if lib == "" {
		return ExportResult{}, ErrNoLibrary
	}
	body, err := json.Marshal([]any{"cover", bookID})
	if err != nil {
		return ExportResult{}, fmt.Errorf("library: encode cover export arguments: %w", err)
	}
	resp, err := c.do(ctx, "export", http.MethodPost, cdbURL(c.cfg.BaseURL, lib, "export"), body, "application/x-msgpack")
	if err != nil {
		return ExportResult{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	res, err := readExportCapped(resp.Body, w, limit)
	var ce *CalibreError
	switch {
	case err == nil:
		return res, nil
	case ctx.Err() != nil:
		return ExportResult{}, fmt.Errorf("library: cover export: %w", ctx.Err())
	case errors.As(err, &ce), errors.Is(err, errMalformed), errors.Is(err, errWrite):
		return ExportResult{}, fmt.Errorf("library: book %d cover: %w", bookID, err)
	default:
		return ExportResult{}, fmt.Errorf("library: cover export: %w: %w", ErrUnreachable, err)
	}
}

// readExport decodes Calibre's export answer, a one-entry msgpack map:
// {"result": bin | nil | str} or {"err": str, "tb": str}. A bin payload is
// streamed into w and hashed; nothing else is ever written.
func readExport(r io.Reader, w io.Writer, want int64) (ExportResult, error) {
	return readEnvelope(r, w, func(size int64) ExportStatus {
		switch {
		case size == 0 && want > 0:
			return ExportMissingOnDisk
		case size != want:
			return ExportSizeMismatch
		}
		return ExportOK
	})
}

// readExportCapped is readExport for an answer of any size up to limit.
func readExportCapped(r io.Reader, w io.Writer, limit int64) (ExportResult, error) {
	return readEnvelope(r, w, func(size int64) ExportStatus {
		if size > limit {
			return ExportTooLarge
		}
		return ExportOK
	})
}

// readEnvelope decodes the envelope; accept decides from the announced size
// whether the payload is written.
func readEnvelope(r io.Reader, w io.Writer, accept func(size int64) ExportStatus) (ExportResult, error) {
	br := bufio.NewReader(r)
	n, err := mapLen(br)
	if err != nil {
		return ExportResult{}, err
	}
	for range n {
		key, err := readStr(br, 64)
		if err != nil {
			return ExportResult{}, err
		}
		switch key {
		case "result":
			return readResult(br, w, accept)
		case "err":
			msg, err := readStr(br, 1<<10)
			if err != nil {
				return ExportResult{}, err
			}
			return ExportResult{}, &CalibreError{Command: "export", Message: firstLine(msg)}
		default:
			if err := skipValue(br); err != nil {
				return ExportResult{}, err
			}
		}
	}
	return ExportResult{}, fmt.Errorf("library: export: %w: no result", errMalformed)
}

func readResult(br *bufio.Reader, w io.Writer, accept func(size int64) ExportStatus) (ExportResult, error) {
	tag, err := readByte(br)
	if err != nil {
		return ExportResult{}, err
	}
	if tag == 0xc0 {
		return ExportResult{Status: ExportNotInCalibre}, nil
	}
	if isStr(tag) {
		if err := br.UnreadByte(); err != nil {
			return ExportResult{}, err
		}
		msg, err := readStr(br, 1<<10)
		if err != nil {
			return ExportResult{}, err
		}
		return ExportResult{}, &CalibreError{Command: "export", Message: firstLine(msg)}
	}
	var size int64
	switch tag {
	case 0xc4, 0xc5, 0xc6:
		size, err = readUint(br, map[byte]int{0xc4: 1, 0xc5: 2, 0xc6: 4}[tag])
		if err != nil {
			return ExportResult{}, err
		}
	default:
		return ExportResult{}, fmt.Errorf("library: export: %w: result type 0x%02x", errMalformed, tag)
	}
	if st := accept(size); st != ExportOK {
		return ExportResult{Status: st}, nil
	}
	h := sha256.New()
	tw := &trackedWriter{w: w}
	if _, err := io.CopyN(io.MultiWriter(tw, h), br, size); err != nil {
		if tw.err != nil {
			return ExportResult{}, fmt.Errorf("library: export: %w: %w", errWrite, tw.err)
		}
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return ExportResult{}, fmt.Errorf("library: export: read payload: %w", err)
	}
	return ExportResult{Status: ExportOK, Bytes: size, SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}

// trackedWriter remembers the writer's own error, so a full scratch disk is
// told apart from a failing connection.
type trackedWriter struct {
	w   io.Writer
	err error
}

func (t *trackedWriter) Write(p []byte) (int, error) {
	n, err := t.w.Write(p)
	if err != nil {
		t.err = err
	}
	return n, err
}

func mapLen(br *bufio.Reader) (int64, error) {
	tag, err := readByte(br)
	if err != nil {
		return 0, err
	}
	switch {
	case tag >= 0x80 && tag <= 0x8f:
		return int64(tag & 0x0f), nil
	case tag == 0xde:
		return readUint(br, 2)
	case tag == 0xdf:
		return readUint(br, 4)
	}
	return 0, fmt.Errorf("library: export: %w: not a map (0x%02x)", errMalformed, tag)
}

func isStr(tag byte) bool {
	return (tag >= 0xa0 && tag <= 0xbf) || tag == 0xd9 || tag == 0xda || tag == 0xdb
}

// readStr reads a msgpack string. Bytes beyond limit are read and dropped.
func readStr(br *bufio.Reader, limit int64) (string, error) {
	tag, err := readByte(br)
	if err != nil {
		return "", err
	}
	var n int64
	switch {
	case tag >= 0xa0 && tag <= 0xbf:
		n = int64(tag & 0x1f)
	case tag == 0xd9:
		n, err = readUint(br, 1)
	case tag == 0xda:
		n, err = readUint(br, 2)
	case tag == 0xdb:
		n, err = readUint(br, 4)
	default:
		return "", fmt.Errorf("library: export: %w: want a string, got 0x%02x", errMalformed, tag)
	}
	if err != nil {
		return "", err
	}
	buf := make([]byte, min(n, limit))
	if _, err := io.ReadFull(br, buf); err != nil {
		return "", unexpected(err)
	}
	if _, err := io.CopyN(io.Discard, br, n-int64(len(buf))); err != nil {
		return "", unexpected(err)
	}
	return string(buf), nil
}

// skipValue skips the value types Calibre's envelope can hold.
func skipValue(br *bufio.Reader) error {
	tag, err := readByte(br)
	if err != nil {
		return err
	}
	var n int64
	switch {
	case tag == 0xc0, tag == 0xc2, tag == 0xc3, tag <= 0x7f, tag >= 0xe0:
		return nil
	case isStr(tag):
		if err := br.UnreadByte(); err != nil {
			return err
		}
		_, err := readStr(br, 0)
		return err
	case tag == 0xc4, tag == 0xc5, tag == 0xc6:
		n, err = readUint(br, map[byte]int{0xc4: 1, 0xc5: 2, 0xc6: 4}[tag])
	default:
		return fmt.Errorf("library: export: %w: value type 0x%02x", errMalformed, tag)
	}
	if err != nil {
		return err
	}
	if _, err := io.CopyN(io.Discard, br, n); err != nil {
		return unexpected(err)
	}
	return nil
}

func readByte(br *bufio.Reader) (byte, error) {
	b, err := br.ReadByte()
	if err != nil {
		return 0, unexpected(err)
	}
	return b, nil
}

func readUint(br *bufio.Reader, size int) (int64, error) {
	var buf [4]byte
	if _, err := io.ReadFull(br, buf[:size]); err != nil {
		return 0, unexpected(err)
	}
	switch size {
	case 1:
		return int64(buf[0]), nil
	case 2:
		return int64(binary.BigEndian.Uint16(buf[:2])), nil
	default:
		return int64(binary.BigEndian.Uint32(buf[:4])), nil
	}
}

// unexpected turns a clean EOF inside the envelope into io.ErrUnexpectedEOF.
func unexpected(err error) error {
	if errors.Is(err, io.EOF) {
		err = io.ErrUnexpectedEOF
	}
	return fmt.Errorf("library: export: %w", err)
}
