//go:build integration

package webadmin

import (
	"database/sql"
	"os"
	"strings"
	"testing"
)

func TestIntegrationAdminSessionMigrationRepairAndAtomicity(t *testing.T) {
	db := integrationDB(t)
	migration, err := os.ReadFile("../../schema/007_admin_sessions.sql")
	if err != nil {
		t.Fatalf("read migration 007: %v", err)
	}

	t.Run("repairs a missing index when the table already exists", func(t *testing.T) {
		integrationExec(t, db, `DROP INDEX IF EXISTS idx_admin_sessions_expires_at`)
		integrationExec(t, db, string(migration))

		var indexCount int
		integrationScanRow(t, db, `
SELECT COUNT(*)
FROM pg_indexes
WHERE schemaname = 'public'
  AND tablename = 'admin_sessions'
  AND indexname IN (
    'idx_admin_sessions_api_key_id',
    'idx_admin_sessions_expires_at'
  )
`, nil, &indexCount)
		if indexCount != 2 {
			t.Fatalf("migration 007 admin session indexes = %d, want 2", indexCount)
		}
	})

	t.Run("rolls back a partially applied migration", func(t *testing.T) {
		const schemaName = "admin_migration_007_atomicity"
		integrationExec(t, db, `DROP SCHEMA IF EXISTS `+schemaName+` CASCADE`)
		t.Cleanup(func() {
			integrationExec(t, db, `DROP SCHEMA IF EXISTS `+schemaName+` CASCADE`)
		})
		integrationExec(t, db, `CREATE SCHEMA `+schemaName)
		integrationExec(t, db, `
CREATE TABLE `+schemaName+`.api_keys (
    id UUID PRIMARY KEY
)
`)

		ctx, cancel := integrationContext(t)
		conn, err := db.Conn(ctx)
		cancel()
		if err != nil {
			t.Fatalf("reserve migration connection: %v", err)
		}
		defer conn.Close()

		ctx, cancel = integrationContext(t)
		if _, err := conn.ExecContext(
			ctx,
			`SET search_path TO `+schemaName,
		); err != nil {
			cancel()
			t.Fatalf("set migration search path: %v", err)
		}
		cancel()

		failingMigration := strings.Replace(
			string(migration),
			"\nCOMMIT;",
			"\nSELECT * FROM migration_007_forced_failure;\n\nCOMMIT;",
			1,
		)
		if failingMigration == string(migration) {
			t.Fatal("migration 007 has no COMMIT boundary for failure injection")
		}

		ctx, cancel = integrationContext(t)
		_, migrationErr := conn.ExecContext(ctx, failingMigration)
		cancel()
		if migrationErr == nil {
			t.Fatal("forced migration statement unexpectedly succeeded")
		}

		ctx, cancel = integrationContext(t)
		if _, err := conn.ExecContext(ctx, "ROLLBACK"); err != nil {
			cancel()
			t.Fatalf("roll back failed migration: %v", err)
		}
		cancel()

		var tableName sql.NullString
		integrationScanRow(
			t,
			db,
			`SELECT to_regclass($1)`,
			[]any{schemaName + ".admin_sessions"},
			&tableName,
		)
		if tableName.Valid {
			t.Fatalf(
				"failed migration left partial table behind: %s",
				tableName.String,
			)
		}

		ctx, cancel = integrationContext(t)
		if _, err := conn.ExecContext(ctx, string(migration)); err != nil {
			cancel()
			t.Fatalf("retry migration 007 after rollback: %v", err)
		}
		cancel()

		integrationScanRow(
			t,
			db,
			`SELECT to_regclass($1)`,
			[]any{schemaName + ".admin_sessions"},
			&tableName,
		)
		if !tableName.Valid {
			t.Fatal("migration retry did not create admin_sessions")
		}

		var indexCount int
		integrationScanRow(t, db, `
SELECT COUNT(*)
FROM pg_indexes
WHERE schemaname = $1
  AND tablename = 'admin_sessions'
  AND indexname IN (
    'idx_admin_sessions_api_key_id',
    'idx_admin_sessions_expires_at'
  )
`, []any{schemaName}, &indexCount)
		if indexCount != 2 {
			t.Fatalf("migration retry indexes = %d, want 2", indexCount)
		}
	})

	if !t.Failed() {
		t.Log("ADMIN_INTEGRATION_OK")
	}
}
