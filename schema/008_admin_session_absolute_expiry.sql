BEGIN;

ALTER TABLE admin_sessions ADD COLUMN absolute_expires_at TIMESTAMPTZ;
UPDATE admin_sessions SET absolute_expires_at = GREATEST(expires_at, created_at + INTERVAL '12 hours');
ALTER TABLE admin_sessions ALTER COLUMN absolute_expires_at SET NOT NULL;
ALTER TABLE admin_sessions ALTER COLUMN absolute_expires_at SET DEFAULT (CURRENT_TIMESTAMP + INTERVAL '12 hours');
ALTER TABLE admin_sessions ADD CONSTRAINT admin_sessions_expiry_order CHECK (expires_at <= absolute_expires_at);
CREATE INDEX idx_admin_sessions_absolute_expires_at ON admin_sessions(absolute_expires_at);

COMMIT;
