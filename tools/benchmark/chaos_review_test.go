package main

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// blockingChaosDB ignores context cancellation, like lib/pq reading from a
// frozen PostgreSQL, until release is closed.
type blockingChaosDB struct {
	release chan struct{}
	once    sync.Once
}

func newBlockingChaosDB() *blockingChaosDB { return &blockingChaosDB{release: make(chan struct{})} }

func (b *blockingChaosDB) unblock() { b.once.Do(func() { close(b.release) }) }

func (b *blockingChaosDB) chaosExecutions(context.Context, []string) ([]ChaosExecutionRow, error) {
	<-b.release
	return nil, nil
}
func (b *blockingChaosDB) chaosQueueDepth(context.Context, []string) (int, error) {
	<-b.release
	return 0, nil
}
func (b *blockingChaosDB) chaosStuck(context.Context, []string, time.Duration, time.Duration) ([]string, error) {
	<-b.release
	return nil, nil
}
func (b *blockingChaosDB) execution(context.Context, string) (DiagnosticExecution, error) {
	return DiagnosticExecution{}, nil
}
func (b *blockingChaosDB) ping(context.Context) (Observation, error) { return Observation{}, nil }

func TestSampleOnceBoundsHungDB(t *testing.T) {
	db := newBlockingChaosDB()
	defer db.unblock()
	sampler := newTestSampler(newFakeProbe(), db)
	sampler.dbTimeout = 100 * time.Millisecond
	started := time.Now()
	sample := sampler.sampleOnce(context.Background(), time.Now())
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("sampleOnce blocked for %s on a hung database", elapsed)
	}
	if sample.QueueDepth != -1 || sample.StuckCount != -1 {
		t.Fatalf("queue=%d stuck=%d", sample.QueueDepth, sample.StuckCount)
	}
}

func TestRunChaosCancelledDuringPgPauseUnpauses(t *testing.T) {
	env, controller := chaosTestEnv(t, "pg-pause::med:5s")
	env.Config.ChaosBaseline = 10 * time.Millisecond
	// The database hangs from the start and only answers once postgres is
	// unpaused, so the sampler is stuck in a query when the run is cancelled.
	db := newBlockingChaosDB()
	defer db.unblock()
	env.Diagnostic = db
	controller.honorCancel = true
	controller.onCall = func(call string) {
		if call == "unpause postgres" {
			db.unblock()
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(80 * time.Millisecond)
		cancel()
	}()
	done := make(chan struct{})
	go func() {
		runChaos(ctx, env)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		db.unblock()
		t.Fatalf("runChaos hung after cancel; calls=%v", controller.snapshotCalls())
	}
	if !slices.Contains(controller.snapshotCalls(), "unpause postgres") {
		t.Fatalf("postgres left paused: %v", controller.snapshotCalls())
	}
}

func TestLatenessInFlightAtFaultStartIsAttributed(t *testing.T) {
	in := latenessInputs()
	in.Windows = []FaultWindow{stopWindow(100, 130, ptrTime(at(140)))}
	in.Manual = append(in.Manual, ManualSample{ExecutionID: "inflight", TriggeredAt: at(99.6)})
	in.Callbacks = append(in.Callbacks, callback("inflight", 110, 204))
	stats, breaches := checkLateness(in)
	if len(breaches) != 0 {
		t.Fatalf("in-flight delivery delayed by the fault was flagged: %+v", breaches)
	}
	for _, stat := range stats {
		if stat.Window == faultKey(0, in.Windows[0]) && stat.Kind == latenessManual && stat.Count != 1 {
			t.Fatalf("fault window stat = %+v", stat)
		}
	}
}

func TestChaosCallbacksFilteredToRun(t *testing.T) {
	callbacks := []CallbackObservation{callback("mine-recurring", 1, 204), callback("other-scenario", 2, 204), callback("mine-manual", 3, 204)}
	filtered := chaosCallbacks(callbacks,
		[]ChaosExecutionRow{{ID: "mine-recurring"}},
		[]ManualSample{{ExecutionID: "mine-manual"}})
	var ids []string
	for _, callback := range filtered {
		ids = append(ids, callback.ExecutionID)
	}
	if !slices.Equal(ids, []string{"mine-recurring", "mine-manual"}) {
		t.Fatalf("filtered = %v", ids)
	}
}

func TestDriverPartialApplyRestores(t *testing.T) {
	controller := &fakeFaultController{leader: "cronlite_1", failOn: "pause cronlite_2"}
	driver := newFaultDriver(controller, 1)
	window, err := driver.apply(context.Background(), FaultSpec{Type: FaultProcPause, Target: "all", Intensity: "med", Duration: time.Second})
	if err == nil {
		t.Fatal("expected apply error")
	}
	if window.ClearedAt.IsZero() {
		t.Fatal("partially applied fault has no ClearedAt")
	}
	if !slices.Contains(controller.snapshotCalls(), "unpause cronlite_1") {
		t.Fatalf("injected service not restored: %v", controller.snapshotCalls())
	}
}

func TestScoreNoDataWithoutSamples(t *testing.T) {
	in := baseInputs()
	in.Windows = []FaultWindow{stopWindow(100, 130, ptrTime(at(140)))}
	in.Timeline = []TimelineSample{sample(10, 1)}
	chaos := scoreChaos(in, 30*time.Second)
	if got := chaos.Scores[0].Invariants[InvariantLeader]; got != invariantNoData {
		t.Fatalf("I6 with no samples in the window = %q", got)
	}
	in.Timeline = nil
	chaos = scoreChaos(in, 30*time.Second)
	if got := chaos.Scores[len(chaos.Scores)-1].Invariants[InvariantLeader]; got != invariantNoData {
		t.Fatalf("run I6 with an empty timeline = %q", got)
	}
}

func TestNoLostSkippedWhenExecutionsUnavailable(t *testing.T) {
	in := baseInputs()
	in.Jobs = []RecurringJob{{ID: "j1", CronExpression: "* * * * *", CreatedAt: at(-30)}}
	in.ExecutionsUnavailable = true
	if breaches := checkNoLost(in, 30*time.Second); len(breaches) != 0 {
		t.Fatalf("failed query reported as lost executions: %d breaches", len(breaches))
	}
	chaos := scoreChaos(in, 30*time.Second)
	if got := chaos.Scores[len(chaos.Scores)-1].Invariants[InvariantNoLost]; got != invariantNoData {
		t.Fatalf("run I1 = %q", got)
	}
}

func TestRecoveryNeedsConsecutiveHealthySamples(t *testing.T) {
	in := baseInputs()
	in.Windows = []FaultWindow{stopWindow(40, 70, nil)}
	in.Timeline = []TimelineSample{sample(71, 1), sample(72, 0), sample(73, 1), sample(74, 1), sample(75, 1)}
	times, _ := recoveryTimes(in, Bound{}, time.Minute)
	if got := times[faultKey(0, in.Windows[0])]; got != 5*time.Second {
		t.Fatalf("recovery = %s, want 5s (third consecutive healthy sample)", got)
	}
}

func TestSteadyOutlierIsWarning(t *testing.T) {
	in := latenessInputs()
	for i := 0; i < 200; i++ {
		id := "s" + strings.Repeat("x", i%7) + string(rune('a'+i%26)) + string(rune('a'+i/26))
		in.Manual = append(in.Manual, ManualSample{ExecutionID: id, TriggeredAt: at(200 + float64(i))})
		in.Callbacks = append(in.Callbacks, callback(id, 200+float64(i)+0.1, 204))
	}
	in.Manual = append(in.Manual, ManualSample{ExecutionID: "outlier", TriggeredAt: at(500)})
	in.Callbacks = append(in.Callbacks, callback("outlier", 505, 204))
	_, breaches := checkLateness(in)
	breach := hasCode(breaches, "chaos_lateness_exceeded")
	if breach == nil || breach.Severity != SeverityWarning {
		t.Fatalf("single steady outlier = %+v", breach)
	}
}
