package pdf

import (
	"bytes"
	"compress/zlib"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func documentObjects(objects []string) *Doc {
	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n")
	xref := map[int]entry{}
	for i, obj := range objects {
		xref[i+1] = entry{kind: 1, offset: int64(b.Len())}
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, obj)
	}
	d := newDoc(context.Background(), bytes.NewReader(b.Bytes()), int64(b.Len()))
	d.xref = xref
	return d
}

func TestPDFMaterializationBudget(t *testing.T) {
	obj := "<< /Payload (" + strings.Repeat("p", 1<<20) + ") >>"
	d := documentObjects([]string{obj, obj, obj, obj, obj, obj, obj, obj, obj, obj, obj, obj, obj, obj, obj, obj, obj, obj, obj, obj})
	var err error
	for num := 1; num <= 20; num++ {
		_, err = d.Resolve(Ref{Num: num})
		if err != nil {
			break
		}
		clear(d.cache) // eviction must not restore the document's allowance
	}
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("combined parsed payload was accepted: %v", err)
	}
}

func TestPDFAggregateObjectNodes(t *testing.T) {
	for _, obj := range []string{
		"[" + strings.Repeat("["+strings.Repeat("0 ", 10_000)+"] ", 11) + "]",
		"<< " + strings.Repeat("/Same 0 ", 100_001) + ">>",
	} {
		d := documentObjects([]string{obj})
		if _, err := d.Resolve(Ref{Num: 1}); !errors.Is(err, ErrTooLarge) {
			t.Errorf("aggregate node budget was bypassed: %v", err)
		}
	}
}

func compressedObjectStream(size int) string {
	return objectStreamMember(size, 99)
}

func objectStreamMember(size, num int) string {
	header := fmt.Sprintf("%d 0 ", num)
	body := []byte(header + "<< /Value 7 >>\n" + strings.Repeat(" ", size))
	var b bytes.Buffer
	w := zlib.NewWriter(&b)
	_, _ = w.Write(body)
	_ = w.Close()
	return fmt.Sprintf("<< /Type /ObjStm /N 1 /First %d /Filter /FlateDecode /Length %d >>\nstream\n%s\nendstream", len(header), b.Len(), b.Bytes())
}

func TestPDFObjectStreamCacheBytes(t *testing.T) {
	objects := make([]string, 12)
	for i := range objects {
		objects[i] = compressedObjectStream(3 << 20)
	}
	d := documentObjects(objects)
	for num := range 12 {
		if _, err := d.objStream(num + 1); err != nil {
			t.Fatal(err)
		}
		retained := 0
		for _, s := range d.streams {
			retained += cap(s.data) + 16*cap(s.pairs)
		}
		if retained > 32<<20 {
			t.Fatalf("retained %d bytes of object streams, want at most 32 MiB", retained)
		}
	}
	// The member in an evicted stream must still resolve on demand.
	d.xref[99] = entry{kind: 2, stream: 1, index: 0}
	o, err := d.Resolve(Ref{Num: 99})
	if err != nil || o.(Dict)["Value"] != int64(7) {
		t.Fatalf("evicted member: %v, %v", o, err)
	}
}

type cancelRepairReader struct {
	*bytes.Reader
	cancel context.CancelFunc
}

func (r cancelRepairReader) ReadAt(b []byte, off int64) (int, error) {
	n, err := r.Reader.ReadAt(b, off)
	if off == 0 && len(b) > 2048 {
		r.cancel()
	}
	return n, err
}

func TestPDFRepairCancellationInsideWindow(t *testing.T) {
	b := []byte("%PDF-1.7\n" + strings.Repeat("trailer", 5_000))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err := Open(ctx, cancelRepairReader{bytes.NewReader(b), cancel}, int64(len(b)))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("repair swallowed cancellation in its final window: %v", err)
	}
}

func TestPDFRepairTrailerLimit(t *testing.T) {
	b := []byte("%PDF-1.7\n" + strings.Repeat("trailer", 4_097))
	_, err := Open(context.Background(), bytes.NewReader(b), int64(len(b)))
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("unbounded repair candidates: %v", err)
	}
}

func TestPDFXrefBudget(t *testing.T) {
	d := documentObjects(nil)
	for i := 1; i <= 131_072; i++ {
		if err := d.add(i, entry{kind: 1, offset: int64(i)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.add(1, entry{kind: 1, offset: 999}); err != nil || d.xref[1].offset != 1 {
		t.Fatalf("newest xref entry should win at the cap: %v", err)
	}
	if err := d.put(d.xref, 1, entry{kind: 1, offset: 999}, true); err != nil || d.xref[1].offset != 999 {
		t.Fatalf("repair replacement should work at the cap: %v", err)
	}
	if err := d.add(131_073, entry{kind: 1, offset: 1}); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("xref exceeds memory budget: %v", err)
	}
}

func TestPDFRepairXrefLimits(t *testing.T) {
	t.Run("scan", func(t *testing.T) {
		var b bytes.Buffer
		b.WriteString("%PDF-1.7\n")
		for i := 1; i <= 131_073; i++ {
			fmt.Fprintf(&b, "%d 0 obj\n0\nendobj\n", i)
		}
		d := newDoc(context.Background(), bytes.NewReader(b.Bytes()), int64(b.Len()))
		if _, _, err := d.scan(); !errors.Is(err, ErrTooLarge) {
			t.Fatalf("repair scan exceeded xref allowance: %v", err)
		}
	})
	for _, mode := range []string{"merge", "members"} {
		t.Run(mode, func(t *testing.T) {
			d := documentObjects([]string{objectStreamMember(0, 999_999)})
			if mode == "merge" {
				b := []byte("%PDF-1.7\n999999 0 obj\n0\nendobj\n")
				d.r, d.size = bytes.NewReader(b), int64(len(b))
			} else {
				e := d.xref[1]
				e.hint = hintObjStm
				d.xref[1] = e
			}
			for i := 2; i <= 131_072; i++ {
				d.xref[i] = entry{kind: 1}
			}
			var err error
			if mode == "merge" {
				err = d.repairEntries()
			} else {
				err = d.addMembers()
			}
			if !errors.Is(err, ErrTooLarge) || len(d.xref) != 131_072 {
				t.Fatalf("repair %s: %d entries, error %v", mode, len(d.xref), err)
			}
		})
	}
}

func TestPDFRepairTrailerOverlap(t *testing.T) {
	b := []byte("%PDF-1.7\n" + strings.Repeat(" ", (1<<20)-41) + "trailer\n<<>>\n" + strings.Repeat(" ", 64))
	d := newDoc(context.Background(), bytes.NewReader(b), int64(len(b)))
	_, trailers, err := d.scan()
	if err != nil || len(trailers) != 1 || trailers[0] != (1<<20)-25 {
		t.Fatalf("overlapping windows: %v, %v", trailers, err)
	}
}

func TestPDFResourceErrorsSurviveOptionalLoads(t *testing.T) {
	for _, mode := range []string{"repair root", "indirect length", "indirect filter"} {
		t.Run(mode, func(t *testing.T) {
			d := documentObjects([]string{
				"<< /Type /Catalog /Payload (" + strings.Repeat("p", 4096) + ") >>",
				"<< /Length 1 0 R >>\nstream\nx\nendstream",
			})
			d.budget.bytes = 1024
			var err error
			switch mode {
			case "repair root":
				err = d.repair()
			case "indirect length":
				_, err = d.Resolve(Ref{Num: 2})
			case "indirect filter":
				_, err = d.Decode(Stream{Dict: Dict{"Filter": Ref{Num: 1}}, Length: 1}, 100)
			}
			if !errors.Is(err, ErrTooLarge) || !errors.Is(d.Err(), ErrTooLarge) {
				t.Fatalf("%s swallowed document limit: %v, sticky %v", mode, err, d.Err())
			}
		})
	}
}

func TestPDFStreamCapacityAndOversizedCacheEntry(t *testing.T) {
	d := documentObjects(nil)
	// Small slices can still retain larger backing allocations.
	d.cacheStream(1, &objStm{data: make([]byte, 1, 20<<20)})
	d.cacheStream(2, &objStm{data: make([]byte, 1, 20<<20)})
	if _, ok := d.streams[1]; ok {
		t.Fatal("cache kept a stream whose backing allocation should have been evicted")
	}
	d.cacheStream(3, &objStm{data: make([]byte, 1, 33<<20)})
	if _, ok := d.streams[3]; ok || d.streamBytes > 32<<20 {
		t.Fatal("oversized object stream was retained")
	}
	// An oversized decoded stream remains usable without being retained.
	d = documentObjects([]string{compressedObjectStream(33 << 20)})
	d.xref[99] = entry{kind: 2, stream: 1, index: 0}
	o, err := d.Resolve(Ref{Num: 99})
	if err != nil || o.(Dict)["Value"] != int64(7) {
		t.Fatalf("uncached oversized member: %v, %v", o, err)
	}
	if len(d.streams) != 0 {
		t.Fatal("oversized decoded stream entered the cache")
	}
}
