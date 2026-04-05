package scheduler

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/djlord-it/easy-cron/internal/domain"
)

// mockStore tracks executions and enforces idempotency.
type mockStore struct {
	mu           sync.Mutex
	executions   map[string]domain.Execution // key: job_id + scheduled_at
	jobs         []domain.JobWithSchedule
	getJobsCalls []getJobsCall // tracks pagination calls
	getJobsErr   error         // if set, GetEnabledJobs returns this error
}

type getJobsCall struct {
	limit  int
	offset int
}

func newMockStore() *mockStore {
	return &mockStore{
		executions: make(map[string]domain.Execution),
	}
}

func (s *mockStore) GetEnabledJobs(ctx context.Context, limit, offset int) ([]domain.JobWithSchedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.getJobsCalls = append(s.getJobsCalls, getJobsCall{limit: limit, offset: offset})

	if s.getJobsErr != nil {
		return nil, s.getJobsErr
	}

	if offset >= len(s.jobs) {
		return nil, nil
	}
	end := offset + limit
	if end > len(s.jobs) {
		end = len(s.jobs)
	}
	return s.jobs[offset:end], nil
}

func (s *mockStore) InsertExecution(ctx context.Context, exec domain.Execution) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := exec.JobID.String() + "|" + exec.ScheduledAt.Format(time.RFC3339)
	if _, exists := s.executions[key]; exists {
		return ErrDuplicateExecution
	}
	s.executions[key] = exec
	return nil
}

func (s *mockStore) addJob(job domain.Job, schedule domain.Schedule) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs = append(s.jobs, domain.JobWithSchedule{Job: job, Schedule: schedule})
}

func (s *mockStore) executionCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.executions)
}

// mockEmitter tracks emitted events.
type mockEmitter struct {
	mu     sync.Mutex
	events []domain.TriggerEvent
}

func (e *mockEmitter) Emit(ctx context.Context, event domain.TriggerEvent) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, event)
	return nil
}

func (e *mockEmitter) eventCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.events)
}

// mockCronParser returns a schedule that fires at fixed times.
type mockCronParser struct {
	fireTimes []time.Time
}

func (p *mockCronParser) Parse(expression string, timezone string) (CronSchedule, error) {
	return &mockCronSchedule{fireTimes: p.fireTimes}, nil
}

type mockCronSchedule struct {
	fireTimes []time.Time
}

func (s *mockCronSchedule) Next(after time.Time) time.Time {
	for _, t := range s.fireTimes {
		if t.After(after) {
			return t
		}
	}
	// Return far future if no more fire times
	return after.Add(24 * time.Hour)
}

// TestScheduler_Idempotency_SameJobSameTime verifies that the scheduler
// cannot create duplicate executions for the same (job_id, scheduled_at).
func TestScheduler_Idempotency_SameJobSameTime(t *testing.T) {
	store := newMockStore()
	emitter := &mockEmitter{}

	jobID := uuid.New()
	scheduleID := uuid.New()

	// Fire time that will be within our tick window
	fireTime := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)

	store.addJob(
		domain.Job{
			ID:         jobID,
			Namespace:  domain.Namespace("test-ns"),
			Name:       "test-job",
			Enabled:    true,
			ScheduleID: scheduleID,
		},
		domain.Schedule{
			ID:             scheduleID,
			CronExpression: "0 * * * *",
			Timezone:       "UTC",
		},
	)

	parser := &mockCronParser{fireTimes: []time.Time{fireTime}}

	sched := New(
		Config{TickInterval: time.Minute},
		store,
		parser,
		emitter,
	)

	// Simulate clock at fire time + 30 seconds
	now := fireTime.Add(30 * time.Second)
	sched.clock = func() time.Time { return now }
	sched.lastTick = fireTime.Add(-time.Minute) // Last tick was before fire time

	ctx := context.Background()

	// First tick - should create execution
	err := sched.processTick(ctx)
	if err != nil {
		t.Fatalf("first tick failed: %v", err)
	}

	if store.executionCount() != 1 {
		t.Errorf("expected 1 execution after first tick, got %d", store.executionCount())
	}
	if emitter.eventCount() != 1 {
		t.Errorf("expected 1 event after first tick, got %d", emitter.eventCount())
	}

	// Reset lastTick to simulate overlapping tick or restart
	sched.lastTick = fireTime.Add(-time.Minute)

	// Second tick with same time window - should NOT create duplicate
	err = sched.processTick(ctx)
	if err != nil {
		t.Fatalf("second tick failed: %v", err)
	}

	// Still only 1 execution (idempotent)
	if store.executionCount() != 1 {
		t.Errorf("expected 1 execution after second tick (idempotent), got %d", store.executionCount())
	}

	// Event count should also be 1 (no duplicate emit)
	if emitter.eventCount() != 1 {
		t.Errorf("expected 1 event after second tick (idempotent), got %d", emitter.eventCount())
	}
}

// TestScheduler_Idempotency_AcrossTicks verifies idempotency is maintained
// even when the scheduler processes the same fire time across multiple ticks.
func TestScheduler_Idempotency_AcrossTicks(t *testing.T) {
	store := newMockStore()
	emitter := &mockEmitter{}

	jobID := uuid.New()
	scheduleID := uuid.New()

	fireTime := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)

	store.addJob(
		domain.Job{
			ID:         jobID,
			Namespace:  domain.Namespace("test-ns"),
			Name:       "test-job",
			Enabled:    true,
			ScheduleID: scheduleID,
		},
		domain.Schedule{
			ID:             scheduleID,
			CronExpression: "0 * * * *",
			Timezone:       "UTC",
		},
	)

	parser := &mockCronParser{fireTimes: []time.Time{fireTime}}

	sched := New(
		Config{TickInterval: 30 * time.Second},
		store,
		parser,
		emitter,
	)

	ctx := context.Background()

	// Tick 1: 09:59:30 -> 10:00:00 (fire time is in window)
	sched.clock = func() time.Time { return fireTime }
	sched.lastTick = fireTime.Add(-30 * time.Second)
	_ = sched.processTick(ctx)

	// Tick 2: 10:00:00 -> 10:00:30 (overlapping due to clock skew or restart)
	// lastTick might be reset to before fire time
	sched.clock = func() time.Time { return fireTime.Add(30 * time.Second) }
	sched.lastTick = fireTime.Add(-30 * time.Second) // Simulate restart
	_ = sched.processTick(ctx)

	// Tick 3: Another attempt at same window
	sched.clock = func() time.Time { return fireTime.Add(45 * time.Second) }
	sched.lastTick = fireTime.Add(-15 * time.Second)
	_ = sched.processTick(ctx)

	// Should still have exactly 1 execution
	if store.executionCount() != 1 {
		t.Errorf("expected exactly 1 execution across all ticks, got %d", store.executionCount())
	}
}

// TestScheduler_DifferentJobsSameTime verifies that different jobs can have
// executions at the same scheduled time.
func TestScheduler_DifferentJobsSameTime(t *testing.T) {
	store := newMockStore()
	emitter := &mockEmitter{}

	fireTime := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)

	// Add two jobs
	for i := 0; i < 2; i++ {
		jobID := uuid.New()
		scheduleID := uuid.New()
		store.addJob(
			domain.Job{
				ID:         jobID,
				Namespace:  domain.Namespace("test-ns"),
				Name:       "test-job",
				Enabled:    true,
				ScheduleID: scheduleID,
			},
			domain.Schedule{
				ID:             scheduleID,
				CronExpression: "0 * * * *",
				Timezone:       "UTC",
			},
		)
	}

	parser := &mockCronParser{fireTimes: []time.Time{fireTime}}

	sched := New(
		Config{TickInterval: time.Minute},
		store,
		parser,
		emitter,
	)

	sched.clock = func() time.Time { return fireTime.Add(30 * time.Second) }
	sched.lastTick = fireTime.Add(-time.Minute)

	ctx := context.Background()
	_ = sched.processTick(ctx)

	// Should have 2 executions (one per job)
	if store.executionCount() != 2 {
		t.Errorf("expected 2 executions (one per job), got %d", store.executionCount())
	}
}

// TestScheduler_SameJobDifferentTimes verifies that the same job can have
// multiple executions at different scheduled times.
func TestScheduler_SameJobDifferentTimes(t *testing.T) {
	store := newMockStore()
	emitter := &mockEmitter{}

	jobID := uuid.New()
	scheduleID := uuid.New()

	fireTime1 := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
	fireTime2 := time.Date(2024, 1, 15, 11, 0, 0, 0, time.UTC)

	store.addJob(
		domain.Job{
			ID:         jobID,
			Namespace:  domain.Namespace("test-ns"),
			Name:       "test-job",
			Enabled:    true,
			ScheduleID: scheduleID,
		},
		domain.Schedule{
			ID:             scheduleID,
			CronExpression: "0 * * * *",
			Timezone:       "UTC",
		},
	)

	parser := &mockCronParser{fireTimes: []time.Time{fireTime1, fireTime2}}

	sched := New(
		Config{TickInterval: 2 * time.Hour}, // Large window to capture both
		store,
		parser,
		emitter,
	)

	sched.clock = func() time.Time { return fireTime2.Add(30 * time.Second) }
	sched.lastTick = fireTime1.Add(-time.Minute)

	ctx := context.Background()
	_ = sched.processTick(ctx)

	// Should have 2 executions (same job, different times)
	if store.executionCount() != 2 {
		t.Errorf("expected 2 executions (different times), got %d", store.executionCount())
	}
}

// TestScheduler_Pagination verifies that the scheduler paginates through
// jobs in chunks rather than loading all at once.
func TestScheduler_Pagination(t *testing.T) {
	store := newMockStore()
	emitter := &mockEmitter{}

	fireTime := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)

	// Add 250 jobs (more than DefaultJobPageSize of 100)
	// This will require 3 pages: 100 + 100 + 50
	numJobs := 250
	for i := 0; i < numJobs; i++ {
		jobID := uuid.New()
		scheduleID := uuid.New()
		store.addJob(
			domain.Job{
				ID:         jobID,
				Namespace:  domain.Namespace("test-ns"),
				Name:       "test-job",
				Enabled:    true,
				ScheduleID: scheduleID,
			},
			domain.Schedule{
				ID:             scheduleID,
				CronExpression: "0 * * * *",
				Timezone:       "UTC",
			},
		)
	}

	parser := &mockCronParser{fireTimes: []time.Time{fireTime}}

	sched := New(
		Config{TickInterval: time.Minute},
		store,
		parser,
		emitter,
	)

	sched.clock = func() time.Time { return fireTime.Add(30 * time.Second) }
	sched.lastTick = fireTime.Add(-time.Minute)

	ctx := context.Background()
	err := sched.processTick(ctx)
	if err != nil {
		t.Fatalf("processTick failed: %v", err)
	}

	// Verify pagination happened: should have 3 pages of calls
	// Page 1: offset=0, Page 2: offset=100, Page 3: offset=200
	store.mu.Lock()
	calls := store.getJobsCalls
	store.mu.Unlock()

	if len(calls) != 3 {
		t.Errorf("expected 3 pagination calls, got %d", len(calls))
	}

	// Verify offsets
	expectedOffsets := []int{0, 100, 200}
	for i, expected := range expectedOffsets {
		if i >= len(calls) {
			break
		}
		if calls[i].offset != expected {
			t.Errorf("call %d: expected offset %d, got %d", i, expected, calls[i].offset)
		}
		if calls[i].limit != DefaultJobPageSize {
			t.Errorf("call %d: expected limit %d, got %d", i, DefaultJobPageSize, calls[i].limit)
		}
	}

	// Verify all jobs were processed
	if store.executionCount() != numJobs {
		t.Errorf("expected %d executions, got %d", numJobs, store.executionCount())
	}
}

// TestProcessTick_GetEnabledJobsError verifies that processTick returns an
// error when the store fails to fetch enabled jobs.
func TestProcessTick_GetEnabledJobsError(t *testing.T) {
	store := newMockStore()
	store.getJobsErr = errors.New("database connection lost")
	emitter := &mockEmitter{}
	parser := &mockCronParser{}

	sched := New(
		Config{TickInterval: time.Minute},
		store,
		parser,
		emitter,
	)

	now := time.Date(2024, 1, 15, 10, 0, 30, 0, time.UTC)
	sched.clock = func() time.Time { return now }
	sched.lastTick = now.Add(-time.Minute)

	err := sched.processTick(context.Background())
	if err == nil {
		t.Fatal("expected error from processTick when GetEnabledJobs fails, got nil")
	}
	if !errors.Is(err, store.getJobsErr) {
		t.Errorf("expected wrapped error containing %q, got %q", store.getJobsErr, err)
	}
	if emitter.eventCount() != 0 {
		t.Errorf("expected 0 events emitted on store error, got %d", emitter.eventCount())
	}
}

// mockFailingParser always returns an error from Parse.
type mockFailingParser struct {
	err error
}

func (p *mockFailingParser) Parse(expression string, timezone string) (CronSchedule, error) {
	return nil, p.err
}

// TestProcessJob_InvalidTimezone verifies that processJob returns an error
// when the schedule has an unloadable timezone string.
func TestProcessJob_InvalidTimezone(t *testing.T) {
	store := newMockStore()
	emitter := &mockEmitter{}
	parser := &mockCronParser{}

	sched := New(
		Config{TickInterval: time.Minute},
		store,
		parser,
		emitter,
	)

	jobID := uuid.New()
	scheduleID := uuid.New()

	jws := domain.JobWithSchedule{
		Job: domain.Job{
			ID:         jobID,
			Namespace:  domain.Namespace("test-ns"),
			Name:       "bad-tz-job",
			Enabled:    true,
			ScheduleID: scheduleID,
		},
		Schedule: domain.Schedule{
			ID:             scheduleID,
			CronExpression: "0 * * * *",
			Timezone:       "Invalid/Nowhere",
		},
	}

	now := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
	lastTick := now.Add(-time.Minute)

	triggered, err := sched.processJob(context.Background(), jws, lastTick, now)
	if err == nil {
		t.Fatal("expected error for invalid timezone, got nil")
	}
	if triggered != 0 {
		t.Errorf("expected 0 triggered jobs for bad timezone, got %d", triggered)
	}
}

// TestProcessJob_CronParseError verifies that processJob returns an error
// when the cron expression cannot be parsed.
func TestProcessJob_CronParseError(t *testing.T) {
	store := newMockStore()
	emitter := &mockEmitter{}
	parseErr := errors.New("invalid cron expression")
	parser := &mockFailingParser{err: parseErr}

	sched := New(
		Config{TickInterval: time.Minute},
		store,
		parser,
		emitter,
	)

	jobID := uuid.New()
	scheduleID := uuid.New()

	jws := domain.JobWithSchedule{
		Job: domain.Job{
			ID:         jobID,
			Namespace:  domain.Namespace("test-ns"),
			Name:       "bad-cron-job",
			Enabled:    true,
			ScheduleID: scheduleID,
		},
		Schedule: domain.Schedule{
			ID:             scheduleID,
			CronExpression: "not a cron",
			Timezone:       "UTC",
		},
	}

	now := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
	lastTick := now.Add(-time.Minute)

	triggered, err := sched.processJob(context.Background(), jws, lastTick, now)
	if err == nil {
		t.Fatal("expected error for invalid cron expression, got nil")
	}
	if !errors.Is(err, parseErr) {
		t.Errorf("expected wrapped error containing %q, got %q", parseErr, err)
	}
	if triggered != 0 {
		t.Errorf("expected 0 triggered jobs for parse error, got %d", triggered)
	}
}

// mockFailingEmitter returns an error on Emit.
type mockFailingEmitter struct {
	mu     sync.Mutex
	events []domain.TriggerEvent
	err    error
}

func (e *mockFailingEmitter) Emit(ctx context.Context, event domain.TriggerEvent) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, event)
	return e.err
}

// TestProcessJob_EmitError verifies that when the emitter fails, the execution
// is still inserted into the store but the error is propagated via log (processJob
// continues to next fire time).
func TestProcessJob_EmitError(t *testing.T) {
	store := newMockStore()
	emitErr := errors.New("channel full")
	emitter := &mockFailingEmitter{err: emitErr}

	fireTime := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
	parser := &mockCronParser{fireTimes: []time.Time{fireTime}}

	sched := New(
		Config{TickInterval: time.Minute},
		store,
		parser,
		emitter,
	)

	jobID := uuid.New()
	scheduleID := uuid.New()

	jws := domain.JobWithSchedule{
		Job: domain.Job{
			ID:         jobID,
			Namespace:  domain.Namespace("test-ns"),
			Name:       "emit-fail-job",
			Enabled:    true,
			ScheduleID: scheduleID,
		},
		Schedule: domain.Schedule{
			ID:             scheduleID,
			CronExpression: "0 * * * *",
			Timezone:       "UTC",
		},
	}

	now := fireTime.Add(30 * time.Second)
	lastTick := fireTime.Add(-time.Minute)

	triggered, err := sched.processJob(context.Background(), jws, lastTick, now)
	// processJob does not return an error for emit failures; it logs and continues.
	// triggered count should be 0 because the emit failed.
	if err != nil {
		t.Fatalf("processJob returned unexpected error: %v", err)
	}
	if triggered != 0 {
		t.Errorf("expected 0 triggered (emit failed), got %d", triggered)
	}

	// The execution should still be in the store (orphaned).
	if store.executionCount() != 1 {
		t.Errorf("expected 1 execution in store (orphaned), got %d", store.executionCount())
	}
}

// TestProcessJob_EmptyTimezoneDefaultsToUTC verifies that an empty timezone
// string defaults to UTC and does not cause an error.
func TestProcessJob_EmptyTimezoneDefaultsToUTC(t *testing.T) {
	store := newMockStore()
	emitter := &mockEmitter{}

	fireTime := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
	parser := &mockCronParser{fireTimes: []time.Time{fireTime}}

	sched := New(
		Config{TickInterval: time.Minute},
		store,
		parser,
		emitter,
	)

	jobID := uuid.New()
	scheduleID := uuid.New()

	jws := domain.JobWithSchedule{
		Job: domain.Job{
			ID:         jobID,
			Namespace:  domain.Namespace("test-ns"),
			Name:       "empty-tz-job",
			Enabled:    true,
			ScheduleID: scheduleID,
		},
		Schedule: domain.Schedule{
			ID:             scheduleID,
			CronExpression: "0 * * * *",
			Timezone:       "", // empty should default to UTC
		},
	}

	now := fireTime.Add(30 * time.Second)
	lastTick := fireTime.Add(-time.Minute)

	triggered, err := sched.processJob(context.Background(), jws, lastTick, now)
	if err != nil {
		t.Fatalf("expected no error for empty timezone, got: %v", err)
	}
	if triggered != 1 {
		t.Errorf("expected 1 triggered job, got %d", triggered)
	}
}
