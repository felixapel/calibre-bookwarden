package library

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"testing"
)

func unhex(t testing.TB, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// result is the msgpack prefix of Calibre's one-entry answer: {"result": …}.
const result = "81a6726573756c74"

var readExportCases = []struct {
	name    string
	in      string // hex
	want    int64
	status  ExportStatus
	bytes   int64
	sha     string
	written string
	check   func(t *testing.T, err error)
}{
	{name: "ok bin16", in: result + "c5000461626364", want: 4, status: ExportOK, bytes: 4, sha: sum("abcd"), written: "abcd"},
	{name: "ok bin8", in: result + "c40378797a", want: 3, status: ExportOK, bytes: 3, sha: sum("xyz"), written: "xyz"},
	{name: "empty file", in: result + "c400", want: 0, status: ExportOK, bytes: 0, sha: sum("")},
	{name: "missing on disk", in: result + "c400", want: 2326, status: ExportMissingOnDisk},
	{name: "not in Calibre", in: result + "c0", want: 4, status: ExportNotInCalibre},
	{name: "size differs", in: result + "c500056162636465", want: 4, status: ExportSizeMismatch},
	{name: "bin32", in: result + "c60000000461626364", want: 4, status: ExportOK, bytes: 4, sha: sum("abcd"), written: "abcd"},
	{name: "map16 header", in: "de0001a6726573756c74c403616263", want: 3, status: ExportOK, bytes: 3, sha: sum("abc"), written: "abc"},
	{
		name: "err map", in: "82a3657272a4626f6f6da27462a178", want: 4,
		check: func(t *testing.T, err error) {
			var ce *CalibreError
			if !errors.As(err, &ce) || ce.Command != "export" || ce.Message != "boom" {
				t.Errorf("err = %v, want CalibreError{export, boom}", err)
			}
		},
	},
	{
		name: "traceback first", in: "82a27462a178a3657272a4626f6f6d", want: 4,
		check: func(t *testing.T, err error) {
			var ce *CalibreError
			if !errors.As(err, &ce) || ce.Message != "boom" {
				t.Errorf("err = %v, want CalibreError(boom)", err)
			}
		},
	},
	{
		name: "result is a string", in: result + "a46e6f7065", want: 4,
		check: func(t *testing.T, err error) {
			var ce *CalibreError
			if !errors.As(err, &ce) || ce.Message != "nope" {
				t.Errorf("err = %v, want CalibreError(nope)", err)
			}
		},
	},
	{
		name: "truncated", in: result + "c40a61626364", want: 10,
		check: func(t *testing.T, err error) {
			if !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Errorf("err = %v, want io.ErrUnexpectedEOF", err)
			}
		},
	},
	{
		name: "truncated header", in: "81a6726573", want: 4,
		check: func(t *testing.T, err error) {
			if !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Errorf("err = %v, want io.ErrUnexpectedEOF", err)
			}
		},
	},
	{
		name: "not a map", in: "93010203", want: 4,
		check: func(t *testing.T, err error) {
			if err == nil || !errors.Is(err, errMalformed) {
				t.Errorf("err = %v, want errMalformed", err)
			}
		},
	},
	{
		name: "no result", in: "81a3666f6fc0", want: 4,
		check: func(t *testing.T, err error) {
			if !errors.Is(err, errMalformed) {
				t.Errorf("err = %v, want errMalformed", err)
			}
		},
	},
}

func TestReadExport(t *testing.T) {
	for _, tt := range readExportCases {
		t.Run(tt.name, func(t *testing.T) {
			var w bytes.Buffer
			got, err := readExport(bytes.NewReader(unhex(t, tt.in)), &w, tt.want)
			if tt.check != nil {
				tt.check(t, err)
				// A cut stream has already written what arrived; the caller
				// deletes the file. Every other error writes nothing.
				if w.Len() != 0 && !errors.Is(err, io.ErrUnexpectedEOF) {
					t.Errorf("wrote %q on an error", w.String())
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := ExportResult{Status: tt.status, Bytes: tt.bytes, SHA256: tt.sha}
			if got != want {
				t.Errorf("readExport() = %+v, want %+v", got, want)
			}
			if w.String() != tt.written {
				t.Errorf("wrote %q, want %q", w.String(), tt.written)
			}
		})
	}
}

func TestReadExportReportsWriteErrors(t *testing.T) {
	full := errors.New("no space left on device")
	_, err := readExport(bytes.NewReader(unhex(t, result+"c5000461626364")), failingWriter{full}, 4)
	if !errors.Is(err, full) || !errors.Is(err, errWrite) {
		t.Fatalf("err = %v, want the write error tagged errWrite", err)
	}
}

type failingWriter struct{ err error }

func (f failingWriter) Write([]byte) (int, error) { return 0, f.err }

// countingWriter counts what readExport writes.
type countingWriter struct{ n int64 }

func (c *countingWriter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return len(p), nil
}

func FuzzReadExport(f *testing.F) {
	for _, tt := range readExportCases {
		f.Add(unhex(f, tt.in), tt.want)
	}
	f.Fuzz(func(t *testing.T, data []byte, want int64) {
		var w countingWriter
		got, err := readExport(bytes.NewReader(data), &w, want)
		if w.n > int64(len(data)) {
			t.Fatalf("wrote %d bytes from a %d-byte answer", w.n, len(data))
		}
		if err == nil && got.Status == ExportOK {
			if w.n != got.Bytes || got.Bytes != want {
				t.Fatalf("ExportOK with %d written, Bytes %d, want %d", w.n, got.Bytes, want)
			}
		}
		if (err != nil || got.Status != ExportOK) && w.n != 0 && !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("wrote %d bytes without ExportOK (status %v, err %v)", w.n, got.Status, err)
		}
	})
}
