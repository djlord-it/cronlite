package main

import (
	"bytes"
	"io"
	"log"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/djlord-it/cronlite/internal/config"
	"github.com/djlord-it/cronlite/internal/cron"
	"github.com/djlord-it/cronlite/internal/service"
	"github.com/djlord-it/cronlite/internal/store/postgres"
)

func TestLogAdminStartupReportsSessionPolicyWithoutSecrets(t *testing.T) {
	const bootstrapToken = "bootstrap-token-must-not-appear"
	cfg := config.Config{
		AdminBootstrapToken:     bootstrapToken,
		AdminSessionTTL:         30 * time.Minute,
		AdminSessionAbsoluteTTL: 12 * time.Hour,
		AdminCookieSecure:       true,
	}

	var output bytes.Buffer
	originalWriter := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(originalWriter) })

	logAdminStartup(cfg)

	got := output.String()
	for _, want := range []string{
		"idle_session_ttl=30m",
		"absolute_session_ttl=12h",
		"secure_cookie=true",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("admin startup log missing %q: %s", want, got)
		}
	}
	if strings.Contains(got, bootstrapToken) || strings.Contains(strings.ToLower(got), "bootstrap") {
		t.Fatalf("admin startup log exposed bootstrap configuration: %s", got)
	}
}

func TestRunServeHelpDocumentsAdminSessionLifetimesWithoutSecrets(t *testing.T) {
	const bootstrapToken = "bootstrap-token-must-not-appear"
	t.Setenv("ADMIN_BOOTSTRAP_TOKEN", bootstrapToken)

	originalArgs := os.Args
	os.Args = []string{"cronlite", "serve", "--help"}
	t.Cleanup(func() { os.Args = originalArgs })

	originalStdout := os.Stdout
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout pipe: %v", err)
	}
	os.Stdout = writeEnd
	t.Cleanup(func() { os.Stdout = originalStdout })

	exitCode := runServe()
	if err := writeEnd.Close(); err != nil {
		t.Fatalf("close stdout pipe: %v", err)
	}
	output, err := io.ReadAll(readEnd)
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}

	if exitCode != exitSuccess {
		t.Fatalf("runServe --help exit code = %d, want %d", exitCode, exitSuccess)
	}
	help := string(output)
	for _, want := range []string{
		"ADMIN_BOOTSTRAP_TOKEN",
		"Secret required by /admin/setup when no API key exists",
		"ADMIN_SESSION_TTL",
		"Idle admin session timeout (default: \"30m\")",
		"ADMIN_SESSION_ABSOLUTE_TTL",
		"Maximum admin session lifetime (default: \"12h\")",
	} {
		if !strings.Contains(help, want) {
			t.Errorf("serve help missing %q:\n%s", want, help)
		}
	}
	if strings.Contains(help, bootstrapToken) {
		t.Fatal("serve help exposed the configured bootstrap token")
	}
}

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
