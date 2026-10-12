package dispatcher

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sync/atomic"
	"testing"
	"time"

	"github.com/djlord-it/cronlite/internal/domain"
	"github.com/google/uuid"
)

// Count outcomes without retaining executions or attempt history in the test.
type sustainedStore struct {
	job       domain.Job
	remaining atomic.Int64
	terminal  atomic.Int64
	attempts  atomic.Int64
}

func (s *sustainedStore) GetJobByID(context.Context, uuid.UUID) (domain.Job, error) {
	return s.job, nil
}
func (s *sustainedStore) GetExecutionScoped(_ context.Context, id uuid.UUID, ns domain.Namespace) (domain.Execution, error) {
	return domain.Execution{ID: id, Namespace: ns, Status: domain.ExecutionStatusInProgress}, nil
}
func (s *sustainedStore) InsertDeliveryAttempt(context.Context, domain.DeliveryAttempt) error {
	s.attempts.Add(1)
	return nil
}
func (s *sustainedStore) UpdateExecutionStatus(context.Context, uuid.UUID, domain.ExecutionStatus) error {
	s.terminal.Add(1)
	return nil
}

func (s *sustainedStore) ExecutionLeaseDuration() time.Duration { return domain.DefaultExecutionLease }
func (s *sustainedStore) ClaimExecution(context.Context, uuid.UUID, domain.Namespace) (*domain.Execution, error) {
	return nil, nil
}
func (s *sustainedStore) RenewExecutionClaim(ctx context.Context, _ domain.Execution) error {
	return ctx.Err()
}
func (s *sustainedStore) CompleteExecution(ctx context.Context, _ domain.Execution, _ domain.ExecutionStatus) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.terminal.Add(1)
	return nil
}
func (s *sustainedStore) DequeueExecution(ctx context.Context) (*domain.Execution, error) {
	executions, err := s.DequeueExecutions(ctx, 1)
	if len(executions) == 0 {
		return nil, err
	}
	return &executions[0], err
}
func (s *sustainedStore) DequeueExecutions(ctx context.Context, limit int) ([]domain.Execution, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		remaining := s.remaining.Load()
		n := min(remaining, int64(limit))
		if n == 0 {
			return nil, nil
		}
		if !s.remaining.CompareAndSwap(remaining, remaining-n) {
			continue
		}
		executions := make([]domain.Execution, n)
		for i := range executions {
			executions[i] = domain.Execution{ID: uuid.New(), JobID: s.job.ID, Namespace: s.job.Namespace, ScheduledAt: time.Now(), Status: domain.ExecutionStatusInProgress, ClaimGeneration: 1}
		}
		return executions, nil
	}
}

type sustainedSender struct {
	active atomic.Int64
	peak   atomic.Int64
	calls  atomic.Int64
	block  atomic.Bool
}

func (s *sustainedSender) Send(ctx context.Context, _ WebhookRequest) WebhookResult {
	active := s.active.Add(1)
	defer s.active.Add(-1)
	for peak := s.peak.Load(); active > peak; peak = s.peak.Load() {
		if s.peak.CompareAndSwap(peak, active) {
			break
		}
	}
	if s.block.Load() {
		<-ctx.Done()
		return WebhookResult{Error: ctx.Err()}
	}
	// Include repeated failures and retries, without retaining per-ID state.
	if s.calls.Add(1)%3 != 0 {
		return WebhookResult{StatusCode: 500}
	}
	return WebhookResult{StatusCode: 200}
}

func TestDispatcher_SustainedLoadAndCancellation(t *testing.T) {
	profileDir := os.Getenv("CRONLITE_RELIABILITY_PROFILE_DIR")
	batch := int64(200)
	if profileDir != "" {
		batch = 10_000
	}
	profile := func(name string) {
		t.Helper()
		if profileDir == "" {
			return
		}
		f, err := os.Create(filepath.Join(profileDir, name+".heap"))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := pprof.WriteHeapProfile(f); err != nil {
			t.Fatal(err)
		}
		for _, kind := range []string{"goroutine", "allocs"} {
			profileFile, err := os.Create(filepath.Join(profileDir, name+"."+kind))
			if err != nil {
				t.Fatal(err)
			}
			debug := 0
			if kind == "goroutine" {
				debug = 2
			}
			err = pprof.Lookup(kind).WriteTo(profileFile, debug)
			_ = profileFile.Close()
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	runtime.GC()
	var baseline runtime.MemStats
	runtime.ReadMemStats(&baseline)
	baseGoroutines := runtime.NumGoroutine()
	profile("baseline")
	store := &sustainedStore{job: domain.Job{ID: uuid.New(), Namespace: "load", Delivery: domain.DeliveryConfig{WebhookURL: "https://example.com"}}}
	sender := &sustainedSender{}
	d := New(store, sender)
	d.backoff = []time.Duration{0, 0, 0, 0}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.RunDBPoll(ctx, time.Millisecond, 3); close(done) }()
	defer func() { cancel(); <-done }()
	wait := func(condition func() bool) {
		t.Helper()
		deadline := time.NewTimer(10 * time.Second)
		defer deadline.Stop()
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for !condition() {
			select {
			case <-deadline.C:
				t.Fatal("load failed to make progress")
			case <-ticker.C:
			}
		}
	}
	for wave := int64(1); wave <= 5; wave++ {
		started := time.Now()
		store.remaining.Add(batch)
		wait(func() bool { return store.terminal.Load() == wave*batch })
		runtime.GC()
		var sample runtime.MemStats
		runtime.ReadMemStats(&sample)
		t.Logf("wave=%d terminal=%d attempts=%d duration=%s heap_delta=%d objects_delta=%d allocated_bytes=%d mallocs=%d goroutines=%d", wave, store.terminal.Load(), store.attempts.Load(), time.Since(started), int64(sample.HeapAlloc)-int64(baseline.HeapAlloc), int64(sample.HeapObjects)-int64(baseline.HeapObjects), sample.TotalAlloc-baseline.TotalAlloc, sample.Mallocs-baseline.Mallocs, runtime.NumGoroutine())
	}
	profile("loaded")
	sender.block.Store(true)
	store.remaining.Add(12)
	wait(func() bool { return sender.active.Load() == 12 })
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("polling or deliveries did not stop on cancellation")
	}
	if sender.active.Load() != 0 || sender.peak.Load() > 12 || store.remaining.Load() != 0 {
		t.Fatalf("unbounded or abandoned work: active=%d peak=%d unclaimed=%d", sender.active.Load(), sender.peak.Load(), store.remaining.Load())
	}
	runtime.GC()
	profile("stopped")
	t.Logf("stopped goroutines=%d baseline=%d peak_deliveries=%d", runtime.NumGoroutine(), baseGoroutines, sender.peak.Load())
}
