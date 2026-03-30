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

// mockStoreWithErrors extends mockStore with configurable errors.
type mockStoreWithErrors struct {
	mu             sync.Mutex
	executions     map[string]domain.Execution
	jobs           []domain.JobWithSchedule
	getJobsErr     error
	insertExecErr  error
	insertExecKeys []string // tracks which job_id|scheduled_at keys were attempted
}

func newMockStoreWithErrors() *mockStoreWithErrors {
	return &mockStoreWithErrors{
		executions: make(map[string]domain.Execution),
	}
}

func (s *mockStoreWithErrors) GetEnabledJobs(ctx context.Context, limit, offset int) ([]domain.JobWithSchedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

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

func (s *mockStoreWithErrors) InsertExecution(ctx context.Context, exec domain.Execution) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := exec.JobID.String() + "|" + exec.ScheduledAt.Format(time.RFC3339)
	s.insertExecKeys = append(s.insertExecKeys, key)

	if s.insertExecErr != nil {
		return s.insertExecErr
	}

	if _, exists := s.executions[key]; exists {
		return ErrDuplicateExecution
	}
	s.executions[key] = exec
	return nil
}

// mockEmitterWithErrors tracks emitted events and can fail.
type mockEmitterWithErrors struct {
	mu     sync.Mutex
	events []domain.TriggerEvent
	err    error
	// failForJobID: if set, only fail for this specific job
	failForJobID *uuid.UUID
}

func (e *mockEmitterWithErrors) Emit(ctx context.Context, event domain.TriggerEvent) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.failForJobID != nil && event.JobID == *e.failForJobID {
		return e.err
	}
	if e.err != nil && e.failForJobID == nil {
		return e.err
	}

	e.events = append(e.events, event)
	return nil
}

func (e *mockEmitterWithErrors) eventCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.events)
}

// mockCronParserWithErrors can fail for specific expressions.
type mockCronParserWithErrors struct {
	fireTimes   []time.Time
	failForExpr string
}

func (p *mockCronParserWithErrors) Parse(expression string, timezone string) (CronSchedule, error) {
	if expression == p.failForExpr {
		return nil, errors.New("invalid cron expression")
	}
	return &mockCronSchedule{fireTimes: p.fireTimes}, nil
}

// mockMetricsSink tracks scheduler metrics calls.
type mockMetricsSink struct {
	mu                sync.Mutex
	tickStartedCalls  int
	tickCompletedArgs []tickCompletedArg
	tickDriftCalls    []time.Duration
}

type tickCompletedArg struct {
	duration      time.Duration
	jobsTriggered int
	err           error
}

func (m *mockMetricsSink) TickStarted() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tickStartedCalls++
}

func (m *mockMetricsSink) TickCompleted(duration time.Duration, jobsTriggered int, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tickCompletedArgs = append(m.tickCompletedArgs, tickCompletedArg{duration, jobsTriggered, err})
}

func (m *mockMetricsSink) TickDrift(drift time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tickDriftCalls = append(m.tickDriftCalls, drift)
}

// TestScheduler_EmitterError_ContinuesProcessing verifies that an emit error
// for one job doesn't stop processing of other jobs.
func TestScheduler_EmitterError_ContinuesProcessing(t *testing.T) {
	store := newMockStoreWithErrors()
	emitter := &mockEmitterWithErrors{}

	fireTime := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)

	failJobID := uuid.New()
	successJobID := uuid.New()

	// Add two jobs
	store.jobs = []domain.JobWithSchedule{
		{
			Job:      domain.Job{ID: failJobID, Namespace: domain.Namespace("test-ns"), Name: "fail-job", Enabled: true, ScheduleID: uuid.New()},
			Schedule: domain.Schedule{ID: uuid.New(), CronExpression: "0 * * * *", Timezone: "UTC"},
		},
		{
			Job:      domain.Job{ID: successJobID, Namespace: domain.Namespace("test-ns"), Name: "success-job", Enabled: true, ScheduleID: uuid.New()},
			Schedule: domain.Schedule{ID: uuid.New(), CronExpression: "0 * * * *", Timezone: "UTC"},
		},
	}

	emitter.err = errors.New("emit failed")
	emitter.failForJobID = &failJobID

	parser := &mockCronParser{fireTimes: []time.Time{fireTime}}

	sched := New(Config{TickInterval: time.Minute}, store, parser, emitter)
	sched.clock = func() time.Time { return fireTime.Add(30 * time.Second) }
	sched.lastTick = fireTime.Add(-time.Minute)

	ctx := context.Background()
	err := sched.processTick(ctx)
	if err != nil {
		t.Fatalf("processTick should not return error: %v", err)
	}

	// The success job should still have been emitted
	if emitter.eventCount() != 1 {
		t.Errorf("expected 1 successful event (the non-failing job), got %d", emitter.eventCount())
	}
}

// TestScheduler_StoreError_GetJobs verifies that a store error aborts the tick.
func TestScheduler_StoreError_GetJobs(t *testing.T) {
	store := newMockStoreWithErrors()
	store.getJobsErr = errors.New("database unavailable")

	emitter := &mockEmitterWithErrors{}
	parser := &mockCronParser{}

	sched := New(Config{TickInterval: time.Minute}, store, parser, emitter)
	now := time.Now().UTC()
	sched.clock = func() time.Time { return now }
	sched.lastTick = now.Add(-time.Minute)

	ctx := context.Background()
	err := sched.processTick(ctx)

	if err == nil {
		t.Error("expected error from processTick when store fails")
	}
	if emitter.eventCount() != 0 {
		t.Error("no events should be emitted when store fails")
	}
}

// TestScheduler_NoJobsEnabled verifies that an empty store produces no errors.
func TestScheduler_NoJobsEnabled(t *testing.T) {
	store := newMockStoreWithErrors()
	emitter := &mockEmitterWithErrors{}
	parser := &mockCronParser{}

	sched := New(Config{TickInterval: time.Minute}, store, parser, emitter)
	now := time.Now().UTC()
	sched.clock = func() time.Time { return now }
	sched.lastTick = now.Add(-time.Minute)

	ctx := context.Background()
	err := sched.processTick(ctx)

	if err != nil {
		t.Errorf("expected nil error for empty store, got: %v", err)
	}
	if emitter.eventCount() != 0 {
		t.Error("expected 0 events for empty store")
	}
}

// TestScheduler_ParseError_SkipsJob verifies that a cron parse error
// skips the job but continues processing others.
func TestScheduler_ParseError_SkipsJob(t *testing.T) {
	store := newMockStoreWithErrors()
	emitter := &mockEmitterWithErrors{}

	fireTime := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)

	store.jobs = []domain.JobWithSchedule{
		{
			Job:      domain.Job{ID: uuid.New(), Namespace: domain.Namespace("test-ns"), Name: "bad-cron", Enabled: true, ScheduleID: uuid.New()},
			Schedule: domain.Schedule{ID: uuid.New(), CronExpression: "INVALID", Timezone: "UTC"},
		},
		{
			Job:      domain.Job{ID: uuid.New(), Namespace: domain.Namespace("test-ns"), Name: "good-job", Enabled: true, ScheduleID: uuid.New()},
			Schedule: domain.Schedule{ID: uuid.New(), CronExpression: "0 * * * *", Timezone: "UTC"},
		},
	}

	parser := &mockCronParserWithErrors{
		fireTimes:   []time.Time{fireTime},
		failForExpr: "INVALID",
	}

	sched := New(Config{TickInterval: time.Minute}, store, parser, emitter)
	sched.clock = func() time.Time { return fireTime.Add(30 * time.Second) }
	sched.lastTick = fireTime.Add(-time.Minute)

	ctx := context.Background()
	err := sched.processTick(ctx)
	if err != nil {
		t.Fatalf("processTick should not return error: %v", err)
	}

	// Only the good job should be emitted
	if emitter.eventCount() != 1 {
		t.Errorf("expected 1 event (good job only), got %d", emitter.eventCount())
	}
}

// TestScheduler_MetricsRecording verifies that metrics are called correctly.
func TestScheduler_MetricsRecording(t *testing.T) {
	store := newMockStoreWithErrors()
	emitter := &mockEmitterWithErrors{}
	parser := &mockCronParser{}
	metrics := &mockMetricsSink{}

	sched := New(Config{TickInterval: time.Minute}, store, parser, emitter)
	sched.WithMetrics(metrics)

	now := time.Now().UTC()
	sched.clock = func() time.Time { return now }
	sched.lastTick = now.Add(-time.Minute)

	ctx := context.Background()
	sched.processTick(ctx)

	metrics.mu.Lock()
	defer metrics.mu.Unlock()

	if metrics.tickStartedCalls != 1 {
		t.Errorf("TickStarted should be called once, got %d", metrics.tickStartedCalls)
	}
	if len(metrics.tickCompletedArgs) != 1 {
		t.Errorf("TickCompleted should be called once, got %d", len(metrics.tickCompletedArgs))
	}
	if metrics.tickCompletedArgs[0].err != nil {
		t.Errorf("TickCompleted should have nil error, got: %v", metrics.tickCompletedArgs[0].err)
	}
}

// TestScheduler_EmptyTimezone_DefaultsUTC verifies that an empty timezone
// in the schedule defaults to UTC.
func TestScheduler_EmptyTimezone_DefaultsUTC(t *testing.T) {
	store := newMockStoreWithErrors()
	emitter := &mockEmitterWithErrors{}

	fireTime := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)

	store.jobs = []domain.JobWithSchedule{
		{
			Job:      domain.Job{ID: uuid.New(), Namespace: domain.Namespace("test-ns"), Name: "no-tz", Enabled: true, ScheduleID: uuid.New()},
			Schedule: domain.Schedule{ID: uuid.New(), CronExpression: "0 * * * *", Timezone: ""}, // empty timezone
		},
	}

	parser := &mockCronParser{fireTimes: []time.Time{fireTime}}

	sched := New(Config{TickInterval: time.Minute}, store, parser, emitter)
	sched.clock = func() time.Time { return fireTime.Add(30 * time.Second) }
	sched.lastTick = fireTime.Add(-time.Minute)

	ctx := context.Background()
	err := sched.processTick(ctx)
	if err != nil {
		t.Fatalf("processTick should not error with empty timezone: %v", err)
	}

	if emitter.eventCount() != 1 {
		t.Errorf("expected 1 event, got %d", emitter.eventCount())
	}
}
