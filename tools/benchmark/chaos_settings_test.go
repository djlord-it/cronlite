package main

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func testChaosSettings() ChaosSettings {
	cfg := defaultConfig()
	cfg.RequeueThreshold = 5 * time.Second
	cfg.ChaosSlack = 2 * time.Second
	return chaosSettingsFor(cfg)
}

func TestFaultLatenessBound(t *testing.T) {
	settings := testChaosSettings()
	bound := settings.faultLatenessBound(FaultSpec{Type: FaultProcStop, Target: "leader", Intensity: "med", Duration: 30 * time.Second})
	if bound.Value != 34100*time.Millisecond {
		t.Fatalf("proc-stop bound = %s", bound.Value)
	}
	if !reflect.DeepEqual(bound.Knobs, []string{"LEADER_RETRY_INTERVAL", "TICK_INTERVAL", "DB_POLL_INTERVAL"}) {
		t.Fatalf("proc-stop knobs = %v", bound.Knobs)
	}
	if !strings.Contains(bound.Plugged, "= 34.1s") {
		t.Fatalf("plugged = %q", bound.Plugged)
	}
	if bound.Formula != "D + L + T + P + ε" {
		t.Fatalf("formula = %q", bound.Formula)
	}

	pg := settings.faultLatenessBound(FaultSpec{Type: FaultPGPause, Target: "postgres", Intensity: "med", Duration: 30 * time.Second})
	if pg.Value != 38*time.Second {
		t.Fatalf("pg-pause bound = %s", pg.Value)
	}
	if !reflect.DeepEqual(pg.Knobs, []string{"DB_OP_TIMEOUT", "TICK_INTERVAL"}) {
		t.Fatalf("pg-pause knobs = %v", pg.Knobs)
	}
}

func TestSteadyBound(t *testing.T) {
	if got := testChaosSettings().steadyLatenessBound(200 * time.Millisecond).Value; got != 2600*time.Millisecond {
		t.Fatalf("steady bound = %s", got)
	}
}

func TestLeaderlessBound(t *testing.T) {
	if got := testChaosSettings().leaderlessBound().Value; got != 3500*time.Millisecond {
		t.Fatalf("leaderless bound = %s", got)
	}
}

func TestStuckLeaseBound(t *testing.T) {
	if got := testChaosSettings().stuckLeaseBound().Value; got != 8*time.Second {
		t.Fatalf("stuck lease bound = %s", got)
	}
}

func TestRelevantConfigNamesEnvironmentVariables(t *testing.T) {
	config := testChaosSettings().relevantConfig()
	if config["RECONCILE_REQUEUE_THRESHOLD"] != "5s" || config["TICK_INTERVAL"] != "1s" {
		t.Fatalf("relevant config = %v", config)
	}
}
