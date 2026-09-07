"""Calibre Bookwarden - Content-grounded metadata verification, cover forensics and curation guardian for Calibre."""

import sys

import calibre_ai_auditor as _core

__version__ = getattr(_core, "__version__", "1.3.0")
sys.modules[__name__] = _core
