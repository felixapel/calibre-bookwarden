"""Authority control and author name normalization engine.

Standardizes direct display names ('First Last') and sort keys ('Last, First'),
handles noble particles, classical/religious figures, and canonical multi-author
delimiters (' & ').
"""

from __future__ import annotations

import re
import unicodedata
from collections.abc import Sequence

# Noble particles preserved with surname in sorting.
NOBLE_PARTICLES = {
    "von",
    "van",
    "de",
    "del",
    "della",
    "da",
    "di",
    "du",
    "des",
    "der",
    "den",
    "el",
    "al",
    "fitz",
    "mac",
    "mc",
}
MULTIWORD_NOBLE_PARTICLES = {"de la", "de las", "de los", "van der"}

# Known corporate/periodical entities
CORPORATE_ENTITIES: dict[str, str] = {
    "the economist": "Economist, The",
    "the new yorker": "New Yorker, The",
    "financial times": "Financial Times",
    "der spiegel": "Spiegel, Der",
    "spiegel online": "Spiegel Online",
    "the wall street journal": "Wall Street Journal, The",
    "nature": "Nature",
    "lonely planet": "Lonely Planet",
    "harvard business review": "Harvard Business Review",
}

# Only these documented names receive compound-surname treatment. A generic
# last-two-words rule corrupts legitimate names from many naming traditions.
COMPOUND_SURNAME_AUTHORITY: dict[str, str] = {
    "mario vargas llosa": "Vargas Llosa",
    "gabriel garcia marquez": "García Márquez",
}


def _authority_key(name: str) -> str:
    decomposed = unicodedata.normalize("NFKD", name)
    without_marks = "".join(c for c in decomposed if not unicodedata.combining(c))
    return re.sub(r"\s+", " ", without_marks).strip().casefold()


def normalize_author_display(name: str) -> str:
    """Normalizes author display name to canonical direct order ('First Last')."""
    if not name:
        return ""
    n = name.strip()

    # If in inverted form 'Last, First', convert to 'First Last' (except corporate/saints)
    if "," in n:
        parts = [p.strip() for p in n.split(",", 1)]
        if len(parts) == 2:
            last, first = parts[0], parts[1]
            if first.lower() in ("the", "der", "saint", "st.", "pope", "papa"):
                # Corporate/saint inverted: keep or restore
                if first.lower() in ("saint", "st."):
                    return f"Saint {last}"
                if first.lower() in ("pope", "papa"):
                    return f"Pope {last}"
                if first.lower() in ("the", "der"):
                    return f"{first.capitalize()} {last}"
            else:
                return f"{first} {last}"

    # Clean double spaces
    n = re.sub(r"\s+", " ", n)
    return n


def compute_author_sort(name: str) -> str:
    """Computes canonical inverted sort key ('Last, First') from direct display name."""
    if not name:
        return ""
    n = name.strip()
    n_lower = n.lower()

    # 1. Corporate check
    if n_lower in CORPORATE_ENTITIES:
        return CORPORATE_ENTITIES[n_lower]

    # 2. Already inverted
    if "," in n:
        return n

    # 3. Saints and Popes
    if n.startswith("Saint ") or n.startswith("St. ") or n.startswith("San "):
        clean = re.sub(r"^(Saint|St\.|San)\s+", "", n)
        return f"{clean}, Saint"
    if n.startswith("Pope ") or n.startswith("Papa "):
        clean = re.sub(r"^(Pope|Papa)\s+", "", n)
        return f"{clean}, Pope"

    words = n.split()
    if len(words) == 1:
        return words[0]

    compound_surname = COMPOUND_SURNAME_AUTHORITY.get(_authority_key(n))
    if compound_surname:
        surname_words = compound_surname.split()
        given = " ".join(words[: -len(surname_words)])
        if given:
            return f"{compound_surname}, {given}"

    # 4. Particles in surname (e.g. Ludwig van der Waals -> van der Waals, Ludwig)
    for particle_words in (2, 1):
        # A leading token such as "Al" or "Van" is a given name unless at
        # least one preceding given-name token establishes a surname particle.
        if len(words) < particle_words + 2:
            continue
        particle = " ".join(words[-(particle_words + 1) : -1]).casefold()
        known_particle = particle in MULTIWORD_NOBLE_PARTICLES if particle_words == 2 else particle in NOBLE_PARTICLES
        if known_particle:
            surname = " ".join(words[-(particle_words + 1) :])
            given = " ".join(words[: -(particle_words + 1)])
            return f"{surname}, {given}"

    surname = words[-1]
    given = " ".join(words[:-1])
    return f"{surname}, {given}"


def join_multiple_authors(authors: Sequence[str]) -> str:
    """Joins multiple authors using Calibre's canonical delimiter ' & '."""
    cleaned = [normalize_author_display(a) for a in authors if a and a.strip()]
    return " & ".join(cleaned)


def join_multiple_author_sorts(authors: Sequence[str]) -> str:
    """Computes and joins canonical sort keys for multiple authors with ' & '."""
    sorts = [compute_author_sort(a) for a in authors if a and a.strip()]
    return " & ".join(sorts)
