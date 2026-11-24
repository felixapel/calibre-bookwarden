package report

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode"
)

// CSVText specifies bounded cell encoding, including an apostrophe prefix for
// formula-leading text. It does not promise behavior across spreadsheet resaves.
func CSVText(value string) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return ' '
		}
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return -1
		}
		return r
	}, value)
	value = Display(strings.Join(strings.Fields(value), " "))
	if value != "" {
		first := []rune(value)[0]
		if strings.ContainsRune("=+-@＝＋－＠", first) {
			value = "'" + value
		}
	}
	return value
}

type cappedCSVWriter struct {
	ctx       context.Context
	writer    io.Writer
	remaining int64
}

func (w *cappedCSVWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	if int64(len(p)) > w.remaining {
		return 0, ErrExportTooLarge
	}
	n, err := w.writer.Write(p)
	w.remaining -= int64(n)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	return n, err
}
func (s *Service) CSV(ctx context.Context, runID int64, f Filter, out io.Writer, limits CSVLimits) error {
	if limits.MaxRows < 0 || limits.MaxBytes < 0 {
		return ErrInvalidLimits
	}
	defaults := DefaultCSVLimits()
	if limits.MaxRows == 0 {
		limits.MaxRows = defaults.MaxRows
	}
	if limits.MaxBytes == 0 {
		limits.MaxBytes = defaults.MaxBytes
	}
	writer := csv.NewWriter(&cappedCSVWriter{ctx: ctx, writer: out, remaining: limits.MaxBytes})
	if err := writer.Write([]string{"run_id", "book_id", "title", "code", "format", "field", "reason"}); err != nil {
		return fmt.Errorf("report: CSV header: %w", err)
	}
	rows := 0
	err := s.Walk(ctx, runID, f, func(row Row) error {
		if rows == limits.MaxRows {
			return ErrExportTooLarge
		}
		rows++
		return writer.Write([]string{strconv.FormatInt(row.RunID, 10), strconv.Itoa(row.BookID), CSVText(row.Title), CSVText(row.Finding.Code), CSVText(row.Finding.Format), CSVText(row.Finding.Field), CSVText(row.Finding.Reason)})
	})
	if err != nil {
		return fmt.Errorf("report: CSV scan: %w", err)
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return fmt.Errorf("report: CSV flush: %w", err)
	}
	return ctx.Err()
}
