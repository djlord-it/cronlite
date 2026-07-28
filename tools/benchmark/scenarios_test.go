package main

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestScenarioRegistryContainsRequiredScenarios(t *testing.T) {
	registry := scenarioRegistry()
	for _, name := range []string{
		"smoke",
		"baseline",
		"cold-warm",
		"warm-sequential",
		"concurrent",
		"control-plane",
		"recurring",
		"slow-receiver",
		"retry",
		"duplicate-race",
		"crash-recovery",
		"leader-failover",
		"database-outage",
	} {
		if _, ok := registry[name]; !ok {
			t.Errorf("missing scenario %q", name)
		}
	}
}

func TestFastRetryProfileIsExplicitlySkipped(t *testing.T) {
	env := &scenarioEnvironment{
		Config: Config{RetryProfile: "fast-test"},
	}
	result := runRetry(context.Background(), env)
	if result.Status != ScenarioSkipped {
		t.Fatalf("result=%+v", result)
	}
	if result.Reason != "fast-test unavailable: CronLite has no test-only retry injection" {
		t.Fatalf("reason = %q", result.Reason)
	}
}

func TestRunSelectedScenariosPreservesFailureAndContinues(t *testing.T) {
	original := scenarioRegistryOverride
	scenarioRegistryOverride = map[string]scenarioFunc{
		"first": func(_ context.Context, _ *scenarioEnvironment) ScenarioResult {
			return ScenarioResult{Name: "first", Status: ScenarioFailed, Reason: "failed"}
		},
		"second": func(_ context.Context, _ *scenarioEnvironment) ScenarioResult {
			return ScenarioResult{Name: "second", Status: ScenarioPassed}
		},
	}
	t.Cleanup(func() { scenarioRegistryOverride = original })

	env := &scenarioEnvironment{Config: Config{Scenarios: []string{"first", "second"}}}
	got := runSelectedScenarios(context.Background(), env)
	if len(got) != 2 || got[0].Status != ScenarioFailed || got[1].Status != ScenarioPassed {
		t.Fatalf("results=%+v", got)
	}
}

func TestRunSingleManualPreservesTriggerFailure(t *testing.T) {
	env := &scenarioEnvironment{
		Config:   Config{PollInterval: time.Millisecond},
		RunID:    "run-1",
		API:      failingScenarioAPI{err: errors.New("trigger failed")},
		Receiver: newCallbackStore(),
	}
	record, err := runSingleManual(context.Background(), env, "smoke", "job-1", "target", 1, false)
	if err == nil {
		t.Fatal("expected trigger error")
	}
	if record.CorrelationID == "" || record.SampleIndex != 1 {
		t.Fatalf("record=%+v", record)
	}
}

func TestRunSingleManualDoesNotReportMissingCallbackWhenPollingFails(t *testing.T) {
	pollErr := errors.New("poll failed")
	env := &scenarioEnvironment{
		Config: Config{
			PollInterval: time.Millisecond,
		},
		RunID: "run-1",
		API: pollFailingScenarioAPI{
			failingScenarioAPI: failingScenarioAPI{err: pollErr},
		},
		Receiver: newCallbackStore(),
	}
	record, err := runSingleManual(
		context.Background(),
		env,
		"smoke",
		"job-1",
		"target",
		1,
		false,
	)
	if !errors.Is(err, pollErr) {
		t.Fatalf("expected poll error, got %v", err)
	}
	for _, finding := range record.Findings {
		if finding.Code == "missing_callback" {
			t.Fatalf("unexpected missing-callback finding after poll failure: %+v", finding)
		}
	}
}

func TestReceiverClientBaseURLUsesLoopbackForWildcardListener(t *testing.T) {
	for input, want := range map[string]string{
		"127.0.0.1:9090": "http://127.0.0.1:9090",
		"0.0.0.0:19090":  "http://127.0.0.1:19090",
		":9090":          "http://127.0.0.1:9090",
	} {
		got, err := receiverClientBaseURL(input)
		if err != nil {
			t.Fatalf("%s: %v", input, err)
		}
		if got != want {
			t.Fatalf("%s: want %s got %s", input, want, got)
		}
	}
}

func TestComposeControllerListsAllDispatcherServices(t *testing.T) {
	controller := &composeController{}
	want := []string{"cronlite_1", "cronlite_2", "cronlite_3"}
	if got := controller.cronLiteServices(); !reflect.DeepEqual(got, want) {
		t.Fatalf("services: want=%v got=%v", want, got)
	}
}

type failingScenarioAPI struct {
	err error
}

type pollFailingScenarioAPI struct {
	failingScenarioAPI
}

func (f pollFailingScenarioAPI) trigger(
	context.Context,
	string,
) (APIExecution, Observation, error) {
	return APIExecution{ID: "execution-1"}, Observation{}, nil
}

func (f failingScenarioAPI) health(context.Context, bool) (Observation, error) {
	return Observation{}, f.err
}
func (f failingScenarioAPI) createJob(context.Context, CreateJobInput) (APIJob, Observation, error) {
	return APIJob{}, Observation{}, f.err
}
func (f failingScenarioAPI) getJob(context.Context, string) (APIJob, Observation, error) {
	return APIJob{}, Observation{}, f.err
}
func (f failingScenarioAPI) updateJob(context.Context, string, UpdateJobInput) (APIJob, Observation, error) {
	return APIJob{}, Observation{}, f.err
}
func (f failingScenarioAPI) pauseJob(context.Context, string) (Observation, error) {
	return Observation{}, f.err
}
func (f failingScenarioAPI) resumeJob(context.Context, string) (Observation, error) {
	return Observation{}, f.err
}
func (f failingScenarioAPI) deleteJob(context.Context, string) (Observation, error) {
	return Observation{}, f.err
}
func (f failingScenarioAPI) trigger(context.Context, string) (APIExecution, Observation, error) {
	return APIExecution{}, Observation{}, f.err
}
func (f failingScenarioAPI) getExecution(context.Context, string) (APIExecution, Observation, error) {
	return APIExecution{}, Observation{}, f.err
}
func (f failingScenarioAPI) listExecutions(context.Context, string) ([]APIExecution, Observation, error) {
	return nil, Observation{}, f.err
}
func (f failingScenarioAPI) pollTerminal(context.Context, string, time.Duration) (PollBounds, error) {
	return PollBounds{}, f.err
}
