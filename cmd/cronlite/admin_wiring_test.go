package main

import (
	"testing"
	"time"

	"github.com/djlord-it/cronlite/internal/config"
	"github.com/djlord-it/cronlite/internal/cron"
	"github.com/djlord-it/cronlite/internal/service"
	"github.com/djlord-it/cronlite/internal/store/postgres"
)

func TestNewWebAdminConfigWiresRuntimeConfiguration(t *testing.T) {
	cfg := config.Config{
		AdminBootstrapToken:     "bootstrap-token",
		AdminSessionTTL:         45 * time.Minute,
		AdminSessionAbsoluteTTL: 9 * time.Hour,
		AdminCookieSecure:       true,
	}
	jobService := service.NewJobService(nil, nil, nil, nil, nil, nil, cron.NewParser())
	var store *postgres.Store

	got := newWebAdminConfig(cfg, jobService, store)

	if got.Service != jobService {
		t.Fatal("admin service dependency was not wired")
	}
	if got.Sessions != store {
		t.Fatal("admin session repository dependency was not wired")
	}
	if got.Keys != store {
		t.Fatal("admin key lookup dependency was not wired")
	}
	if got.BootstrapToken != cfg.AdminBootstrapToken {
		t.Fatal("admin bootstrap token was not wired")
	}
	if got.SessionTTL != cfg.AdminSessionTTL {
		t.Fatalf("session TTL = %s, want %s", got.SessionTTL, cfg.AdminSessionTTL)
	}
	if got.SessionAbsoluteTTL != cfg.AdminSessionAbsoluteTTL {
		t.Fatalf("absolute session TTL = %s, want %s", got.SessionAbsoluteTTL, cfg.AdminSessionAbsoluteTTL)
	}
	if got.CookieSecure != cfg.AdminCookieSecure {
		t.Fatal("admin cookie security setting was not wired")
	}
	if got.Logger == nil {
		t.Fatal("admin logger was not wired")
	}
}
