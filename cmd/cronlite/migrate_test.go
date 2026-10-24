package main

import "testing"

func TestRunMigrateRequiresDatabaseURL(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	if got := runMigrate(nil); got != exitInvalidConfig {
		t.Fatalf("runMigrate(nil) = %d, want %d", got, exitInvalidConfig)
	}
}
