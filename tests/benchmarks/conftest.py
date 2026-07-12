"""Shared fixtures for the benchmark suite.

All benchmarks live under tests/benchmarks/ and use these factories to
generate realistic, deterministic corpora.
"""

from __future__ import annotations

import random
import string
from typing import Any

import pytest

# Realistic book titles, authors, ISBNs to seed the corpus
TITLES = [
    "The Great Gatsby",
    "Moby-Dick; or, The Whale",
    "Pride and Prejudice",
    "The Hunchback of Notre-Dame",
    "War and Peace",
    "Crime and Punishment",
    "One Hundred Years of Solitude",
    "The Lord of the Rings",
    "Foundation",
    "Snow Crash",
    "A Brief History of Time",
    "Sapiens",
    "Project Hail Mary",
    "The Stand",
    "Brave New World",
    "1984",
    "Animal Farm",
    "Dune",
    "La casa de los espíritus",
    "The Pragmatic Programmer",
]

AUTHORS = [
    "F. Scott Fitzgerald",
    "Herman Melville",
    "Jane Austen",
    "Victor Hugo",
    "Leo Tolstoy",
    "Fyodor Dostoevsky",
    "Gabriel García Márquez",
    "J.R.R. Tolkien",
    "Isaac Asimov",
    "Neal Stephenson",
    "Stephen Hawking",
    "Yuval Noah Harari",
    "Andy Weir",
    "Stephen King",
    "Aldous Huxley",
    "George Orwell",
    "Frank Herbert",
    "Isabel Allende",
    "Andrew Hunt",
    "David Thomas",
]

PUBLISHERS = [
    "Penguin",
    "Penguin Books",
    "Harper",
    "HarperCollins",
    "Random House",
    "Scribner",
    "Bantam",
    "Bantam Spectra",
    "Vintage",
    "Tor",
    "Addison-Wesley",
    "Prentice Hall",
    "Simon & Schuster",
]

LANGUAGES = ["eng", "spa", "fre", "ger", "rus", "ita", "por", "jpn"]


def random_isbn13(rng: random.Random) -> str:
    """Generate a checksum-valid ISBN-13."""
    body = "978" + "".join(rng.choices(string.digits, k=9))
    total = sum(int(c) * (1 if i % 2 == 0 else 3) for i, c in enumerate(body))
    check = (10 - (total % 10)) % 10
    return body + str(check)


@pytest.fixture
def rng() -> random.Random:
    """Deterministic RNG for reproducible benchmarks."""
    return random.Random(42)


@pytest.fixture
def factory(rng: random.Random):
    """Bundle of corpus factories for benchmarks."""

    class Factory:
        def __init__(self, rng: random.Random):
            self.rng = rng

        def random_title_pair(self) -> dict[str, Any]:
            """Returns (declared, observed) title pair with controlled noise."""
            base = rng.choice(TITLES)
            noise = rng.choice(["", " ", " (Special Edition)", "!!!", " by Author", " vol 1"])
            declared = base + noise
            observed = base
            return {"declared": declared, "observed": observed}

        def random_author_pair(self) -> dict[str, Any]:
            base = rng.choice(AUTHORS)
            return {"declared": [base], "observed": [base]}

        def random_isbn_pair(self) -> dict[str, Any]:
            isbn = random_isbn13(rng)
            return {"declared": isbn, "observed": isbn}

        def random_publisher_pair(self) -> dict[str, Any]:
            base = rng.choice(PUBLISHERS)
            return {"declared": base, "observed": base}

        def random_date_pair(self) -> dict[str, Any]:
            year = rng.randint(1950, 2025)
            return {"declared": f"{year}-01-01", "observed": f"{year}"}

        def random_language_pair(self) -> dict[str, Any]:
            lang = rng.choice(LANGUAGES)
            return {"declared": lang, "observed": lang}

        def random_series_pair(self) -> dict[str, Any]:
            return {"declared": None, "observed": None}

        def random_field_inputs(self, n: int) -> list[dict[str, Any]]:
            """Generate n mixed field inputs for engine benchmarks."""
            inputs: list[dict[str, Any]] = []
            for _ in range(n):
                kind = rng.choice(["title", "isbn", "author", "publisher"])
                if kind == "title":
                    inputs.append({"field": "title", **self.random_title_pair()})
                elif kind == "isbn":
                    inputs.append({"field": "isbn", **self.random_isbn_pair()})
                elif kind == "author":
                    inputs.append({"field": "authors", **self.random_author_pair()})
                else:
                    inputs.append({"field": "publisher", **self.random_publisher_pair()})
            return inputs

        def realistic_corpus(self, n: int) -> list[dict[str, Any]]:
            """Generate n realistic declared/observed pairs for engine throughput tests."""
            out: list[dict[str, Any]] = []
            for _ in range(n):
                title = rng.choice(TITLES)
                author = rng.choice(AUTHORS)
                isbn = random_isbn13(rng)
                publisher = rng.choice(PUBLISHERS)
                year = rng.randint(1950, 2025)
                # Some books have wrong metadata (the interesting case)
                if rng.random() < 0.3:
                    title = f"WRONG: {title}"
                if rng.random() < 0.2:
                    author = "Unknown"
                out.append(
                    {
                        "declared": {
                            "title": title,
                            "authors": [author],
                            "isbn": isbn,
                            "publisher": publisher,
                            "published_date": f"{year}-01-01",
                            "language": "eng",
                        },
                        "observed": {
                            "title_extracted": rng.choice(TITLES),
                            "authors_extracted": [rng.choice(AUTHORS)],
                            "isbn_extracted": random_isbn13(rng),
                            "publisher_extracted": rng.choice(PUBLISHERS),
                            "date_extracted": str(year),
                            "language_detected": "eng",
                        },
                    }
                )
            return out

    return Factory(rng)
