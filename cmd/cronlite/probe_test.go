package main

import (
	"database/sql"
	"github.com/google/uuid"
	"net/url"
	"os"
	"strings"
	"testing"
)

// TestProbeClaimedAtColumn_NoConnection verifies that probeExecutionOwnership
// returns an error when the database is unreachable (no valid connection).
// This covers the failure path without requiring a running Postgres instance.
func TestProbeClaimedAtColumn_NoConnection(t *testing.T) {
	// Open a DB handle with an invalid DSN — no actual connection is made
	// until QueryRow, so sql.Open itself won't fail.
	db, err := sql.Open("postgres", "postgres://invalid:invalid@localhost:1/nonexistent?sslmode=disable")
	if err != nil {
		t.Fatalf("sql.Open failed unexpectedly: %v", err)
	}
	defer db.Close()

	err = probeExecutionOwnership(db)
	if err == nil {
		t.Fatal("expected probeExecutionOwnership to return an error for unreachable DB, got nil")
	}
}

func TestProbeExecutionOwnershipRespectsSearchPath(t *testing.T) {
	dsn := os.Getenv("CRONLITE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("PostgreSQL DSN required")
	}
	base, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	schema := "ownership_probe_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := base.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := base.Exec("DROP SCHEMA " + schema + " CASCADE"); err != nil {
			t.Errorf("drop probe schema: %v", err)
		}
	}()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db, err := sql.Open("postgres", u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE executions(claimed_at timestamptz, claim_generation bigint, lease_until timestamptz); CREATE TABLE delivery_attempts(claim_generation bigint)"); err != nil {
		t.Fatal(err)
	}
	if err := probeExecutionOwnership(db); err != nil {
		t.Fatal("non-public schema rejected", err)
	}
	if _, err := db.Exec("ALTER TABLE executions DROP COLUMN lease_until"); err != nil {
		t.Fatal(err)
	}
	if err := probeExecutionOwnership(db); err == nil || !strings.Contains(err.Error(), "011") {
		t.Fatalf("missing migration accepted: %v", err)
	}
}

// Integration tests for probeExecutionOwnership with a real database:
//
// - With migration 003 applied: probeExecutionOwnership(db) should return nil.
// - Without migration 003: probeExecutionOwnership(db) should return sql.ErrNoRows.
//
// These are covered by the HA test harness (docker-compose.ha-test.yml) which
// runs the full migration set. A standalone integration test would require
// spinning up Postgres, which is out of scope for unit tests.
