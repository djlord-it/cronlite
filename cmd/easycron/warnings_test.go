package main

import (
	"bytes"
	"log"
	"strings"
	"testing"

	"github.com/djlord-it/easy-cron/internal/config"
)

// captureLogOutput calls logConfigWarnings with the given config and returns
// the captured log output as a string.
func captureLogOutput(cfg *config.Config) string {
	var buf bytes.Buffer
	original := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(original)

	logConfigWarnings(cfg)
	return buf.String()
}

func TestLogConfigWarnings_ChannelNoReconciler(t *testing.T) {
	cfg := &config.Config{
		DispatchMode:     "channel",
		ReconcileEnabled: false,
		MetricsEnabled:   true,
		DispatcherWorkers: 1,
	}
	output := captureLogOutput(cfg)

	// Warning 1: channel + no reconciler (specialization)
	if !strings.Contains(output, "WARNING [P0]: DISPATCH_MODE=channel with RECONCILE_ENABLED=false") {
		t.Error("expected channel+no-reconciler P0 warning, got:", output)
	}

	// Warning 2: no reconciler (general) should also fire
	if !strings.Contains(output, "WARNING [P0]: RECONCILE_ENABLED=false") {
		t.Error("expected general no-reconciler P0 warning, got:", output)
	}

	// Warning 4: channel mode info
	if !strings.Contains(output, "INFO: DISPATCH_MODE=channel") {
		t.Error("expected channel mode INFO, got:", output)
	}

	// Warning 3: metrics enabled, should NOT fire
	if strings.Contains(output, "WARNING [P1]: METRICS_ENABLED=false") {
		t.Error("did not expect metrics warning when metrics enabled, got:", output)
	}

	// Warning 5: not db mode, should NOT fire
	if strings.Contains(output, "DISPATCHER_WORKERS=1") {
		t.Error("did not expect db workers warning in channel mode, got:", output)
	}
}

func TestLogConfigWarnings_ChannelWithReconciler(t *testing.T) {
	cfg := &config.Config{
		DispatchMode:     "channel",
		ReconcileEnabled: true,
		MetricsEnabled:   true,
		DispatcherWorkers: 1,
	}
	output := captureLogOutput(cfg)

	// No P0 warnings expected
	if strings.Contains(output, "WARNING [P0]") {
		t.Error("did not expect any P0 warnings, got:", output)
	}

	// Channel info should still fire
	if !strings.Contains(output, "INFO: DISPATCH_MODE=channel") {
		t.Error("expected channel mode INFO, got:", output)
	}
}

func TestLogConfigWarnings_DBNoReconciler(t *testing.T) {
	cfg := &config.Config{
		DispatchMode:     "db",
		ReconcileEnabled: false,
		MetricsEnabled:   true,
		DispatcherWorkers: 2,
	}
	output := captureLogOutput(cfg)

	// Warning 1: should NOT fire (not channel mode)
	if strings.Contains(output, "DISPATCH_MODE=channel with RECONCILE_ENABLED=false") {
		t.Error("did not expect channel-specific P0 warning in db mode, got:", output)
	}

	// Warning 2: no reconciler (general) should fire
	if !strings.Contains(output, "WARNING [P0]: RECONCILE_ENABLED=false") {
		t.Error("expected general no-reconciler P0 warning, got:", output)
	}

	// Warning 4: should NOT fire (not channel mode)
	if strings.Contains(output, "INFO: DISPATCH_MODE=channel") {
		t.Error("did not expect channel mode INFO in db mode, got:", output)
	}

	// Warning 5: workers=2, should NOT fire
	if strings.Contains(output, "DISPATCHER_WORKERS=1") {
		t.Error("did not expect db workers warning with workers=2, got:", output)
	}
}

func TestLogConfigWarnings_DBWithReconciler(t *testing.T) {
	cfg := &config.Config{
		DispatchMode:     "db",
		ReconcileEnabled: true,
		MetricsEnabled:   true,
		DispatcherWorkers: 4,
	}
	output := captureLogOutput(cfg)

	// No warnings at all expected (metrics on, reconciler on, db mode, workers > 1)
	if strings.Contains(output, "WARNING") {
		t.Error("did not expect any warnings, got:", output)
	}
	if strings.Contains(output, "INFO") {
		t.Error("did not expect any INFO messages, got:", output)
	}
}

func TestLogConfigWarnings_MetricsDisabled(t *testing.T) {
	cfg := &config.Config{
		DispatchMode:     "db",
		ReconcileEnabled: true,
		MetricsEnabled:   false,
		DispatcherWorkers: 4,
	}
	output := captureLogOutput(cfg)

	if !strings.Contains(output, "WARNING [P1]: METRICS_ENABLED=false") {
		t.Error("expected metrics P1 warning, got:", output)
	}
}

func TestLogConfigWarnings_DBWorkersOne(t *testing.T) {
	cfg := &config.Config{
		DispatchMode:     "db",
		ReconcileEnabled: true,
		MetricsEnabled:   true,
		DispatcherWorkers: 1,
	}
	output := captureLogOutput(cfg)

	if !strings.Contains(output, "INFO: DISPATCH_MODE=db with DISPATCHER_WORKERS=1") {
		t.Error("expected db workers=1 INFO, got:", output)
	}
}

func TestLogConfigWarnings_DBWorkersFour(t *testing.T) {
	cfg := &config.Config{
		DispatchMode:     "db",
		ReconcileEnabled: true,
		MetricsEnabled:   true,
		DispatcherWorkers: 4,
	}
	output := captureLogOutput(cfg)

	if strings.Contains(output, "DISPATCHER_WORKERS") {
		t.Error("did not expect workers warning with workers=4, got:", output)
	}
}

func TestLogConfigWarnings_SSLModeDisable(t *testing.T) {
	cfg := &config.Config{
		DatabaseURL:       "postgres://user:pass@host:5432/db?sslmode=disable",
		DispatchMode:      "db",
		ReconcileEnabled:  true,
		MetricsEnabled:    true,
		DispatcherWorkers: 4,
	}
	output := captureLogOutput(cfg)

	if !strings.Contains(output, "WARNING") || !strings.Contains(output, "sslmode=disable") {
		t.Error("expected sslmode=disable warning, got:", output)
	}
}

func TestLogConfigWarnings_SSLModeNotDisable(t *testing.T) {
	cfg := &config.Config{
		DatabaseURL:       "postgres://user:pass@host:5432/db?sslmode=require",
		DispatchMode:      "db",
		ReconcileEnabled:  true,
		MetricsEnabled:    true,
		DispatcherWorkers: 4,
	}
	output := captureLogOutput(cfg)

	if strings.Contains(output, "sslmode") {
		t.Error("did not expect sslmode warning, got:", output)
	}
}

func TestLogConfigWarnings_AllWarnings(t *testing.T) {
	// Worst case: channel mode, no reconciler, no metrics
	cfg := &config.Config{
		DispatchMode:     "channel",
		ReconcileEnabled: false,
		MetricsEnabled:   false,
		DispatcherWorkers: 1,
	}
	output := captureLogOutput(cfg)

	expected := []string{
		"WARNING [P0]: DISPATCH_MODE=channel with RECONCILE_ENABLED=false",
		"WARNING [P0]: RECONCILE_ENABLED=false",
		"WARNING [P1]: METRICS_ENABLED=false",
		"INFO: DISPATCH_MODE=channel",
	}
	for _, want := range expected {
		if !strings.Contains(output, want) {
			t.Errorf("expected %q in output, got: %s", want, output)
		}
	}

	// Should NOT have db workers warning (channel mode)
	if strings.Contains(output, "DISPATCHER_WORKERS=1") {
		t.Error("did not expect db workers warning in channel mode, got:", output)
	}
}
