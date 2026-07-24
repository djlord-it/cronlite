-- Lightweight web admin sessions.

CREATE TABLE admin_sessions (
    token_hash TEXT PRIMARY KEY,
    api_key_id UUID NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    csrf_token TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX idx_admin_sessions_api_key_id ON admin_sessions(api_key_id);
CREATE INDEX idx_admin_sessions_expires_at ON admin_sessions(expires_at);
