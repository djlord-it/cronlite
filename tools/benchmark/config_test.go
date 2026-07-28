package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestDefaultConfigIsSafeSmoke(t *testing.T) {
	cfg := defaultConfig()
	if !reflect.DeepEqual([]string{"smoke"}, cfg.Scenarios) {
		t.Fatalf("scenarios = %v", cfg.Scenarios)
	}
	if cfg.SampleCount != 10 || cfg.AllowDisruptive || cfg.AllowNonLocal {
		t.Fatalf("unsafe defaults: %+v", cfg)
	}
}

func TestValidateRejectsDisruptiveScenarioWithoutAuthorization(t *testing.T) {
	cfg := defaultConfig()
	cfg.Scenarios = []string{"duplicate-race"}
	if err := cfg.Validate(); !errors.Is(err, ErrDisruptiveNotAllowed) {
		t.Fatalf("expected disruptive error, got %v", err)
	}
}

func TestValidateRejectsRemoteTargetWithoutAuthorization(t *testing.T) {
	cfg := defaultConfig()
	cfg.BaseURL = "https://cron.example.com"
	if err := cfg.Validate(); !errors.Is(err, ErrNonLocalNotAllowed) {
		t.Fatalf("expected non-local error, got %v", err)
	}
}

func TestValidateAcceptsExplicitRemoteTarget(t *testing.T) {
	cfg := defaultConfig()
	cfg.BaseURL = "https://cron.example.com"
	cfg.AllowNonLocal = true
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate explicit remote target: %v", err)
	}
}

func TestValidateRequiresDatabaseForDiagnosticMode(t *testing.T) {
	cfg := defaultConfig()
	cfg.Diagnostic = true
	if err := cfg.Validate(); !errors.Is(err, ErrDatabaseURLRequired) {
		t.Fatalf("expected database URL error, got %v", err)
	}
}

func TestValidateRejectsUnknownScenario(t *testing.T) {
	cfg := defaultConfig()
	cfg.Scenarios = []string{"made-up"}
	if err := cfg.Validate(); !errors.Is(err, ErrUnknownScenario) {
		t.Fatalf("expected unknown scenario error, got %v", err)
	}
}

func TestRedactedConfigDoesNotExposeSecrets(t *testing.T) {
	cfg := defaultConfig()
	cfg.APIKey = "secret-api-key"
	cfg.WebhookSecret = "secret-webhook-key"
	cfg.DatabaseURL = "postgres://user:password@localhost/db"
	raw, err := json.Marshal(cfg.Redacted())
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"secret-api-key", "secret-webhook-key", "password"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatalf("serialized config exposed %q: %s", secret, raw)
		}
	}
}
