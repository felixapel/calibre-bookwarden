package inspecttest

import (
	"bytes"
	"compress/zlib"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
)

// PDFOptions describe a generated PDF. Offsets are computed while writing.
type PDFOptions struct {
	Info           map[string]string // written as hex strings of the given bytes
	Update         map[string]string // when set, an incremental update replaces Info with these
	XMP            string
	Text           string // drawn on TextPage; "" leaves every page without text
	TextPage       int    // 1-based; 0 means 1
	TextInForm     bool   // Text is drawn by a Form XObject the page calls
	LZW            bool   // content streams claim /LZWDecode
	Lang           string
	Pages          int // 0 means 1
	XRefStream     bool
	ObjectStreams  bool // dictionaries go into one object stream (with XRefStream or Hybrid)
	Hybrid         bool // a classic table whose /XRefStm lists the object stream's members
	BreakOffsets   bool // every xref offset is 7 bytes off
	CRLFShift      bool // every LF becomes CRLF after writing; streams stay uncompressed
	NoStartxref    bool
	PrevLoop       bool // the trailer's /Prev points at its own section
	IndirectLength bool // the text stream's /Length is an indirect object
	WrongLength    bool // the text stream's /Length is 5 bytes too long
	Encrypt        string
	NoRoot         bool // no /Root, and no /Type /Catalog to repair from
	NoPages        bool
	PageCycle      bool   // the page tree lists itself as a kid
	Count          string // replaces the page tree's /Count value; "-" leaves it out
}

type pdfObj struct {
	num    int
	dict   string // entries without << >>; streams get their /Length added
	stream []byte
	length string // overrides the /Length value
}

// PDF builds a PDF from o.
func PDF(o PDFOptions) []byte {
	pages := max(o.Pages, 1)
	textPage := max(o.TextPage, 1)
	next := 1
	alloc := func() int { next++; return next - 1 }
	catalog, tree := alloc(), alloc()
	pageNums := make([]int, pages)
	for i := range pageNums {
		pageNums[i] = alloc()
	}
	contentNums := make([]int, pages)
	for i := range contentNums {
		contentNums[i] = alloc()
	}
	font := alloc()
	form, info, meta, enc, length := 0, 0, 0, 0, 0
	if o.TextInForm {
		form = alloc()
	}
	if o.Info != nil {
		info = alloc()
	}
	if o.XMP != "" {
		meta = alloc()
	}
	if o.Encrypt != "" {
		enc = alloc()
	}
	if o.IndirectLength {
		length = alloc()
	}

	compress := !o.CRLFShift
	stream := func(content string) ([]byte, string) {
		switch {
		case o.LZW:
			return []byte("\x80\x0b\x60\x50\x22\x0c\x0c\x85\x01"), "/Filter /LZWDecode"
		case compress:
			return deflate([]byte(content)), "/Filter /FlateDecode"
		}
		return []byte(content), ""
	}
	text := "BT /F1 12 Tf 72 700 Td (" + escapePDF(o.Text) + ") Tj ET"

	var objs []pdfObj
	cat := "/Type /Catalog"
	if o.NoRoot {
		cat = "/Type /NotACatalog"
	}
	if !o.NoPages {
		cat += fmt.Sprintf(" /Pages %d 0 R", tree)
	}
	if o.Lang != "" {
		cat += " /Lang (" + escapePDF(o.Lang) + ")"
	}
	if meta != 0 {
		cat += fmt.Sprintf(" /Metadata %d 0 R", meta)
	}
	objs = append(objs, pdfObj{num: catalog, dict: cat})
	kids := refs(pageNums)
	if o.PageCycle {
		kids += fmt.Sprintf(" %d 0 R", tree)
	}
	count := fmt.Sprintf(" /Count %d", pages)
	switch o.Count {
	case "":
	case "-":
		count = ""
	default:
		count = " /Count " + o.Count
	}
	objs = append(objs, pdfObj{num: tree, dict: fmt.Sprintf("/Type /Pages /Kids [%s]%s", kids, count)})
	for i, n := range pageNums {
		res := fmt.Sprintf("/Font << /F1 %d 0 R >>", font)
		if form != 0 {
			res += fmt.Sprintf(" /XObject << /X1 %d 0 R >>", form)
		}
		objs = append(objs, pdfObj{num: n, dict: fmt.Sprintf(
			"/Type /Page /Parent %d 0 R /MediaBox [0 0 612 792] /Resources << %s >> /Contents %d 0 R", tree, res, contentNums[i])})
	}
	for i, n := range contentNums {
		content := "q 0 0 1 rg 0 0 10 10 re f Q"
		if o.Text != "" && i+1 == textPage {
			content = text
			if form != 0 {
				content = "q /X1 Do Q"
			}
		}
		data, filter := stream(content)
		obj := pdfObj{num: n, dict: filter, stream: data}
		if i+1 == textPage {
			if length != 0 {
				obj.length = fmt.Sprintf("%d 0 R", length)
			}
			if o.WrongLength {
				obj.length = fmt.Sprint(len(data) + 5)
			}
		}
		objs = append(objs, obj)
	}
	objs = append(objs, pdfObj{num: font, dict: "/Type /Font /Subtype /Type1 /BaseFont /Helvetica"})
	if form != 0 {
		data, filter := stream(text)
		objs = append(objs, pdfObj{num: form, dict: fmt.Sprintf(
			"/Type /XObject /Subtype /Form /BBox [0 0 612 792] /Resources << /Font << /F1 %d 0 R >> >> %s", font, filter), stream: data})
	}
	if info != 0 {
		objs = append(objs, pdfObj{num: info, dict: hexDict(o.Info)})
	}
	if meta != 0 {
		objs = append(objs, pdfObj{num: meta, dict: "/Type /Metadata /Subtype /XML", stream: []byte(o.XMP)})
	}
	if enc != 0 {
		e := "/Filter /EBX_HANDLER /V 2 /ADEPT_LICENSE (generated)"
		if o.Encrypt == "Standard" {
			e = "/Filter /Standard /V 2 /R 3 /Length 128 /P -1340 /O <" + strings.Repeat("ab", 32) + "> /U <" + strings.Repeat("cd", 32) + ">"
		}
		objs = append(objs, pdfObj{num: enc, dict: e})
	}
	if length != 0 {
		textData, _ := stream(text)
		if form != 0 {
			textData, _ = stream("q /X1 Do Q")
		}
		objs = append(objs, pdfObj{num: length, dict: fmt.Sprint(len(textData))})
	}

	trailer := ""
	if !o.NoRoot {
		trailer += fmt.Sprintf(" /Root %d 0 R", catalog)
	}
	if info != 0 {
		trailer += fmt.Sprintf(" /Info %d 0 R", info)
	}
	if enc != 0 {
		trailer += fmt.Sprintf(" /Encrypt %d 0 R /ID [<%s> <%s>]", enc, strings.Repeat("01", 16), strings.Repeat("01", 16))
	}

	w := &pdfWriter{offsets: map[int]int{}, member: map[int][2]int{}}
	if o.BreakOffsets {
		w.shift = 7
	}
	w.buf.WriteString("%PDF-1.7\n%\xe2\xe3\xcf\xd3\n")
	var members []pdfObj
	for _, obj := range objs {
		if o.ObjectStreams && obj.stream == nil && obj.num != enc {
			members = append(members, obj)
			continue
		}
		w.object(obj)
	}
	if members != nil {
		stm := alloc()
		var header, body strings.Builder
		for i, m := range members {
			fmt.Fprintf(&header, "%d %d ", m.num, body.Len())
			body.WriteString(value(m.dict) + "\n")
			w.member[m.num] = [2]int{stm, i}
		}
		data := header.String() + body.String()
		w.object(pdfObj{num: stm, dict: fmt.Sprintf("/Type /ObjStm /N %d /First %d /Filter /FlateDecode", len(members), header.Len()), stream: deflate([]byte(data))})
	}
	size := next
	switch {
	case o.XRefStream:
		w.xrefStream(alloc(), trailer, o.PrevLoop, o.NoStartxref)
	default:
		extra := ""
		if o.Hybrid && members != nil {
			stmXref := alloc()
			size = next
			extra = fmt.Sprintf(" /XRefStm %d", w.buf.Len())
			w.memberStream(stmXref, size)
		}
		w.table(size, trailer+extra, o.PrevLoop, o.NoStartxref)
	}
	if o.Update != nil {
		prev := w.lastXref
		w.object(pdfObj{num: info, dict: hexDict(o.Update)})
		off := w.buf.Len()
		fmt.Fprintf(&w.buf, "xref\n0 1\n0000000000 65535 f \n%d 1\n%010d 00000 n \n", info, w.offsets[info])
		fmt.Fprintf(&w.buf, "trailer\n<< /Size %d%s /Prev %d >>\nstartxref\n%d\n%%%%EOF\n", next, trailer, prev, off)
	}
	out := w.buf.Bytes()
	if o.CRLFShift {
		out = bytes.ReplaceAll(out, []byte("\n"), []byte("\r\n"))
	}
	return out
}

type pdfWriter struct {
	buf      bytes.Buffer
	offsets  map[int]int
	member   map[int][2]int // object → object stream and index
	shift    int
	lastXref int
}

func (w *pdfWriter) object(o pdfObj) {
	w.offsets[o.num] = w.buf.Len()
	if o.stream == nil {
		fmt.Fprintf(&w.buf, "%d 0 obj\n%s\nendobj\n", o.num, value(o.dict))
		return
	}
	length := o.length
	if length == "" {
		length = fmt.Sprint(len(o.stream))
	}
	fmt.Fprintf(&w.buf, "%d 0 obj\n<< %s /Length %s >>\nstream\n", o.num, o.dict, length)
	w.buf.Write(o.stream)
	w.buf.WriteString("\nendstream\nendobj\n")
}

func (w *pdfWriter) table(size int, trailer string, prevLoop, noStartxref bool) {
	off := w.buf.Len()
	w.lastXref = off
	fmt.Fprintf(&w.buf, "xref\n0 %d\n0000000000 65535 f \n", size)
	for num := 1; num < size; num++ {
		if o, ok := w.offsets[num]; ok {
			fmt.Fprintf(&w.buf, "%010d 00000 n \n", o+w.shift)
		} else {
			w.buf.WriteString("0000000000 65535 f \n")
		}
	}
	if prevLoop {
		trailer += fmt.Sprintf(" /Prev %d", off)
	}
	fmt.Fprintf(&w.buf, "trailer\n<< /Size %d%s >>\n", size, trailer)
	if !noStartxref {
		fmt.Fprintf(&w.buf, "startxref\n%d\n%%%%EOF\n", off)
	}
}

// xrefStream writes every object as an xref stream with /W [1 3 1] and the
// PNG Up predictor.
func (w *pdfWriter) xrefStream(num int, trailer string, prevLoop, noStartxref bool) {
	off := w.buf.Len()
	w.lastXref = off
	w.offsets[num] = off
	size := num + 1
	rows := make([][]byte, size)
	for n := range rows {
		row := []byte{0, 0, 0, 0, 0}
		if o, ok := w.offsets[n]; ok {
			o += w.shift
			row = []byte{1, byte(o >> 16), byte(o >> 8), byte(o), 0} //nolint:gosec // offsets of small test files fit in 3 bytes
		} else if m, ok := w.member[n]; ok {
			row = []byte{2, byte(m[0] >> 16), byte(m[0] >> 8), byte(m[0]), byte(m[1])} //nolint:gosec // offsets of small test files fit in 3 bytes
		}
		rows[n] = row
	}
	if prevLoop {
		trailer += fmt.Sprintf(" /Prev %d", off)
	}
	w.object(pdfObj{num: num, dict: fmt.Sprintf("/Type /XRef /Size %d /W [1 3 1]%s /Filter /FlateDecode /DecodeParms << /Predictor 12 /Columns 5 >>", size, trailer),
		stream: deflate(pngUp(rows))})
	if !noStartxref {
		fmt.Fprintf(&w.buf, "startxref\n%d\n%%%%EOF\n", off)
	}
}

// memberStream writes the xref stream a hybrid file's /XRefStm points at:
// only the object stream's members.
func (w *pdfWriter) memberStream(num, size int) {
	nums := make([]int, 0, len(w.member))
	for n := range w.member {
		nums = append(nums, n)
	}
	slices.Sort(nums)
	var index strings.Builder
	var rows [][]byte
	for _, n := range nums {
		m := w.member[n]
		fmt.Fprintf(&index, "%d 1 ", n)
		rows = append(rows, []byte{2, byte(m[0] >> 16), byte(m[0] >> 8), byte(m[0]), byte(m[1])}) //nolint:gosec // small test files
	}
	w.object(pdfObj{num: num, dict: fmt.Sprintf("/Type /XRef /Size %d /W [1 3 1] /Index [%s] /Filter /FlateDecode /DecodeParms << /Predictor 12 /Columns 5 >>", size, index.String()),
		stream: deflate(pngUp(rows))})
}

// value writes dictionary entries as a dictionary, and anything else as is.
func value(dict string) string {
	if dict == "" || strings.HasPrefix(dict, "/") {
		return "<< " + dict + " >>"
	}
	return dict
}

func pngUp(rows [][]byte) []byte {
	var out []byte
	prev := make([]byte, 5)
	for _, row := range rows {
		out = append(out, 2)
		for i, b := range row {
			out = append(out, b-prev[i])
		}
		prev = row
	}
	return out
}

func deflate(b []byte) []byte {
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	_, _ = zw.Write(b)
	_ = zw.Close()
	return buf.Bytes()
}

func refs(nums []int) string {
	parts := make([]string, len(nums))
	for i, n := range nums {
		parts[i] = fmt.Sprintf("%d 0 R", n)
	}
	return strings.Join(parts, " ")
}

func hexDict(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "/%s <%s> ", k, hex.EncodeToString([]byte(m[k])))
	}
	return strings.TrimSpace(b.String())
}

var escapePDF = strings.NewReplacer(`\`, `\\`, "(", `\(`, ")", `\)`).Replace
