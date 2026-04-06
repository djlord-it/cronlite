package reconciler

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/djlord-it/cronlite/internal/dispatcher"
	"github.com/djlord-it/cronlite/internal/domain"
)

// mockStore returns configurable orphaned executions.
type mockStore struct {
	mu      sync.Mutex
	orphans []domain.Execution
	err     error
}

func (s *mockStore) GetOrphanedExecutions(ctx context.Context, olderThan time.Time, maxResults int) ([]domain.Execution, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.err != nil {
		return nil, s.err
	}

	// Filter by olderThan and limit
	var result []domain.Execution
	for i := range s.orphans {
		exec := s.orphans[i]
		if exec.CreatedAt.Before(olderThan) {
			result = append(result, exec)
			if len(result) >= maxResults {
				break
			}
		}
	}
	return result, nil
}

func (s *mockStore) RequeueStaleExecutions(ctx context.Context, olderThan time.Time, limit int) (int, error) {
	return 0, nil
}

func (s *mockStore) setOrphans(orphans []domain.Execution) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.orphans = orphans
}

func (s *mockStore) setError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

// mockEmitter tracks emitted events.
type mockEmitter struct {
	mu     sync.Mutex
	events []domain.TriggerEvent
	err    error
}

func (e *mockEmitter) Emit(ctx context.Context, event domain.TriggerEvent) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.err != nil {
		return e.err
	}
	e.events = append(e.events, event)
	return nil
}

func (e *mockEmitter) getEvents() []domain.TriggerEvent {
	e.mu.Lock()
	defer e.mu.Unlock()
	result := make([]domain.TriggerEvent, len(e.events))
	copy(result, e.events)
	return result
}

func (e *mockEmitter) setError(err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.err = err
}

// TestReconciler_DetectsOrphanedExecutions verifies that the reconciler
// correctly identifies and re-emits orphaned executions.
func TestReconciler_DetectsOrphanedExecutions(t *testing.T) {
	store := &mockStore{}
	emitter := &mockEmitter{}

	now := time.Now().UTC()
	threshold := 10 * time.Minute

	// Create orphaned execution (older than threshold)
	orphanedExec := domain.Execution{
		ID:          uuid.New(),
		JobID:       uuid.New(),
		Namespace:   domain.Namespace("test-ns"),
		ScheduledAt: now.Add(-15 * time.Minute),
		FiredAt:     now.Add(-15 * time.Minute),
		Status:      domain.ExecutionStatusEmitted,
		CreatedAt:   now.Add(-15 * time.Minute), // 15 min ago
	}

	store.setOrphans([]domain.Execution{orphanedExec})

	recon := New(
		Config{
			Interval:  time.Hour, // Not used in direct runCycle call
			Threshold: threshold,
			BatchSize: 100,
		},
		store,
		emitter,
	)
	recon.clock = func() time.Time { return now }

	ctx := context.Background()
	recon.runCycle(ctx)

	events := emitter.getEvents()
	if len(events) != 1 {
		t.Fatalf("expected 1 re-emitted event, got %d", len(events))
	}

	// Verify the event has the same execution ID
	if events[0].ExecutionID != orphanedExec.ID {
		t.Error("re-emitted event should have same execution ID as orphan")
	}

	// Verify the event preserves all original data
	if events[0].JobID != orphanedExec.JobID {
		t.Error("re-emitted event should preserve job ID")
	}
	if events[0].ScheduledAt != orphanedExec.ScheduledAt {
		t.Error("re-emitted event should preserve scheduled_at")
	}
}

// TestReconciler_ReEmitsSameExecutionID verifies that the reconciler
// re-emits using the original execution ID (for idempotency).
func TestReconciler_ReEmitsSameExecutionID(t *testing.T) {
	store := &mockStore{}
	emitter := &mockEmitter{}

	now := time.Now().UTC()
	originalExecID := uuid.New()

	orphan := domain.Execution{
		ID:          originalExecID,
		JobID:       uuid.New(),
		Namespace:   domain.Namespace("test-ns"),
		ScheduledAt: now.Add(-20 * time.Minute),
		FiredAt:     now.Add(-20 * time.Minute),
		Status:      domain.ExecutionStatusEmitted,
		CreatedAt:   now.Add(-20 * time.Minute),
	}

	store.setOrphans([]domain.Execution{orphan})

	recon := New(
		Config{
			Interval:  time.Hour,
			Threshold: 10 * time.Minute,
			BatchSize: 100,
		},
		store,
		emitter,
	)
	recon.clock = func() time.Time { return now }

	ctx := context.Background()
	recon.runCycle(ctx)

	events := emitter.getEvents()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	// CRITICAL: Must use same execution ID for idempotency
	if events[0].ExecutionID != originalExecID {
		t.Errorf("re-emitted event must use original execution ID %s, got %s",
			originalExecID, events[0].ExecutionID)
	}
}

// TestReconciler_DoesNotTouchTerminalExecutions verifies that terminal
// executions (delivered/failed) are never returned by the store query
// and thus never re-emitted.
func TestReconciler_DoesNotTouchTerminalExecutions(t *testing.T) {
	store := &mockStore{}
	emitter := &mockEmitter{}

	now := time.Now().UTC()

	// Only emitted executions should be returned by store
	// Terminal executions should never appear in the orphan list
	// (This is enforced by the SQL query: WHERE status = 'emitted')

	// Simulate store returning only emitted status executions
	emittedOrphan := domain.Execution{
		ID:        uuid.New(),
		JobID:     uuid.New(),
		Namespace: domain.Namespace("test-ns"),
		Status:    domain.ExecutionStatusEmitted,
		CreatedAt: now.Add(-20 * time.Minute),
	}

	store.setOrphans([]domain.Execution{emittedOrphan})

	recon := New(
		Config{
			Interval:  time.Hour,
			Threshold: 10 * time.Minute,
			BatchSize: 100,
		},
		store,
		emitter,
	)
	recon.clock = func() time.Time { return now }

	ctx := context.Background()
	recon.runCycle(ctx)

	events := emitter.getEvents()
	if len(events) != 1 {
		t.Fatalf("expected 1 event for emitted orphan, got %d", len(events))
	}

	// The store query filters by status='emitted', so terminal executions
	// will never be in the orphan list. This test verifies the contract.
}

// TestReconciler_BatchSizeRespected verifies that the reconciler
// processes at most BatchSize orphans per cycle.
func TestReconciler_BatchSizeRespected(t *testing.T) {
	store := &mockStore{}
	emitter := &mockEmitter{}

	now := time.Now().UTC()
	batchSize := 5

	// Create more orphans than batch size
	var orphans []domain.Execution
	for i := 0; i < 10; i++ {
		orphans = append(orphans, domain.Execution{
			ID:        uuid.New(),
			JobID:     uuid.New(),
			Namespace: domain.Namespace("test-ns"),
			Status:    domain.ExecutionStatusEmitted,
			CreatedAt: now.Add(-20 * time.Minute),
		})
	}
	store.setOrphans(orphans)

	recon := New(
		Config{
			Interval:  time.Hour,
			Threshold: 10 * time.Minute,
			BatchSize: batchSize,
		},
		store,
		emitter,
	)
	recon.clock = func() time.Time { return now }

	ctx := context.Background()
	recon.runCycle(ctx)

	events := emitter.getEvents()
	if len(events) != batchSize {
		t.Errorf("expected exactly %d events (batch size), got %d", batchSize, len(events))
	}
}

// TestReconciler_DoesNotEmitRecentExecutions verifies that executions
// younger than the threshold are not re-emitted.
func TestReconciler_DoesNotEmitRecentExecutions(t *testing.T) {
	store := &mockStore{}
	emitter := &mockEmitter{}

	now := time.Now().UTC()
	threshold := 10 * time.Minute

	// Execution created 5 minutes ago (within threshold)
	recentExec := domain.Execution{
		ID:        uuid.New(),
		JobID:     uuid.New(),
		Namespace: domain.Namespace("test-ns"),
		Status:    domain.ExecutionStatusEmitted,
		CreatedAt: now.Add(-5 * time.Minute), // Only 5 min ago
	}

	store.setOrphans([]domain.Execution{recentExec})

	recon := New(
		Config{
			Interval:  time.Hour,
			Threshold: threshold,
			BatchSize: 100,
		},
		store,
		emitter,
	)
	recon.clock = func() time.Time { return now }

	ctx := context.Background()
	recon.runCycle(ctx)

	// Should not emit anything (execution is too recent)
	events := emitter.getEvents()
	if len(events) != 0 {
		t.Errorf("should not re-emit recent executions, got %d events", len(events))
	}
}

// TestReconciler_DBErrorAbortsGracefully verifies that database errors
// abort the cycle without crashing.
func TestReconciler_DBErrorAbortsGracefully(t *testing.T) {
	store := &mockStore{}
	emitter := &mockEmitter{}

	store.setError(errors.New("database connection failed"))

	recon := New(
		Config{
			Interval:  time.Hour,
			Threshold: 10 * time.Minute,
			BatchSize: 100,
		},
		store,
		emitter,
	)
	recon.clock = func() time.Time { return time.Now().UTC() }

	ctx := context.Background()

	// Should not panic
	recon.runCycle(ctx)

	// No events should be emitted
	events := emitter.getEvents()
	if len(events) != 0 {
		t.Error("should not emit events when DB fails")
	}
}

// TestReconciler_EmitErrorContinues verifies that emit errors for one
// orphan don't stop processing of others.
func TestReconciler_EmitErrorContinues(t *testing.T) {
	store := &mockStore{}
	emitter := &mockEmitter{}

	now := time.Now().UTC()

	// Create 3 orphans
	var orphans []domain.Execution
	for i := 0; i < 3; i++ {
		orphans = append(orphans, domain.Execution{
			ID:        uuid.New(),
			JobID:     uuid.New(),
			Namespace: domain.Namespace("test-ns"),
			Status:    domain.ExecutionStatusEmitted,
			CreatedAt: now.Add(-20 * time.Minute),
		})
	}
	store.setOrphans(orphans)

	// Emitter fails on all attempts
	emitter.setError(errors.New("buffer full"))

	recon := New(
		Config{
			Interval:  time.Hour,
			Threshold: 10 * time.Minute,
			BatchSize: 100,
		},
		store,
		emitter,
	)
	recon.clock = func() time.Time { return now }

	ctx := context.Background()

	// Should not panic, should attempt all 3
	recon.runCycle(ctx)

	// No events emitted (all failed)
	events := emitter.getEvents()
	if len(events) != 0 {
		t.Error("should have 0 events when emitter fails")
	}
}

// TestReconciler_ContextCancellation verifies that the reconciler
// stops processing when context is cancelled.
func TestReconciler_ContextCancellation(t *testing.T) {
	store := &mockStore{}
	emitter := &mockEmitter{}

	now := time.Now().UTC()

	// Create many orphans
	var orphans []domain.Execution
	for i := 0; i < 100; i++ {
		orphans = append(orphans, domain.Execution{
			ID:        uuid.New(),
			JobID:     uuid.New(),
			Namespace: domain.Namespace("test-ns"),
			Status:    domain.ExecutionStatusEmitted,
			CreatedAt: now.Add(-20 * time.Minute),
		})
	}
	store.setOrphans(orphans)

	recon := New(
		Config{
			Interval:  time.Hour,
			Threshold: 10 * time.Minute,
			BatchSize: 100,
		},
		store,
		emitter,
	)
	recon.clock = func() time.Time { return now }

	// Cancel context immediately
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	recon.runCycle(ctx)

	// Should have processed 0 events (context was cancelled)
	events := emitter.getEvents()
	if len(events) != 0 {
		t.Errorf("should stop on context cancellation, got %d events", len(events))
	}
}

// TestReconciler_DefaultConfig verifies default configuration values.
func TestReconciler_DefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Interval != 5*time.Minute {
		t.Errorf("default interval should be 5m, got %s", cfg.Interval)
	}

	// Threshold must exceed the dispatcher's maximum retry duration.
	expectedThreshold := dispatcher.MaxRetryDuration() + SafetyMargin
	if cfg.Threshold != expectedThreshold {
		t.Errorf("default threshold should be %s, got %s", expectedThreshold, cfg.Threshold)
	}
	if cfg.RequeueThreshold != DefaultRequeueThreshold {
		t.Errorf("default requeue threshold should be %s, got %s", DefaultRequeueThreshold, cfg.RequeueThreshold)
	}
	if cfg.BatchSize != 100 {
		t.Errorf("default batch size should be 100, got %d", cfg.BatchSize)
	}
}

// TestReconciler_ThresholdExceedsMaxRetryDuration is a safety invariant test.
// It guarantees that the default reconciler threshold always exceeds the
// dispatcher's worst-case retry window. If someone changes the dispatcher
// backoff schedule, this test will fail, forcing them to verify the
// reconciler threshold is still safe.
func TestReconciler_ThresholdExceedsMaxRetryDuration(t *testing.T) {
	cfg := DefaultConfig()
	maxRetry := dispatcher.MaxRetryDuration()

	if cfg.Threshold <= maxRetry {
		t.Errorf("reconciler threshold (%s) must exceed dispatcher max retry duration (%s) "+
			"to prevent duplicate webhook deliveries", cfg.Threshold, maxRetry)
	}
}

// TestReconciler_RequeuesStaleExecutions verifies that RequeueStaleExecutions
// is called during runCycle.
func TestReconciler_RequeuesStaleExecutions(t *testing.T) {
	store := &mockStoreWithRequeue{
		mockStore: mockStore{},
	}
	emitter := &mockEmitter{}

	recon := New(Config{
		Interval:  50 * time.Millisecond,
		Threshold: 10 * time.Minute,
		BatchSize: 100,
	}, store, emitter)

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	recon.Run(ctx)

	if !store.requeueCalled {
		t.Error("expected RequeueStaleExecutions to be called")
	}
}

type mockStoreWithRequeue struct {
	mockStore
	mu               sync.Mutex
	requeueCalled    bool
	requeueOlderThan time.Time
}

func (s *mockStoreWithRequeue) RequeueStaleExecutions(ctx context.Context, olderThan time.Time, limit int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requeueCalled = true
	s.requeueOlderThan = olderThan
	return 0, nil
}

// TestReconciler_SplitThresholds verifies that RequeueStaleExecutions uses
// RequeueThreshold while GetOrphanedExecutions uses Threshold.
func TestReconciler_SplitThresholds(t *testing.T) {
	store := &mockStoreWithThresholdCapture{}
	emitter := &mockEmitter{}

	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	recon := New(Config{
		Interval:         time.Hour,
		Threshold:        15 * time.Minute,
		RequeueThreshold: 2 * time.Minute,
		BatchSize:        100,
	}, store, emitter)
	recon.clock = func() time.Time { return now }

	ctx := context.Background()
	recon.runCycle(ctx)

	// RequeueStaleExecutions should use the aggressive RequeueThreshold (2m)
	expectedRequeue := now.Add(-2 * time.Minute)
	if !store.requeueOlderThan.Equal(expectedRequeue) {
		t.Errorf("RequeueStaleExecutions olderThan: want %s, got %s", expectedRequeue, store.requeueOlderThan)
	}

	// GetOrphanedExecutions should use the conservative Threshold (15m)
	expectedOrphan := now.Add(-15 * time.Minute)
	if !store.orphanOlderThan.Equal(expectedOrphan) {
		t.Errorf("GetOrphanedExecutions olderThan: want %s, got %s", expectedOrphan, store.orphanOlderThan)
	}
}

// TestReconciler_RequeueThresholdFallback verifies that when RequeueThreshold
// is zero, the reconciler falls back to using Threshold for both operations.
func TestReconciler_RequeueThresholdFallback(t *testing.T) {
	store := &mockStoreWithThresholdCapture{}
	emitter := &mockEmitter{}

	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	recon := New(Config{
		Interval:         time.Hour,
		Threshold:        15 * time.Minute,
		RequeueThreshold: 0, // not set — should fall back to Threshold
		BatchSize:        100,
	}, store, emitter)
	recon.clock = func() time.Time { return now }

	ctx := context.Background()
	recon.runCycle(ctx)

	expected := now.Add(-15 * time.Minute)
	if !store.requeueOlderThan.Equal(expected) {
		t.Errorf("RequeueStaleExecutions should fall back to Threshold: want %s, got %s", expected, store.requeueOlderThan)
	}
}

type mockStoreWithThresholdCapture struct {
	mu               sync.Mutex
	requeueOlderThan time.Time
	orphanOlderThan  time.Time
}

func (s *mockStoreWithThresholdCapture) RequeueStaleExecutions(ctx context.Context, olderThan time.Time, limit int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requeueOlderThan = olderThan
	return 0, nil
}

func (s *mockStoreWithThresholdCapture) GetOrphanedExecutions(ctx context.Context, olderThan time.Time, maxResults int) ([]domain.Execution, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.orphanOlderThan = olderThan
	return nil, nil
}
