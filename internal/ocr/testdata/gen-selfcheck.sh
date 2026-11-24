#!/bin/sh
# Regenerates selfcheck.pdf: one page whose ISBN is drawn as outlines, so the
# PDF has no text layer and only OCR can read it. Ghostscript comes from the
# pinned test Calibre image, so the bytes don't depend on the host.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
docker run --rm -i --user "$(id -u):$(id -g)" -v "$here:/out" --entrypoint gs \
  bookwarden-test-calibre:9.16.0 \
  -q -dBATCH -dNOPAUSE -dSAFER -dNoOutputFonts -sDEVICE=pdfwrite \
  -sOutputFile=/out/selfcheck.pdf - <<'PS'
%!PS
/Helvetica findfont 28 scalefont setfont
72 700 moveto (ISBN 978-0-00-000006-4) show
showpage
PS
