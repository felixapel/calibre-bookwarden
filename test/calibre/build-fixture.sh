#!/bin/sh
# Creates a small synthetic Calibre library. Every title, author and ISBN is
# invented; the ISBNs have valid checksums.
set -eu
lib=$1
src=$(mktemp -d)
trap 'rm -rf "$src"' EXIT

epub() { # name title author isbn language text
  printf '%s\n' "$6" >"$src/$1.txt"
  ebook-convert "$src/$1.txt" "$src/$1.epub" \
    --title "$2" --authors "$3" --isbn "$4" --language "$5" \
    --no-default-epub-cover >/dev/null
}
added_id() { sed -n 's/^Added book ids: \([0-9][0-9]*\)$/\1/p'; }

# Generated covers (ADR-023): a grey left-to-right gradient, a flat grey, or a
# sawtooth of four left-to-right ramps (enough detail to compare, ADR-024).
cat >"$src/cover.py" <<'EOF'
import struct, sys, zlib

path, w, h, kind = sys.argv[-1].split(":")
w, h = int(w), int(h)
def chunk(tag, data):
    body = tag + data
    return struct.pack(">I", len(data)) + body + struct.pack(">I", zlib.crc32(body) & 0xFFFFFFFF)
if kind == "gradient":
    row = bytes(x * 255 // (w - 1) for x in range(w))
elif kind == "saw":
    row = bytes(x * 4 * 255 // w % 256 for x in range(w))
else:
    row = bytes([128]) * w
rows = b"".join(b"\x00" + row for _ in range(h))
with open(path, "wb") as f:
    f.write(b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack(">IIBBBBB", w, h, 8, 0, 0, 0, 0))
            + chunk(b"IDAT", zlib.compress(rows)) + chunk(b"IEND", b""))
EOF
cover() { calibre-debug "$src/cover.py" -- "$src/$1.png:$2:$3:$4" >/dev/null; }
cover good 800 1200 gradient
cover small 300 450 gradient
cover flat 600 900 flat
cover saw 800 1200 saw
cover sawsmall 400 600 saw

epub clockwork "The Clockwork Garden" "Ada Example" 9780000000002 en \
  "A garden mechanism is inspected one gear at a time."
epub signals "Lighthouse Signals" "Bea Sample" 9780000000019 en \
  "A lighthouse log names the exact edition inside the file."
epub tides "Binary Tides" "Cy Placeholder" 9780000000026 en \
  "Two formats carry the same generated evidence."
epub vanished "Vanished File" "Eve Absent" 9780000000033 en \
  "This file is removed from disk after Calibre lists it."
ebook-convert "$src/tides.epub" "$src/tides.azw3" >/dev/null
ebook-convert "$src/tides.epub" "$src/tides.mobi" >/dev/null

# Book 1: Calibre agrees with the file, and its cover is good.
calibredb add "$src/clockwork.epub" --cover "$src/good.png" --with-library "$lib" >/dev/null
# Book 2: Calibre's title and language disagree with the file, and its author
# is the placeholder Calibre uses for none (so a fix fills it; ADR-035); its
# cover is 300 × 450 px.
calibredb add "$src/signals.epub" --title "Incorrect Catalogue Title" \
  --authors "Unknown" --languages fr --cover "$src/small.png" --with-library "$lib" >/dev/null
# Book 3: two formats of one book, and a flat grey cover.
id=$(calibredb add "$src/tides.epub" --cover "$src/flat.png" --with-library "$lib" | added_id)
calibredb add_format "$id" "$src/tides.azw3" --with-library "$lib"
calibredb add_format "$id" "$src/tides.mobi" --with-library "$lib"
# Book 4: a record without any format.
calibredb add --empty --title "No Formats Here" --authors "Dee Nobody" \
  --with-library "$lib" >/dev/null
# Book 5: listed in Calibre, but its file is gone from disk.
id=$(calibredb add "$src/vanished.epub" --with-library "$lib" | added_id)
find "$lib" -path "*($id)/*.epub" -delete
# Book 6: a PDF from a mainstream producer, with an Info title and a text layer.
cat >"$src/paper.ps" <<'EOF'
%!PS
[ /Title (Paper Trail) /Author (Fay Sample) /DOCINFO pdfmark
/Helvetica findfont 14 scalefont setfont
72 700 moveto (A tiny generated book.) show
72 680 moveto (ISBN 978-0-00-000004-0) show
showpage
EOF
gs -q -dBATCH -dNOPAUSE -dSAFER -sDEVICE=pdfwrite \
  -sOutputFile="$src/paper.pdf" "$src/paper.ps"
calibredb add "$src/paper.pdf" --title "Paper Trail" --authors "Fay Sample" \
  --isbn 9780000000040 --with-library "$lib" >/dev/null
# Book 7: a comic archive of three small grey PNG pages.
cat >"$src/panels.py" <<'EOF'
import struct, sys, zipfile, zlib

def png(w, h, shade):
    def chunk(kind, data):
        body = kind + data
        return struct.pack(">I", len(data)) + body + struct.pack(">I", zlib.crc32(body) & 0xFFFFFFFF)
    rows = b"".join(b"\x00" + bytes([shade]) * w for _ in range(h))
    return (b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack(">IIBBBBB", w, h, 8, 0, 0, 0, 0))
            + chunk(b"IDAT", zlib.compress(rows)) + chunk(b"IEND", b""))

with zipfile.ZipFile(sys.argv[-1], "w", zipfile.ZIP_DEFLATED) as z:
    for i in range(1, 4):
        z.writestr("page-%d.png" % i, png(40, 60, 60 * i))
EOF
calibre-debug "$src/panels.py" -- "$src/panels.cbz" >/dev/null
calibredb add "$src/panels.cbz" --title "Panel Count" --authors "Gil Drawn" \
  --with-library "$lib" >/dev/null
# Book 8: a scanned-style PDF whose ISBN is drawn as outlines, so it has no
# text layer and only OCR can read it. It is retitled after adding, and
# Calibre keeps the old title sort.
cat >"$src/ledger.ps" <<'EOF'
%!PS
/Helvetica findfont 28 scalefont setfont
72 700 moveto (ISBN 978-0-00-000005-7) show
showpage
EOF
gs -q -dBATCH -dNOPAUSE -dSAFER -dNoOutputFonts -sDEVICE=pdfwrite \
  -sOutputFile="$src/ledger.pdf" "$src/ledger.ps"
id=$(calibredb add "$src/ledger.pdf" --title "Draft Ledger" --authors "Hal Scanner" \
  --languages eng --with-library "$lib" | added_id)
calibredb set_metadata "$id" --field "title:The Scanned Ledger" --with-library "$lib" >/dev/null
# Book 9: book 1's file added again (ADR-024): the same file, ISBN, title and
# authors, with a sawtooth cover.
calibredb add "$src/clockwork.epub" --duplicates --cover "$src/saw.png" --with-library "$lib" >/dev/null
# Book 10: a record without formats whose cover is book 9's at half the size.
calibredb add --empty --title "Sawtooth Proof" --authors "Ivy Example" \
  --cover "$src/sawsmall.png" --with-library "$lib" >/dev/null
