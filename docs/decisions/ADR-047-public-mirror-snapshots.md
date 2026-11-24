# ADR-047: The public mirror is a snapshot per release

- Status: Accepted
- Date: 2026-11-23

## Context

Bookwarden is developed on a private forge, and the forge's history is not fit
to publish. Commits there change lines with LAN addresses, carry a tailnet host
name in their tree, and all carry a personal mail address. Rewriting that
history to remove them would touch every commit and could miss what no pattern
names; leaving it behind cannot.

The public repository on GitHub needs the code of each release, not how it was
written.

## Decision

The mirror's history is one commit per release. `scripts/mirror-export.sh
<tag>` builds it locally:

- It refuses a tag that is not on `main` and a work tree that is not clean.
- It exports the tag's tree with `git archive`, runs `scripts/leak-check.sh`
  on it and on the release notes, and stops on any hit.
- It commits that tree to the local branch `public`: the first export as a
  root commit, each later one with the previous `public` commit as its parent.
  The tree is the one `git archive` exports, and it must equal the tag's
  tree: an `export-ignore` or `export-subst` attribute, or a filter, that
  changes it stops the export, so what was checked is what is committed.
- Author and committer are the public identity in `BW_PUBLIC_NAME` and
  `BW_PUBLIC_EMAIL`, which must be a `users.noreply.github.com` address. The
  commit's dates are the tag commit's, in UTC.
- The message is `Bookwarden <tag>` and the tag's release notes, written by
  git-cliff from `cliff.toml`, as the release workflow writes them.
- The commit is tagged `refs/public-tags/<tag>`, a namespace apart from the
  forge's own tags, so the two never collide.
- It prints the commit and the `git push` command, and never pushes. Pushing
  is the maintainer's step. A tag already exported is reported and left alone.

The push maps `public` to the mirror's `main` and `refs/public-tags/<tag>` to
the mirror's tag `<tag>`, so a GitHub tag names the same release, and the same
tree, as the forge's tag.

## Consequences

- The mirror shows each release's code, its release notes and its tags. The
  notes list the subjects of the commits since the previous release, with
  short forge commit ids that don't exist on GitHub; the commits themselves,
  their authors, branches, pull requests and the forge's CI runs are not in
  the mirror.
- The image's `org.opencontainers.image.revision` label names the forge's
  commit, which is not in the mirror. To find the code of an image, use its
  `org.opencontainers.image.source` label and its version tag: the mirror's
  tag of that name has the same tree.
- Pull requests on GitHub can't be merged as such, because the next export
  replaces the tree. They are read and applied by hand on the forge
  (`CONTRIBUTING.md`), and reach the mirror with the next release.
- A leak-check hit in a tag blocks its export. The fix is a new tag, not an
  edited export.
