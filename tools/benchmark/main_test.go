package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRunWritesAllRequiredOutputs(t *testing.T) {
	dir := t.TempDir()
	deps := fixtureRuntimeDependencies(false)
	code := run(context.Background(), []string{
		"--output", dir,
		"--receiver-addr", "127.0.0.1:0",
	}, deps)
	if code != exitSuccess {
		t.Fatalf("exit code = %d; stderr=%s", code, deps.Stderr.(*bytes.Buffer))
	}
	for _, name := range []string{
		"benchmark-results.json",
		"benchmark-results.csv",
		"benchmark-report.md",
	} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestRunFailsOnCorrectnessWhenConfigured(t *testing.T) {
	dir := t.TempDir()
	deps := fixtureRuntimeDependencies(true)
	code := run(context.Background(), []string{
		"--output", dir,
		"--receiver-addr", "127.0.0.1:0",
		"--fail-on-correctness",
	}, deps)
	if code != exitCorrectness {
		t.Fatalf("exit code = %d; stderr=%s", code, deps.Stderr.(*bytes.Buffer))
	}
}

func TestRunRejectsInvalidConfiguration(t *testing.T) {
	deps := fixtureRuntimeDependencies(false)
	code := run(context.Background(), []string{
		"--scenario", "duplicate-race",
	}, deps)
	if code != exitConfiguration {
		t.Fatalf("exit code = %d; stderr=%s", code, deps.Stderr.(*bytes.Buffer))
	}
}

func TestParseConfigReadsEnvironmentSecretsWithoutPrintingThem(t *testing.T) {
	t.Setenv("CRONLITE_API_KEY", "environment-secret")
	var stderr bytes.Buffer
	cfg, command, err := parseConfig([]string{"--sample-count", "12"}, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.APIKey != "environment-secret" || cfg.SampleCount != 12 {
		t.Fatalf("config=%+v", cfg.Redacted())
	}
	if bytes.Contains([]byte(command), []byte("environment-secret")) {
		t.Fatalf("command exposed API key: %s", command)
	}
}

func TestWaitForAPIReadinessRetriesUntilHealthy(t *testing.T) {
	api := &flakyHealthAPI{failures: 2}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := waitForAPIReadiness(ctx, api, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if api.calls != 3 {
		t.Fatalf("health calls = %d", api.calls)
	}
}

func fixtureRuntimeDependencies(withCritical bool) runtimeDependencies {
	return runtimeDependencies{
		Stdout: io.Discard,
		Stderr: &bytes.Buffer{},
		StartReceiver: func(_ string, secret string, limits ReceiverLimits) (*receiverServer, error) {
			return startReceiver("127.0.0.1:0", secret, limits)
		},
		NewAPI: func(string, string, time.Duration) scenarioAPI {
			return failingScenarioAPI{}
		},
		RunScenarios: func(_ context.Context, _ *scenarioEnvironment) []ScenarioResult {
			result := fixtureRunResult().Scenarios
			if !withCritical {
				result[0].Status = ScenarioPassed
				result[0].Findings = nil
				result[0].Executions[0].Findings = nil
			}
			return result
		},
		CaptureEnvironment: func(context.Context, Config, *composeController) EnvironmentInfo {
			return fixtureRunResult().Environment
		},
		WriteOutputs: writeOutputs,
	}
}

type flakyHealthAPI struct {
	calls    int
	failures int
}

func (f *flakyHealthAPI) health(context.Context, bool) (Observation, error) {
	f.calls++
	if f.calls <= f.failures {
		return Observation{}, errors.New("not ready")
	}
	return Observation{StatusCode: 200}, nil
}
