package main

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestComposePostgresHealthcheckWaitsForFinalServerProcess(t *testing.T) {
	compose, err := os.ReadFile("docker-compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	healthcheck := string(compose)
	if !strings.Contains(healthcheck, `test \"$$(head -1 \"$$PGDATA/postmaster.pid\")\" = \"1\"`) {
		t.Fatal("postgres healthcheck must reject the temporary init server")
	}
}

func TestComposeBenchmarkStackDoesNotThrottleDispatcherMeasurements(t *testing.T) {
	compose, err := os.ReadFile("docker-compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	configuration := string(compose)
	for _, setting := range []string{
		`RATE_LIMIT: "100000"`,
		`NAMESPACE_RATE_LIMIT: "100000"`,
	} {
		if !strings.Contains(configuration, setting) {
			t.Errorf("benchmark compose is missing %s", setting)
		}
	}
}

func TestComposeUsesProductionRequeueThresholdUnlessFailureRunOverridesIt(t *testing.T) {
	compose, err := os.ReadFile("docker-compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	const setting = `RECONCILE_REQUEUE_THRESHOLD: "${BENCHMARK_REQUEUE_THRESHOLD:-2m}"`
	if !strings.Contains(string(compose), setting) {
		t.Fatalf("benchmark compose is missing configurable safe default %s", setting)
	}
}

func TestComposeControllerRejectsUnownedProject(t *testing.T) {
	controller := &composeController{
		File:        "tools/benchmark/docker-compose.yml",
		Project:     "production",
		OwnedPrefix: benchmarkComposePrefix,
		Runner:      &recordingCommandRunner{},
	}
	if err := controller.validateDestructiveTarget(); !errors.Is(err, ErrUnownedComposeProject) {
		t.Fatalf("expected ownership error, got %v", err)
	}
}

func TestComposeControllerTargetsFileAndProjectWithoutShell(t *testing.T) {
	runner := &recordingCommandRunner{}
	controller := &composeController{
		File:        "tools/benchmark/docker-compose.yml",
		Project:     benchmarkComposePrefix + "run-1",
		OwnedPrefix: benchmarkComposePrefix,
		Runner:      runner,
	}
	if err := controller.stopService(context.Background(), "cronlite_1"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"compose",
		"--file", "tools/benchmark/docker-compose.yml",
		"--project-name", benchmarkComposePrefix + "run-1",
		"stop", "cronlite_1",
	}
	if !reflect.DeepEqual(runner.args, want) {
		t.Fatalf("args: want=%v got=%v", want, runner.args)
	}
}

func TestComposeControllerRejectsUnknownService(t *testing.T) {
	controller := &composeController{
		File:        "tools/benchmark/docker-compose.yml",
		Project:     benchmarkComposePrefix + "run-1",
		OwnedPrefix: benchmarkComposePrefix,
		Runner:      &recordingCommandRunner{},
	}
	if err := controller.stopService(context.Background(), "production-db"); !errors.Is(err, ErrUnknownComposeService) {
		t.Fatalf("expected service error, got %v", err)
	}
}

type recordingCommandRunner struct {
	name string
	args []string
	err  error
}

func (r *recordingCommandRunner) run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.name = name
	r.args = append([]string(nil), args...)
	return nil, r.err
}
