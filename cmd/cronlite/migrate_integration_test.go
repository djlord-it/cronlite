//go:build integration

package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"io"
	"net/url"
	"os"
	"strings"
	"testing"
)

func newMigrateTestDB(t *testing.T) string {
	t.Helper()
	adminURL := os.Getenv("MIGRATE_TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("MIGRATE_TEST_DATABASE_URL is not set")
	}
	admin, err := sql.Open("postgres", adminURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		t.Fatal(err)
	}
	name := "cronlite_cli_" + hex.EncodeToString(buf)
	if _, err := admin.ExecContext(context.Background(), "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() {
		if _, err := admin.ExecContext(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			t.Errorf("drop database: %v", err)
		}
	})
	u, err := url.Parse(adminURL)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	return u.String()
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	_ = w.Close()
	os.Stdout = orig
	return <-done
}

func TestRunMigrateAppliesAndStatus(t *testing.T) {
	t.Setenv("DATABASE_URL", newMigrateTestDB(t))

	if got := runMigrate(nil); got != exitSuccess {
		t.Fatalf("runMigrate(nil) = %d, want %d", got, exitSuccess)
	}

	var code int
	out := captureStdout(t, func() { code = runMigrate([]string{"--status"}) })
	if code != exitSuccess {
		t.Fatalf("runMigrate(--status) = %d", code)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 11 {
		t.Fatalf("got %d status lines, want 11:\n%s", len(lines), out)
	}
	for _, l := range lines {
		if !strings.Contains(l, "applied") {
			t.Errorf("line %q does not contain \"applied\"", l)
		}
	}
}
