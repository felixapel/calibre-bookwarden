#!/usr/bin/env bash
# Scans an image for HIGH and CRITICAL vulnerabilities and secrets with Trivy. ci.yml,
# release.yml and nightly.yml all call this, so the three can't drift.
#
#   scan-image.sh <image>
#
# Trivy runs as a container on the host's Docker (the job container's paths don't exist
# there, so nothing is mounted but the socket). The named volume keeps Trivy's
# vulnerability DB between runs. --timeout bounds the whole scan, DB download included, at
# 15 minutes instead of the default 5 (a slow download failed CI on run 7689).
set -euo pipefail
[ $# -eq 1 ] || { echo "usage: $0 <image>" >&2; exit 2; }
exec docker run --rm -v /var/run/docker.sock:/var/run/docker.sock \
  -v bookwarden-trivy-cache:/root/.cache/ \
  aquasec/trivy:0.76.2@sha256:8f6bb947dbc528902e11f2f5be52f7b519f6a39d5d148f41166f2040499768fb \
  image --timeout 15m --quiet --exit-code 1 --severity HIGH,CRITICAL --ignore-unfixed --scanners vuln,secret \
  "$1"
