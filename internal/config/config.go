package config

import (
	"encoding/json"
	"log"
	"os"
	"time"
)

// Config holds all configuration for the easycron application.
// Values are loaded from environment variables; see printUsage() for the full list.
type Config struct {
	DatabaseURL string `json:"database_url"`
	RedisAddr   string `json:"redis_addr,omitempty"`
	HTTPAddr    string `json:"http_addr"`
	APIKey  string `json:"-"` // never serialized, not even masked

	TickInterval    time.Duration `json:"-"`
	TickIntervalStr string        `json:"tick_interval"`

	DBOpTimeout    time.Duration `json:"-"`
	DBOpTimeoutStr string        `json:"db_op_timeout"`

	DBMaxOpenConns       int           `json:"db_max_open_conns"`
	DBMaxIdleConns       int           `json:"db_max_idle_conns"`
	DBConnMaxLifetime    time.Duration `json:"-"`
	DBConnMaxLifetimeStr string        `json:"db_conn_max_lifetime"`
	DBConnMaxIdleTime    time.Duration `json:"-"`
	DBConnMaxIdleTimeStr string        `json:"db_conn_max_idle_time"`

	HTTPShutdownTimeout       time.Duration `json:"-"`
	HTTPShutdownTimeoutStr    string        `json:"http_shutdown_timeout"`
	DispatcherDrainTimeout    time.Duration `json:"-"`
	DispatcherDrainTimeoutStr string        `json:"dispatcher_drain_timeout"`

	MetricsEnabled bool   `json:"metrics_enabled"`
	MetricsPath    string `json:"metrics_path"`

	ReconcileEnabled     bool          `json:"reconcile_enabled"`
	ReconcileInterval    time.Duration `json:"-"`
	ReconcileIntervalStr string        `json:"reconcile_interval"`

	// ReconcileThreshold must exceed the dispatcher's maximum retry window (currently 12m30s).
	ReconcileThreshold    time.Duration `json:"-"`
	ReconcileThresholdStr string        `json:"reconcile_threshold"`

	// ReconcileRequeueThreshold: age before in_progress execution is requeued (crash recovery).
	// Can be aggressive (FOR UPDATE SKIP LOCKED prevents premature requeue).
	ReconcileRequeueThreshold    time.Duration `json:"-"`
	ReconcileRequeueThresholdStr string        `json:"reconcile_requeue_threshold"`

	ReconcileBatchSize int `json:"reconcile_batch_size"`
	EventBusBufferSize int `json:"eventbus_buffer_size"`

	// CircuitBreakerThreshold: 0 disables the circuit breaker.
	CircuitBreakerThreshold  int           `json:"circuit_breaker_threshold"`
	CircuitBreakerCooldown   time.Duration `json:"-"`
	CircuitBreakerCooldownStr string       `json:"circuit_breaker_cooldown"`

	// DispatchMode: "channel" (in-memory) or "db" (Postgres FOR UPDATE SKIP LOCKED polling).
	DispatchMode      string        `json:"dispatch_mode"`
	DBPollInterval    time.Duration `json:"-"`
	DBPollIntervalStr string        `json:"db_poll_interval"`
	DispatcherWorkers int           `json:"dispatcher_workers"`

	// LeaderLockKey: all instances sharing the same database must use the same key.
	LeaderLockKey int64 `json:"leader_lock_key"`

	// LeaderRetryInterval determines the maximum failover gap.
	LeaderRetryInterval    time.Duration `json:"-"`
	LeaderRetryIntervalStr string        `json:"leader_retry_interval"`

	// LeaderHeartbeatInterval: pings the dedicated connection to detect local
	// connection death. Does NOT renew the advisory lock.
	LeaderHeartbeatInterval    time.Duration `json:"-"`
	LeaderHeartbeatIntervalStr string        `json:"leader_heartbeat_interval"`

	// MaxFiresPerTick: safety cap on how many executions a single job can emit per tick.
	MaxFiresPerTick int `json:"max_fires_per_tick"`

	// IPRateLimit: per-IP request rate (req/sec). Applied before auth.
	IPRateLimit int `json:"ip_rate_limit"`
	// NamespaceRateLimit: per-namespace request rate (req/sec). Applied after auth.
	NamespaceRateLimit int `json:"namespace_rate_limit"`

	// CloudflareOnly: when true, reject requests not from Cloudflare edge IPs.
	CloudflareOnly bool `json:"cloudflare_only"`
}

// Load reads configuration from environment variables with defaults.
func Load() Config {
	cfg := Config{
		DatabaseURL:               os.Getenv("DATABASE_URL"),
		RedisAddr:                 os.Getenv("REDIS_ADDR"),
		HTTPAddr:                  os.Getenv("HTTP_ADDR"),
		APIKey:                    os.Getenv("API_KEY"),
		TickIntervalStr:           os.Getenv("TICK_INTERVAL"),
		DBOpTimeoutStr:            os.Getenv("DB_OP_TIMEOUT"),
		DBConnMaxLifetimeStr:      os.Getenv("DB_CONN_MAX_LIFETIME"),
		DBConnMaxIdleTimeStr:      os.Getenv("DB_CONN_MAX_IDLE_TIME"),
		HTTPShutdownTimeoutStr:    os.Getenv("HTTP_SHUTDOWN_TIMEOUT"),
		DispatcherDrainTimeoutStr: os.Getenv("DISPATCHER_DRAIN_TIMEOUT"),
		MetricsEnabled:            os.Getenv("METRICS_ENABLED") == "true",
		CloudflareOnly:            os.Getenv("CLOUDFLARE_ONLY") == "true",
		MetricsPath:               os.Getenv("METRICS_PATH"),
		ReconcileEnabled:          os.Getenv("RECONCILE_ENABLED") == "true",
		ReconcileIntervalStr:          os.Getenv("RECONCILE_INTERVAL"),
		ReconcileThresholdStr:         os.Getenv("RECONCILE_THRESHOLD"),
		ReconcileRequeueThresholdStr:  os.Getenv("RECONCILE_REQUEUE_THRESHOLD"),
	}

	if batchStr := os.Getenv("RECONCILE_BATCH_SIZE"); batchStr != "" {
		if batch, err := parseInt(batchStr); err == nil && batch > 0 {
			cfg.ReconcileBatchSize = batch
		}
	}
	if cfg.ReconcileBatchSize == 0 {
		cfg.ReconcileBatchSize = 100
	}

	if bufStr := os.Getenv("EVENTBUS_BUFFER_SIZE"); bufStr != "" {
		if n, err := parseInt(bufStr); err == nil && n > 0 {
			cfg.EventBusBufferSize = n
		} else {
			log.Printf("config: invalid EVENTBUS_BUFFER_SIZE %q (must be a positive integer), using default 100", bufStr)
		}
	}
	if cfg.EventBusBufferSize == 0 {
		cfg.EventBusBufferSize = 100
	}

	if cbThreshStr := os.Getenv("CIRCUIT_BREAKER_THRESHOLD"); cbThreshStr != "" {
		if n, err := parseInt(cbThreshStr); err == nil {
			cfg.CircuitBreakerThreshold = n
		} else {
			log.Printf("config: invalid CIRCUIT_BREAKER_THRESHOLD %q, using default 5", cbThreshStr)
		}
	}
	if cfg.CircuitBreakerThreshold == 0 && os.Getenv("CIRCUIT_BREAKER_THRESHOLD") == "" {
		cfg.CircuitBreakerThreshold = 5
	}

	cfg.CircuitBreakerCooldownStr = os.Getenv("CIRCUIT_BREAKER_COOLDOWN")

	cfg.DispatchMode = os.Getenv("DISPATCH_MODE")
	if cfg.DispatchMode == "" {
		cfg.DispatchMode = "channel"
	}

	cfg.DBPollIntervalStr = os.Getenv("DB_POLL_INTERVAL")
	cfg.LeaderRetryIntervalStr = os.Getenv("LEADER_RETRY_INTERVAL")
	cfg.LeaderHeartbeatIntervalStr = os.Getenv("LEADER_HEARTBEAT_INTERVAL")

	if lockKeyStr := os.Getenv("LEADER_LOCK_KEY"); lockKeyStr != "" {
		if n, err := parseInt(lockKeyStr); err == nil && n > 0 {
			cfg.LeaderLockKey = int64(n)
		} else {
			log.Printf("config: invalid LEADER_LOCK_KEY %q (must be a positive integer), using default 728379", lockKeyStr)
		}
	}
	if cfg.LeaderLockKey == 0 {
		cfg.LeaderLockKey = 728379
	}

	if workersStr := os.Getenv("DISPATCHER_WORKERS"); workersStr != "" {
		if n, err := parseInt(workersStr); err == nil && n > 0 {
			cfg.DispatcherWorkers = n
		} else {
			log.Printf("config: invalid DISPATCHER_WORKERS %q (must be a positive integer), using default 1", workersStr)
		}
	}
	if cfg.DispatcherWorkers == 0 {
		cfg.DispatcherWorkers = 1
	}

	if mfStr := os.Getenv("MAX_FIRES_PER_TICK"); mfStr != "" {
		if n, err := parseInt(mfStr); err == nil && n > 0 {
			cfg.MaxFiresPerTick = n
		} else {
			log.Printf("config: invalid MAX_FIRES_PER_TICK %q (must be a positive integer), using default 1000", mfStr)
		}
	}
	if cfg.MaxFiresPerTick == 0 {
		cfg.MaxFiresPerTick = 1000
	}

	if rlStr := os.Getenv("RATE_LIMIT"); rlStr != "" {
		if n, err := parseInt(rlStr); err == nil && n > 0 {
			cfg.IPRateLimit = n
		} else {
			log.Printf("config: invalid RATE_LIMIT %q (must be a positive integer), using default 10", rlStr)
		}
	}
	if cfg.IPRateLimit == 0 {
		cfg.IPRateLimit = 10
	}

	if nsrlStr := os.Getenv("NAMESPACE_RATE_LIMIT"); nsrlStr != "" {
		if n, err := parseInt(nsrlStr); err == nil && n > 0 {
			cfg.NamespaceRateLimit = n
		} else {
			log.Printf("config: invalid NAMESPACE_RATE_LIMIT %q (must be a positive integer), using default 100", nsrlStr)
		}
	}
	if cfg.NamespaceRateLimit == 0 {
		cfg.NamespaceRateLimit = 100
	}

	if maxOpenStr := os.Getenv("DB_MAX_OPEN_CONNS"); maxOpenStr != "" {
		if n, err := parseInt(maxOpenStr); err == nil && n > 0 {
			cfg.DBMaxOpenConns = n
		}
	}
	if cfg.DBMaxOpenConns == 0 {
		cfg.DBMaxOpenConns = 25
	}

	if maxIdleStr := os.Getenv("DB_MAX_IDLE_CONNS"); maxIdleStr != "" {
		if n, err := parseInt(maxIdleStr); err == nil && n > 0 {
			cfg.DBMaxIdleConns = n
		}
	}
	if cfg.DBMaxIdleConns == 0 {
		cfg.DBMaxIdleConns = 5
	}

	// Support Railway's PORT variable as fallback for HTTP_ADDR.
	if cfg.HTTPAddr == "" {
		if port := os.Getenv("PORT"); port != "" {
			cfg.HTTPAddr = ":" + port
		} else {
			cfg.HTTPAddr = ":8080"
		}
	}
	if cfg.TickIntervalStr == "" {
		cfg.TickIntervalStr = "30s"
	}
	if cfg.DBOpTimeoutStr == "" {
		cfg.DBOpTimeoutStr = "5s"
	}
	if cfg.DBConnMaxLifetimeStr == "" {
		cfg.DBConnMaxLifetimeStr = "30m"
	}
	if cfg.DBConnMaxIdleTimeStr == "" {
		cfg.DBConnMaxIdleTimeStr = "5m"
	}
	if cfg.HTTPShutdownTimeoutStr == "" {
		cfg.HTTPShutdownTimeoutStr = "10s"
	}
	if cfg.DispatcherDrainTimeoutStr == "" {
		cfg.DispatcherDrainTimeoutStr = "30s"
	}
	if cfg.MetricsPath == "" {
		cfg.MetricsPath = "/metrics"
	}
	if cfg.ReconcileIntervalStr == "" {
		cfg.ReconcileIntervalStr = "5m"
	}
	if cfg.ReconcileThresholdStr == "" {
		cfg.ReconcileThresholdStr = "15m"
	}
	if cfg.ReconcileRequeueThresholdStr == "" {
		cfg.ReconcileRequeueThresholdStr = "2m"
	}
	if cfg.CircuitBreakerCooldownStr == "" {
		cfg.CircuitBreakerCooldownStr = "2m"
	}
	if cfg.DBPollIntervalStr == "" {
		cfg.DBPollIntervalStr = "500ms"
	}
	if cfg.LeaderRetryIntervalStr == "" {
		cfg.LeaderRetryIntervalStr = "5s"
	}
	if cfg.LeaderHeartbeatIntervalStr == "" {
		cfg.LeaderHeartbeatIntervalStr = "2s"
	}

	// Parse durations; validation is handled separately by Validate().
	if d, err := time.ParseDuration(cfg.TickIntervalStr); err == nil {
		cfg.TickInterval = d
	}
	if d, err := time.ParseDuration(cfg.DBOpTimeoutStr); err == nil {
		cfg.DBOpTimeout = d
	}
	if d, err := time.ParseDuration(cfg.DBConnMaxLifetimeStr); err == nil {
		cfg.DBConnMaxLifetime = d
	}
	if d, err := time.ParseDuration(cfg.DBConnMaxIdleTimeStr); err == nil {
		cfg.DBConnMaxIdleTime = d
	}
	if d, err := time.ParseDuration(cfg.HTTPShutdownTimeoutStr); err == nil {
		cfg.HTTPShutdownTimeout = d
	}
	if d, err := time.ParseDuration(cfg.DispatcherDrainTimeoutStr); err == nil {
		cfg.DispatcherDrainTimeout = d
	}
	if d, err := time.ParseDuration(cfg.ReconcileIntervalStr); err == nil {
		cfg.ReconcileInterval = d
	}
	if d, err := time.ParseDuration(cfg.ReconcileThresholdStr); err == nil {
		cfg.ReconcileThreshold = d
	}
	if d, err := time.ParseDuration(cfg.ReconcileRequeueThresholdStr); err == nil {
		cfg.ReconcileRequeueThreshold = d
	}
	if d, err := time.ParseDuration(cfg.CircuitBreakerCooldownStr); err == nil {
		cfg.CircuitBreakerCooldown = d
	}
	if d, err := time.ParseDuration(cfg.DBPollIntervalStr); err == nil {
		cfg.DBPollInterval = d
	}
	if d, err := time.ParseDuration(cfg.LeaderRetryIntervalStr); err == nil {
		cfg.LeaderRetryInterval = d
	}
	if d, err := time.ParseDuration(cfg.LeaderHeartbeatIntervalStr); err == nil {
		cfg.LeaderHeartbeatInterval = d
	}

	return cfg
}

// parseInt parses a string as an integer.
func parseInt(s string) (int, error) {
	var n int
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, os.ErrInvalid
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}

// MaskedJSON returns the configuration as JSON with secrets masked.
func (c Config) MaskedJSON() ([]byte, error) {
	masked := struct {
		DatabaseURL            string `json:"database_url"`
		RedisAddr              string `json:"redis_addr,omitempty"`
		HTTPAddr               string `json:"http_addr"`
		TickInterval           string `json:"tick_interval"`
		DBOpTimeout            string `json:"db_op_timeout"`
		DBMaxOpenConns         int    `json:"db_max_open_conns"`
		DBMaxIdleConns         int    `json:"db_max_idle_conns"`
		DBConnMaxLifetime      string `json:"db_conn_max_lifetime"`
		DBConnMaxIdleTime      string `json:"db_conn_max_idle_time"`
		HTTPShutdownTimeout    string `json:"http_shutdown_timeout"`
		DispatcherDrainTimeout string `json:"dispatcher_drain_timeout"`
		MetricsEnabled         bool   `json:"metrics_enabled"`
		MetricsPath            string `json:"metrics_path"`
		ReconcileEnabled       bool   `json:"reconcile_enabled"`
		ReconcileInterval      string `json:"reconcile_interval"`
		ReconcileThreshold         string `json:"reconcile_threshold"`
		ReconcileRequeueThreshold  string `json:"reconcile_requeue_threshold"`
		ReconcileBatchSize         int    `json:"reconcile_batch_size"`
		EventBusBufferSize      int    `json:"eventbus_buffer_size"`
		CircuitBreakerThreshold int    `json:"circuit_breaker_threshold"`
		CircuitBreakerCooldown  string `json:"circuit_breaker_cooldown"`
		DispatchMode             string `json:"dispatch_mode"`
		DBPollInterval           string `json:"db_poll_interval"`
		DispatcherWorkers        int    `json:"dispatcher_workers"`
		LeaderLockKey            int64  `json:"leader_lock_key"`
		LeaderRetryInterval      string `json:"leader_retry_interval"`
		LeaderHeartbeatInterval  string `json:"leader_heartbeat_interval"`
		MaxFiresPerTick          int    `json:"max_fires_per_tick"`
		IPRateLimit              int    `json:"ip_rate_limit"`
		NamespaceRateLimit       int    `json:"namespace_rate_limit"`
		CloudflareOnly           bool   `json:"cloudflare_only"`
	}{
		DatabaseURL:            maskSecret(c.DatabaseURL),
		RedisAddr:              maskSecret(c.RedisAddr),
		HTTPAddr:               c.HTTPAddr,
		TickInterval:           c.TickIntervalStr,
		DBOpTimeout:            c.DBOpTimeoutStr,
		DBMaxOpenConns:         c.DBMaxOpenConns,
		DBMaxIdleConns:         c.DBMaxIdleConns,
		DBConnMaxLifetime:      c.DBConnMaxLifetimeStr,
		DBConnMaxIdleTime:      c.DBConnMaxIdleTimeStr,
		HTTPShutdownTimeout:    c.HTTPShutdownTimeoutStr,
		DispatcherDrainTimeout: c.DispatcherDrainTimeoutStr,
		MetricsEnabled:         c.MetricsEnabled,
		MetricsPath:            c.MetricsPath,
		ReconcileEnabled:       c.ReconcileEnabled,
		ReconcileInterval:      c.ReconcileIntervalStr,
		ReconcileThreshold:        c.ReconcileThresholdStr,
		ReconcileRequeueThreshold: c.ReconcileRequeueThresholdStr,
		ReconcileBatchSize:        c.ReconcileBatchSize,
		EventBusBufferSize:      c.EventBusBufferSize,
		CircuitBreakerThreshold: c.CircuitBreakerThreshold,
		CircuitBreakerCooldown:  c.CircuitBreakerCooldownStr,
		DispatchMode:            c.DispatchMode,
		DBPollInterval:          c.DBPollIntervalStr,
		DispatcherWorkers:       c.DispatcherWorkers,
		LeaderLockKey:           c.LeaderLockKey,
		LeaderRetryInterval:     c.LeaderRetryIntervalStr,
		LeaderHeartbeatInterval: c.LeaderHeartbeatIntervalStr,
		MaxFiresPerTick:         c.MaxFiresPerTick,
		IPRateLimit:             c.IPRateLimit,
		NamespaceRateLimit:      c.NamespaceRateLimit,
		CloudflareOnly:          c.CloudflareOnly,
	}
	return json.MarshalIndent(masked, "", "  ")
}

// maskSecret masks a secret value, preserving only the URI scheme if present.
func maskSecret(s string) string {
	if s == "" {
		return ""
	}
	for _, scheme := range []string{"postgres://", "postgresql://", "redis://", "rediss://"} {
		if len(s) >= len(scheme) && s[:len(scheme)] == scheme {
			return scheme + "***"
		}
	}
	return "***"
}
