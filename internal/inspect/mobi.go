package inspect

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"golang.org/x/text/encoding/charmap"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

const (
	maxRecord0    = 1 << 20  // the header record is read up to this
	maxTextRecord = 64 << 10 // text records are 4 KiB plus trailing entries

	compressionNone     = 1
	compressionPalmDOC  = 2
	compressionHuffCDIC = 17480

	exthPresent = 0x40
)

// mobiLanguages maps a MOBI locale's primary language id to its code.
var mobiLanguages = map[uint32]string{
	0x03: "ca", 0x04: "zh", 0x05: "cs", 0x06: "da", 0x07: "de", 0x08: "el", 0x09: "en", 0x0A: "es",
	0x0B: "fi", 0x0C: "fr", 0x0E: "hu", 0x10: "it", 0x11: "ja", 0x12: "ko", 0x13: "nl", 0x14: "nb",
	0x15: "pl", 0x16: "pt", 0x19: "ru", 0x1D: "sv", 0x1F: "tr",
}

func inspectMOBI(_ context.Context, r io.ReaderAt, size int64, _ Limits) Result {
	kind, records, err := palmRecords(r, size)
	if err != nil {
		return failure(err)
	}
	rec0, err := readRecord(r, records, 0, maxRecord0, true)
	if err != nil {
		return failure(err)
	}
	if len(rec0) < 16 {
		return failure(fmt.Errorf("%w: short header record", errCorrupt))
	}
	compression := binary.BigEndian.Uint16(rec0)
	textRecords := binary.BigEndian.Uint16(rec0[8:])
	var (
		meta       *evidence.FileMetadata
		encryption uint16
		extraFlags uint16
	)
	if kind == "BOOKMOBI" { // in a PalmDOC text, offset 12 is the reading position
		encryption = binary.BigEndian.Uint16(rec0[12:])
		if meta, extraFlags, err = mobiHeader(rec0); err != nil {
			return failure(err)
		}
	}
	switch {
	case encryption != 0:
		return Result{State: evidence.StateDRM, Text: evidence.TextUnverified, Metadata: meta}
	case compression == compressionHuffCDIC:
		return Result{State: evidence.StateUnsupported, Reason: evidence.ReasonHuffCDIC, Text: evidence.TextUnverified, Metadata: meta}
	case compression != compressionNone && compression != compressionPalmDOC, textRecords == 0:
		return Result{State: evidence.StateCorrupt, Metadata: meta}
	}
	text, err := readRecord(r, records, 1, maxTextRecord, false)
	if err == nil {
		text, err = stripTrailing(text, extraFlags)
	}
	if err == nil && compression == compressionPalmDOC {
		text, err = palmdocDecompress(text, maxTextRecord)
	}
	if err != nil {
		res := failure(err)
		res.Metadata = meta
		return res
	}
	res := Result{State: evidence.StateOK, Text: evidence.TextAbsent, Metadata: meta}
	if len(bytes.TrimLeft(text, " \t\r\n\f\v\x00")) > 0 {
		res.Text = evidence.TextPresent
	}
	return res
}

// palmRecords reads a PalmDB's type and the [start, end) of each record.
func palmRecords(r io.ReaderAt, size int64) (string, [][2]int64, error) {
	hdr := make([]byte, 78)
	if _, err := r.ReadAt(hdr, 0); err != nil {
		return "", nil, fmt.Errorf("%w: PalmDB header: %w", errCorrupt, err)
	}
	kind := string(hdr[60:68])
	if kind != "BOOKMOBI" && kind != "TEXtREAd" {
		return "", nil, fmt.Errorf("%w: not a MOBI or PalmDOC file", errCorrupt)
	}
	n := int(binary.BigEndian.Uint16(hdr[76:]))
	if n < 2 {
		return "", nil, fmt.Errorf("%w: %d records", errCorrupt, n)
	}
	list := make([]byte, 8*n)
	if _, err := r.ReadAt(list, 78); err != nil {
		return "", nil, fmt.Errorf("%w: record list: %w", errCorrupt, err)
	}
	records := make([][2]int64, n)
	for i := range records {
		start := int64(binary.BigEndian.Uint32(list[8*i:]))
		if start < int64(78+8*n) || start > size || i > 0 && start < records[i-1][0] {
			return "", nil, fmt.Errorf("%w: record %d at %d", errCorrupt, i, start)
		}
		records[i][0] = start
		if i > 0 {
			records[i-1][1] = start
		}
	}
	records[n-1][1] = size
	return kind, records, nil
}

// readRecord reads record i: truncated to limit, or refused past it.
func readRecord(r io.ReaderAt, records [][2]int64, i int, limit int64, truncate bool) ([]byte, error) {
	n := records[i][1] - records[i][0]
	if n > limit {
		if !truncate {
			return nil, fmt.Errorf("%w: record %d is %d bytes", errCorrupt, i, n)
		}
		n = limit
	}
	b := make([]byte, n)
	if _, err := r.ReadAt(b, records[i][0]); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: record %d: %w", errCorrupt, i, err)
	}
	return b, nil
}

// mobiHeader reads the MOBI header and its EXTH records from record 0.
func mobiHeader(rec0 []byte) (*evidence.FileMetadata, uint16, error) {
	if len(rec0) < 24 || string(rec0[16:20]) != "MOBI" {
		return nil, 0, fmt.Errorf("%w: no MOBI header", errCorrupt)
	}
	headerLen := int64(binary.BigEndian.Uint32(rec0[20:]))
	end := int(min(16+headerLen, int64(len(rec0))))
	u32 := func(at int) uint32 {
		if at+4 > end {
			return 0
		}
		return binary.BigEndian.Uint32(rec0[at:])
	}
	var extraFlags uint16
	if headerLen >= 0xE4 && end >= 244 {
		extraFlags = binary.BigEndian.Uint16(rec0[242:])
	}
	decode := func(b []byte) string { return string(b) }
	if u32(28) == 1252 {
		decode = func(b []byte) string {
			s, _ := charmap.Windows1252.NewDecoder().Bytes(b)
			return string(s)
		}
	}

	var m evidence.FileMetadata
	if off, n := int64(u32(84)), int64(u32(88)); n > 0 && off+n <= int64(len(rec0)) {
		m.Title = decode(rec0[off : off+n])
	}
	if u32(128)&exthPresent != 0 {
		if err := readEXTH(rec0, 16+headerLen, decode, &m); err != nil {
			return nil, 0, err
		}
	}
	if m.Languages == nil {
		if lang, ok := mobiLanguages[u32(92)&0xFF]; ok {
			m.Languages = []string{lang}
		}
	}
	return newMeta(m), extraFlags, nil
}

// readEXTH reads the metadata records that follow the MOBI header.
func readEXTH(rec0 []byte, at int64, decode func([]byte) string, m *evidence.FileMetadata) error {
	size := int64(len(rec0))
	if at < 0 || at+12 > size || string(rec0[at:at+4]) != "EXTH" {
		return fmt.Errorf("%w: no EXTH header", errCorrupt)
	}
	count := binary.BigEndian.Uint32(rec0[at+8:])
	p := at + 12
	for range count {
		if p+8 > size {
			return fmt.Errorf("%w: EXTH records past their record", errCorrupt)
		}
		typ, n := binary.BigEndian.Uint32(rec0[p:]), int64(binary.BigEndian.Uint32(rec0[p+4:]))
		if n < 8 || p+n > size {
			return fmt.Errorf("%w: EXTH record of %d bytes", errCorrupt, n)
		}
		value := decode(rec0[p+8 : p+n])
		switch typ {
		case 100:
			m.Authors = append(m.Authors, value)
		case 101:
			if m.Publisher == "" {
				m.Publisher = value
			}
		case 104:
			m.ISBNs = append(m.ISBNs, value)
		case 106:
			if m.Date == "" {
				m.Date = value
			}
		case 503:
			m.Title = value
		case 524:
			m.Languages = append(m.Languages, value)
		}
		p += n
	}
	return nil
}

// stripTrailing removes the entries the extra-data flags announce at the end
// of a text record: each higher bit's entry ends with its size, which counts
// itself, written backwards; bit 0's multibyte overlap comes before them.
func stripTrailing(rec []byte, flags uint16) ([]byte, error) {
	size := len(rec)
	for bit := 1; bit < 16; bit++ {
		if flags&(1<<bit) == 0 {
			continue
		}
		n, shift := 0, 0
		for i := size - 1; i >= 0 && shift < 28; i-- {
			v := rec[i]
			n |= int(v&0x7F) << shift
			shift += 7
			if v&0x80 != 0 {
				break
			}
		}
		if n > size {
			return nil, fmt.Errorf("%w: trailing entry of %d bytes", errCorrupt, n)
		}
		size -= n
	}
	if flags&1 != 0 && size > 0 {
		n := int(rec[size-1]&3) + 1
		if n > size {
			return nil, fmt.Errorf("%w: multibyte overlap of %d bytes", errCorrupt, n)
		}
		size -= n
	}
	return rec[:size], nil
}
