-- 001_initial.sql
-- PostgreSQL schema for cronlite

CREATE TABLE schedules (
    id UUID PRIMARY KEY,
    cron_expression TEXT NOT NULL,
    timezone TEXT NOT NULL DEFAULT 'UTC',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE jobs (
    id UUID PRIMARY KEY,
    project_id UUID NOT NULL,
    name TEXT NOT NULL,
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
    project_id UUID NOT NULL,
    scheduled_at TIMESTAMPTZ NOT NULL,
    fired_at TIMESTAMPTZ NOT NULL,
    status TEXT NOT NULL,
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
