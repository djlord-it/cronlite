package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/djlord-it/cronlite/internal/dispatcher"
	"github.com/djlord-it/cronlite/internal/tailnet"
	"github.com/djlord-it/cronlite/internal/webhookurl"
)

// ValidationError represents a configuration validation error.
type ValidationError struct {
	Field   string
	Message string
}

func (e ValidationError) Error() string {
	return fmt.Sprintf("%s: %s", e.Field, e.Message)
}

// ValidationErrors is a collection of validation errors.
type ValidationErrors []ValidationError

func (e ValidationErrors) Error() string {
	if len(e) == 0 {
		return ""
	}
	if len(e) == 1 {
		return e[0].Error()
	}
	msg := fmt.Sprintf("%d validation errors:", len(e))
	for _, err := range e {
		msg += "\n  - " + err.Error()
	}
	return msg
}

// Validate checks the configuration for errors.
// Returns nil if valid, or ValidationErrors if invalid.
func Validate(cfg Config) error {
	var errs ValidationErrors

	// DATABASE_URL is required
	if cfg.DatabaseURL == "" {
		errs = append(errs, ValidationError{
			Field:   "DATABASE_URL",
			Message: "required",
		})
	}

	// TICK_INTERVAL must be a valid duration
	if cfg.TickIntervalStr != "" {
		d, err := time.ParseDuration(cfg.TickIntervalStr)
		if err != nil {
			errs = append(errs, ValidationError{
				Field:   "TICK_INTERVAL",
				Message: fmt.Sprintf("invalid duration: %v", err),
			})
		} else if d <= 0 {
			errs = append(errs, ValidationError{
				Field:   "TICK_INTERVAL",
				Message: "must be positive",
			})
		}
	}

	// DISPATCH_MODE must be "channel" or "db"
	if cfg.DispatchMode != "" && cfg.DispatchMode != "channel" && cfg.DispatchMode != "db" {
		errs = append(errs, ValidationError{
			Field:   "DISPATCH_MODE",
			Message: fmt.Sprintf("must be 'channel' or 'db', got %q", cfg.DispatchMode),
		})
	}

	// DB dispatch mode: leader election parameters
	if cfg.DispatchMode == "db" {
		if cfg.DBMaxOpenConns == 1 {
			errs = append(errs, ValidationError{Field: "DB_MAX_OPEN_CONNS", Message: "must allow at least 2 connections in db mode; leader election reserves one"})
		}
		if cfg.DBPollIntervalStr != "" && cfg.DBPollInterval <= 0 {
			errs = append(errs, ValidationError{Field: "DB_POLL_INTERVAL", Message: "must be a positive duration"})
		}
		if cfg.LeaderRetryInterval <= 0 && cfg.LeaderRetryIntervalStr != "" {
			errs = append(errs, ValidationError{
				Field:   "LEADER_RETRY_INTERVAL",
				Message: "must be positive",
			})
		}
		if cfg.LeaderHeartbeatInterval <= 0 && cfg.LeaderHeartbeatIntervalStr != "" {
			errs = append(errs, ValidationError{
				Field:   "LEADER_HEARTBEAT_INTERVAL",
				Message: "must be positive",
			})
		}
		if cfg.LeaderHeartbeatInterval > 0 && cfg.LeaderRetryInterval > 0 &&
			cfg.LeaderHeartbeatInterval >= cfg.LeaderRetryInterval {
			errs = append(errs, ValidationError{
				Field:   "LEADER_HEARTBEAT_INTERVAL",
				Message: fmt.Sprintf("must be less than LEADER_RETRY_INTERVAL (%s)", cfg.LeaderRetryInterval),
			})
		}
	}
	if cfg.DBOpTimeoutStr != "" && cfg.DBOpTimeout <= 0 {
		errs = append(errs, ValidationError{Field: "DB_OP_TIMEOUT", Message: "must be a positive duration"})
	}
	if cfg.MigrateLockTimeoutStr != "" && cfg.MigrateLockTimeout <= 0 {
		errs = append(errs, ValidationError{Field: "MIGRATE_LOCK_TIMEOUT", Message: "must be a positive duration"})
	}
	if cfg.DispatcherWorkers > int(^uint(0)>>1)/4 {
		errs = append(errs, ValidationError{Field: "DISPATCHER_WORKERS", Message: "delivery slot count overflows int"})
	}
	// Both dispatch modes use leases even when automatic recovery is disabled.
	if cfg.ReconcileRequeueThresholdStr != "" {
		minimum := 30 * time.Second
		if cfg.Environment == "benchmark" {
			minimum = time.Millisecond
		}
		if cfg.ReconcileRequeueThreshold < minimum {
			errs = append(errs, ValidationError{Field: "RECONCILE_REQUEUE_THRESHOLD", Message: fmt.Sprintf("execution lease must be >= %s", minimum)})
		}
	}

	if _, err := webhookurl.ParseAllowlist(cfg.WebhookAllowedPrivateSuffixes); err != nil {
		errs = append(errs, ValidationError{Field: "WEBHOOK_ALLOWED_PRIVATE_SUFFIXES", Message: err.Error()})
	}

	if cfg.TailnetEnabled() {
		errs = append(errs, validateTailnet(cfg, tailnet.Supported)...)
	}
	if !cfg.WebhookAllowTailnet && cfg.WebhookTailnetHosts != "" {
		errs = append(errs, ValidationError{
			Field:   "WEBHOOK_TAILNET_HOSTS",
			Message: "only used when WEBHOOK_ALLOW_TAILNET=true",
		})
	}
	if cfg.TSDisablePublicListener && !cfg.TSListen {
		errs = append(errs, ValidationError{
			Field:   "TS_DISABLE_PUBLIC_LISTENER",
			Message: "requires TS_LISTEN=true; otherwise no HTTP listener would remain",
		})
	}
	if cfg.TSIdentityLogin && (!cfg.TSListen || !cfg.AdminEnabled) {
		errs = append(errs, ValidationError{
			Field:   "TS_IDENTITY_LOGIN",
			Message: "requires TS_LISTEN=true and ADMIN_ENABLED=true; identity sign-in is only offered on the tailnet listener",
		})
	}

	// Reconciler parameters (when enabled)
	if cfg.ReconcileEnabled {
		if cfg.ReconcileInterval <= 0 && cfg.ReconcileIntervalStr != "" {
			errs = append(errs, ValidationError{
				Field:   "RECONCILE_INTERVAL",
				Message: "must be positive",
			})
		}
		if cfg.ReconcileThreshold <= 0 && cfg.ReconcileThresholdStr != "" {
			errs = append(errs, ValidationError{
				Field:   "RECONCILE_THRESHOLD",
				Message: "must be positive",
			})
		}
		maxRetry := dispatcher.MaxRetryDuration()
		if cfg.ReconcileThreshold > 0 && cfg.ReconcileThreshold < maxRetry {
			errs = append(errs, ValidationError{
				Field: "RECONCILE_THRESHOLD",
				Message: fmt.Sprintf("must retain the initial-notification age minimum (%s)",
					maxRetry),
			})
		}
	}

	// Circuit breaker: cooldown must be positive when enabled
	if cfg.CircuitBreakerThreshold > 0 && cfg.CircuitBreakerCooldown <= 0 && cfg.CircuitBreakerCooldownStr != "" {
		errs = append(errs, ValidationError{
			Field:   "CIRCUIT_BREAKER_COOLDOWN",
			Message: "must be positive when circuit breaker is enabled",
		})
	}

	if cfg.AdminEnabled {
		if cfg.AdminSessionTTL <= 0 {
			errs = append(errs, ValidationError{
				Field:   "ADMIN_SESSION_TTL",
				Message: "must be a positive duration when admin UI is enabled",
			})
		}
		if cfg.AdminSessionAbsoluteTTL <= 0 {
			errs = append(errs, ValidationError{
				Field:   "ADMIN_SESSION_ABSOLUTE_TTL",
				Message: "must be a positive duration when admin UI is enabled",
			})
		}
		if cfg.AdminSessionTTL > 0 &&
			cfg.AdminSessionAbsoluteTTL > 0 &&
			cfg.AdminSessionAbsoluteTTL < cfg.AdminSessionTTL {
			errs = append(errs, ValidationError{
				Field:   "ADMIN_SESSION_ABSOLUTE_TTL",
				Message: "must be greater than or equal to ADMIN_SESSION_TTL",
			})
		}
	}

	if strings.EqualFold(cfg.Environment, "production") {
		errs = append(errs, validateProduction(cfg)...)
	}

	if len(errs) > 0 {
		return errs
	}
	return nil
}

// validateTailnet checks the settings used when WEBHOOK_ALLOW_TAILNET or
// TS_LISTEN is true. supported reports whether the binary was built with
// Tailscale support.
func validateTailnet(cfg Config, supported bool) ValidationErrors {
	var errs ValidationErrors
	field := "WEBHOOK_ALLOW_TAILNET"
	if !cfg.WebhookAllowTailnet {
		field = "TS_LISTEN"
	}
	if !supported {
		errs = append(errs, ValidationError{
			Field:   field,
			Message: "this binary was built without Tailscale support; use a build made with -tags tailscale",
		})
	}
	if cfg.TSStateDir == "" {
		errs = append(errs, ValidationError{
			Field:   "TS_STATE_DIR",
			Message: fmt.Sprintf("required when %s=true (holds the node's identity across restarts)", field),
		})
	}
	if !validNodeName(cfg.TSHostname) {
		errs = append(errs, ValidationError{
			Field:   "TS_HOSTNAME",
			Message: fmt.Sprintf("must be a single DNS label (a-z, 0-9, '-', at most 63 characters), got %q", cfg.TSHostname),
		})
	}
	if cfg.WebhookAllowTailnet {
		if cfg.WebhookTailnetHosts == "" {
			errs = append(errs, ValidationError{
				Field:   "WEBHOOK_TAILNET_HOSTS",
				Message: "required when WEBHOOK_ALLOW_TAILNET=true; list the tailnet hosts each namespace may call, e.g. billing=invoices;ops=*, or *=* to allow every namespace every host",
			})
		} else if _, err := webhookurl.ParseTailnetPolicy(cfg.WebhookTailnetHosts); err != nil {
			errs = append(errs, ValidationError{Field: "WEBHOOK_TAILNET_HOSTS", Message: err.Error()})
		}
	}
	return errs
}

func validNodeName(s string) bool {
	if s == "" || len(s) > 63 || s[0] == '-' || s[len(s)-1] == '-' {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

func validateProduction(cfg Config) ValidationErrors {
	var errs ValidationErrors

	if cfg.DispatchMode != "db" {
		errs = append(errs, ValidationError{
			Field:   "DISPATCH_MODE",
			Message: "must be 'db' when CRONLITE_ENV=production",
		})
	}
	if !cfg.ReconcileEnabled {
		errs = append(errs, ValidationError{
			Field:   "RECONCILE_ENABLED",
			Message: "must be true when CRONLITE_ENV=production",
		})
	}
	if !cfg.MetricsEnabled {
		errs = append(errs, ValidationError{
			Field:   "METRICS_ENABLED",
			Message: "must be true when CRONLITE_ENV=production",
		})
	}
	if cfg.APIKey == "" {
		errs = append(errs, ValidationError{
			Field:   "API_KEY",
			Message: "required when CRONLITE_ENV=production",
		})
	}
	if cfg.AdminEnabled && !cfg.AdminCookieSecure {
		errs = append(errs, ValidationError{
			Field:   "ADMIN_COOKIE_SECURE",
			Message: "must be true when admin UI is enabled in production",
		})
	}

	return errs
}
