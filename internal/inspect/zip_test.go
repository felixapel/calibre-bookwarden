package inspect_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/inspect"
)

type entry struct {
	name   string
	data   []byte
	method uint16
}

func zipOf(t testing.TB, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, e := range entries {
		f, err := w.CreateHeader(&zip.FileHeader{Name: e.name, Method: e.method})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(e.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func manyEntries(t testing.TB, n int) []byte {
	t.Helper()
	entries := make([]entry, n)
	for i := range entries {
		entries[i] = entry{name: fmt.Sprintf("e%05d", i), method: zip.Store}
	}
	return zipOf(t, entries...)
}

func randomBytes(n int, seed uint64) []byte {
	b := make([]byte, n)
	r := rand.NewChaCha8([32]byte{byte(seed)})
	_, _ = r.Read(b)
	return b
}

// eocd is an end-of-central-directory record claiming count entries.
func eocd(count uint16, size, offset uint32) []byte {
	b := make([]byte, 22)
	copy(b, "PK\x05\x06")
	binary.LittleEndian.PutUint16(b[8:], count)
	binary.LittleEndian.PutUint16(b[10:], count)
	binary.LittleEndian.PutUint32(b[12:], size)
	binary.LittleEndian.PutUint32(b[16:], offset)
	return b
}

// zip64Claim is a zip64 end record, its locator and a classic end record,
// claiming count entries.
func zip64Claim(count uint64) []byte {
	rec := make([]byte, 56)
	copy(rec, "PK\x06\x06")
	binary.LittleEndian.PutUint64(rec[4:], 44)
	binary.LittleEndian.PutUint64(rec[24:], count)
	binary.LittleEndian.PutUint64(rec[32:], count)
	loc := make([]byte, 20)
	copy(loc, "PK\x06\x07")
	binary.LittleEndian.PutUint32(loc[16:], 1)
	return append(append(rec, loc...), eocd(0xFFFF, 0xFFFFFFFF, 0xFFFFFFFF)...)
}

func TestZipLimits(t *testing.T) {
	ctx := context.Background()
	stored := zipOf(t, entry{name: "a.txt", data: []byte("hello, world"), method: zip.Store})
	badCRC := bytes.Clone(stored)
	badCRC[30+len("a.txt")] ^= 0xFF
	random := make([]entry, 4)
	for i := range random {
		random[i] = entry{name: fmt.Sprintf("r%d", i), data: randomBytes(1<<20, uint64(i)), method: zip.Deflate}
	}
	tests := []struct {
		name   string
		data   []byte
		lim    inspect.Limits
		state  evidence.State
		reason string
	}{
		{"10,001 entries", manyEntries(t, 10_001), inspect.Limits{}, evidence.StateNotInspected, evidence.ReasonTooManyEntries},
		{"10,000 entries", manyEntries(t, 10_000), inspect.Limits{}, evidence.StateOK, ""},
		{"an end record claiming 65,000 entries", eocd(65_000, 0, 0), inspect.Limits{}, evidence.StateNotInspected, evidence.ReasonTooManyEntries},
		{"a zip64 end record claiming 2^40 entries", zip64Claim(1 << 40), inspect.Limits{}, evidence.StateNotInspected, evidence.ReasonTooManyEntries},
		{"2 MiB of zeros", zipOf(t, entry{name: "z", data: make([]byte, 2<<20), method: zip.Deflate}), inspect.Limits{}, evidence.StateNotInspected, evidence.ReasonCompressionRatio},
		{"512 KiB of zeros", zipOf(t, entry{name: "z", data: make([]byte, 512<<10), method: zip.Deflate}), inspect.Limits{}, evidence.StateOK, ""},
		{"4 MiB past a 3 MiB total", zipOf(t, random...), inspect.Limits{MaxDecompressed: 3 << 20}, evidence.StateNotInspected, evidence.ReasonDecompressedTooLarge},
		{"4 MiB under the default total", zipOf(t, random...), inspect.Limits{}, evidence.StateOK, ""},
		{"a wrong CRC", badCRC, inspect.Limits{}, evidence.StateCorrupt, ""},
		{"truncated", stored[:len(stored)-10], inspect.Limits{}, evidence.StateCorrupt, ""},
		{"not a zip", []byte("this is not a zip archive at all"), inspect.Limits{}, evidence.StateCorrupt, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := inspect.VerifyZip(ctx, bytes.NewReader(tt.data), int64(len(tt.data)), tt.lim)
			if got.State != tt.state || got.Reason != tt.reason {
				t.Errorf("got %s/%s, want %s/%s", got.State, got.Reason, tt.state, tt.reason)
			}
		})
	}
}

func TestZipEntryCountIsRefusedBeforeArchiveZip(t *testing.T) {
	calls := inspect.CountZipReaders(t)
	data := manyEntries(t, 10_001)
	got := inspect.VerifyZip(context.Background(), bytes.NewReader(data), int64(len(data)), inspect.Limits{})
	if got.Reason != evidence.ReasonTooManyEntries || *calls != 0 {
		t.Errorf("got %s/%s after %d zip.NewReader calls, want too_many_entries after 0", got.State, got.Reason, *calls)
	}
}

// An end record can claim fewer entries than the directory holds: archive/zip
// only compares the low 16 bits of the count.
func TestZipEntryCountIsTheDirectoryNotTheClaim(t *testing.T) {
	data := manyEntries(t, 10_001)
	end := len(data) - 22
	binary.LittleEndian.PutUint16(data[end+8:], 3)
	binary.LittleEndian.PutUint16(data[end+10:], 3)
	got := inspect.VerifyZip(context.Background(), bytes.NewReader(data), int64(len(data)), inspect.Limits{})
	if got.Reason != evidence.ReasonTooManyEntries {
		t.Errorf("got %s/%s, want not_inspected/too_many_entries", got.State, got.Reason)
	}
}

func TestZipRead(t *testing.T) {
	data := zipOf(t, entry{name: "OEBPS/content.opf", data: []byte("<package/>"), method: zip.Deflate})
	r := bytes.NewReader(data)
	if got, err := inspect.ReadZipEntry(r, int64(len(data)), "OEBPS/content.opf", 64); err != nil || string(got) != "<package/>" {
		t.Errorf("read = %q, %v; want <package/>", got, err)
	}
	if _, err := inspect.ReadZipEntry(r, int64(len(data)), "OEBPS/missing.opf", 64); err == nil {
		t.Error("reading a missing entry: no error")
	}
	if _, err := inspect.ReadZipEntry(r, int64(len(data)), "OEBPS/content.opf", 9); err == nil {
		t.Error("reading an entry past its limit: no error")
	}
}
