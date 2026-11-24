-- The crash-loop guard (B4): a book a worker has taken is marked in hand
-- until its result is stored or the batch is interrupted in an orderly way.
-- A book still marked at the next start was in hand when the process died;
-- its crashes go up by one, and at two it is failed as process_crashed
-- instead of being inspected again.
ALTER TABLE run_books
    ADD COLUMN in_hand_since timestamptz,
    ADD COLUMN crashes integer NOT NULL DEFAULT 0 CHECK (crashes >= 0);
