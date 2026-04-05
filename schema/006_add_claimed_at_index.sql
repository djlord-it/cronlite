-- 006_add_claimed_at_index.sql
-- Partial index for the reconciler's stale execution requeue query.
-- The query filters on claimed_at for rows WHERE status = 'in_progress'.
-- Without this index, the query must sequentially scan all in_progress rows.
--
-- NOTE: This uses a regular CREATE INDEX (not CONCURRENTLY) because the project
-- applies migrations via psql or Docker initdb, both of which wrap files in an
-- implicit transaction. CREATE INDEX CONCURRENTLY cannot run inside a transaction.
-- For large production tables where locking is a concern, run this manually:
--   CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_executions_in_progress_claimed_at
--       ON executions(claimed_at) WHERE status = 'in_progress';

CREATE INDEX IF NOT EXISTS idx_executions_in_progress_claimed_at
    ON executions(claimed_at)
    WHERE status = 'in_progress';
