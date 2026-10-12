-- Stop/drain ALL old CronLite writers before activating the new protocol.
-- Additive and restart-safe: legacy rows remain generation 0 with no lease.
-- The new reconciler recovers legacy in_progress rows in bounded batches.
BEGIN;
ALTER TABLE executions ADD COLUMN IF NOT EXISTS claim_generation BIGINT NOT NULL DEFAULT 0;
ALTER TABLE executions ADD COLUMN IF NOT EXISTS lease_until TIMESTAMPTZ;
ALTER TABLE delivery_attempts ADD COLUMN IF NOT EXISTS claim_generation BIGINT NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS idx_executions_in_progress_lease
    ON executions (lease_until) WHERE status = 'in_progress';
COMMIT;
