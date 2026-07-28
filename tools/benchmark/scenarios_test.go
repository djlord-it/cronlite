package main

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
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
		"load",
	} {
		if _, ok := registry[name]; !ok {
			t.Errorf("missing scenario %q", name)
		}
	}
}

func TestLoadScenarioKeepsItsOwnReportName(t *testing.T) {
	env := &scenarioEnvironment{
		Config: Config{
			Concurrency: []int{1},
			SampleCount: 1,
		},
		API:      failingScenarioAPI{err: errors.New("expected")},
		Receiver: newCallbackStore(),
	}
	result := scenarioRegistry()["load"](context.Background(), env)
	if result.Name != "load" {
		t.Fatalf("load scenario reported name %q", result.Name)
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

func TestProductionRetryCaseAllowsFullBackoffSchedule(t *testing.T) {
	ctx, cancel := productionRetryCaseContext(context.Background())
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("production retry case has no deadline")
	}
	remaining := time.Until(deadline)
	if remaining < 17*time.Minute || remaining > 19*time.Minute {
		t.Fatalf("production retry case timeout = %s", remaining)
	}
}

func TestProductionRetryCasesRunConcurrently(t *testing.T) {
	api := &concurrentRetryScenarioAPI{}
	env := &scenarioEnvironment{
		Config: Config{
			RetryProfile:      "real-policy",
			Timeout:           time.Second,
			PollInterval:      time.Millisecond,
			WebhookSecret:     "secret",
			ReceiverPublicURL: "http://host.docker.internal:19090",
		},
		RunID:    "run-1",
		API:      api,
		Receiver: newCallbackStore(),
	}

	result := runRetry(context.Background(), env)
	if len(result.Executions) != 7 {
		t.Fatalf("retry executions = %d, want 7", len(result.Executions))
	}
	if api.maximum.Load() < 2 {
		t.Fatalf("retry cases ran sequentially; max concurrent polls = %d", api.maximum.Load())
	}
}

func TestConnectionFailureTargetUsesApprovedReceiverHostAndClosedPort(t *testing.T) {
	target, err := connectionFailureTarget("http://host.docker.internal:19090")
	if err != nil {
		t.Fatal(err)
	}
	if target != "http://host.docker.internal:1/hook" {
		t.Fatalf("connection failure target = %q", target)
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

func TestRunSingleManualAllowsFailedConnectionWithoutCallback(t *testing.T) {
	env := &scenarioEnvironment{
		Config: Config{
			PollInterval: time.Millisecond,
		},
		RunID:    "run-1",
		API:      terminalFailedScenarioAPI{},
		Receiver: newCallbackStore(),
	}
	record, err := runSingleManual(
		context.Background(),
		env,
		"connection-failure",
		"job-1",
		"http://host.docker.internal:1/hook",
		1,
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, finding := range record.Findings {
		if finding.Code == "missing_callback" {
			t.Fatalf("failed connection produced false missing-callback finding: %+v", finding)
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

type concurrentRetryScenarioAPI struct {
	failingScenarioAPI
	current atomic.Int32
	maximum atomic.Int32
}

type terminalFailedScenarioAPI struct {
	failingScenarioAPI
}

type deadlineCapturingScenarioAPI struct {
	failingScenarioAPI
	deadline    time.Time
	hasDeadline bool
}

func (terminalFailedScenarioAPI) trigger(
	context.Context,
	string,
) (APIExecution, Observation, error) {
	return APIExecution{ID: "execution-1"}, Observation{}, nil
}

func (terminalFailedScenarioAPI) pollTerminal(
	context.Context,
	string,
	time.Duration,
) (PollBounds, error) {
	now := time.Now().UTC()
	return PollBounds{
		PollCount:       1,
		FirstTerminalAt: &now,
		FinalExecution:  APIExecution{Status: "failed"},
	}, nil
}

func (f *concurrentRetryScenarioAPI) createJob(
	context.Context,
	CreateJobInput,
) (APIJob, Observation, error) {
	return APIJob{ID: uuid.NewString()}, Observation{}, nil
}

func (f *concurrentRetryScenarioAPI) trigger(
	context.Context,
	string,
) (APIExecution, Observation, error) {
	return APIExecution{ID: uuid.NewString()}, Observation{}, nil
}

func (f *concurrentRetryScenarioAPI) pollTerminal(
	context.Context,
	string,
	time.Duration,
) (PollBounds, error) {
	current := f.current.Add(1)
	for {
		maximum := f.maximum.Load()
		if current <= maximum || f.maximum.CompareAndSwap(maximum, current) {
			break
		}
	}
	time.Sleep(25 * time.Millisecond)
	f.current.Add(-1)
	now := time.Now().UTC()
	return PollBounds{
		PollCount:       1,
		FirstTerminalAt: &now,
		FinalExecution:  APIExecution{Status: "delivered"},
	}, nil
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

func (f *deadlineCapturingScenarioAPI) health(ctx context.Context, _ bool) (Observation, error) {
	f.deadline, f.hasDeadline = ctx.Deadline()
	return Observation{}, context.DeadlineExceeded
}

func TestDatabaseOutageHealthProbeUsesShortDeadline(t *testing.T) {
	api := &deadlineCapturingScenarioAPI{}
	started := time.Now()

	_, _ = probeDegradedHealth(context.Background(), api)

	if !api.hasDeadline {
		t.Fatal("database outage health probe has no deadline")
	}
	timeout := api.deadline.Sub(started)
	if timeout < time.Second || timeout > 3*time.Second {
		t.Fatalf("database outage health probe timeout = %s, want about 2s", timeout)
	}
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
