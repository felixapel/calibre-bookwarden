# ADR-021: Write approved proposals through a guarded, write-ahead ledger

- Status: Accepted (M4.1)
- Date: 2026-10-19
- Decision: approved in the M4.1 implementation plan
- Amended by [ADR-029](ADR-029-agent-fixing.md): a unanimous three-model panel may approve or reject undecided proposals; its approvals go through the same Apply. Since 2026-11-04 the panel no longer approves or rejects anything (mode on retired); only a person's approval is applied

## Context

Spec section 8 has bookwarden write approved fields to Calibre one book at a
time, under one writer, with a write-ahead ledger, a stale check, readback,
restore with an incident on a mismatch, and undo. Calibre 9.16's own code
(`srv/cdb.py`, `db/write.py`, `db/cache.py`, `utils/date.py`) adds facts the
spec doesn't name:

- set-fields applies its changes in body order and answers the book as JSON.
  Every effective change moves `last_modified`, a sort-only edit included; a
  value equal to the current one is skipped.
- Writing `title` recomputes the title sort and writing `authors` recomputes
  the author sort. Both rename the book's folder. An author no book uses any
  more is deleted.
- `identifiers` replaces the whole map.
- `pubdate` goes through `parse_only_date`: a date alone is stored as midnight
  UTC, day 1 becomes day 2, and the last day of every month except December
  moves back a day, for timestamps too (on their UTC date).
- Text is stripped and whitespace collapses. In authors `&` becomes `;` and
  `|` becomes `,`.
- Authors and publishers are items shared between books, keyed by ICU lower
  case. Writing a name that differs from an existing one only in case renames
  it for every book, and set-fields can't turn that off.

## Decision

**Approval.** A `proposals` row per (run, book, field) holds approved or
rejected, with the values copied from the verified sealed decision, never from
a request. Only the newest checked evidence for a book can be decided.
Applied, stale, blocked and failed are final for that evidence.

**Stale check.** Before writing, the book is read again. It is stale when it
is gone, its UUID differs, a newer run checked it, its record (all fields,
`last_modified` and title sort included) or its format snapshot differs from
the sealed one, or a stored proposal differs from the sealed decision. A stale
book writes nothing and records no operation.

**Writability.** A field is written only when Calibre stores both the approved
value and the current value exactly, so the readback can pass and undo can
restore, and, for authors and publisher, when no existing name differs from a
written one only in case. Otherwise it is blocked with a reason; the book's
other fields still apply. Undo and restore apply the same checks to what they
write.

**Changes.** An ordered list of Calibre field names and JSON values. An ISBN
is written as the full identifiers map with `isbn` replaced; a date as
`YYYY-MM-DD`; languages as ISO 639-3 codes. Restores write before values in
pairs, title then sort and authors then author sort, so Calibre's
recomputation is overwritten by the exact before value.

**Ledger.** `operations` holds an immutable header (kind, the book, the
changes, the before record) and `operation_events` the states, append-only;
triggers reject updates and deletes on both and on `settings`. Each state
commits before the next step: requested, writing, verifying, then completed,
or restoring and then restored or failed. Linked proposals become applied or
failed in the same transaction.

**Readback** compares every change with the set-fields answer and a fresh
read. Any set-fields error or failed read counts as a mismatch. A mismatch
opens an incident in the same transaction as `restoring`, writes back the
before values that differ, and reads back again. Every operation that doesn't
complete has exactly one incident, and an open incident pauses all writes.

**Recovery.** Each writer session first finishes unfinished operations. One
left in `requested` sent nothing and fails. One in `writing`, `verifying` or
`restoring` is restored, but only the fields that still hold the value it
wrote, each with its sort; a field at any other value is left alone and
named, and the operation fails for Felix to check. Every restore that writes
first records the book as it found it, in a second `restoring` event. A
restore Calibre refuses for good (anything but an unreachable server or a
timeout) ends `failed`, so its incident can be acknowledged; a transient error
leaves it `restoring` for the next session. Every Calibre call has a deadline
(30 s to read, 2 minutes to write), so a hung server can't hold the writer
lock.

**Undo** is its own operation, allowed on a completed operation without an
earlier undo, while the book's record equals that operation's after record.

**One writer:** a session-level advisory lock per `Apply`, `Undo` or
`Recover`. **The writes switch** is an append-only `settings` log whose newest
row wins; none means off. **Retention** skips runs that a proposal or
operation points to.

## Consequences

- Dates on the first of a month, and on the last day of any month but
  December, are never written; Calibre would store a different day.
- A title fix replaces a custom title sort with Calibre's own; undo restores
  it.
- An author that a fix leaves unused is deleted by Calibre with its own sort
  and link. Undo brings the name back, not those extras.
- After a crash, a field someone edited before the restart stays as they set
  it, and the operation ends failed for a manual check. A title or author sort
  edited alone can't be told from Calibre's recomputation, so it goes back
  with its field; the ledger keeps the record found before the restore.
- The stale check also refuses books whose files, title sort or newer checks
  changed, so some approvals need a recheck.
- Calibre's server doesn't see writes made to `metadata.db` behind its back
  (CWA), so neither does the stale check. The shadow run checks a fix in both.
- Amended 2026-10-27: Calibre 9.16 stores `last_modified` with microseconds
  and reports them from its cache right after a write. Once it reloads the
  library, its loader (`calibre.db.tables.c_parse`) truncates them, so it
  reports the same instant in whole seconds, even at .999999. So the stale check
  and undo treat a whole-second value that the recorded value truncates to
  as unchanged. Any other difference, including a later second or
  microseconds where whole seconds were recorded, is still a change
  (`TestApplyLastModified`, `TestUndoLastModified`). Only the comparison
  substitutes the recorded instant; the ledger keeps what Calibre reported.
  Before this, a Calibre restart made every earlier operation impossible to
  undo.
- Amended 2026-10-29: a restore fails closed. After a readback mismatch or
  a write error, a pair (title and sort, authors and author sort, or a field
  alone) goes back only while every field holds a value the operation knows
  (`holdsKnown`): the before value, a value it wrote, or a value Calibre
  answered with (the set-fields response or the readback). The one exception
  is a sort or author sort the operation didn't write itself, that no
  observed book carries, which is accepted while its title or authors hold a
  known value other than the before one, since Calibre recomputed it from
  them; where an observed book carries the field, the value must equal a
  known one. A pair holding a third value was edited since: it is left, named
  in `restore_incomplete: <field>`, and the operation ends failed with its
  incident. So on the write-error path a write that landed but that Calibre
  altered now stays and fails with an incident instead of being reverted.
  The crash path is unchanged: it still writes back only what holds the value
  the operation wrote. A field the operation wrote and the before record
  can't express (a title sort the book never had, or an unknown field) can't
  be put back at all: Apply blocks it up front as `calibre_cannot_restore`, a
  restore, on either path and whatever the book holds, ends failed naming it,
  and Undo refuses with a blocked error before writing anything
  (`TestHoldsKnown`, `TestMismatchRestoreLeavesLaterEdits`,
  `TestWriteErrorAfterLandedAlteredWrite`, `TestRestoreReportsWhatItCannotWrite`,
  `TestRecoverNamesUnrestorableField`, `TestUndoRefusesWhatItCannotWrite`).
  The Apply summary counts stale and blocked rows only once their `Close`
  committed.
