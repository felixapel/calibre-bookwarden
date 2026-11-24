package inspect_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"reflect"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/inspect"
	"github.com/felixapel/calibre-bookwarden/internal/inspect/inspecttest"
)

func mobiWith(version int, change func(*inspecttest.MOBIOptions)) []byte {
	o := inspecttest.CalibreMOBI(version)
	change(&o)
	return inspecttest.MOBI(o)
}

// patched changes a generated file in place.
func patched(b []byte, change func([]byte)) []byte {
	change(b)
	return b
}

func manyAuthors() []string {
	out := make([]string, 5000)
	for i := range out {
		out[i] = fmt.Sprint("Author ", i%100)
	}
	return out
}

var calibreMeta = &evidence.FileMetadata{
	Limits: &evidence.MetadataLimits{Version: 1},
	Title:  "Binary Tides", Authors: []string{"Cy Placeholder"}, Languages: []string{"en"},
	ISBNs: []string{"9780000000026"}, Date: "2026-10-13T06:42:00+00:00",
}

var mobiCases = []struct {
	name   string
	data   []byte
	state  evidence.State
	reason string
	text   string
	check  func(t *testing.T, m *evidence.FileMetadata)
}{
	{"MOBI 6 from ebook-convert", inspecttest.MOBI(inspecttest.CalibreMOBI(6)), evidence.StateOK, "", evidence.TextPresent, func(t *testing.T, m *evidence.FileMetadata) {
		if !reflect.DeepEqual(m, calibreMeta) {
			t.Errorf("metadata %+v, want %+v", m, calibreMeta)
		}
	}},
	{"AZW3 from ebook-convert", inspecttest.MOBI(inspecttest.CalibreMOBI(8)), evidence.StateOK, "", evidence.TextPresent, func(t *testing.T, m *evidence.FileMetadata) {
		if !reflect.DeepEqual(m, calibreMeta) {
			t.Errorf("metadata %+v, want %+v", m, calibreMeta)
		}
	}},
	{"no updated title", mobiWith(6, func(o *inspecttest.MOBIOptions) {
		delete(o.EXTH, 503)
		o.Title = "Full Name Title"
	}), evidence.StateOK, "", evidence.TextPresent, func(t *testing.T, m *evidence.FileMetadata) {
		if m == nil || m.Title != "Full Name Title" {
			t.Errorf("metadata %+v, want the title from the full name", m)
		}
	}},
	{"no language record", mobiWith(6, func(o *inspecttest.MOBIOptions) {
		delete(o.EXTH, 524)
		o.Locale = 0x0407
	}), evidence.StateOK, "", evidence.TextPresent, func(t *testing.T, m *evidence.FileMetadata) {
		if m == nil || len(m.Languages) != 1 || m.Languages[0] != "de" {
			t.Errorf("metadata %+v, want language de from the locale", m)
		}
	}},
	{"Windows-1252", mobiWith(6, func(o *inspecttest.MOBIOptions) {
		o.Encoding, o.EXTH[100] = 1252, []string{"Ren\xe9 Example"}
	}), evidence.StateOK, "", evidence.TextPresent, func(t *testing.T, m *evidence.FileMetadata) {
		if m == nil || len(m.Authors) != 1 || m.Authors[0] != "René Example" {
			t.Errorf("metadata %+v, want author René Example", m)
		}
	}},
	{"encrypted", mobiWith(6, func(o *inspecttest.MOBIOptions) { o.Encryption = 2 }), evidence.StateDRM, "", evidence.TextUnverified, func(t *testing.T, m *evidence.FileMetadata) {
		if m == nil || m.Title != "Binary Tides" {
			t.Errorf("metadata %+v, want it read despite the DRM", m)
		}
	}},
	{"HUFF/CDIC", mobiWith(6, func(o *inspecttest.MOBIOptions) { o.Compression = 17480 }), evidence.StateUnsupported, evidence.ReasonHuffCDIC, evidence.TextUnverified, nil},
	{"an unknown compression", mobiWith(6, func(o *inspecttest.MOBIOptions) { o.Compression = 3 }), evidence.StateCorrupt, "", "", nil},
	{"uncompressed", mobiWith(6, func(o *inspecttest.MOBIOptions) { o.Compression = 1 }), evidence.StateOK, "", evidence.TextPresent, nil},
	{"no trailing entries", mobiWith(6, func(o *inspecttest.MOBIOptions) { o.ExtraFlags = 0 }), evidence.StateOK, "", evidence.TextPresent, nil},
	{"three kinds of trailing entries", mobiWith(8, func(o *inspecttest.MOBIOptions) { o.ExtraFlags = 0b111 }), evidence.StateOK, "", evidence.TextPresent, nil},
	{"a first text record pointing before its start", mobiWith(6, func(o *inspecttest.MOBIOptions) {
		o.ExtraFlags, o.Record1 = 0, []byte{'a', 0x80, 0x19}
	}), evidence.StateCorrupt, "", "", nil},
	{"a first text record of only whitespace", mobiWith(6, func(o *inspecttest.MOBIOptions) { o.Text = " \n " }), evidence.StateOK, "", evidence.TextAbsent, nil},
	{"record offsets decreasing", patched(inspecttest.MOBI(inspecttest.CalibreMOBI(6)), func(b []byte) {
		copy(b[78+16:], b[78:82])
	}), evidence.StateCorrupt, "", "", nil},
	{"a record past the end of the file", patched(inspecttest.MOBI(inspecttest.CalibreMOBI(6)), func(b []byte) {
		binary.BigEndian.PutUint32(b[78+16:], uint32(len(b)+10)) //nolint:gosec // small test file
	}), evidence.StateCorrupt, "", "", nil},
	{"one record", patched(inspecttest.MOBI(inspecttest.CalibreMOBI(6)), func(b []byte) {
		binary.BigEndian.PutUint16(b[76:], 1)
	}), evidence.StateCorrupt, "", "", nil},
	{"an EXTH record overrunning its record", patched(inspecttest.MOBI(inspecttest.CalibreMOBI(6)), func(b []byte) {
		exth := bytes.Index(b, []byte("EXTH"))
		binary.BigEndian.PutUint32(b[exth+16:], 0xFFFF)
	}), evidence.StateCorrupt, "", "", nil},
	{"5,000 author records", mobiWith(6, func(o *inspecttest.MOBIOptions) { o.EXTH[100] = manyAuthors() }), evidence.StateOK, "", evidence.TextPresent, func(t *testing.T, m *evidence.FileMetadata) {
		if m == nil || len(m.Authors) != 32 {
			t.Errorf("metadata %+v, want 32 authors", m)
		}
	}},
	{"a PalmDOC text", inspecttest.MOBI(inspecttest.MOBIOptions{PalmDOC: true, Compression: 2, Text: "Plain text.", Encryption: 7}), evidence.StateOK, "", evidence.TextPresent, func(t *testing.T, m *evidence.FileMetadata) {
		if m != nil {
			t.Errorf("metadata %+v, want none", m)
		}
	}},
	{"a BOOKMOBI without a MOBI header", patched(inspecttest.MOBI(inspecttest.CalibreMOBI(6)), func(b []byte) {
		copy(b[78+bytes.Index(b[78:], []byte("MOBI")):], "JUNK") // record 0's, not the PalmDB creator
	}), evidence.StateCorrupt, "", "", nil},
}

func TestMOBI(t *testing.T) {
	for _, tt := range mobiCases {
		t.Run(tt.name, func(t *testing.T) {
			res := inspect.Inspect(context.Background(), "AZW3", bytes.NewReader(tt.data), int64(len(tt.data)), inspect.Limits{})
			checkInvariants(t, res)
			if res.State != tt.state || res.Reason != tt.reason || res.Text != tt.text {
				t.Errorf("got %s/%s text %q, want %s/%s text %q", res.State, res.Reason, res.Text, tt.state, tt.reason, tt.text)
			}
			if tt.check != nil {
				tt.check(t, res.Metadata)
			}
		})
	}
}

func FuzzMOBI(f *testing.F) {
	for _, tt := range mobiCases {
		f.Add(tt.data)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		checkInvariants(t, inspect.Inspect(context.Background(), "MOBI", bytes.NewReader(data), int64(len(data)), inspect.Limits{}))
	})
}
