from typing import Any

from calibre_ai_auditor.storage.models import BookRecord


def find_duplicates(books: list[BookRecord]) -> list[dict[str, Any]]:
    """
    Finds likely duplicate books in the library.
    Criteria:
    - Identical ISBN
    - Very similar Title + Author (fuzzy)
    """
    duplicates = []
    seen_isbns: dict[str, str] = {}
    seen_titles: dict[str, str] = {}

    for book in books:
        # 1. Check ISBN
        isbn = book.current_metadata.get("identifiers", {}).get("isbn")
        if isbn:
            if isbn in seen_isbns:
                duplicates.append(
                    {
                        "type": "isbn_match",
                        "books": [seen_isbns[isbn], book.book_key],
                        "value": isbn,
                    }
                )
            else:
                seen_isbns[isbn] = book.book_key

        # 2. Check Title+Author
        title = book.current_metadata.get("title", "").lower().strip()
        authors = book.current_metadata.get("authors", [])
        author = "".join(authors).lower().strip()
        key = f"{title}|{author}"
        if key in seen_titles:
            duplicates.append(
                {
                    "type": "title_author_match",
                    "books": [seen_titles[key], book.book_key],
                    "value": key,
                }
            )
        else:
            seen_titles[key] = book.book_key

    return duplicates
