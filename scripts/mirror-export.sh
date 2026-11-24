#!/usr/bin/env bash
# Turns a release tag into one commit on the local branch "public": the tag's
# tree as git archive exports it, checked by leak-check.sh, committed under
# the public identity with the tag's release notes. The first export is a
# root commit; each later one has the previous public commit as its parent.
# The commit is also tagged refs/public-tags/<tag>. It never pushes: it
# prints the push command (ADR-047).
#
#   BW_PUBLIC_NAME=... BW_PUBLIC_EMAIL=...@users.noreply.github.com \
#     scripts/mirror-export.sh <tag>
#
# Needs git-cliff on PATH for the release notes.
set -euo pipefail

die() { # MESSAGE [EXIT]
  echo "mirror-export: $1" >&2
  exit "${2:-2}"
}
[ $# -eq 1 ] || die "usage: mirror-export.sh <tag>"
tag=$1
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
cd "$(git -C "$here" rev-parse --show-toplevel)"

name=${BW_PUBLIC_NAME:-}
email=${BW_PUBLIC_EMAIL:-}
[ -n "$name" ] && [ -n "$email" ] || die "set BW_PUBLIC_NAME and BW_PUBLIC_EMAIL to the public identity"
[[ $email =~ ^[A-Za-z0-9._+-]+@users\.noreply\.github\.com$ ]] ||
  die "BW_PUBLIC_EMAIL must be a GitHub noreply address, <id>+<user>@users.noreply.github.com"
status=$(git status --porcelain) || die "git status failed"
[ -z "$status" ] || die "the work tree is not clean"
commit=$(git rev-parse -q --verify "refs/tags/$tag^{commit}") || die "no tag $tag"
git merge-base --is-ancestor "$commit" refs/heads/main || die "$tag is not on main"

# public and its tags move together; if they disagree, someone changed one
# by hand, and guessing could export a tag twice or skip one.
old=$(git rev-parse -q --verify refs/heads/public || true)
if [ -n "$old" ] && [ -z "$(git for-each-ref --points-at "$old" refs/public-tags/)" ]; then
  die "public's tip $old has no refs/public-tags/ tag; repair public or its tags first"
fi
if existing=$(git rev-parse -q --verify "refs/public-tags/$tag"); then
  if [ -z "$old" ] || ! git merge-base --is-ancestor "$existing" "$old"; then
    die "refs/public-tags/$tag ($existing) is not on public; repair public or its tags first"
  fi
  echo "mirror-export: $tag is already exported as $existing"
  exit 0
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
mkdir "$tmp/tree" "$tmp/notes"
git archive --format=tar "$commit" | tar -xf - -C "$tmp/tree"
"$here/leak-check.sh" "$tmp/tree" || die "the tree of $tag has leak-check hits (above); nothing was exported" 1

# The notes: previous tag..tag, as the release workflow writes them. They
# are public too, so they are checked like the tree.
exclude=()
case $tag in *-*) ;; *) exclude=(--exclude '*-*') ;; esac
prev=$(git describe --tags --abbrev=0 --match 'v[0-9]*' "${exclude[@]}" "$commit^" 2>/dev/null || true)
range=$tag
if [ -n "$prev" ]; then range="$prev..$tag"; fi
git-cliff --config "$tmp/tree/cliff.toml" --tag "$tag" "$range" >"$tmp/notes/release-notes.md"
"$here/leak-check.sh" "$tmp/notes" || die "the release notes of $tag have leak-check hits; nothing was exported" 1

# The commit's tree is built from the export itself.
export GIT_INDEX_FILE=$tmp/index
git --work-tree="$tmp/tree" add -A -f .
tree=$(git write-tree)
unset GIT_INDEX_FILE
# What was checked must be what is committed: an export-ignore or
# export-subst attribute, or a clean filter, would make them differ.
[ "$tree" = "$(git rev-parse "$commit^{tree}")" ] ||
  die "the exported tree differs from the tag's tree (export-ignore, export-subst or a filter); nothing was exported" 1

parent=()
if [ -n "$old" ]; then parent=(-p "$old"); fi
# The tag's own commit time, in UTC, so the export's time and the local
# time zone stay out of the public commit.
date="$(git log -1 --format=%ct "$commit") +0000"
new=$(printf 'Bookwarden %s\n\n%s\n' "$tag" "$(cat "$tmp/notes/release-notes.md")" |
  GIT_AUTHOR_NAME=$name GIT_AUTHOR_EMAIL=$email GIT_AUTHOR_DATE=$date \
    GIT_COMMITTER_NAME=$name GIT_COMMITTER_EMAIL=$email GIT_COMMITTER_DATE=$date \
    git commit-tree "$tree" "${parent[@]}")
# Both refs in one transaction; the old values guard against a concurrent
# change (all zeros: the ref must not exist yet).
zero=0000000000000000000000000000000000000000
printf 'start\nupdate refs/heads/public %s %s\nupdate refs/public-tags/%s %s %s\ncommit\n' \
  "$new" "${old:-$zero}" "$tag" "$new" "$zero" | git update-ref --stdin >/dev/null

echo "mirror-export: $tag exported as $new"
echo "To publish it (only when Felix says so):"
printf '  git push <github-remote> refs/heads/public:refs/heads/main refs/public-tags/%s:refs/tags/%s\n' "$tag" "$tag"
