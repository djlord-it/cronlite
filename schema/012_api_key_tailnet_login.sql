BEGIN;

-- Keys created for Tailscale identity sign-in to the admin UI: one per
-- (namespace, Tailscale login). Their token is never shown, so they cannot
-- be used with the API.
ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS tailnet_login TEXT;
CREATE UNIQUE INDEX IF NOT EXISTS idx_api_keys_namespace_tailnet_login
    ON api_keys (namespace, tailnet_login) WHERE tailnet_login IS NOT NULL;

COMMIT;
