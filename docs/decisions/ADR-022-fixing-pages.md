# ADR-022: Drive the guarded writer from the pages, in the background, one book at a time

- Status: Accepted (M4.2)
- Date: 2026-10-19
- Decision: approved in the M4.2 implementation plan

## Context

M4.1 (ADR-021) built the writer: approvals, the stale check, the write-ahead
ledger, readback, restore, incidents, undo and the writes switch, with no
pages. Spec sections 8 and 11 put it in front of Felix: the Fixes page with
ticks and approve/reject, History with undo, Incidents, and Settings with the
switch. Applying many fixes takes longer than a page request, the content
server can be slow, and the container can be stopped at any moment.

## Decision

**One admin.** Approvals, applies, undos, acknowledgements and switch changes
are recorded as done by `admin`: there is exactly one admin account, and its
session carries only a user id.

**Unwritable proposals can't be approved.** The page runs the writer's rules
on the proposed value and on Calibre's value as the check read it. A field
Calibre would store differently, or whose current value it couldn't put back,
shows the reason instead of a tick, and approving it fails. The shared-name
check needs Calibre, so it still runs only when applying.

**Apply runs in the background.** "Apply approved" queues one request for the
writer's own goroutine and returns at once. A second request while one is
queued or running is refused, as are requests while writes are off or
paused. History shows whether an apply is running and the last result.

**A stop finishes the current book.** Stopping the process cancels the
writer's context, which the apply loop checks only between books. Each book's
stale check, write, readback and restore run under a context that isn't
cancelled, bounded by the writer's per-call deadlines. Undo and recovery run
their writes the same way, so a closed browser tab can't interrupt an
operation either.

**Recovery at startup.** The writer recovers unfinished operations once
before it takes requests. If another instance holds the writer lock or
Calibre is down, it logs that; the next writer session tries again.

## Consequences

- A stop waits for the current book, up to the writer's deadlines (30 s per
  read, 2 minutes per write), but never longer than `BOOKWARDEN_STOP_GRACE`
  (default 25 s). The writer is never cancelled mid-operation: past the grace
  the process logs what is still running, gives the work 2 more seconds, then
  exits with the database pool left open and the operation abandoned. The
  ledger recovers it at the next start, as after a crash. Docker's default
  10-second stop timeout would cut even the grace short, so the Unraid
  template sets `--stop-timeout 30`, at least 5 seconds above the grace. The
  check runner follows the same rule: a stop lets the books in hand finish
  and starts no new one; past the grace its work is cancelled and the book
  stays pending.
- Two quick clicks on "Apply approved" start one apply.
- The ledger can't tell actions apart by person; with one admin, it doesn't
  need to.
