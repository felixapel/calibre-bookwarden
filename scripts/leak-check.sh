#!/usr/bin/env bash
# Searches a tree for things that must not be public: private (RFC 1918) and
# tailnet addresses, tailnet host names, Unraid disk and share paths, and the
# names of the maintainer's server, services and scripts. Contents and path
# names are both searched. Prints file:line:pattern for each hit (line 0 for
# a path name), never the matched text. Exits 1 on any hit, 2 on an error.
#
#   scripts/leak-check.sh [path]     (default: this repository)
#
# .git and .claude are not searched, nor files grep takes for binary.
# <path>/scripts/leak-check.allow lists deliberate uses as "file:pattern"
# lines (file relative to <path>), each with a comment saying why; "#"
# starts a comment.
set -euo pipefail
export LC_ALL=C
die() {
  echo "leak-check: $1" >&2
  exit 2
}
root=${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}
[ -d "$root" ] || die "$root is not a directory"
cd "$root"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# Each pattern is written in pieces so that this file doesn't match itself.
# Documentation ranges (192.0.2.0/24, 198.51.100.0/24, 203.0.113.0/24) and
# public addresses are not private, so they don't match.
oct='[0-9]{1,3}'
names=(private-address tailnet-address tailnet server-path server-name service-name)
declare -A re strip
re[private-address]='(^|[^0-9.])(1''0\.'"$oct"'\.'"$oct"'\.[0-9]|17''2\.(1[6-9]|2[0-9]|3[01])\.'"$oct"'\.[0-9]|19''2\.168\.[0-9])'
# Tailscale hands out 100.64.0.0/10 and an IPv6 /48 of its own. The range
# itself, written as 100.64.0.0/10, is documentation, not an address.
re[tailnet-address]='(^|[^0-9.])10''0\.(6[4-9]|[7-9][0-9]|1[01][0-9]|12[0-7])\.'"$oct"'\.[0-9]|fd7a'':115c'
strip[tailnet-address]='10''0\.64\.0\.0/10'
re[tailnet]='\.ts''\.net'
re[server-path]='/mnt''/(user|disks|cache|disk[0-9]+|remotes|addons)([^a-z0-9]|$)'
re[server-name]='felix''server'
re[service-name]='her''mes|hyper''qwen|anti''gravity|dupli''cati([^o]|$)|pg-''nightly|cuaren''tena|agentic-''workflows|net-''guardian|bookwarden-''tunnel'

# Path names: every file and directory below the root.
find . \( -name .git -o -name .claude \) -prune -o -mindepth 1 -print0 | sed -z 's|^\./||' >"$tmp/paths"
for name in "${names[@]}"; do
  while IFS= read -r -d '' p; do
    if [ -n "${strip[$name]:-}" ]; then
      p2=$(printf '%s' "$p" | sed -E "s#${strip[$name]}##g")
      printf '%s' "$p2" | grep -qiE -e "${re[$name]}" || continue
    fi
    printf '%s:0:%s\n' "$p" "$name"
  done < <(grep -zaiE -e "${re[$name]}" "$tmp/paths" || true)
done >>"$tmp/hits"

# Contents: candidate files in one pass, then line numbers file by file, so
# a ":" in a file name can't confuse the output. grep exits 1 for no match
# and 2 for an error, which stops the check.
any=""
for name in "${names[@]}"; do any+="${any:+|}(${re[$name]})"; done
set +e
grep -rlIiEZ --exclude-dir=.git --exclude-dir=.claude -e "$any" . >"$tmp/files"
rc=$?
set -e
[ "$rc" -le 1 ] || die "grep could not read the tree (exit $rc)"
while IFS= read -r -d '' f; do
  f=${f#./}
  for name in "${names[@]}"; do
    set +e
    grep -niE -e "${re[$name]}" -- "$f" >"$tmp/lines"
    rc=$?
    set -e
    [ "$rc" -le 1 ] || die "grep could not read $f (exit $rc)"
    if [ -n "${strip[$name]:-}" ]; then
      sed -E "s#${strip[$name]}##g" "$tmp/lines" | grep -iE -e "^[0-9]+:.*(${re[$name]})" >"$tmp/kept" || true
      mv "$tmp/kept" "$tmp/lines"
    fi
    while IFS=: read -r n _; do
      printf '%s:%s:%s\n' "$f" "$n" "$name"
    done <"$tmp/lines"
  done
done <"$tmp/files" >>"$tmp/hits"

# The allowlist: "file:pattern"; the pattern is what follows the last colon,
# and a hit is "file:line:pattern".
allow=scripts/leak-check.allow
if [ -f "$allow" ]; then
  sed -e 's/#.*//' -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//' -e '/^$/d' "$allow" >"$tmp/allow"
  awk -v allow="$tmp/allow" '
    BEGIN { while ((getline line < allow) > 0) ok[line] = 1 }
    {
      n = split($0, a, ":")
      file = substr($0, 1, length($0) - length(a[n - 1]) - length(a[n]) - 2)
      if (!((file ":" a[n]) in ok)) print
    }' "$tmp/hits" >"$tmp/left"
  mv "$tmp/left" "$tmp/hits"
fi
# Two patterns on one line are two hits.
sort -u "$tmp/hits" >"$tmp/out"
cat "$tmp/out"
[ ! -s "$tmp/out" ]
