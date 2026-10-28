package main

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

type chaosRecordingAPI struct {
	failingScenarioAPI
	mu       sync.Mutex
	creates  []CreateJobInput
	triggers int
}

func (a *chaosRecordingAPI) createJob(_ context.Context, input CreateJobInput) (APIJob, Observation, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.creates = append(a.creates, input)
	return APIJob{ID: uuid.NewString(), CronExpression: input.CronExpression, CreatedAt: time.Now().UTC()}, Observation{}, nil
}

func (a *chaosRecordingAPI) trigger(context.Context, string) (APIExecution, Observation, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.triggers++
	return APIExecution{ID: uuid.NewString()}, Observation{}, nil
}

func (a *chaosRecordingAPI) deleteJob(context.Context, string) (Observation, error) {
	return Observation{}, nil
}

type fakeChaosDiagnostic struct {
	fakeChaosDB
}

func (fakeChaosDiagnostic) execution(context.Context, string) (DiagnosticExecution, error) {
	return DiagnosticExecution{}, nil
}

func (fakeChaosDiagnostic) ping(context.Context) (Observation, error) { return Observation{}, nil }

func chaosTestEnv(t *testing.T, faults string) (*scenarioEnvironment, *fakeFaultController) {
	t.Helper()
	specs, err := parseFaultSpecs(faults)
	if err != nil {
		t.Fatal(err)
	}
	cfg := defaultConfig()
	cfg.ReceiverPublicURL = "http://benchmark-receiver:19090"
	cfg.RequeueThreshold = 5 * time.Second
	cfg.ChaosFaults = specs
	cfg.ChaosJobs = 0
	cfg.ChaosTriggerRate = 0
	cfg.ChaosSampleInterval = 20 * time.Millisecond
	cfg.ChaosBaseline = 50 * time.Millisecond
	cfg.ChaosSteady = 50 * time.Millisecond
	cfg.ChaosRecoveryTimeout = 500 * time.Millisecond
	controller := &fakeFaultController{leader: "cronlite_1"}
	grace := 10 * time.Millisecond
	return &scenarioEnvironment{
		Config:             cfg,
		RunID:              "run-1",
		API:                &chaosRecordingAPI{},
		Receiver:           newCallbackStore(),
		Diagnostic:         &fakeChaosDiagnostic{},
		ChaosController:    controller,
		ChaosProbe:         newFakeProbe(),
		chaosGraceOverride: &grace,
	}, controller
}

func TestChaosWorkloadCreatesJobs(t *testing.T) {
	env, _ := chaosTestEnv(t, "proc-stop")
	env.Config.ChaosJobs = 3
	workload, err := startChaosWorkload(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}
	workload.stop()
	api := env.API.(*chaosRecordingAPI)
	var recurring, manual int
	for _, create := range api.creates {
		switch create.CronExpression {
		case "* * * * *":
			recurring++
		case "0 0 * * *":
			manual++
		}
	}
	if recurring != 3 || manual != 1 {
		t.Fatalf("recurring=%d manual=%d creates=%+v", recurring, manual, api.creates)
	}
	if got := len(workload.jobIDs()); got != 4 {
		t.Fatalf("jobIDs = %d", got)
	}
	if got := len(workload.recurringJobs()); got != 3 {
		t.Fatalf("recurring jobs = %d", got)
	}
}

func TestChaosWorkloadTriggerRate(t *testing.T) {
	env, _ := chaosTestEnv(t, "proc-stop")
	env.Config.ChaosTriggerRate = 20
	workload, err := startChaosWorkload(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	samples := workload.stop()
	if len(samples) < 4 || len(samples) > 8 {
		t.Fatalf("manual samples = %d, want 4..8", len(samples))
	}
	for _, sample := range samples {
		if sample.ExecutionID == "" || sample.TriggeredAt.IsZero() {
			t.Fatalf("sample = %+v", sample)
		}
	}
}

func TestRunChaosEndToEndWithFakes(t *testing.T) {
	env, controller := chaosTestEnv(t, "proc-stop:leader:med:50ms")
	result := runChaos(context.Background(), env)
	if result.Status != ScenarioPassed {
		t.Fatalf("status = %s reason=%q findings=%+v", result.Status, result.Reason, result.Findings)
	}
	if result.Chaos == nil || len(result.Chaos.Windows) != 1 || result.Chaos.Windows[0].RecoveredAt == nil {
		t.Fatalf("chaos = %+v", result.Chaos)
	}
	if len(result.Chaos.Timeline) == 0 {
		t.Fatal("no timeline samples")
	}
	calls := controller.snapshotCalls()
	if len(calls) < 2 || calls[0] != "stop cronlite_1" || calls[1] != "start cronlite_1" {
		t.Fatalf("calls = %v", calls)
	}
	if !slices.Contains(calls[2:], "unpause cronlite_1") {
		t.Fatalf("cleanup missing: %v", calls)
	}
}

func TestRunChaosCancelledStillCleansUp(t *testing.T) {
	env, controller := chaosTestEnv(t, "proc-pause:leader:med:5s")
	env.Config.ChaosBaseline = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(60 * time.Millisecond)
		cancel()
	}()
	started := time.Now()
	runChaos(ctx, env)
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("cancelled run took %s", elapsed)
	}
	calls := controller.snapshotCalls()
	if !slices.Contains(calls, "pause cronlite_1") || !slices.Contains(calls, "unpause cronlite_1") || !slices.Contains(calls, "start cronlite_1") {
		t.Fatalf("calls = %v", calls)
	}
}

func TestRunChaosSkipsWithoutChaosDiagnostics(t *testing.T) {
	env, _ := chaosTestEnv(t, "proc-stop")
	env.Diagnostic = nil
	if result := runChaos(context.Background(), env); result.Status != ScenarioSkipped {
		t.Fatalf("status = %s", result.Status)
	}
}
