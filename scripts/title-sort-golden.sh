#!/bin/sh
# Regenerates internal/findings/testdata/title_sort_golden.json from the
# pinned Calibre in test/calibre, so the Go port is checked against Calibre
# itself rather than against a reading of its source.
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)
docker build -q -t bookwarden-test-calibre:9.16.0 "$root/test/calibre" >/dev/null
docker run --rm --network none -v "$root/test/calibre/title_sort_golden.py:/golden.py:ro" \
  --entrypoint /opt/calibre/calibre-debug bookwarden-test-calibre:9.16.0 /golden.py \
  > "$root/internal/findings/testdata/title_sort_golden.json"
echo "title-sort golden: $(grep -c '"title"' "$root/internal/findings/testdata/title_sort_golden.json") cases"
