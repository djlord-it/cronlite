package main

import (
	"context"
	"testing"
)

func TestCrashScenarioSkipsWithoutProcessControl(t *testing.T) {
	env := &scenarioEnvironment{
		Config:       defaultConfig(),
		Capabilities: Capabilities{DockerCompose: false},
	}
	got := runCrashRecovery(context.Background(), env)
	if got.Status != ScenarioSkipped {
		t.Fatalf("result=%+v", got)
	}
}

func TestDuplicateRaceScenarioSkipsWithoutMultipleInstances(t *testing.T) {
	env := &scenarioEnvironment{
		Config: defaultConfig(),
		Capabilities: Capabilities{
			DockerCompose:     true,
			MultipleInstances: false,
		},
	}
	got := runDuplicateRace(context.Background(), env)
	if got.Status != ScenarioSkipped {
		t.Fatalf("result=%+v", got)
	}
}

func TestDuplicateRaceVerdictUsesReceiverCallbacks(t *testing.T) {
	record := fixtureExecutionRecord()
	record.Callbacks = append(record.Callbacks, CallbackObservation{
		ExecutionID:            "exec-1",
		AttemptID:              "attempt-2",
		SignatureValid:         true,
		BodySHA256:             "same",
		ConcurrentForExecution: 2,
	})
	result := duplicateRaceVerdict(correlate(record))
	if result.Status != ScenarioFailed {
		t.Fatalf("result=%+v", result)
	}
	if !hasFinding(result.Findings, SeverityCritical, "concurrent_duplicate_delivery") {
		t.Fatalf("findings=%+v", result.Findings)
	}
}
