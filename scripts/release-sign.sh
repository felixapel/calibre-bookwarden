#!/usr/bin/env bash
# Signs every image digest in a file with cosign and attaches the SBOM to each as a signed
# spdxjson attestation (ADR-042). release.yml calls it; release-rehearsal.sh proves it.
#
#   release-sign.sh <digests-file> <sbom>
#
# The digests file holds one repo@sha256:<64 hex> per line. The key and its password come
# from the environment and never touch the disk:
#   COSIGN_KEY         content of cosign.key (REQUIRED)
#   COSIGN_PASSWORD    its password (REQUIRED)
#   BW_HTTP_REGISTRY   host:port of the one plain-HTTP registry; only its references get
#                      --allow-http-registry, every other registry keeps TLS (none)
# No transparency log, timestamp authority or Fulcio is used: the signing config has no
# services, so verification needs --insecure-ignore-tlog.
set -euo pipefail

die() { printf 'error: %s\n' "$1" >&2; exit "${2:-1}"; }

[ $# -eq 2 ] || die "usage: $0 <digests-file> <sbom>" 2
digests=$1
sbom=$2
[ -n "${COSIGN_KEY:-}" ] || die "COSIGN_KEY is required"
[ -n "${COSIGN_PASSWORD:-}" ] || die "COSIGN_PASSWORD is required"
BW_HTTP_REGISTRY=${BW_HTTP_REGISTRY:-}
[ -s "$sbom" ] || die "SBOM not found or empty: $sbom"
[ -s "$digests" ] || die "digests file not found or empty: $digests"
while read -r ref || [ -n "$ref" ]; do
  [[ $ref =~ ^[A-Za-z0-9._:/-]+@sha256:[0-9a-f]{64}$ ]] || die "not a repo@sha256:<digest> reference: $ref"
done <"$digests"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
cosign signing-config create --out "$tmp/signing-config.json"

# The list is read on fd 3, so a cosign that reads stdin can't eat it.
while read -r ref <&3 || [ -n "$ref" ]; do
  http=()
  if [ -n "$BW_HTTP_REGISTRY" ] && [[ $ref == "$BW_HTTP_REGISTRY"/* ]]; then
    http=(--allow-http-registry)
  fi
  cosign sign --yes --key env://COSIGN_KEY --signing-config "$tmp/signing-config.json" "${http[@]}" "$ref"
  cosign attest --yes --key env://COSIGN_KEY --type spdxjson --predicate "$sbom" \
    --signing-config "$tmp/signing-config.json" "${http[@]}" "$ref"
  printf 'signed and attested %s\n' "$ref"
done 3<"$digests"
