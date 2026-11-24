#!/usr/bin/env bash
# Self-contained test for release-sign.sh: a stub cosign goes first on PATH, logs its argv
# and fails on demand. Nothing is signed.
#
#   scripts/release-sign_test.sh
set -euo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
SCRIPT="$HERE/release-sign.sh"
ROOT=$(mktemp -d)
trap 'rm -rf "$ROOT"' EXIT

HEX=$(printf 'b%.0s' {1..64})
GITEA_REF="192.0.2.10:3010/felix/bookwarden@sha256:$HEX"
GHCR_REF="ghcr.io/felixapel/bookwarden@sha256:$HEX"
FAILS=0
RC=0
OUT=""
D=""

new_case() {
  D="$ROOT/$1"
  mkdir -p "$D/bin"
  cat >"$D/bin/cosign" <<'STUB'
#!/usr/bin/env bash
echo "cosign $*" >>"$STUB_LOG"
# A cosign that reads stdin must not eat the digests list.
[ "${STUB_READ_STDIN:-0}" = 1 ] && cat >/dev/null
case "$1 $*" in
  "signing-config "*)
    out=${*: -1}
    echo '{}' >"$out"
    ;;
  "sign "*) [ "${STUB_SIGN_FAIL:-0}" = 1 ] && exit 1 ;;
  "attest "*) [ "${STUB_ATTEST_FAIL:-0}" = 1 ] && exit 1 ;;
esac
exit 0
STUB
  chmod +x "$D/bin/cosign"
  : >"$D/log"
  printf '%s\n%s\n' "$GITEA_REF" "$GHCR_REF" >"$D/digests.txt"
  echo '{"spdxVersion":"SPDX-2.3"}' >"$D/sbom.spdx.json"
}

# run_sign [VAR=val ...]: runs the script with a key, a password and the HTTP registry set;
# a VAR= argument overrides one of them.
run_sign() {
  set +e
  OUT=$(env PATH="$D/bin:$PATH" STUB_LOG="$D/log" COSIGN_KEY=key-content COSIGN_PASSWORD=pw \
    BW_HTTP_REGISTRY=192.0.2.10:3010 "$@" "$SCRIPT" "$D/digests.txt" "$D/sbom.spdx.json" 2>&1 </dev/null)
  RC=$?
  set -e
}

check() { # check <description> <status>
  if [ "$2" = 0 ]; then
    echo "ok   - $1"
  else
    echo "FAIL - $1"
    FAILS=$((FAILS + 1))
  fi
}
yes_if() { if "$@"; then echo 0; else echo 1; fi; }
count() { grep -cE -- "$1" "$D/log" || true; }
not_logged() { ! grep -qE -- "$1" "$D/log"; }

new_case happy
run_sign
check "happy: exit 0" "$(yes_if test "$RC" = 0)"
[ "$RC" = 0 ] || echo "$OUT"
check "happy: one signing config" "$(yes_if test "$(count '^cosign signing-config create --out ')" = 1)"
check "happy: one sign and one attest per digest" "$(yes_if test "$(count '^cosign sign ')" = 2 -a "$(count '^cosign attest ')" = 2)"
check "happy: the Gitea digest is signed over HTTP" "$(yes_if test "$(count "^cosign sign .*--allow-http-registry $GITEA_REF\$")" = 1)"
check "happy: the Gitea attestation goes over HTTP" "$(yes_if test "$(count "^cosign attest .*--allow-http-registry $GITEA_REF\$")" = 1)"
check "happy: GHCR keeps TLS" "$(yes_if not_logged "allow-http-registry.*ghcr.io")"
check "happy: the GHCR digest is signed and attested exactly once" "$(yes_if test "$(count "^cosign sign .*$GHCR_REF\$")" = 1 -a "$(count "^cosign attest .*$GHCR_REF\$")" = 1)"
check "happy: the key comes from the environment" "$(yes_if test "$(count '--key env://COSIGN_KEY')" = 4)"
check "happy: the attestation is the SBOM as spdxjson" "$(yes_if test "$(count "--type spdxjson --predicate $D/sbom.spdx.json")" = 2)"
check "happy: every call uses the signing config" "$(yes_if test "$(count '--signing-config ')" = 4)"
check "happy: the key never reaches the log or the output" "$(yes_if not_logged 'key-content' && ! grep -q key-content <<<"$OUT")"

new_case stdin
run_sign STUB_READ_STDIN=1
check "a cosign that reads stdin still signs every digest" "$(yes_if test "$RC" = 0 -a "$(count '^cosign sign ')" = 2)"

new_case nohttp
run_sign BW_HTTP_REGISTRY=
check "without BW_HTTP_REGISTRY nothing goes over HTTP" "$(yes_if test "$RC" = 0 && not_logged 'allow-http-registry')"

new_case nonewline
printf '%s\n%s' "$GITEA_REF" "$GHCR_REF" >"$D/digests.txt"
run_sign
check "a last line without a newline is still signed" "$(yes_if test "$RC" = 0 -a "$(count "^cosign sign .*$GHCR_REF\$")" = 1)"

new_case prefix
printf '%s\n' "192.0.2.10:30100/felix/bookwarden@sha256:$HEX" >"$D/digests.txt"
run_sign
check "a registry that only starts with BW_HTTP_REGISTRY keeps TLS" "$(yes_if test "$RC" = 0 && not_logged 'allow-http-registry')"

new_case nokey
run_sign COSIGN_KEY=
check "no COSIGN_KEY: non-zero, nothing signed" "$(yes_if test "$RC" != 0 && not_logged '^cosign (sign|attest)')"

new_case nopw
run_sign COSIGN_PASSWORD=
check "no COSIGN_PASSWORD: non-zero, nothing signed" "$(yes_if test "$RC" != 0 && not_logged '^cosign (sign|attest)')"

new_case signfail
run_sign STUB_SIGN_FAIL=1
check "a failed sign: non-zero, stops at the first digest" "$(yes_if test "$RC" != 0 -a "$(count '^cosign sign ')" = 1)"

new_case attestfail
run_sign STUB_ATTEST_FAIL=1
check "a failed attest: non-zero" "$(yes_if test "$RC" != 0)"

new_case badref
printf '%s\n' "192.0.2.10:3010/felix/bookwarden:v2.0.0" >"$D/digests.txt"
run_sign
check "a reference without a digest: non-zero, nothing signed" "$(yes_if test "$RC" != 0 && not_logged '^cosign (sign|attest)')"

new_case empty
: >"$D/digests.txt"
run_sign
check "an empty digests file: non-zero" "$(yes_if test "$RC" != 0)"

new_case nosbom
rm "$D/sbom.spdx.json"
run_sign
check "a missing SBOM: non-zero, nothing signed" "$(yes_if test "$RC" != 0 && not_logged '^cosign (sign|attest)')"

new_case usage
set +e
OUT=$(env PATH="$D/bin:$PATH" STUB_LOG="$D/log" COSIGN_KEY=k COSIGN_PASSWORD=p "$SCRIPT" "$D/digests.txt" 2>&1)
RC=$?
set -e
check "one argument: exit 2" "$(yes_if test "$RC" = 2)"

if [ "$FAILS" = 0 ]; then
  echo "all release-sign checks passed"
else
  echo "$FAILS check(s) failed" >&2
  exit 1
fi
