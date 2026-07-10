#!/usr/bin/env bash
set -euo pipefail

# Bootstrap native deps for full Calibre pytest on CachyOS/Arch (per strategist)
# Run this before gate if collection fails on imagehash / pi-heif / ocrmypdf etc.

echo "Installing system deps for Calibre test env (CachyOS/Arch)..."

# Use paru if available (user's tool), else pacman
if command -v paru >/dev/null 2>&1; then
  PKG=paru
elif command -v yay >/dev/null 2>&1; then
  PKG=yay
else
  PKG="sudo pacman"
fi

$PKG -Syu --needed --noconfirm \
  libheif \
  tesseract \
  tesseract-data-eng \
  tesseract-data-deu \
  poppler \
  ghostscript \
  libxml2 \
  libxslt \
  python-pip \
  base-devel

echo "System deps installed."

# Then uv sync if in project
if [ -f pyproject.toml ]; then
  echo "Running uv sync --extra dev (if applicable)..."
  uv sync --extra dev || echo "uv sync skipped or partial; use --with in runs"
fi

echo "Bootstrap complete. Re-run pytest with full PYTHONPATH/uv."
