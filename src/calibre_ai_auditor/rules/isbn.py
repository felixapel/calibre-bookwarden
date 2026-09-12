"""Single home for ISBN checksum math.

Previously copy-pasted in curation/duplicates, verification/rules, and
verification/identity_v2. Behavior of each caller is preserved; they now
delegate here so the next fix lands once.
"""

from __future__ import annotations

import re


def _clean(value: str) -> str:
    return re.sub(r"[^0-9Xx]", "", value or "")


def isbn10_checksum_valid(value: str) -> bool:
    s = _clean(value).upper()
    if len(s) != 10 or not s[:9].isdigit() or not (s[9].isdigit() or s[9] == "X"):
        return False
    total = sum(int(c) * (10 - i) for i, c in enumerate(s[:9]))
    total += 10 if s[9] == "X" else int(s[9])
    return total % 11 == 0


def isbn13_checksum_valid(value: str) -> bool:
    s = _clean(value)
    if len(s) != 13 or not s.isdigit():
        return False
    total = sum(int(c) * (1 if i % 2 == 0 else 3) for i, c in enumerate(s[:12]))
    return (10 - total % 10) % 10 == int(s[-1])


def canonical_isbn13(value: str) -> str | None:
    """Checksum-validated canonical ISBN-13, or None."""
    s = _clean(value).upper()
    if len(s) == 10 and isbn10_checksum_valid(s):
        body = "978" + s[:9]
        check = (10 - sum((1 if i % 2 == 0 else 3) * int(c) for i, c in enumerate(body)) % 10) % 10
        return body + str(check)
    if len(s) == 13 and isbn13_checksum_valid(s):
        return s
    return None
