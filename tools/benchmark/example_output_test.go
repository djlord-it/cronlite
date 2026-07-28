package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGenerateExampleOutput(t *testing.T) {
	outputDir := t.TempDir()
	if requested := os.Getenv("CRONLITE_BENCHMARK_EXAMPLE_OUTPUT"); requested != "" {
		outputDir = requested
	}
	started := time.Now().UTC()
	cfg := defaultConfig()
	cfg.Scenarios = []string{"smoke"}
	cfg.StartCompose = true
	cfg.Diagnostic = true
	cfg.BaseURL = "http://127.0.0.1:18080"
	cfg.ReceiverAddr = "0.0.0.0:19090"
	cfg.ReceiverPublicURL = "http://host.docker.internal:19090"
	cfg.MetricsURL = "http://127.0.0.1:18080/metrics"
	cfg.DatabaseURL = "postgres://cronlite:cronlite@127.0.0.1:15432/cronlite?sslmode=disable"
	cfg.DispatchMode = "db"
	environment := captureEnvironment(context.Background(), cfg, nil)
	if environment.RelevantConfig == nil {
		environment.RelevantConfig = make(map[string]string)
	}
	environment.RelevantConfig["example_kind"] = "capability-limited local example"
	result := RunResult{
		SchemaVersion: resultSchemaVersion,
		RunID:         "example-docker-unavailable",
		RandomSeed:    cfg.RandomSeed,
		Command: "go run ./tools/benchmark --start-compose --diagnostic " +
			"--scenario smoke --sample-count 10 --output tools/benchmark/example-output",
		StartedAt:   started,
		FinishedAt:  time.Now().UTC(),
		Environment: environment,
		Config:      cfg.Redacted(),
		Scenarios: []ScenarioResult{{
			Name:       "smoke",
			Status:     ScenarioSkipped,
			Reason:     "managed local smoke skipped: Docker daemon was unavailable",
			StartedAt:  started,
			FinishedAt: time.Now().UTC(),
		}},
		Limitations: benchmarkLimitations(),
	}
	paths, err := writeOutputs(outputDir, result)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{paths.JSON, paths.CSV, paths.Markdown} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("generated output %s: %v", filepath.Base(path), err)
		}
	}
}
