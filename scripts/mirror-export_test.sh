#!/usr/bin/env bash
# Self-contained test for mirror-export.sh: a temporary repository with a
# bare "remote", tags on main, a stub git-cliff, and the real leak-check.sh.
# Nothing is pushed anywhere.
#
#   scripts/mirror-export_test.sh
set -euo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT=$(mktemp -d)
trap 'rm -rf "$ROOT"' EXIT
FAILS=0

check() {
  if [ "$2" = yes ]; then echo "ok: $1"; else echo "FAIL: $1"; FAILS=$((FAILS + 1)); fi
}
yes_if() { if "$@"; then echo yes; else echo no; fi; }
lacks() { ! grep -qE -e "$1" <<<"$2"; } # ERE TEXT

export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1
export BW_PUBLIC_NAME="Test Mirror"
export BW_PUBLIC_EMAIL="12345+test@users.noreply.github.com"
PRIVATE_EMAIL="someone@example.org"

# A stub git-cliff: the notes name the tag and the range it was given.
mkdir -p "$ROOT/bin"
cat >"$ROOT/bin/git-cliff" <<'STUB'
#!/usr/bin/env bash
tag=""
while [ $# -gt 0 ]; do
  case $1 in
    --tag) tag=$2; shift 2 ;;
    --config) shift 2 ;;
    *) range=$1; shift ;;
  esac
done
printf '## %s\n\nchanges in %s\n' "$tag" "$range"
if [ -n "${STUB_NOTES_LEAK:-}" ]; then printf 'fix: proxy at %s.7.20\n' "$STUB_NOTES_LEAK"; fi
STUB
chmod +x "$ROOT/bin/git-cliff"
# A git that refuses to push and logs any attempt.
REAL_GIT=$(command -v git)
cat >"$ROOT/bin/git" <<STUB
#!/usr/bin/env bash
for a in "\$@"; do
  if [ "\$a" = push ]; then echo "push \$*" >>"$ROOT/push.log"; exit 1; fi
done
exec "$REAL_GIT" "\$@"
STUB
chmod +x "$ROOT/bin/git"
export PATH="$ROOT/bin:$PATH"

git init -q --bare "$ROOT/remote.git"
R=$ROOT/repo
git init -q -b main "$R"
cd "$R"
git config user.name "Private Person"
git config user.email "$PRIVATE_EMAIL"
git remote add origin "$ROOT/remote.git"
mkdir scripts
cp "$HERE/mirror-export.sh" "$HERE/leak-check.sh" scripts/
printf '# no deliberate uses\n' >scripts/leak-check.allow
printf '[changelog]\n' >cliff.toml
printf 'one\n' >README.md
git add -A && git commit -q -m "feat: one" && git tag -a v1.0.0 -m v1.0.0
printf 'two\n' >>README.md
git commit -q -am "feat: two" && git tag -a v1.1.0 -m v1.1.0

run() { # ARGS...: runs the script, sets RC and OUT
  set +e
  OUT=$(scripts/mirror-export.sh "$@" 2>&1)
  RC=$?
  set -e
}
count() { git rev-list --count public 2>/dev/null || echo 0; }
tip() { git rev-parse -q --verify refs/heads/public || echo none; }

run v1.0.0
check "first export succeeds" "$(yes_if test "$RC" = 0)"
run v1.1.0
check "second export succeeds" "$(yes_if test "$RC" = 0)"
check "public has exactly 2 commits" "$(yes_if test "$(count)" = 2)"
check "the first is a root commit" "$(yes_if test -z "$(git rev-parse public~1^@ 2>/dev/null)")"
for t in v1.0.0 v1.1.0; do
  check "$t: the commit's tree is the tag's tree" \
    "$(yes_if test "$(git rev-parse "refs/public-tags/$t^{tree}")" = "$(git rev-parse "$t^{tree}")")"
done
check "the second commit's parent is the first" \
  "$(yes_if test "$(git rev-parse public~1)" = "$(git rev-parse refs/public-tags/v1.0.0)")"
check "author and committer are the public identity" \
  "$(yes_if test "$(git log public --format='%an <%ae>|%cn <%ce>' | sort -u)" = "Test Mirror <$BW_PUBLIC_EMAIL>|Test Mirror <$BW_PUBLIC_EMAIL>")"
check "only the noreply address appears" "$(yes_if test "$(git log public --format='%ae %ce' | sort -u)" = "$BW_PUBLIC_EMAIL $BW_PUBLIC_EMAIL")"
check "the message is the title and the tag's notes" \
  "$(yes_if test "$(git log -1 --format=%B refs/public-tags/v1.1.0 | head -n 3)" = "$(printf 'Bookwarden v1.1.0\n\n## v1.1.0')")"
check "the notes cover the previous tag to this one" "$(yes_if grep -q 'changes in v1.0.0..v1.1.0' <<<"$(git log -1 --format=%B refs/public-tags/v1.1.0)")"
check "it prints the commit id" "$(yes_if grep -qF "$(git rev-parse refs/public-tags/v1.1.0)" <<<"$OUT")"
check "it prints the push command" "$(yes_if grep -q 'git push .*public' <<<"$OUT")"
check "the commit date is the tag's commit date, in UTC" \
  "$(yes_if test "$(git log -1 --format='%at %ad|%ct %cd' --date=format:%z refs/public-tags/v1.1.0)" = "$(git log -1 --format=%ct v1.1.0) +0000|$(git log -1 --format=%ct v1.1.0) +0000")"
check "the push command names full refs" "$(yes_if grep -q 'refs/heads/public:refs/heads/main' <<<"$OUT")"
check "the forge's own tag is untouched" "$(yes_if test "$(git cat-file -t v1.1.0)" = tag)"

before=$(tip)
run v1.1.0
check "a tag already exported: exit 0" "$(yes_if test "$RC" = 0)"
check "a tag already exported: says so" "$(yes_if grep -qi 'already exported' <<<"$OUT")"
check "a tag already exported: still one commit for it" "$(yes_if test "$(count)" = 2 -a "$(tip)" = "$before")"

# A leak in the release notes (a commit subject) blocks the export too.
git commit -q --allow-empty -m "chore: empty" && git tag -a v1.1.1 -m v1.1.1
STUB_NOTES_LEAK=192.168 run v1.1.1
check "a leak in the release notes: exit 1" "$(yes_if test "$RC" = 1)"
check "a leak in the release notes: public unchanged" "$(yes_if test "$(tip)" = "$before")"
check "a leak in the release notes: no public tag" "$(yes_if test -z "$(git for-each-ref refs/public-tags/v1.1.1)")"

# A leak in the tag's tree.
addr=192.168
printf 'proxy at %s.7.20\n' "$addr" >notes.md
git add notes.md && git commit -q -m "docs: notes" && git tag -a v1.2.0 -m v1.2.0
run v1.2.0
check "a tag with a leak-check hit: exit 1" "$(yes_if test "$RC" = 1)"
check "a refusal's message doesn't end in its exit code" "$(yes_if lacks ' [0-9]+$' "$OUT")"
check "a tag with a leak-check hit: names the hit, not the text" "$(yes_if grep -q 'notes.md:1:private-address' <<<"$OUT")"
check "a tag with a leak-check hit: public unchanged" "$(yes_if test "$(tip)" = "$before")"
check "a tag with a leak-check hit: no public tag" "$(yes_if test -z "$(git for-each-ref refs/public-tags/v1.2.0)")"
git rm -q notes.md && git commit -q -m "docs: drop notes"

# Refusals; public stays as it was.
git tag -a v1.3.0 -m v1.3.0
refused() { # NAME: the last run was refused and changed nothing
  check "$1: refused" "$(yes_if test "$RC" != 0)"
  check "$1: public unchanged" "$(yes_if test "$(tip)" = "$before")"
}
BW_PUBLIC_NAME="" run v1.3.0
refused "no name"
(unset BW_PUBLIC_EMAIL && scripts/mirror-export.sh v1.3.0 >/dev/null 2>&1) && RC=0 || RC=$?
refused "no address"
BW_PUBLIC_EMAIL="$PRIVATE_EMAIL" run v1.3.0
refused "an address that is not noreply.github.com"
BW_PUBLIC_EMAIL="x@noreply.github.com.example.org" run v1.3.0
refused "an address that only contains noreply.github.com"
BW_PUBLIC_EMAIL="someone@private.example@users.noreply.github.com" run v1.3.0
refused "an address with a second @"
BW_PUBLIC_EMAIL="someone.noreply.github.com" run v1.3.0
refused "an address without @"
printf 'dirty\n' >>README.md
run v1.3.0
refused "a dirty work tree"
git checkout -q -- README.md
printf 'stray\n' >untracked.txt
run v1.3.0
refused "an untracked file"
rm untracked.txt
git switch -q -c side
printf 'side\n' >side.txt
git add side.txt && git commit -q -m "feat: side" && git tag -a v9.9.9 -m v9.9.9
git switch -q main
run v9.9.9
refused "a tag not on main"
run v0.0.404
refused "a tag that does not exist"

# public and refs/public-tags disagree: a public tag that is gone, or one
# that public doesn't contain, is refused, not re-exported or skipped.
saved=$(git rev-parse refs/public-tags/v1.1.0)
git update-ref -d refs/public-tags/v1.1.0
run v1.1.0
refused "public's tip has no public tag"
git update-ref refs/public-tags/v1.1.0 "$saved"
git branch -q -f public refs/public-tags/v1.0.0
run v1.1.0
check "a public tag that public doesn't contain: refused" "$(yes_if test "$RC" != 0)"
git branch -q -f public "$saved"

# A tree that changes on the way through git archive (export-subst) is
# refused: what was checked must be what is committed.
# shellcheck disable=SC2016 # a literal git archive placeholder
printf '$Format:%%H$\n' >stamp.txt
printf 'stamp.txt export-subst\n' >.gitattributes
git add stamp.txt .gitattributes && git commit -q -m "chore: stamp" && git tag -a v1.4.0 -m v1.4.0
run v1.4.0
refused "an export whose tree differs from the tag's"
check "an export whose tree differs: says why" "$(yes_if grep -q "differs from the tag's tree" <<<"$OUT")"
git rm -q stamp.txt .gitattributes && git commit -q -m "chore: no stamp"

# It never pushes: no executed git push in the script, nothing on the remote.
check "the script runs no git push (only prints it)" \
  "$(yes_if test -z "$(grep -n 'git push' scripts/mirror-export.sh | grep -v -E 'printf|echo|^[0-9]+:[[:space:]]*#')")"
check "the remote received nothing" "$(yes_if test -z "$(git -C "$ROOT/remote.git" for-each-ref)")"
check "git push was never run" "$(yes_if test ! -e "$ROOT/push.log")"

if [ "$FAILS" -ne 0 ]; then
  echo "mirror-export_test: $FAILS failed" >&2
  exit 1
fi
echo "mirror-export_test: OK"
