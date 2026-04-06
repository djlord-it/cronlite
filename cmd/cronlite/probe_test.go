package main

import (
	"database/sql"
	"testing"
)

// TestProbeClaimedAtColumn_NoConnection verifies that probeClaimedAtColumn
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

	err = probeClaimedAtColumn(db)
	if err == nil {
		t.Fatal("expected probeClaimedAtColumn to return an error for unreachable DB, got nil")
	}
}

// Integration tests for probeClaimedAtColumn with a real database:
//
// - With migration 003 applied: probeClaimedAtColumn(db) should return nil.
// - Without migration 003: probeClaimedAtColumn(db) should return sql.ErrNoRows.
//
// These are covered by the HA test harness (docker-compose.ha-test.yml) which
// runs the full migration set. A standalone integration test would require
// spinning up Postgres, which is out of scope for unit tests.
