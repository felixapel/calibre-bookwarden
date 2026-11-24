#!/bin/sh
# Re-records the public catalog answers in internal/sources/testdata from the
# live sources. Manual only: CI and unit tests never touch the network. The
# ISBNs are of well-known published books, not anything from a library.
set -eu
out=$(cd "$(dirname "$0")/.." && pwd)/internal/sources/testdata
ua='bookwarden/fixtures (+https://github.com/felixapel/calibre-bookwarden)'
get() { # url file
  curl --fail --silent --show-error --max-time 30 --proto '=https' -A "$ua" "$1" -o "$out/$2"
  sleep 1
}
get 'https://services.dnb.de/sru/dnb?version=1.1&operation=searchRetrieve&query=num%3D9783551551672&recordSchema=MARC21-xml&maximumRecords=10' dnb-9783551551672.xml
get 'https://services.dnb.de/sru/dnb?version=1.1&operation=searchRetrieve&query=num%3D9780000000002&recordSchema=MARC21-xml&maximumRecords=10' dnb-none.xml
get 'https://catalogo.bne.es/view/sru/34BNE_INST?version=1.2&operation=searchRetrieve&query=alma.isbn%3D9788478884452&recordSchema=marcxml&maximumRecords=10' bne-9788478884452.xml
get 'https://openlibrary.org/books/OL27958188M.json' ol-edition-OL27958188M.json
get 'https://openlibrary.org/works/OL82563W.json' ol-work-OL82563W.json
get 'https://openlibrary.org/authors/OL23919A.json' ol-author-OL23919A.json
echo "recorded: $(find "$out" -maxdepth 1 -type f | wc -l) files"
