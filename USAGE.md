# Usage Guide

`calibre-ai-auditor` provides two primary ways to manage your library metadata: a modern **WebUI** for interactive review and a powerful **CLI** for automation and standalone inspection.

## 1. WebUI Workflow (Recommended)

The WebUI is designed for high-concurrency homelab environments and provides the best experience for reviewing evidence.

### Dashboard
Start at the **Dashboard** to see the overall health of your services. Ensure **Calibre CLI**, **Ollama**, and **PostgreSQL** (if using) are marked as **OK**.

### Scanning
1.  Navigate to **Scan Library**.
2.  Set the **Limit** (e.g., 100 books) and click **Start Scan**.
3.  The system will use `calibredb` to discover books and record their current metadata in the auditor's database.

### Auditing
Once a scan is complete, click **Start Audit** for that run.
- The auditor will extract text snippets from the book files.
*   It will query **OpenLibrary** and **Google Books** for matching candidates.
- The **LLM Judge** will evaluate the evidence and candidates to produce a structured verdict.

### Review Queue
Navigate to **Review Queue** to see books that require manual confirmation.
- **Evidence Ladder**: See exactly which snippet (e.g., Copyright page) matches a candidate.
- **Risk Flags**: The system will warn you about "Author Swaps" or "ISBN Mismatches".
- **Approve/Reject**: Click **Approve** to queue a change for application.

### Applying Changes
Navigate to **Changes & Undo**. Review your approved patches and click **Apply** (requires write mode to be enabled). The system will automatically create an **OPF Backup** before modifying your library.

---

## 2. CLI Workflow

The CLI is ideal for standalone inspection of new files before they enter your library.

### System Check
Verify your environment and dependencies:
```bash
bookaudit doctor
```

### Standalone File Inspection
Extract metadata and snippets from an EPUB or PDF without adding it to Calibre:
```bash
bookaudit inspect --path "/path/to/book.epub"
```

### Library Management
Run a scan and audit entirely from the terminal:
```bash
# Scan 10 books
bookaudit scan --limit 10
# Audit the latest run
bookaudit audit --run latest
# Export a summary report
bookaudit report --run latest --format markdown
```

---

## 3. Configuration & Safety

### Read-Only Mode
By default, the app is in **Read-Only Mode**. You can browse, scan, and audit safely. To enable writing:
1. Update your `.env` or `config.yml`: `BOOKAUDIT_READ_ONLY=false`.
2. Ensure your Docker volume for `/library` is **not** marked as `:ro`.

### Privacy Filters
Control what data is sent to remote LLMs (e.g., OpenAI):
- `allow_remote_text`: Set to `false` to block sending snippets to remote models.
- `max_remote_chars`: Caps the length of text sent to remote models.
- `allow_remote_images`: Block sending cover images to remote vision models.
