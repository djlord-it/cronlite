-- Admin control center; apply after 009. Additive and safe to reapply.
BEGIN;
CREATE TABLE IF NOT EXISTS admin_saved_views (
 id UUID PRIMARY KEY,
 api_key_id UUID NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
 namespace TEXT NOT NULL,
 name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 80),
 query TEXT NOT NULL CHECK (length(query) <= 2000),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 UNIQUE (api_key_id, namespace, name)
);
CREATE TABLE IF NOT EXISTS admin_bulk_batches (
 id UUID PRIMARY KEY,
 -- Retain actor ID and audit history even after API key deletion.
 api_key_id UUID NOT NULL,
 namespace TEXT NOT NULL,
 action TEXT NOT NULL CHECK (action IN ('pause', 'resume')),
 items JSONB NOT NULL CHECK (jsonb_array_length(items) BETWEEN 1 AND 100),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 expires_at TIMESTAMPTZ NOT NULL,
 confirmed_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_admin_bulk_actor ON admin_bulk_batches(api_key_id, namespace, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_admin_bulk_expiry ON admin_bulk_batches(expires_at) WHERE confirmed_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_jobs_namespace_created ON jobs(namespace, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_executions_namespace_created ON executions(namespace, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_attempts_execution_finished ON delivery_attempts(execution_id, finished_at DESC, id DESC);
COMMIT;
