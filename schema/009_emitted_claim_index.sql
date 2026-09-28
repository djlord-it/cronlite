-- Keep the claim and orphan lookup index limited to queued executions.
-- Terminal status updates no longer rewrite the broad status/created_at index.
CREATE INDEX IF NOT EXISTS idx_executions_emitted_created_at
    ON executions (created_at, id)
    WHERE status = 'emitted';

DROP INDEX IF EXISTS idx_executions_status_created_at;
