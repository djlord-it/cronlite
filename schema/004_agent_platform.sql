-- schema/004_agent_platform.sql
-- Agent Platform: API keys, tags, namespace, execution ack/trigger

-- 1. API keys table
CREATE TABLE api_keys (
    id UUID PRIMARY KEY,
    namespace TEXT NOT NULL,
    token_hash TEXT NOT NULL UNIQUE,
    label TEXT NOT NULL DEFAULT '',
    scopes TEXT[] NOT NULL DEFAULT '{}',
    enabled BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_used_at TIMESTAMPTZ
);

CREATE INDEX idx_api_keys_namespace ON api_keys(namespace);

-- 2. Tags table (key-value pairs per job)
CREATE TABLE tags (
    job_id UUID NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    key TEXT NOT NULL,
    value TEXT NOT NULL,
    PRIMARY KEY (job_id, key)
);

CREATE INDEX idx_tags_key_value ON tags(key, value);

-- 3. Add namespace to jobs, migrate data, drop project_id
ALTER TABLE jobs ADD COLUMN namespace TEXT;
UPDATE jobs SET namespace = 'default' WHERE namespace IS NULL;
ALTER TABLE jobs ALTER COLUMN namespace SET NOT NULL;
ALTER TABLE jobs DROP COLUMN project_id;
CREATE INDEX idx_jobs_namespace ON jobs(namespace);

-- 4. Add namespace to executions, migrate data, drop project_id
ALTER TABLE executions ADD COLUMN namespace TEXT;
UPDATE executions SET namespace = 'default' WHERE namespace IS NULL;
ALTER TABLE executions ALTER COLUMN namespace SET NOT NULL;
ALTER TABLE executions DROP COLUMN project_id;

-- 5. Add trigger_type and acknowledged_at to executions
ALTER TABLE executions ADD COLUMN trigger_type TEXT NOT NULL DEFAULT 'scheduled';
ALTER TABLE executions ADD COLUMN acknowledged_at TIMESTAMPTZ;

-- 6. Partial index for pending-ack polling
CREATE INDEX idx_executions_pending_ack
    ON executions(namespace, created_at DESC)
    WHERE acknowledged_at IS NULL AND status IN ('delivered', 'failed');

-- 7. Drop old project_id index (now replaced by namespace index)
DROP INDEX IF EXISTS idx_jobs_project_id;
