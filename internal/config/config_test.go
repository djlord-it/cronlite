package config

import (
	"os"
	"testing"
	"time"
)

func TestLoad_TimeoutDefaults(t *testing.T) {
	// Clear any existing env vars
	os.Unsetenv("DB_OP_TIMEOUT")
	os.Unsetenv("DB_MAX_OPEN_CONNS")
	os.Unsetenv("DB_MAX_IDLE_CONNS")
	os.Unsetenv("DB_CONN_MAX_LIFETIME")
	os.Unsetenv("DB_CONN_MAX_IDLE_TIME")
	os.Unsetenv("HTTP_SHUTDOWN_TIMEOUT")
	os.Unsetenv("DISPATCHER_DRAIN_TIMEOUT")

	cfg := Load()

	// Verify timeout defaults
	if cfg.DBOpTimeout != 5*time.Second {
		t.Errorf("DBOpTimeout: expected 5s, got %v", cfg.DBOpTimeout)
	}
	if cfg.DBMaxOpenConns != 25 {
		t.Errorf("DBMaxOpenConns: expected 25, got %d", cfg.DBMaxOpenConns)
	}
	if cfg.DBMaxIdleConns != 5 {
		t.Errorf("DBMaxIdleConns: expected 5, got %d", cfg.DBMaxIdleConns)
	}
	if cfg.DBConnMaxLifetime != 30*time.Minute {
		t.Errorf("DBConnMaxLifetime: expected 30m, got %v", cfg.DBConnMaxLifetime)
	}
	if cfg.DBConnMaxIdleTime != 5*time.Minute {
		t.Errorf("DBConnMaxIdleTime: expected 5m, got %v", cfg.DBConnMaxIdleTime)
	}
	if cfg.HTTPShutdownTimeout != 10*time.Second {
		t.Errorf("HTTPShutdownTimeout: expected 10s, got %v", cfg.HTTPShutdownTimeout)
	}
	if cfg.DispatcherDrainTimeout != 30*time.Second {
		t.Errorf("DispatcherDrainTimeout: expected 30s, got %v", cfg.DispatcherDrainTimeout)
	}
}

func TestLoad_TimeoutCustomValues(t *testing.T) {
	// Set custom values
	os.Setenv("DB_OP_TIMEOUT", "10s")
	os.Setenv("DB_MAX_OPEN_CONNS", "50")
	os.Setenv("DB_MAX_IDLE_CONNS", "10")
	os.Setenv("DB_CONN_MAX_LIFETIME", "1h")
	os.Setenv("DB_CONN_MAX_IDLE_TIME", "10m")
	os.Setenv("HTTP_SHUTDOWN_TIMEOUT", "20s")
	os.Setenv("DISPATCHER_DRAIN_TIMEOUT", "60s")
	defer func() {
		os.Unsetenv("DB_OP_TIMEOUT")
		os.Unsetenv("DB_MAX_OPEN_CONNS")
		os.Unsetenv("DB_MAX_IDLE_CONNS")
		os.Unsetenv("DB_CONN_MAX_LIFETIME")
		os.Unsetenv("DB_CONN_MAX_IDLE_TIME")
		os.Unsetenv("HTTP_SHUTDOWN_TIMEOUT")
		os.Unsetenv("DISPATCHER_DRAIN_TIMEOUT")
	}()

	cfg := Load()

	if cfg.DBOpTimeout != 10*time.Second {
		t.Errorf("DBOpTimeout: expected 10s, got %v", cfg.DBOpTimeout)
	}
	if cfg.DBMaxOpenConns != 50 {
		t.Errorf("DBMaxOpenConns: expected 50, got %d", cfg.DBMaxOpenConns)
	}
	if cfg.DBMaxIdleConns != 10 {
		t.Errorf("DBMaxIdleConns: expected 10, got %d", cfg.DBMaxIdleConns)
	}
	if cfg.DBConnMaxLifetime != time.Hour {
		t.Errorf("DBConnMaxLifetime: expected 1h, got %v", cfg.DBConnMaxLifetime)
	}
	if cfg.DBConnMaxIdleTime != 10*time.Minute {
		t.Errorf("DBConnMaxIdleTime: expected 10m, got %v", cfg.DBConnMaxIdleTime)
	}
	if cfg.HTTPShutdownTimeout != 20*time.Second {
		t.Errorf("HTTPShutdownTimeout: expected 20s, got %v", cfg.HTTPShutdownTimeout)
	}
	if cfg.DispatcherDrainTimeout != 60*time.Second {
		t.Errorf("DispatcherDrainTimeout: expected 60s, got %v", cfg.DispatcherDrainTimeout)
	}
}

func TestMaskedJSON_IncludesTimeoutConfig(t *testing.T) {
	// Clear env vars to get defaults
	os.Unsetenv("DB_OP_TIMEOUT")
	os.Unsetenv("HTTP_SHUTDOWN_TIMEOUT")
	os.Unsetenv("DISPATCHER_DRAIN_TIMEOUT")

	cfg := Load()
	data, err := cfg.MaskedJSON()
	if err != nil {
		t.Fatalf("MaskedJSON failed: %v", err)
	}

	json := string(data)

	// Check that timeout fields are present in output
	if !containsString(json, `"db_op_timeout"`) {
		t.Error("MaskedJSON missing db_op_timeout field")
	}
	if !containsString(json, `"http_shutdown_timeout"`) {
		t.Error("MaskedJSON missing http_shutdown_timeout field")
	}
	if !containsString(json, `"dispatcher_drain_timeout"`) {
		t.Error("MaskedJSON missing dispatcher_drain_timeout field")
	}
	if !containsString(json, `"db_max_open_conns"`) {
		t.Error("MaskedJSON missing db_max_open_conns field")
	}
}

func TestLoad_EventBusBufferSizeDefault(t *testing.T) {
	os.Unsetenv("EVENTBUS_BUFFER_SIZE")

	cfg := Load()

	if cfg.EventBusBufferSize != 100 {
		t.Errorf("EventBusBufferSize: expected 100, got %d", cfg.EventBusBufferSize)
	}
}

func TestLoad_EventBusBufferSizeCustom(t *testing.T) {
	os.Setenv("EVENTBUS_BUFFER_SIZE", "500")
	defer os.Unsetenv("EVENTBUS_BUFFER_SIZE")

	cfg := Load()

	if cfg.EventBusBufferSize != 500 {
		t.Errorf("EventBusBufferSize: expected 500, got %d", cfg.EventBusBufferSize)
	}
}

func TestLoad_EventBusBufferSizeInvalidFallsBack(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{"negative", "-1"},
		{"zero", "0"},
		{"non-numeric", "abc"},
		{"float", "1.5"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Setenv("EVENTBUS_BUFFER_SIZE", tt.value)
			defer os.Unsetenv("EVENTBUS_BUFFER_SIZE")

			cfg := Load()

			if cfg.EventBusBufferSize != 100 {
				t.Errorf("EventBusBufferSize: expected fallback to 100 for %q, got %d", tt.value, cfg.EventBusBufferSize)
			}
		})
	}
}

func TestMaskedJSON_IncludesEventBusBufferSize(t *testing.T) {
	os.Unsetenv("EVENTBUS_BUFFER_SIZE")

	cfg := Load()
	data, err := cfg.MaskedJSON()
	if err != nil {
		t.Fatalf("MaskedJSON failed: %v", err)
	}

	if !containsString(string(data), `"eventbus_buffer_size"`) {
		t.Error("MaskedJSON missing eventbus_buffer_size field")
	}
}

func TestLoad_CircuitBreakerDefaults(t *testing.T) {
	os.Unsetenv("CIRCUIT_BREAKER_THRESHOLD")
	os.Unsetenv("CIRCUIT_BREAKER_COOLDOWN")

	cfg := Load()

	if cfg.CircuitBreakerThreshold != 5 {
		t.Errorf("CircuitBreakerThreshold: expected 5, got %d", cfg.CircuitBreakerThreshold)
	}
	if cfg.CircuitBreakerCooldown != 2*time.Minute {
		t.Errorf("CircuitBreakerCooldown: expected 2m, got %v", cfg.CircuitBreakerCooldown)
	}
}

func TestLoad_CircuitBreakerCustomValues(t *testing.T) {
	os.Setenv("CIRCUIT_BREAKER_THRESHOLD", "10")
	os.Setenv("CIRCUIT_BREAKER_COOLDOWN", "5m")
	defer func() {
		os.Unsetenv("CIRCUIT_BREAKER_THRESHOLD")
		os.Unsetenv("CIRCUIT_BREAKER_COOLDOWN")
	}()

	cfg := Load()

	if cfg.CircuitBreakerThreshold != 10 {
		t.Errorf("CircuitBreakerThreshold: expected 10, got %d", cfg.CircuitBreakerThreshold)
	}
	if cfg.CircuitBreakerCooldown != 5*time.Minute {
		t.Errorf("CircuitBreakerCooldown: expected 5m, got %v", cfg.CircuitBreakerCooldown)
	}
}

func TestLoad_CircuitBreakerDisabled(t *testing.T) {
	os.Setenv("CIRCUIT_BREAKER_THRESHOLD", "0")
	defer os.Unsetenv("CIRCUIT_BREAKER_THRESHOLD")

	cfg := Load()

	if cfg.CircuitBreakerThreshold != 0 {
		t.Errorf("CircuitBreakerThreshold: expected 0, got %d", cfg.CircuitBreakerThreshold)
	}
}

func TestLoad_DispatchModeDefaults(t *testing.T) {
	os.Unsetenv("DISPATCH_MODE")
	os.Unsetenv("DB_POLL_INTERVAL")
	os.Unsetenv("DISPATCHER_WORKERS")

	cfg := Load()

	if cfg.DispatchMode != "channel" {
		t.Errorf("DispatchMode: expected 'channel', got %q", cfg.DispatchMode)
	}
	if cfg.DBPollInterval != 500*time.Millisecond {
		t.Errorf("DBPollInterval: expected 500ms, got %v", cfg.DBPollInterval)
	}
	if cfg.DispatcherWorkers != 1 {
		t.Errorf("DispatcherWorkers: expected 1, got %d", cfg.DispatcherWorkers)
	}
}

func TestLoad_DispatchModeDB(t *testing.T) {
	os.Setenv("DISPATCH_MODE", "db")
	os.Setenv("DB_POLL_INTERVAL", "1s")
	os.Setenv("DISPATCHER_WORKERS", "4")
	defer func() {
		os.Unsetenv("DISPATCH_MODE")
		os.Unsetenv("DB_POLL_INTERVAL")
		os.Unsetenv("DISPATCHER_WORKERS")
	}()

	cfg := Load()

	if cfg.DispatchMode != "db" {
		t.Errorf("DispatchMode: expected 'db', got %q", cfg.DispatchMode)
	}
	if cfg.DBPollInterval != 1*time.Second {
		t.Errorf("DBPollInterval: expected 1s, got %v", cfg.DBPollInterval)
	}
	if cfg.DispatcherWorkers != 4 {
		t.Errorf("DispatcherWorkers: expected 4, got %d", cfg.DispatcherWorkers)
	}
}

func TestMaskedJSON_RedisAddrMasked(t *testing.T) {
	os.Setenv("REDIS_ADDR", "redis://user:password@redis.internal:6379")
	defer os.Unsetenv("REDIS_ADDR")

	cfg := Load()
	data, err := cfg.MaskedJSON()
	if err != nil {
		t.Fatalf("MaskedJSON failed: %v", err)
	}

	json := string(data)
	if containsString(json, "password") {
		t.Error("MaskedJSON should mask REDIS_ADDR credentials")
	}
	if !containsString(json, `"redis_addr"`) {
		t.Error("MaskedJSON should include redis_addr field")
	}
}

func TestLoad_APIKey(t *testing.T) {
	os.Setenv("API_KEY", "test-secret-key")
	defer os.Unsetenv("API_KEY")

	cfg := Load()

	if cfg.APIKey != "test-secret-key" {
		t.Errorf("APIKey: expected 'test-secret-key', got %q", cfg.APIKey)
	}
}

func TestLoad_APIKeyEmpty(t *testing.T) {
	os.Unsetenv("API_KEY")

	cfg := Load()

	if cfg.APIKey != "" {
		t.Errorf("APIKey: expected empty, got %q", cfg.APIKey)
	}
}

func TestMaskedJSON_ExcludesAPIKey(t *testing.T) {
	os.Setenv("API_KEY", "super-secret")
	defer os.Unsetenv("API_KEY")

	cfg := Load()
	data, err := cfg.MaskedJSON()
	if err != nil {
		t.Fatalf("MaskedJSON failed: %v", err)
	}

	if containsString(string(data), "super-secret") {
		t.Error("MaskedJSON should NOT include API_KEY value")
	}
	if containsString(string(data), "api_key") {
		t.Error("MaskedJSON should NOT include api_key field at all")
	}
}

func containsString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
