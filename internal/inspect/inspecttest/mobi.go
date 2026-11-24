package inspecttest

import (
	"encoding/binary"
	"slices"
)

// MOBIOptions describe a generated MOBI or AZW3. Strings are written as the
// bytes given, so tests choose their encoding.
type MOBIOptions struct {
	Version     int // MOBI header version: 6 (MOBI, 232-byte header) or 8 (AZW3, 264)
	Title       string
	Text        string
	EXTH        map[uint32][]string // written in key order
	Encoding    uint32              // 65001 (UTF-8) or 1252
	Locale      uint32
	Compression uint16 // 1 none, 2 PalmDOC; anything else is written uncompressed
	Encryption  uint16
	ExtraFlags  uint16 // the trailing entries each text record carries
	Record1     []byte // replaces the first text record as written
	PalmDOC     bool   // a TEXtREAd file: no MOBI header, no metadata
}

// CalibreMOBI is shaped like ebook-convert's output for book 3 of the
// fixture library.
func CalibreMOBI(version int) MOBIOptions {
	return MOBIOptions{
		Version: version, Title: "Binary Tides", Text: "<html><body><p>Two formats carry the same generated evidence.</p></body></html>",
		EXTH: map[uint32][]string{
			100: {"Cy Placeholder"}, 104: {"9780000000026"}, 106: {"2026-10-13T06:42:00+00:00"},
			503: {"Binary Tides"}, 524: {"en"},
		},
		Encoding: 65001, Locale: 0x09, Compression: 2, ExtraFlags: 3,
	}
}

// MOBI builds a PalmDB of three records: the header record, one text record
// and an end-of-file record.
func MOBI(o MOBIOptions) []byte {
	text := []byte(o.Text)
	if o.Compression == 2 {
		text = palmdocLiterals(text)
	}
	text = append(text, trailingEntries(o.ExtraFlags)...)
	if o.Record1 != nil {
		text = o.Record1
	}
	kind := "BOOKMOBI"
	rec0 := mobiHeader(o)
	if o.PalmDOC {
		kind, rec0 = "TEXtREAd", rec0[:16]
	}
	records := [][]byte{rec0, text, {0xe9, 0x8e, '\r', '\n'}}

	db := make([]byte, 78, 78+8*len(records)+2)
	copy(db, "generated")
	copy(db[60:], kind)
	binary.BigEndian.PutUint16(db[76:], uint16(len(records))) //nolint:gosec // three records
	off := 78 + 8*len(records) + 2
	for i, r := range records {
		db = binary.BigEndian.AppendUint32(db, uint32(off)) //nolint:gosec // small test files
		db = binary.BigEndian.AppendUint32(db, uint32(2*i)) //nolint:gosec // small test files
		off += len(r)
	}
	db = append(db, 0, 0)
	for _, r := range records {
		db = append(db, r...)
	}
	return db
}

func mobiHeader(o MOBIOptions) []byte {
	headerLen := 232
	if o.Version == 8 {
		headerLen = 264
	}
	var exth []byte
	if len(o.EXTH) > 0 {
		keys := make([]uint32, 0, len(o.EXTH))
		for k := range o.EXTH {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		var recs []byte
		n := 0
		for _, k := range keys {
			for _, v := range o.EXTH[k] {
				recs = binary.BigEndian.AppendUint32(recs, k)
				recs = binary.BigEndian.AppendUint32(recs, uint32(8+len(v))) //nolint:gosec // small test files
				recs = append(recs, v...)
				n++
			}
		}
		exth = append([]byte("EXTH"), binary.BigEndian.AppendUint32(nil, uint32(12+len(recs)))...) //nolint:gosec // small test files
		exth = binary.BigEndian.AppendUint32(exth, uint32(n))                                      //nolint:gosec // small test files
		exth = append(exth, recs...)
	}
	rec := make([]byte, 16+headerLen)
	be16 := func(at int, v uint16) { binary.BigEndian.PutUint16(rec[at:], v) }
	be32 := func(at int, v uint32) { binary.BigEndian.PutUint32(rec[at:], v) }
	be16(0, o.Compression)
	be32(4, uint32(len(o.Text))) //nolint:gosec // small test files
	be16(8, 1)                   // one text record
	be16(10, 4096)
	be16(12, o.Encryption)
	copy(rec[16:], "MOBI")
	be32(20, uint32(headerLen)) //nolint:gosec // 232 or 264
	be32(24, 2)                 // a book
	be32(28, o.Encoding)
	be32(36, uint32(o.Version)) //nolint:gosec // 6 or 8
	nameOff := len(rec) + len(exth)
	be32(84, uint32(nameOff))      //nolint:gosec // small test files
	be32(88, uint32(len(o.Title))) //nolint:gosec // small test files
	be32(92, o.Locale)
	if exth != nil {
		be32(128, 0x50)
	}
	be16(242, o.ExtraFlags)
	rec = append(rec, exth...)
	rec = append(rec, o.Title...)
	return append(rec, 0, 0)
}

// palmdocLiterals is valid PalmDOC that only uses literal runs.
func palmdocLiterals(text []byte) []byte {
	var out []byte
	for len(text) > 0 {
		n := min(len(text), 8)
		out = append(out, byte(n))
		out = append(out, text[:n]...)
		text = text[n:]
	}
	return out
}

// trailingEntries is what flags announce after a text record: one byte of a
// multibyte character's overlap for bit 0, and a three-byte entry with its
// size for each higher bit, the lowest bit last.
func trailingEntries(flags uint16) []byte {
	var out []byte
	if flags&1 != 0 {
		out = append(out, 0x00) // no overlap bytes, then the count byte itself
	}
	for bit := 15; bit >= 1; bit-- {
		if flags&(1<<bit) != 0 {
			out = append(out, 't', 'b', 's', 0x84) // a size of 4, backward-encoded
		}
	}
	return out
}
