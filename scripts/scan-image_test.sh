#!/usr/bin/env bash
# Self-contained test for scan-image.sh: a stub docker logs its argv. Nothing is scanned.
#
#   scripts/scan-image_test.sh
set -euo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
SCRIPT="$HERE/scan-image.sh"
D=$(mktemp -d)
trap 'rm -rf "$D"' EXIT
FAILS=0

mkdir -p "$D/bin"
cat >"$D/bin/docker" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$@" >"$STUB_LOG"
exit "${STUB_RC:-0}"
STUB
chmod +x "$D/bin/docker"

check() { # check <description> <status>
  if [ "$2" = 0 ]; then
    echo "ok   - $1"
  else
    echo "FAIL - $1"
    FAILS=$((FAILS + 1))
  fi
}

# The exact call CI made before the move, one argument per line, plus the 15-minute DB
# download timeout.
want=$(
  cat <<'ARGS'
run
--rm
-v
/var/run/docker.sock:/var/run/docker.sock
-v
bookwarden-trivy-cache:/root/.cache/
aquasec/trivy:0.76.2@sha256:8f6bb947dbc528902e11f2f5be52f7b519f6a39d5d148f41166f2040499768fb
image
--timeout
15m
--quiet
--exit-code
1
--severity
HIGH,CRITICAL
--ignore-unfixed
--scanners
vuln,secret
bookwarden:ci-42
ARGS
)

set +e
PATH="$D/bin:$PATH" STUB_LOG="$D/log" "$SCRIPT" bookwarden:ci-42 >/dev/null 2>&1
rc=$?
set -e
check "exit 0 when Trivy passes" "$([ "$rc" = 0 ] && echo 0 || echo 1)"
check "the exact Trivy arguments" "$([ "$(cat "$D/log")" = "$want" ] && echo 0 || echo 1)"
[ "$(cat "$D/log")" = "$want" ] || diff <(echo "$want") "$D/log" || true

set +e
PATH="$D/bin:$PATH" STUB_LOG="$D/log" STUB_RC=1 "$SCRIPT" bookwarden:ci-42 >/dev/null 2>&1
rc=$?
set -e
check "a finding fails the script" "$([ "$rc" = 1 ] && echo 0 || echo 1)"

set +e
PATH="$D/bin:$PATH" STUB_LOG="$D/log" "$SCRIPT" >/dev/null 2>&1
rc=$?
set -e
check "no image: exit 2" "$([ "$rc" = 2 ] && echo 0 || echo 1)"

if [ "$FAILS" = 0 ]; then
  echo "all scan-image checks passed"
else
  echo "$FAILS check(s) failed" >&2
  exit 1
fi
