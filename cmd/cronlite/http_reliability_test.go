package main

import (
	"net"
	"os"
	"testing"
)

func TestServeRejectsOccupiedListener(t *testing.T) {
	dsn := os.Getenv("CRONLITE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("CRONLITE_TEST_DATABASE_URL is required")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	t.Setenv("DATABASE_URL", dsn)
	t.Setenv("HTTP_ADDR", listener.Addr().String())
	t.Setenv("DISPATCH_MODE", "channel")
	t.Setenv("METRICS_ENABLED", "false")
	t.Setenv("ADMIN_ENABLED", "false")
	t.Setenv("REDIS_ADDR", "")
	if got := runServe(); got != exitRuntimeError {
		t.Fatalf("occupied listener returned exit code %d", got)
	}
}
