package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/djlord-it/cronlite/internal/dispatcher"
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
		if cfg.ReconcileRequeueThreshold <= 0 && cfg.ReconcileRequeueThresholdStr != "" {
			errs = append(errs, ValidationError{
				Field:   "RECONCILE_REQUEUE_THRESHOLD",
				Message: "must be positive",
			})
		}
		maxRetry := dispatcher.MaxRetryDuration()
		if cfg.ReconcileThreshold > 0 && cfg.ReconcileThreshold < maxRetry {
			errs = append(errs, ValidationError{
				Field: "RECONCILE_THRESHOLD",
				Message: fmt.Sprintf("must be >= dispatcher max retry duration (%s) to prevent duplicate deliveries",
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

	if strings.EqualFold(cfg.Environment, "production") {
		errs = append(errs, validateProduction(cfg)...)
	}

	if len(errs) > 0 {
		return errs
	}
	return nil
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

	return errs
}
