#!/usr/bin/env bash
# Self-contained test for leak-check.sh: temporary trees with one file per
# pattern, an allowlisted hit, and a clean tree.
#
#   scripts/leak-check_test.sh
set -euo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
SCRIPT="$HERE/leak-check.sh"
ROOT=$(mktemp -d)
trap 'rm -rf "$ROOT"' EXIT
FAILS=0

check() {
  if [ "$2" = yes ]; then echo "ok: $1"; else echo "FAIL: $1"; FAILS=$((FAILS + 1)); fi
}
yes_if() { if "$@"; then echo yes; else echo no; fi; }
lacks() { ! grep -qiE -e "$1" <<<"$2"; } # ERE TEXT

# The strings are built in pieces so this file holds none of them whole.
addr=192.168
tail=ts.net
mnt=/mnt
server=felix
service=her
ten=10
sev=172
cg=100.101

# One file per pattern; each private string sits on line 2.
t=$ROOT/leaky
mkdir -p "$t/docs" "$t/.git" "$t/.claude"
printf 'intro\nthe proxy at %s.7.20 answers\n' "$addr" >"$t/docs/address.md"
printf 'intro\nopen http://box.example.%s/\n' "$tail" >"$t/docs/tailnet.md"
printf 'intro\ndumps go to %s/user/appdata\n' "$mnt" >"$t/docs/path.md"
printf 'intro\nruns on %sServer\n' "$server" >"$t/docs/server.md"
printf 'intro\nthe %smes agent\n' "$service" >"$t/docs/service.md"
printf 'intro\nten: %s.4.5.6 and %s.20.1.2\n' "$ten" "$sev" >"$t/docs/ranges.md"
printf 'intro\nnode at %s.7.8 on the %s\n' "$cg" "tail""net" >"$t/docs/cgnat.md"
printf 'intro\nbacked up to %s/disks/ext\n' "$mnt" >"$t/docs/disks.md"
printf 'intro\nboth %s.1.2 and %sSERVER here\n' "$addr" "${server^^}" >"$t/docs/two.md"
printf 'intro\nproxy %s.0.3\n' "$addr" >"$t/docs/we:ird.md"
printf '\xe9t\xe9\nproxy %s.0.4\n' "$addr" >"$t/docs/latin1.md"
mkdir -p "$t/docs/${server}server-notes"
printf 'fine\n' >"$t/docs/${server}server-notes/readme.md"
printf 'the range 100.64.0.0/10 is refused\n' >"$t/docs/range-doc.md"
# Not scanned: .git and .claude.
printf '%s.1.1\n' "$addr" >"$t/.git/config"
printf '%s.1.1\n' "$addr" >"$t/.claude/notes"
# Not private: documentation ranges, versions, a public address.
printf 'see 192.0.2.10, 198.51.100.7 and 203.0.113.9; v10.1 and 1.10.2.3; 8.8.8.8\n' >"$t/docs/fine.md"

set +e
out=$("$SCRIPT" "$t" 2>&1)
rc=$?
set -e
check "a leaky tree exits 1" "$(yes_if test "$rc" = 1)"
for want in docs/address.md:2:private-address docs/tailnet.md:2:tailnet docs/path.md:2:server-path \
  docs/server.md:2:server-name docs/service.md:2:service-name docs/ranges.md:2:private-address \
  docs/cgnat.md:2:tailnet-address docs/disks.md:2:server-path docs/two.md:2:private-address \
  docs/two.md:2:server-name docs/we:ird.md:2:private-address docs/latin1.md:2:private-address \
  "docs/${server}server-notes:0:server-name" "docs/${server}server-notes/readme.md:0:server-name"; do
  check "reports $want" "$(yes_if grep -qxF "$want" <<<"$out")"
done
check "one line per hit, nothing else" "$(yes_if test "$(wc -l <<<"$out")" = 14)"
check "the CGNAT range written as 100.64.0.0/10 is not a hit" "$(yes_if lacks '^docs/range-doc' "$out")"
check "never prints the matched text" "$(yes_if lacks "$addr|$tail|$mnt/user|$mnt/disks|${service}mes|10\.4\.5\.6|172\.20|$cg" "$out")"
check "skips .git, .claude, documentation ranges and versions" "$(yes_if lacks '^(\.git|\.claude|docs/fine\.md)' "$out")"

# An allowlisted hit passes; the others still fail.
a=$ROOT/allowed
mkdir -p "$a/scripts" "$a/docs"
printf 'intro\nthe %smes agent\n' "$service" >"$a/docs/service.md"
printf 'intro\nruns on %sServer\n' "$server" >"$a/docs/server.md"
printf '# A deliberate mention, for the test.\ndocs/service.md:service-name\n' >"$a/scripts/leak-check.allow"
set +e
out=$("$SCRIPT" "$a" 2>&1)
rc=$?
set -e
check "an allowlisted hit is not reported" "$(yes_if lacks '^docs/service\.md' "$out")"
check "a hit the allowlist doesn't name still fails" "$(yes_if test "$rc" = 1 -a "$out" = docs/server.md:2:server-name)"
rm "$a/docs/server.md"
set +e
out=$("$SCRIPT" "$a" 2>&1)
rc=$?
set -e
check "a tree whose only hit is allowlisted exits 0 and prints nothing" "$(yes_if test "$rc" = 0 -a -z "$out")"

# An allowlist with only comments allows nothing.
e=$ROOT/empty-allow
mkdir -p "$e/scripts" "$e/docs"
printf 'intro\nruns on %sServer\n' "$server" >"$e/docs/server.md"
printf '# nothing is allowed\n\n' >"$e/scripts/leak-check.allow"
set +e
out=$("$SCRIPT" "$e" 2>&1)
rc=$?
set -e
check "an allowlist of comments only still reports the hit" "$(yes_if test "$rc" = 1 -a "$out" = docs/server.md:2:server-name)"

# An indented allowlist entry still counts.
i=$ROOT/indented
mkdir -p "$i/scripts" "$i/docs"
printf 'the %smes agent\n' "$service" >"$i/docs/service.md"
printf '  docs/service.md:service-name  # indented\n' >"$i/scripts/leak-check.allow"
set +e
out=$("$SCRIPT" "$i" 2>&1)
rc=$?
set -e
check "an indented allowlist entry counts" "$(yes_if test "$rc" = 0 -a -z "$out")"

# A path that isn't a directory is an error (2), not a leak (1).
set +e
"$SCRIPT" "$ROOT/no-such-dir" >/dev/null 2>&1
rc=$?
set -e
check "a missing path exits 2" "$(yes_if test "$rc" = 2)"

# A file grep can't read is an error, not a clean tree (root reads anything).
if [ "$(id -u)" != 0 ]; then
  u=$ROOT/unreadable
  mkdir -p "$u/docs"
  printf 'x\n' >"$u/docs/secret.md"
  chmod 000 "$u/docs/secret.md"
  set +e
  "$SCRIPT" "$u" >/dev/null 2>&1
  rc=$?
  set -e
  chmod 600 "$u/docs/secret.md"
  check "an unreadable file exits 2" "$(yes_if test "$rc" = 2)"
fi

# A clean tree.
c=$ROOT/clean
mkdir -p "$c/docs"
printf 'nothing private here, 192.0.2.10\n' >"$c/docs/readme.md"
set +e
out=$("$SCRIPT" "$c" 2>&1)
rc=$?
set -e
check "a clean tree exits 0 and prints nothing" "$(yes_if test "$rc" = 0 -a -z "$out")"

if [ "$FAILS" -ne 0 ]; then
  echo "leak-check_test: $FAILS failed" >&2
  exit 1
fi
echo "leak-check_test: OK"
