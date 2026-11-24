#!/usr/bin/env bash
# Proves release-sign.sh and the README's cosign verify commands against a throwaway
# registry on 127.0.0.1, with the cosign release.yml pins. Not part of CI.
#
#   scripts/release-rehearsal.sh [image]     (default bookwarden:dev; build it first)
#
# It starts a registry, pushes the image, makes a throwaway key pair with a fixed password in
# a temp directory, signs and attests the pushed digest with release-sign.sh, then runs the
# three verify commands from the README's Releases section with the throwaway public key.
# The registry and the temp directory are removed on exit. Needs docker, curl and network
# access for the cosign download.
set -euo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT=$(cd "$HERE/.." && pwd)
IMAGE=${1:-bookwarden:dev}
REGISTRY_IMAGE=registry:3.1.2@sha256:82f6ff4a7f982960c8aa51617fea3aabc485df6e55cce80d22deb1de36f1bc3c
NAME=bw-rehearsal-$$

die() { printf 'rehearsal: %s\n' "$1" >&2; exit 1; }

tmp=$(mktemp -d)
cleanup() {
  docker rm -f "$NAME" >/dev/null 2>&1 || true
  rm -rf "$tmp"
}
trap cleanup EXIT

# The cosign version and hash release.yml pins.
wf="$ROOT/.gitea/workflows/release.yml"
url=$(grep -o 'https://github.com/sigstore/cosign/releases/download/[^ ]*' "$wf" | head -1)
sum=$(sed -n 's/^ *COSIGN_SHA256: *\([0-9a-f]\{64\}\)$/\1/p' "$wf")
[ -n "$url" ] && [ -n "$sum" ] || die "cannot read the cosign pin from release.yml"
mkdir -p "$tmp/bin"
curl -fsSL --retry 3 -o "$tmp/bin/cosign" "$url"
echo "$sum  $tmp/bin/cosign" | sha256sum -c - >/dev/null || die "cosign does not match its pinned hash"
chmod +x "$tmp/bin/cosign"
export PATH="$tmp/bin:$PATH"
echo "rehearsal: $(cosign version 2>&1 | grep -m1 GitVersion | tr -s ' ')"

docker image inspect "$IMAGE" >/dev/null 2>&1 || die "image $IMAGE not found; build it first"
docker run -d --name "$NAME" -p 127.0.0.1::5000 "$REGISTRY_IMAGE" >/dev/null
port=$(docker port "$NAME" 5000/tcp | head -1 | sed 's/.*://')
registry="127.0.0.1:$port"
for _ in $(seq 1 50); do
  curl -fsS "http://$registry/v2/" >/dev/null 2>&1 && break
  sleep 0.2
done
curl -fsS "http://$registry/v2/" >/dev/null || die "the registry did not come up"

repo="$registry/felix/bookwarden"
docker tag "$IMAGE" "$repo:rehearsal"
docker push -q "$repo:rehearsal" >/dev/null
digest=$(docker image inspect -f '{{range .RepoDigests}}{{println .}}{{end}}' "$IMAGE" | grep "^$repo@" | head -1)
docker rmi "$repo:rehearsal" >/dev/null
[[ $digest =~ @sha256:[0-9a-f]{64}$ ]] || die "no digest for the pushed image"
echo "rehearsal: pushed $digest"

# A throwaway key pair; the password is fixed because the key dies with the temp directory.
export COSIGN_PASSWORD=rehearsal-only
(cd "$tmp" && cosign generate-key-pair >/dev/null 2>&1) || die "cannot make a key pair"
COSIGN_KEY=$(cat "$tmp/cosign.key")
export COSIGN_KEY
echo "$digest" >"$tmp/digests.txt"
printf '{"spdxVersion":"SPDX-2.3","SPDXID":"SPDXRef-DOCUMENT","name":"rehearsal","dataLicense":"CC0-1.0","documentNamespace":"https://example.invalid/rehearsal","creationInfo":{"created":"2026-01-01T00:00:00Z","creators":["Tool: rehearsal"]},"packages":[]}\n' >"$tmp/sbom.spdx.json"
BW_HTTP_REGISTRY="$registry" "$HERE/release-sign.sh" "$tmp/digests.txt" "$tmp/sbom.spdx.json"
unset COSIGN_KEY COSIGN_PASSWORD
# What the signature and attestation left in the repository (tags or referrers), so the
# release's dry-run clean-up can be checked against it.
echo "rehearsal: tags after signing: $(curl -fsS "http://$registry/v2/felix/bookwarden/tags/list" | tr -d '\n')"
echo "rehearsal: referrers of the digest: $(curl -fsS -H 'Accept: application/vnd.oci.image.index.v1+json' "http://$registry/v2/felix/bookwarden/referrers/${digest##*@}" | tr -d '\n' | cut -c1-400)"

# The README's verify commands, exactly as written, pointed at the local digest and key.
mapfile -t cmds < <(awk '/^## Releases/{r=1} r && /^cosign verify/' "$ROOT/README.md")
[ "${#cmds[@]}" = 3 ] || die "expected 3 cosign verify commands in the README, found ${#cmds[@]}"
ok=0
for c in "${cmds[@]}"; do
  c=${c//ghcr.io\/felixapel\/bookwarden@sha256:<digest>/$digest}
  c=${c//<forge-registry>\/felix\/bookwarden@sha256:<digest>/$digest}
  c=${c//cosign.pub/$tmp/cosign.pub}
  case $c in *"<"*) die "unreplaced placeholder in: $c" ;; esac
  read -r -a argv <<<"$c"
  if "${argv[@]}" >"$tmp/out" 2>&1; then
    ok=$((ok + 1))
    echo "rehearsal: ok   ${c//$tmp/<tmp>}"
  else
    echo "rehearsal: FAIL ${c//$tmp/<tmp>}"
    sed 's/^/  /' "$tmp/out"
  fi
done
[ "$ok" = 3 ] || die "$((3 - ok)) of 3 verify commands failed"
echo "rehearsal: OK, all 3 verify commands passed"
