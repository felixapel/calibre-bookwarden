#!/bin/sh
# Fuzzes every parser briefly: a smoke test that they survive mutated input,
# not a substitute for long fuzzing runs. Minimizing a new input is capped,
# so it can't eat a target's whole time. A failing target doesn't stop the
# others; the script fails at the end and names every failed target.
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)
fuzztime=${FUZZTIME:-15s}
cd "$root"

# Every target, one per line: package directory, then the target's name.
targets='
./internal/library/ FuzzReadExport
./internal/inspect/ FuzzEPUB
./internal/inspect/ FuzzHTMLBodyText
./internal/inspect/ FuzzNavLandmarks
./internal/inspect/ FuzzCBZ
./internal/inspect/ FuzzMOBI
./internal/inspect/ FuzzPDF
./internal/inspect/ FuzzCover
./internal/inspect/ FuzzEPUBExcerpt
./internal/inspect/ FuzzExcerptText
./internal/duplicates/ FuzzCoverGroups
./internal/findings/ FuzzExtractISBNs
./internal/findings/ FuzzScanISBNs
./internal/findings/ FuzzTitleNoise
./internal/findings/ FuzzJoinedAuthor
./internal/findings/ FuzzDateKey
./internal/findings/ FuzzCanonicalISBN
./internal/seriesindex/ FuzzSeriesIndexNormalize
./internal/sources/ FuzzMARC
./internal/sources/ FuzzOpenLibrary
./internal/decide/ FuzzDecide
./internal/decide/ FuzzCanonTitleV7
./internal/decide/ FuzzNameKeyV7
./internal/extract/ FuzzNormalizeOffsets
./internal/extract/ FuzzVerify
./internal/extract/ FuzzParse
./internal/extract/ FuzzAgree
./internal/extraction/ FuzzExtractSeals
'

# Self-check, both ways: every fuzz target in the code is listed with its
# package, and every listed target exists in that package. (go test -fuzz
# with a name that matches nothing passes without fuzzing.)
found=$(grep -rEo --include='*_test.go' '^func Fuzz[A-Za-z0-9_]*\([A-Za-z_][A-Za-z0-9_]* \*testing\.F\)' internal cmd |
  sed -E 's#^(.*)/[^/]*_test\.go:func (Fuzz[A-Za-z0-9_]*).*#./\1/ \2#' | sort -u)
if [ -z "$found" ]; then
  echo "fuzz: could not list the fuzz targets" >&2
  exit 1
fi
listed=$(printf '%s\n' "$targets" | grep -v '^$' | sort -u)
missing=$(printf '%s\n' "$found" | grep -vxF "$listed" || true)
stale=$(printf '%s\n' "$listed" | grep -vxF "$found" || true)
if [ -n "$missing" ] || [ -n "$stale" ]; then
  [ -z "$missing" ] || printf 'fuzz: targets missing from %s:\n%s\n' "$0" "$missing" >&2
  [ -z "$stale" ] || printf 'fuzz: listed targets that do not exist:\n%s\n' "$stale" >&2
  exit 1
fi

failed=
run() { # package target
  if ! go test -run '^$' -fuzz "^$2\$" -fuzztime "$fuzztime" -fuzzminimizetime 2s "$1"; then
    failed="$failed $2"
  fi
}
while read -r pkg name; do
  if [ -n "$pkg" ]; then
    run "$pkg" "$name"
  fi
done <<EOF_TARGETS
$targets
EOF_TARGETS

if [ -n "$failed" ]; then
  echo "fuzz: FAILED:$failed" >&2
  exit 1
fi
echo "fuzz: OK"
