package config

import (
	"encoding/json"
	"log"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all configuration for the cronlite application.
// Values are loaded from environment variables; see printUsage() for the full list.
type Config struct {
	Environment string `json:"environment,omitempty"`
	DatabaseURL string `json:"database_url"`
	RedisAddr   string `json:"redis_addr,omitempty"`
	HTTPAddr    string `json:"http_addr"`
	APIKey      string `json:"-"` // never serialized, not even masked

	TickInterval    time.Duration `json:"-"`
	TickIntervalStr string        `json:"tick_interval"`

	DBOpTimeout    time.Duration `json:"-"`
	DBOpTimeoutStr string        `json:"db_op_timeout"`

	MigrateOnStart        bool          `json:"migrate_on_start"`
	MigrateLockTimeout    time.Duration `json:"-"`
	MigrateLockTimeoutStr string        `json:"migrate_lock_timeout"`

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
	MetricsPublic  bool   `json:"metrics_public"`

	ReconcileEnabled     bool          `json:"reconcile_enabled"`
	ReconcileInterval    time.Duration `json:"-"`
	ReconcileIntervalStr string        `json:"reconcile_interval"`

	// ReconcileThreshold must exceed the dispatcher's maximum retry window (currently 16m30s).
	ReconcileThreshold    time.Duration `json:"-"`
	ReconcileThresholdStr string        `json:"reconcile_threshold"`

	// ReconcileRequeueThreshold is the renewable execution lease duration.
	ReconcileRequeueThreshold    time.Duration `json:"-"`
	ReconcileRequeueThresholdStr string        `json:"reconcile_requeue_threshold"`

	ReconcileBatchSize int `json:"reconcile_batch_size"`
	EventBusBufferSize int `json:"eventbus_buffer_size"`

	// CircuitBreakerThreshold: 0 disables the circuit breaker.
	CircuitBreakerThreshold   int           `json:"circuit_breaker_threshold"`
	CircuitBreakerCooldown    time.Duration `json:"-"`
	CircuitBreakerCooldownStr string        `json:"circuit_breaker_cooldown"`

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

	// CORSOrigins: comma-separated allowed origins (empty = disabled, * = all).
	CORSOrigins string `json:"cors_origins,omitempty"`

	// Admin UI is opt-in and served under /admin.
	AdminEnabled               bool          `json:"admin_enabled"`
	AdminBootstrapToken        string        `json:"-"`
	AdminSessionTTL            time.Duration `json:"-"`
	AdminSessionTTLStr         string        `json:"admin_session_ttl"`
	AdminSessionAbsoluteTTL    time.Duration `json:"-"`
	AdminSessionAbsoluteTTLStr string        `json:"admin_session_absolute_ttl"`
	AdminCookieSecure          bool          `json:"admin_cookie_secure"`

	// WebhookAllowedPrivateSuffixes lists hostnames (".suffix" or exact) whose
	// private-range addresses webhook delivery may dial. Loopback, link-local
	// and other always-blocked ranges are never allowed.
	WebhookAllowedPrivateSuffixes []string `json:"webhook_allowed_private_suffixes"`

	// WebhookAllowTailnet delivers webhooks for hostnames under this node's
	// tailnet MagicDNS suffix over an embedded Tailscale node. Requires a
	// binary built with -tags tailscale.
	WebhookAllowTailnet bool   `json:"webhook_allow_tailnet"`
	TSAuthKey           string `json:"-"` // never serialized, not even masked
	TSHostname          string `json:"ts_hostname"`
	TSStateDir          string `json:"ts_state_dir,omitempty"`
}

// Load reads configuration from environment variables with defaults.
func Load() Config {
	cfg := Config{
		ReconcileBatchSize:           100,
		EventBusBufferSize:           100,
		LeaderLockKey:                728379,
		DispatcherWorkers:            1,
		MaxFiresPerTick:              1000,
		IPRateLimit:                  10,
		NamespaceRateLimit:           100,
		DBMaxOpenConns:               25,
		DBMaxIdleConns:               5,
		Environment:                  os.Getenv("CRONLITE_ENV"),
		DatabaseURL:                  os.Getenv("DATABASE_URL"),
		RedisAddr:                    os.Getenv("REDIS_ADDR"),
		HTTPAddr:                     os.Getenv("HTTP_ADDR"),
		APIKey:                       os.Getenv("API_KEY"),
		TickIntervalStr:              os.Getenv("TICK_INTERVAL"),
		DBOpTimeoutStr:               os.Getenv("DB_OP_TIMEOUT"),
		DBConnMaxLifetimeStr:         os.Getenv("DB_CONN_MAX_LIFETIME"),
		DBConnMaxIdleTimeStr:         os.Getenv("DB_CONN_MAX_IDLE_TIME"),
		HTTPShutdownTimeoutStr:       os.Getenv("HTTP_SHUTDOWN_TIMEOUT"),
		DispatcherDrainTimeoutStr:    os.Getenv("DISPATCHER_DRAIN_TIMEOUT"),
		MetricsEnabled:               os.Getenv("METRICS_ENABLED") == "true",
		MetricsPath:                  os.Getenv("METRICS_PATH"),
		MetricsPublic:                os.Getenv("METRICS_PUBLIC") == "true",
		ReconcileEnabled:             os.Getenv("RECONCILE_ENABLED") == "true",
		ReconcileIntervalStr:         os.Getenv("RECONCILE_INTERVAL"),
		ReconcileThresholdStr:        os.Getenv("RECONCILE_THRESHOLD"),
		ReconcileRequeueThresholdStr: os.Getenv("RECONCILE_REQUEUE_THRESHOLD"),
		AdminEnabled:                 os.Getenv("ADMIN_ENABLED") == "true",
		MigrateOnStart:               os.Getenv("MIGRATE_ON_START") == "true",
		MigrateLockTimeoutStr:        os.Getenv("MIGRATE_LOCK_TIMEOUT"),
		AdminBootstrapToken:          os.Getenv("ADMIN_BOOTSTRAP_TOKEN"),
		AdminSessionTTLStr:           os.Getenv("ADMIN_SESSION_TTL"),
		AdminSessionAbsoluteTTLStr:   os.Getenv("ADMIN_SESSION_ABSOLUTE_TTL"),
		WebhookAllowTailnet:          os.Getenv("WEBHOOK_ALLOW_TAILNET") == "true",
		TSAuthKey:                    os.Getenv("TS_AUTHKEY"),
		TSHostname:                   os.Getenv("TS_HOSTNAME"),
		TSStateDir:                   os.Getenv("TS_STATE_DIR"),
	}

	cookieSecure := os.Getenv("ADMIN_COOKIE_SECURE")
	if cookieSecure == "" {
		cfg.AdminCookieSecure = strings.EqualFold(cfg.Environment, "production")
	} else {
		cfg.AdminCookieSecure = cookieSecure == "true"
	}

	if batchStr := os.Getenv("RECONCILE_BATCH_SIZE"); batchStr != "" {
		if batch, err := parseInt(batchStr); err == nil && batch > 0 {
			cfg.ReconcileBatchSize = batch
		}
	}

	if bufStr := os.Getenv("EVENTBUS_BUFFER_SIZE"); bufStr != "" {
		if n, err := parseInt(bufStr); err == nil && n > 0 {
			cfg.EventBusBufferSize = n
		} else {
			log.Printf("config: invalid EVENTBUS_BUFFER_SIZE %q (must be a positive integer), using default 100", bufStr)
		}
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

	if workersStr := os.Getenv("DISPATCHER_WORKERS"); workersStr != "" {
		if n, err := parseInt(workersStr); err == nil && n > 0 {
			cfg.DispatcherWorkers = n
		} else {
			log.Printf("config: invalid DISPATCHER_WORKERS %q (must be a positive integer), using default 1", workersStr)
		}
	}

	if mfStr := os.Getenv("MAX_FIRES_PER_TICK"); mfStr != "" {
		if n, err := parseInt(mfStr); err == nil && n > 0 {
			cfg.MaxFiresPerTick = n
		} else {
			log.Printf("config: invalid MAX_FIRES_PER_TICK %q (must be a positive integer), using default 1000", mfStr)
		}
	}

	if rlStr := os.Getenv("RATE_LIMIT"); rlStr != "" {
		if n, err := parseInt(rlStr); err == nil && n > 0 {
			cfg.IPRateLimit = n
		} else {
			log.Printf("config: invalid RATE_LIMIT %q (must be a positive integer), using default 10", rlStr)
		}
	}

	if nsrlStr := os.Getenv("NAMESPACE_RATE_LIMIT"); nsrlStr != "" {
		if n, err := parseInt(nsrlStr); err == nil && n > 0 {
			cfg.NamespaceRateLimit = n
		} else {
			log.Printf("config: invalid NAMESPACE_RATE_LIMIT %q (must be a positive integer), using default 100", nsrlStr)
		}
	}

	cfg.CORSOrigins = os.Getenv("CORS_ORIGINS")

	for _, entry := range strings.Split(os.Getenv("WEBHOOK_ALLOWED_PRIVATE_SUFFIXES"), ",") {
		if entry = strings.TrimSpace(entry); entry != "" {
			cfg.WebhookAllowedPrivateSuffixes = append(cfg.WebhookAllowedPrivateSuffixes, entry)
		}
	}

	if maxOpenStr := os.Getenv("DB_MAX_OPEN_CONNS"); maxOpenStr != "" {
		if n, err := parseInt(maxOpenStr); err == nil && n > 0 {
			cfg.DBMaxOpenConns = n
		}
	}

	if maxIdleStr := os.Getenv("DB_MAX_IDLE_CONNS"); maxIdleStr != "" {
		if n, err := parseInt(maxIdleStr); err == nil && n > 0 {
			cfg.DBMaxIdleConns = n
		}
	}

	// Support Railway's PORT variable as fallback for HTTP_ADDR.
	if cfg.HTTPAddr == "" {
		if port := os.Getenv("PORT"); port != "" {
			cfg.HTTPAddr = ":" + port
		} else {
			cfg.HTTPAddr = ":8080"
		}
	}
	if cfg.TSHostname == "" {
		cfg.TSHostname = "cronlite"
	}
	if cfg.TickIntervalStr == "" {
		cfg.TickIntervalStr = "30s"
	}
	if cfg.MigrateLockTimeoutStr == "" {
		cfg.MigrateLockTimeoutStr = "2m"
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
		cfg.ReconcileThresholdStr = "19m"
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
	if cfg.AdminSessionTTLStr == "" {
		cfg.AdminSessionTTLStr = "30m"
	}
	if cfg.AdminSessionAbsoluteTTLStr == "" {
		cfg.AdminSessionAbsoluteTTLStr = "12h"
	}

	// ParseDuration returns zero on error; Validate reports invalid values separately.
	cfg.TickInterval, _ = time.ParseDuration(cfg.TickIntervalStr)
	cfg.DBOpTimeout, _ = time.ParseDuration(cfg.DBOpTimeoutStr)
	cfg.MigrateLockTimeout, _ = time.ParseDuration(cfg.MigrateLockTimeoutStr)
	cfg.DBConnMaxLifetime, _ = time.ParseDuration(cfg.DBConnMaxLifetimeStr)
	cfg.DBConnMaxIdleTime, _ = time.ParseDuration(cfg.DBConnMaxIdleTimeStr)
	cfg.HTTPShutdownTimeout, _ = time.ParseDuration(cfg.HTTPShutdownTimeoutStr)
	cfg.DispatcherDrainTimeout, _ = time.ParseDuration(cfg.DispatcherDrainTimeoutStr)
	cfg.ReconcileInterval, _ = time.ParseDuration(cfg.ReconcileIntervalStr)
	cfg.ReconcileThreshold, _ = time.ParseDuration(cfg.ReconcileThresholdStr)
	cfg.ReconcileRequeueThreshold, _ = time.ParseDuration(cfg.ReconcileRequeueThresholdStr)
	cfg.CircuitBreakerCooldown, _ = time.ParseDuration(cfg.CircuitBreakerCooldownStr)
	cfg.DBPollInterval, _ = time.ParseDuration(cfg.DBPollIntervalStr)
	cfg.LeaderRetryInterval, _ = time.ParseDuration(cfg.LeaderRetryIntervalStr)
	cfg.LeaderHeartbeatInterval, _ = time.ParseDuration(cfg.LeaderHeartbeatIntervalStr)
	cfg.AdminSessionTTL, _ = time.ParseDuration(cfg.AdminSessionTTLStr)
	cfg.AdminSessionAbsoluteTTL, _ = time.ParseDuration(cfg.AdminSessionAbsoluteTTLStr)

	return cfg
}

// parseInt parses a string as an integer.
func parseInt(s string) (int, error) {
	if s == "" {
		return 0, nil
	}
	n, err := strconv.ParseUint(s, 10, strconv.IntSize-1)
	return int(n), err
}

// MaskedJSON returns the configuration as JSON with secrets masked.
func (c Config) MaskedJSON() ([]byte, error) {
	c.DatabaseURL = maskSecret(c.DatabaseURL)
	c.RedisAddr = maskSecret(c.RedisAddr)
	// Preserve the config command's existing omission of CORS origins.
	c.CORSOrigins = ""
	return json.MarshalIndent(c, "", "  ")
}

// maskSecret masks a secret value, preserving only the URI scheme if present.
func maskSecret(s string) string {
	if s == "" {
		return ""
	}
	for _, scheme := range []string{"postgres://", "postgresql://", "redis://", "rediss://"} {
		if strings.HasPrefix(s, scheme) {
			return scheme + "***"
		}
	}
	return "***"
}
