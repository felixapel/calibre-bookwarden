# Safety First

This project adheres to a strict "read-only by default" philosophy to protect your Calibre libraries and metadata files.

## What is Read-Only?
By default, the application operates in a completely read-only mode:
- **Scan**: Reads library database to discover books.
- **Inspect**: Reads file content (PDF, EPUB) and cover images.
- **Audit**: Fetches candidate metadata from external providers (OpenLibrary, Google Books) and queries local/remote LLMs.

**The default Docker configuration mounts the `library` directory as read-only (`:ro`).**
**The default configuration has `BOOKAUDIT_READ_ONLY=true`.**

## What Can Write?
Write operations only occur when explicitly instructed and confirmed by the user. The primary write actions are:
- `apply`: Modifies Calibre metadata and potentially ebook-meta.
- `undo`: Restores metadata from backups.

**WebUI and API endpoints that modify data require `{"force": true}` in the request body after explicit user confirmation.**

Affected endpoints: `POST /api/apply`, `POST /api/undo/{change_id}`, `POST /api/runs/{run_id}/revert`.

## How Backups Work
Before any write operation (e.g., `apply`), the system automatically exports the current metadata state as an OPF file via `calibredb export_metadata` and saves it to `.artifacts/backups/<book_id>/`.

## How Undo Works
The `undo` command reverses an applied change by using the backup OPF file saved prior to the `apply` operation. It restores the exact state of the book's metadata before the change.

## Safe Testing Practices
1. **Never test `apply` on your primary, real Calibre library first.**
2. Use a disposable or isolated test library when testing write capabilities.
3. Keep the Docker volume mount for the library as `ro` unless you explicitly intend to test writing.
4. Ensure `.state` and `.artifacts` directories are writable to store logs, evidence packages, and backups safely.
