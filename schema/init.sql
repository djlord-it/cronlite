-- init.sql — Fresh schema for Railway Postgres (combines all migrations)
-- Run once: psql $DATABASE_URL < schema/init.sql

CREATE TABLE schedules (
    id UUID PRIMARY KEY,
    cron_expression TEXT NOT NULL,
    timezone TEXT NOT NULL DEFAULT 'UTC',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE jobs (
    id UUID PRIMARY KEY,
    name TEXT NOT NULL,
    namespace TEXT NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT true,
    schedule_id UUID NOT NULL REFERENCES schedules(id),
    delivery_type TEXT NOT NULL,
    webhook_url TEXT NOT NULL,
    secret TEXT NOT NULL,
    timeout_ms BIGINT NOT NULL,
    analytics_enabled BOOLEAN NOT NULL DEFAULT false,
    analytics_retention_seconds INT NOT NULL DEFAULT 86400,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE executions (
    id UUID PRIMARY KEY,
    job_id UUID NOT NULL REFERENCES jobs(id),
    namespace TEXT NOT NULL,
    scheduled_at TIMESTAMPTZ NOT NULL,
    fired_at TIMESTAMPTZ NOT NULL,
    status TEXT NOT NULL,
    trigger_type TEXT NOT NULL DEFAULT 'scheduled',
    claimed_at TIMESTAMPTZ,
    acknowledged_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (job_id, scheduled_at)
);

CREATE TABLE delivery_attempts (
    id UUID PRIMARY KEY,
    execution_id UUID NOT NULL REFERENCES executions(id),
    attempt INT NOT NULL,
    status_code INT NOT NULL,
    error TEXT NOT NULL DEFAULT '',
    started_at TIMESTAMPTZ NOT NULL,
    finished_at TIMESTAMPTZ NOT NULL
);

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

CREATE TABLE tags (
    job_id UUID NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    key TEXT NOT NULL,
    value TEXT NOT NULL,
    PRIMARY KEY (job_id, key)
);

-- Indexes
CREATE INDEX idx_jobs_enabled ON jobs(enabled);
CREATE INDEX idx_jobs_namespace ON jobs(namespace);
CREATE INDEX idx_executions_status_created_at ON executions(status, created_at);
CREATE INDEX idx_executions_job_id ON executions(job_id);
CREATE INDEX idx_delivery_attempts_execution_id ON delivery_attempts(execution_id);
CREATE INDEX idx_api_keys_namespace ON api_keys(namespace);
CREATE INDEX idx_tags_key_value ON tags(key, value);
CREATE INDEX idx_executions_pending_ack
    ON executions(namespace, created_at DESC)
    WHERE acknowledged_at IS NULL AND status IN ('delivered', 'failed');
