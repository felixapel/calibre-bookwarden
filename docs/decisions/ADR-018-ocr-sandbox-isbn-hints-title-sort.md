# ADR-018: Sandbox PDF ISBN hints and check title sort against Calibre's rule

- Status: Accepted
- Date: 2026-10-18
- Decision: approved in the M2.4 implementation plan

## Context

M2 must finish three things the spec carried forward: OCR of PDFs without a
text layer, a decision on whether ISBNs read from a PDF's text layer count as
file evidence, and a proven title-sort snapshot and policy. OCR needs native
tools (`pdftoppm`, `tesseract`) that parse hostile files; Landlock is not
available on Unraid's kernel.

## Decision

**Sandbox.** Native tools run in a child re-executed from the bookwarden binary.
Before `execve` the launcher sets `PR_SET_NO_NEW_PRIVS`, rlimits (CPU 60 s,
address space 768 MiB, file size 64 MiB, 64 open files, no core dumps) and a
pure-Go seccomp filter (amd64 only). The filter kills on a foreign audit arch or
an x32 syscall number; returns `EPERM` for `socket`, `socketpair`, `connect`,
`bind`, `io_uring_setup/enter/register` (io_uring can open sockets without
`socket(2)`), `ptrace`, `process_vm_readv/writev`, `pidfd_getfd`, `mount`,
`fork`, `vfork` and `clone` without
`CLONE_THREAD`; and returns `ENOSYS` for `clone3`. The child inherits no
environment: it gets exactly `OMP_THREAD_LIMIT=1`, which keeps tesseract to one
thread so the CPU and memory limits hold. The child runs in its own process
group with a parent-death signal and is killed at its deadline. Before its
first child, bookwarden makes itself non-dumpable: the tool runs as the same
UID, and would otherwise be able to read bookwarden's startup environment
(the database URL and the Calibre password) from `/proc`, and on kernels
without Yama, such as Unraid 7.3's 6.18 (checked 2026-10-18), take its open
sockets or write its memory. A non-dumpable process needs `CAP_SYS_PTRACE`
for any of that, which the container drops. Stdout is capped
at 1 MiB in memory; stderr is discarded. On other platforms OCR is unavailable.

**ISBN hints.** A readable, unencrypted PDF whose text layer is present gets
hints from `pdftotext`; one whose text layer is absent gets OCR (`pdftoppm`
gray PNG with the longest side 3000 px, then `tesseract -l eng --psm 3`). Both
read pages 1–5 and the last 5; an implausible page count reads 1–5. Only
checksum-valid ISBNs are kept, as canonical ISBN-13, at most 16; no text is
stored. One sandboxed child runs at a time across the process, and each book
has 180 s of tool time, counted only while its child holds the slot. The state
is `ok`, `timeout`, `failed` or `unavailable`. A startup self-check OCRs an
embedded PDF; if it fails, hints are `unavailable` and a warning is logged.

Text-layer ISBNs are hints of the same class as OCR, never authority: the
glyph-to-Unicode mapping is producer-controlled and scanned PDFs often carry a
hidden OCR layer. Info and XMP ISBNs remain the internal `file` root. Hints
create no findings in M2.4; M3 uses them only to seed exact-ISBN lookups.

**Title sort.** The snapshot records Calibre's `title_sort` from `/ajax/books`
as an optional field; packages without it seal unchanged. The expected value
is a port of Calibre 9.16's `title_sort` with default tweaks (per-language
article table, quote-pair stripping, Python's Unicode whitespace), using the
book's first language with Calibre's English fallback. Findings version 3 adds
`title_sort_missing`, `title_sort_article_not_moved` (info) and
`title_sort_mismatch`; a sort matching any other table language's result is
accepted, because Calibre-Web and non-English Calibre installs use other
article sets. Versions 1 and 2 keep their historical derivations.

## Consequences

- A correct copyright-page ISBN cannot make a book Tier A on its own; it needs
  a source record to confirm it.
- Poppler's and tesseract's C code is exposed to hostile PDFs, contained by
  the sandbox; the denylist is not an allowlist, so a compromised tool can
  still read files its UID can read inside the container, and can still
  disrupt bookwarden: signal it (a SIGTERM stops it gracefully) or fill the
  scratch tmpfs with 64 MiB files. It cannot reach the network, the
  database or Calibre credentials, or bookwarden's memory.
- Libraries with many scanned PDFs make long runs, since tool work is serial.
  Reusing hints by file hash is deferred until the shadow run measures it.
- Encrypted PDFs and PDFs with an unverified text layer get no hints.
- A title sort that is wrong for its book's language but right for another
  table language is accepted.

> Amended 2026-11-22 (B9): the filter also returns `EPERM` for `unshare`, `setns`, `keyctl`, `add_key`, `request_key`, `bpf`, `userfaultfd` and `perf_event_open`, so a host without Docker's seccomp profile doesn't leave them open; `execve` (the filter is installed before the tool starts) and `kill`/`tgkill` (`abort()`) stay allowed.

> Amended 2026-11-24: the rest of the mount API is refused with `EPERM` too: `umount2`, `pivot_root`, `open_tree`, `open_tree_attr`, `move_mount`, `fsopen`, `fsconfig`, `fsmount`, `fspick` and `mount_setattr`, so a container started with `CAP_SYS_ADMIN` doesn't open them to the tools.
