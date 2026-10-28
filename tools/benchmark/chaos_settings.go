package main

import (
	"fmt"
	"strings"
	"time"
)

// ChaosSettings holds the CronLite settings that chaos bounds are derived
// from. P0 copies them from tools/benchmark/docker-compose.yml (and the core
// default for DB_OP_TIMEOUT); keep both in sync until P5 reads them from the
// running stack.
type ChaosSettings struct {
	TickInterval       time.Duration `json:"tick_interval_ns"`
	LeaderRetry        time.Duration `json:"leader_retry_interval_ns"`
	LeaderHeartbeat    time.Duration `json:"leader_heartbeat_interval_ns"`
	DBPoll             time.Duration `json:"db_poll_interval_ns"`
	Lease              time.Duration `json:"lease_ns"`
	ReconcileInterval  time.Duration `json:"reconcile_interval_ns"`
	ReconcileThreshold time.Duration `json:"reconcile_threshold_ns"`
	DBOpTimeout        time.Duration `json:"db_op_timeout_ns"`
	Slack              time.Duration `json:"slack_ns"`
}

func chaosSettingsFor(cfg Config) ChaosSettings {
	return ChaosSettings{
		TickInterval:       time.Second,
		LeaderRetry:        time.Second,
		LeaderHeartbeat:    500 * time.Millisecond,
		DBPoll:             100 * time.Millisecond,
		Lease:              cfg.RequeueThreshold,
		ReconcileInterval:  time.Second,
		ReconcileThreshold: 19 * time.Minute,
		DBOpTimeout:        5 * time.Second,
		Slack:              cfg.ChaosSlack,
	}
}

// Bound is an invariant limit with the formula that produced it, so a breach
// points at the settings that move it.
type Bound struct {
	Value   time.Duration `json:"value_ns"`
	Formula string        `json:"formula"`
	Plugged string        `json:"plugged"`
	Knobs   []string      `json:"knobs"`
}

type boundTerm struct {
	symbol string
	value  time.Duration
	knob   string
}

func sumBound(terms ...boundTerm) Bound {
	var bound Bound
	var symbols, values []string
	for _, term := range terms {
		bound.Value += term.value
		symbols = append(symbols, term.symbol)
		values = append(values, term.value.String())
		if term.knob != "" {
			bound.Knobs = append(bound.Knobs, term.knob)
		}
	}
	bound.Formula = strings.Join(symbols, " + ")
	bound.Plugged = fmt.Sprintf("%s = %s", strings.Join(values, " + "), bound.Value)
	return bound
}

func (s ChaosSettings) slack() boundTerm { return boundTerm{"ε", s.Slack, ""} }

func (s ChaosSettings) faultLatenessBound(f FaultSpec) Bound {
	duration := boundTerm{"D", f.Duration, ""}
	tick := boundTerm{"T", s.TickInterval, "TICK_INTERVAL"}
	if f.Type == FaultPGPause {
		return sumBound(duration, boundTerm{"DB_OP_TIMEOUT", s.DBOpTimeout, "DB_OP_TIMEOUT"}, tick, s.slack())
	}
	return sumBound(
		duration,
		boundTerm{"L", s.LeaderRetry, "LEADER_RETRY_INTERVAL"},
		tick,
		boundTerm{"P", s.DBPoll, "DB_POLL_INTERVAL"},
		s.slack(),
	)
}

func (s ChaosSettings) steadyLatenessBound(baselineP99 time.Duration) Bound {
	bound := sumBound(boundTerm{"baseline p99 × 3", 3 * baselineP99, ""}, s.slack())
	bound.Plugged = fmt.Sprintf("%s × 3 + %s = %s", baselineP99, s.Slack, bound.Value)
	return bound
}

func (s ChaosSettings) leaderlessBound() Bound {
	return sumBound(
		boundTerm{"L", s.LeaderRetry, "LEADER_RETRY_INTERVAL"},
		boundTerm{"H", s.LeaderHeartbeat, "LEADER_HEARTBEAT_INTERVAL"},
		s.slack(),
	)
}

func (s ChaosSettings) stuckLeaseBound() Bound {
	return sumBound(
		boundTerm{"Q", s.Lease, "RECONCILE_REQUEUE_THRESHOLD"},
		boundTerm{"RI", s.ReconcileInterval, "RECONCILE_INTERVAL"},
		s.slack(),
	)
}

func (s ChaosSettings) relevantConfig() map[string]string {
	return map[string]string{
		"TICK_INTERVAL":               s.TickInterval.String(),
		"LEADER_RETRY_INTERVAL":       s.LeaderRetry.String(),
		"LEADER_HEARTBEAT_INTERVAL":   s.LeaderHeartbeat.String(),
		"DB_POLL_INTERVAL":            s.DBPoll.String(),
		"RECONCILE_REQUEUE_THRESHOLD": s.Lease.String(),
		"RECONCILE_INTERVAL":          s.ReconcileInterval.String(),
		"RECONCILE_THRESHOLD":         s.ReconcileThreshold.String(),
		"DB_OP_TIMEOUT":               s.DBOpTimeout.String(),
		"chaos_slack":                 s.Slack.String(),
	}
}
