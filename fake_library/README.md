# Local test library (not committed)

Place a disposable Calibre library here for Docker and integration testing.
Ebook files and `metadata.db` are gitignored — do not commit copyrighted books.

```bash
# Example: point Calibre at this folder or copy a small test library locally
export BOOKWARDEN_LIBRARY_PATH="$(pwd)/fake_library"
# (Legacy BOOKAUDIT_LIBRARY_PATH is also supported)
```
