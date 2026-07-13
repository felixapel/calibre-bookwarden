#!/usr/bin/env bash
set -euo pipefail

echo "Bootstrapping Calibre test env for full pytest (CachyOS/Arch per CLAUDE.md tools: paru)"

# Install system deps if paru available (user's preferred)
if command -v paru >/dev/null 2>&1; then
  echo "Using paru for system deps (libheif for pi-heif, etc.)"
  paru -S --needed --noconfirm libheif tesseract poppler 2>&1 || echo "paru may need interaction or already installed"
else
  echo "paru not found; ensure system deps for heif/ocr manually: sudo pacman -S libheif tesseract"
fi

# Python deps via uv (to unblock collection)
echo "Installing python test deps via uv"
uv pip install --system imagehash google-generativeai fastapi safety uvicorn pytest-asyncio pymupdf4llm 2>&1 || echo "uv pip may be partial"

echo "Bootstrap done. Try full pytest now. If still collection error on native, run with --continue-on-collection-errors or mark ocr_live."
