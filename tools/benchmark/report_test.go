package main

import (
	"strings"
	"testing"
	"time"
)

func TestReportPutsCriticalFindingsBeforePerformance(t *testing.T) {
	report := renderReport(fixtureRunResult(), OutputPaths{
		JSON:     "benchmark-results.json",
		CSV:      "benchmark-results.csv",
		Markdown: "benchmark-report.md",
	})
	critical := strings.Index(report, "CRITICAL:")
	performance := strings.Index(report, "## API Latency")
	if critical < 0 || performance < 0 || critical > performance {
		t.Fatalf("incorrect report ordering:\n%s", report)
	}
}

func TestReportContainsRequiredSections(t *testing.T) {
	report := renderReport(fixtureRunResult(), OutputPaths{})
	for _, section := range []string{
		"Environment and Configuration",
		"Correctness Findings",
		"Scenario Summary",
		"Baseline HTTP Latency",
		"API Latency",
		"Scheduler Accuracy",
		"Delivery Latency",
		"Retry Timing",
		"Throughput",
		"Failure Analysis",
		"Duplicate-Delivery Findings",
		"Resource Usage",
		"Limitations",
		"Reproduction Instructions",
		"Raw File Locations",
	} {
		if !strings.Contains(report, "## "+section) {
			t.Errorf("missing section %q", section)
		}
	}
}

func TestReportStatesSmallSamplePercentilesAreUnstable(t *testing.T) {
	report := renderReport(fixtureRunResult(), OutputPaths{})
	if !strings.Contains(report, "p99 is unstable") {
		t.Fatalf("missing percentile warning:\n%s", report)
	}
}

func TestReportIncludesNamedControlPlaneAPIObservation(t *testing.T) {
	result := fixtureRunResult()
	result.Scenarios[0].Observations = append(result.Scenarios[0].Observations, Observation{
		Kind:     "api",
		Name:     "api_create_latency_ms",
		Duration: 12 * time.Millisecond,
	})
	report := renderReport(result, OutputPaths{})
	if !strings.Contains(report, "| api_create_latency_ms | 1 |") {
		t.Fatalf("API observation missing from report:\n%s", report)
	}
}

func TestReportCountsScheduledDeliveredExecutionAsSuccessful(t *testing.T) {
	result := fixtureRunResult()
	record := result.Scenarios[0].Executions[0]
	record.PollBounds = nil
	record.APIExecution.Status = "delivered"
	record.APIExecution.TriggerType = "scheduled"
	result.Scenarios[0].Name = "recurring"
	result.Scenarios[0].Executions = []ExecutionRecord{record}

	report := renderReport(result, OutputPaths{})
	if !strings.Contains(report, "| recurring | 1.000 | 1.000 | 100.00% |") {
		t.Fatalf("scheduled success missing from throughput:\n%s", report)
	}
}

func fixtureRunResult() RunResult {
	record := correlate(fixtureExecutionRecord())
	record.Findings = append(record.Findings, Finding{
		Severity:    SeverityCritical,
		Code:        "invalid_webhook_signature",
		Message:     "signature failed",
		Scenario:    "smoke",
		ExecutionID: "exec-1",
	})
	started := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	return RunResult{
		SchemaVersion: resultSchemaVersion,
		RunID:         "run-1",
		RandomSeed:    1,
		Command:       "go run ./tools/benchmark --scenario smoke",
		StartedAt:     started,
		FinishedAt:    started.Add(time.Second),
		Environment: EnvironmentInfo{
			OS:           "darwin",
			Architecture: "arm64",
			CPUCount:     8,
			GoVersion:    "go1.25.8",
			CommitSHA:    "abc123",
			DispatchMode: "db",
		},
		Config: defaultConfig().Redacted(),
		Scenarios: []ScenarioResult{{
			Name:       "smoke",
			Status:     ScenarioFailed,
			StartedAt:  started,
			FinishedAt: started.Add(time.Second),
			Executions: []ExecutionRecord{record},
			Findings:   record.Findings,
		}},
		Findings: record.Findings,
		Limitations: []string{
			"terminal status update time is not persisted",
		},
	}
}
