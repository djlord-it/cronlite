-- schema/005_drop_scopes.sql
-- Remove unused scopes column from api_keys table.
-- The scopes field was never enforced by any authorization logic.

ALTER TABLE api_keys DROP COLUMN scopes;
