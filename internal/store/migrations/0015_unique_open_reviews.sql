-- At most one queued or running review per run and kind. AgentRepo.Queue
-- guarded this with WHERE NOT EXISTS alone, which two concurrent transactions
-- both pass under READ COMMITTED; the index makes the database refuse it.
--
-- Rows the race already made must not make this migration fail, and no row is
-- deleted. Per (run_id, kind) one open review is kept: a running one if there
-- is one (AgentRepo.Next resumes a running review before a queued one, and the
-- resume state is kept per review id), else the oldest. Every other open one is
-- closed as state 'stopped', the only terminal state besides 'done'. 'done'
-- would claim the review judged the run, and it did not, so 'stopped' it is.
-- The stop_reason is a new sentence: the existing ones ("agent fixing switched
-- off", "token cap reached", "failed repeatedly: ...") would each state a cause
-- that is false here.
UPDATE reviews r
   SET state = 'stopped',
       stop_reason = 'duplicate open review closed by migration 0015',
       finished_at = now()
  FROM (SELECT id, row_number() OVER (PARTITION BY run_id, kind
                                      ORDER BY (state = 'running') DESC, id) AS rn
          FROM reviews
         WHERE state IN ('queued', 'running')) ranked
 WHERE r.id = ranked.id AND ranked.rn > 1;

CREATE UNIQUE INDEX reviews_open_once ON reviews (run_id, kind) WHERE state IN ('queued', 'running');

-- Not added, on purpose:
--  * A second index for kind = 'auto'. Migration 0012 already made
--    reviews_auto_once UNIQUE (run_id) WHERE kind = 'auto', in every state, so
--    duplicate auto reviews cannot exist, finished or not.
--  * An index for the duplicate_keys self-join in the report. The primary key
--    (run_id, kind, value, book_id) already serves it: equality on the first
--    three columns, then book_id <> k.book_id from the same index.
