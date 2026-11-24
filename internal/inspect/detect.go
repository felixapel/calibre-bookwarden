package inspect

import (
	"bytes"
	"encoding/binary"
	"io"
)

// Detect names what the bytes are: pdf, epub, zip, mobi, palmdoc, rar, jpeg,
// png, gif, webp or unknown.
func Detect(r io.ReaderAt, size int64) string {
	head := make([]byte, min(max(size, 0), 1024))
	n, _ := r.ReadAt(head, 0)
	head = head[:n]
	switch {
	case bytes.HasPrefix(head, []byte("%PDF-")):
		return "pdf"
	case bytes.HasPrefix(head, []byte("PK\x03\x04")):
		if epubMimetype(head) {
			return "epub"
		}
		return "zip"
	case bytes.HasPrefix(head, []byte("PK\x05\x06")):
		return "zip"
	case len(head) >= 68 && string(head[60:68]) == "BOOKMOBI":
		return "mobi"
	case len(head) >= 68 && string(head[60:68]) == "TEXtREAd":
		return "palmdoc"
	case bytes.HasPrefix(head, []byte("Rar!\x1a\x07")):
		return "rar"
	case bytes.HasPrefix(head, []byte("\xff\xd8\xff")):
		return "jpeg"
	case bytes.HasPrefix(head, []byte("\x89PNG\r\n\x1a\n")):
		return "png"
	case bytes.HasPrefix(head, []byte("GIF87a")), bytes.HasPrefix(head, []byte("GIF89a")):
		return "gif"
	case len(head) >= 12 && string(head[:4]) == "RIFF" && string(head[8:12]) == "WEBP":
		return "webp"
	case bytes.Contains(head, []byte("%PDF-")):
		return "pdf" // readers accept junk before the header
	}
	return "unknown"
}

// epubMimetype reports whether a zip's first entry is the stored EPUB
// mimetype file.
func epubMimetype(head []byte) bool {
	if len(head) < 30 {
		return false
	}
	method := binary.LittleEndian.Uint16(head[8:])
	nameLen := int(binary.LittleEndian.Uint16(head[26:]))
	extraLen := int(binary.LittleEndian.Uint16(head[28:]))
	data := 30 + nameLen + extraLen
	return method == 0 && string(head[30:min(30+nameLen, len(head))]) == "mimetype" &&
		data <= len(head) && bytes.HasPrefix(head[data:], []byte("application/epub+zip"))
}
