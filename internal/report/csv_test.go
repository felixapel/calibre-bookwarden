package report

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestCSVEncoding(t *testing.T) {
	for _, tt := range []struct{ raw, want string }{{`García, "Book"`, `García, "Book"`}, {" \t=SUM(1,2)", "'=SUM(1,2)"}, {"\r\n+1", "'+1"}, {"-2", "'-2"}, {"@a", "'@a"}, {"＝1", "'＝1"}, {"＋1", "'＋1"}, {"－1", "'－1"}, {"＠a", "'＠a"}, {"a\r\nb\tc\u00a0d", "a b c d"}, {"\u200b\x00=1", "'=1"}, {"a,b\n7,8", "a,b 7,8"}, {strings.Repeat("ñ", 600), strings.Repeat("ñ", 512)}} {
		b := sampleBook(2, 1)
		b.Package.Calibre.Title = tt.raw
		var out bytes.Buffer
		if err := New(&fakeStore{books: []Book{b}}).CSV(context.Background(), 7, Filter{}, &out, CSVLimits{}); err != nil {
			t.Fatal(err)
		}
		rows, err := csv.NewReader(&out).ReadAll()
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 2 || !reflect.DeepEqual(rows[0], []string{"run_id", "book_id", "title", "code", "format", "field", "reason"}) || rows[1][0] != "7" || rows[1][1] != "2" || rows[1][2] != tt.want {
			t.Fatalf("%q -> %+v want %q", tt.raw, rows, tt.want)
		}
	}
}

type failCSVWriter struct{}

func (failCSVWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func TestCSVLimits(t *testing.T) {
	ctx := context.Background()
	s := New(&fakeStore{books: []Book{sampleBook(2, 2)}})
	var full bytes.Buffer
	if err := s.CSV(ctx, 7, Filter{}, &full, CSVLimits{}); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		limits CSVLimits
		want   error
	}{{CSVLimits{2, int64(full.Len())}, nil}, {CSVLimits{1, 0}, ErrExportTooLarge}, {CSVLimits{2, int64(full.Len() - 1)}, ErrExportTooLarge}, {CSVLimits{0, 1}, ErrExportTooLarge}, {CSVLimits{-1, 0}, ErrInvalidLimits}, {CSVLimits{0, -1}, ErrInvalidLimits}} {
		var out bytes.Buffer
		if err := s.CSV(ctx, 7, Filter{}, &out, tt.limits); !errors.Is(err, tt.want) {
			t.Fatalf("limits %+v=%v", tt.limits, err)
		}
	}
	if err := s.CSV(ctx, 7, Filter{}, failCSVWriter{}, CSVLimits{}); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("flush=%v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := s.CSV(cancelled, 7, Filter{}, io.Discard, CSVLimits{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
	bad := New(&fakeStore{books: []Book{sampleBook(1, 100), sampleBook(2, 1)}, corrupt: 2})
	if err := bad.CSV(ctx, 7, Filter{}, io.Discard, CSVLimits{}); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("late corruption=%v", err)
	}
}
