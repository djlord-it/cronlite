package config

import (
	"strings"
	"testing"
	"time"

	"github.com/djlord-it/cronlite/internal/dispatcher"
)

func TestValidate_ValidConfig(t *testing.T) {
	cfg := Config{
		DatabaseURL:     "postgres://localhost/cronlite",
		TickIntervalStr: "30s",
	}

	if err := Validate(cfg); err != nil {
		t.Errorf("valid config should not return error, got: %v", err)
	}
}

func TestValidate_MissingDatabaseURL(t *testing.T) {
	cfg := Config{
		DatabaseURL:     "",
		TickIntervalStr: "30s",
	}

	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for missing DATABASE_URL")
	}

	if !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Errorf("error should mention DATABASE_URL: %q", err.Error())
	}
}

func TestValidate_InvalidTickInterval(t *testing.T) {
	tests := []struct {
		name     string
		interval string
		wantErr  string
	}{
		{"non-parseable", "invalid", "invalid duration"},
		{"negative", "-1s", "must be positive"},
		{"zero", "0s", "must be positive"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{
				DatabaseURL:     "postgres://localhost/cronlite",
				TickIntervalStr: tt.interval,
			}

			err := Validate(cfg)
			if err == nil {
				t.Fatalf("expected error for tick_interval=%q", tt.interval)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q should contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestValidate_MultipleErrors(t *testing.T) {
	cfg := Config{
		DatabaseURL:     "", // missing
		TickIntervalStr: "invalid",
	}

	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected errors")
	}

	errs, ok := err.(ValidationErrors)
	if !ok {
		t.Fatalf("expected ValidationErrors, got %T", err)
	}

	if len(errs) != 2 {
		t.Errorf("expected 2 validation errors, got %d: %v", len(errs), errs)
	}
}

func TestValidationError_Format(t *testing.T) {
	err := ValidationError{Field: "DATABASE_URL", Message: "required"}
	got := err.Error()
	want := "DATABASE_URL: required"
	if got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestValidate_DispatchModeInvalid(t *testing.T) {
	cfg := Config{
		DatabaseURL:  "postgres://localhost/test",
		DispatchMode: "invalid",
	}
	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected validation error for invalid DISPATCH_MODE")
	}
}

func TestValidate_DispatchModeValid(t *testing.T) {
	for _, mode := range []string{"channel", "db"} {
		cfg := Config{
			DatabaseURL:  "postgres://localhost/test",
			DispatchMode: mode,
		}
		err := Validate(cfg)
		if err != nil {
			t.Errorf("unexpected error for mode %q: %v", mode, err)
		}
	}
}

func TestValidationErrors_Format(t *testing.T) {
	// Single error
	single := ValidationErrors{{Field: "F1", Message: "M1"}}
	if single.Error() != "F1: M1" {
		t.Errorf("single error = %q, want 'F1: M1'", single.Error())
	}

	// Multiple errors
	multi := ValidationErrors{
		{Field: "F1", Message: "M1"},
		{Field: "F2", Message: "M2"},
	}
	got := multi.Error()
	if !strings.Contains(got, "2 validation errors") {
		t.Errorf("multi error should contain '2 validation errors': %q", got)
	}
	if !strings.Contains(got, "F1: M1") || !strings.Contains(got, "F2: M2") {
		t.Errorf("multi error should contain both errors: %q", got)
	}

	// Empty
	empty := ValidationErrors{}
	if empty.Error() != "" {
		t.Errorf("empty errors should return empty string, got %q", empty.Error())
	}
}

// validDBConfig returns a minimal valid Config for DB dispatch mode.
func validDBConfig() Config {
	return Config{
		DatabaseURL:                "postgres://localhost/test",
		DispatchMode:               "db",
		LeaderRetryInterval:        5 * time.Second,
		LeaderRetryIntervalStr:     "5s",
		LeaderHeartbeatInterval:    2 * time.Second,
		LeaderHeartbeatIntervalStr: "2s",
	}
}

func TestValidate_LeaderHeartbeatMustBeLessThanRetry(t *testing.T) {
	cfg := validDBConfig()
	cfg.LeaderHeartbeatInterval = 10 * time.Second // >= retry (5s)
	cfg.LeaderHeartbeatIntervalStr = "10s"

	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error when heartbeat >= retry interval")
	}
	if !strings.Contains(err.Error(), "LEADER_HEARTBEAT_INTERVAL") {
		t.Errorf("error should mention LEADER_HEARTBEAT_INTERVAL: %q", err)
	}
}

func TestValidate_LeaderIntervals_Valid(t *testing.T) {
	cfg := validDBConfig()
	if err := Validate(cfg); err != nil {
		t.Errorf("valid DB config should not error: %v", err)
	}
}

func TestValidate_ReconcileThresholdBelowMaxRetry(t *testing.T) {
	cfg := validDBConfig()
	cfg.ReconcileEnabled = true
	cfg.ReconcileThreshold = 1 * time.Minute // way below MaxRetryDuration
	cfg.ReconcileThresholdStr = "1m"
	cfg.ReconcileInterval = 5 * time.Minute
	cfg.ReconcileIntervalStr = "5m"

	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error when reconcile threshold < max retry duration")
	}

	maxRetry := dispatcher.MaxRetryDuration()
	if !strings.Contains(err.Error(), maxRetry.String()) {
		t.Errorf("error should mention max retry duration: %q", err)
	}
}

func TestValidate_ReconcileThresholdSafe(t *testing.T) {
	cfg := validDBConfig()
	cfg.ReconcileEnabled = true
	cfg.ReconcileThreshold = 15 * time.Minute
	cfg.ReconcileThresholdStr = "15m"
	cfg.ReconcileInterval = 5 * time.Minute
	cfg.ReconcileIntervalStr = "5m"
	cfg.ReconcileRequeueThreshold = 2 * time.Minute
	cfg.ReconcileRequeueThresholdStr = "2m"

	if err := Validate(cfg); err != nil {
		t.Errorf("valid reconciler config should not error: %v", err)
	}
}

func TestValidate_CircuitBreakerCooldownRequired(t *testing.T) {
	cfg := Config{
		DatabaseURL:               "postgres://localhost/test",
		CircuitBreakerThreshold:   5,
		CircuitBreakerCooldown:    -1 * time.Second,
		CircuitBreakerCooldownStr: "-1s",
	}

	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error when circuit breaker enabled with non-positive cooldown")
	}
	if !strings.Contains(err.Error(), "CIRCUIT_BREAKER_COOLDOWN") {
		t.Errorf("error should mention CIRCUIT_BREAKER_COOLDOWN: %q", err)
	}
}

func TestValidate_ProductionRequiresSafeRuntimeSettings(t *testing.T) {
	cfg := validDBConfig()
	cfg.Environment = "production"
	cfg.DispatchMode = "channel"
	cfg.ReconcileEnabled = false
	cfg.MetricsEnabled = false
	cfg.APIKey = ""

	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected production validation errors")
	}

	for _, want := range []string{
		"DISPATCH_MODE",
		"RECONCILE_ENABLED",
		"METRICS_ENABLED",
		"API_KEY",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("expected production validation error to mention %s, got: %v", want, err)
		}
	}
}

func TestValidate_ProductionValidConfig(t *testing.T) {
	cfg := validDBConfig()
	cfg.Environment = "production"
	cfg.ReconcileEnabled = true
	cfg.ReconcileInterval = 5 * time.Minute
	cfg.ReconcileIntervalStr = "5m"
	cfg.ReconcileThreshold = 15 * time.Minute
	cfg.ReconcileThresholdStr = "15m"
	cfg.ReconcileRequeueThreshold = 2 * time.Minute
	cfg.ReconcileRequeueThresholdStr = "2m"
	cfg.MetricsEnabled = true
	cfg.APIKey = "prod-key"

	if err := Validate(cfg); err != nil {
		t.Fatalf("expected valid production config, got: %v", err)
	}
}

func TestValidate_DevelopmentAllowsUnsafeRuntimeSettings(t *testing.T) {
	cfg := Config{
		Environment:      "development",
		DatabaseURL:      "postgres://localhost/test",
		DispatchMode:     "channel",
		ReconcileEnabled: false,
		MetricsEnabled:   false,
		APIKey:           "",
	}

	if err := Validate(cfg); err != nil {
		t.Fatalf("expected development config to remain valid, got: %v", err)
	}
}
