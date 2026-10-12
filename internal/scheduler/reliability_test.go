package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/djlord-it/cronlite/internal/cron"
	"github.com/djlord-it/cronlite/internal/domain"
	"github.com/google/uuid"
)

type reliabilityParser struct{}

func (reliabilityParser) Parse(expression, timezone string) (CronSchedule, error) {
	schedule, err := cron.NewParser().Parse(expression, timezone)
	return schedule, err
}

type transientInsertStore struct {
	*mockStore
	failAt time.Time
	failed bool
}

type jobInsertFailureStore struct {
	*mockStore
	failJob uuid.UUID
}

func (s *jobInsertFailureStore) InsertExecution(ctx context.Context, execution domain.Execution) error {
	if execution.JobID == s.failJob {
		return errors.New("job-specific persistence failure")
	}
	return s.mockStore.InsertExecution(ctx, execution)
}

func TestScheduler_FailedJobDoesNotStarveHealthyJob(t *testing.T) {
	start := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	failed, healthy := uuid.New(), uuid.New()
	store := &jobInsertFailureStore{mockStore: newMockStore(), failJob: failed}
	for _, id := range []uuid.UUID{failed, healthy} {
		store.addJob(domain.Job{ID: id, Namespace: "test", CreatedAt: start}, domain.Schedule{CronExpression: "* * * * *", Timezone: "UTC"})
	}
	scheduler := New(Config{TickInterval: time.Second, MaxFiresPerTick: 2}, store, reliabilityParser{}, &mockEmitter{})
	scheduler.lastTick = start
	now := start.Add(2*time.Minute + 30*time.Second)
	scheduler.clock = func() time.Time { return now }
	for tick := 0; tick < 4; tick++ {
		if err := scheduler.processTick(context.Background()); err != nil {
			t.Fatal(err)
		}
		if store.executionCount() != tick+2 {
			t.Fatalf("healthy job starved by failed job: tick%d rows%d want%d", tick, store.executionCount(), tick+2)
		}
		if tick < 3 {
			now = now.Add(time.Minute)
		}
	}
	store.failJob = uuid.Nil
	for range 3 {
		if err := scheduler.processTick(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if store.executionCount() != 10 || !scheduler.lastTick.Equal(now) {
		t.Fatalf("failed job did not recover all due slots: rows%d cursor%s", store.executionCount(), scheduler.lastTick)
	}
}

func (s *transientInsertStore) InsertExecution(ctx context.Context, execution domain.Execution) error {
	if execution.ScheduledAt.Equal(s.failAt) && !s.failed {
		s.failed = true
		return errors.New("temporary database failure")
	}
	return s.mockStore.InsertExecution(ctx, execution)
}

func TestScheduler_RetriesFailedPersistenceWithoutSkippingSlot(t *testing.T) {
	first := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	store := &transientInsertStore{mockStore: newMockStore(), failAt: first}
	job := domain.Job{ID: uuid.New(), Namespace: "test", CreatedAt: first.Add(-time.Hour)}
	store.addJob(job, domain.Schedule{CronExpression: "* * * * *", Timezone: "UTC"})
	scheduler := New(Config{TickInterval: time.Second}, store, reliabilityParser{}, &mockEmitter{})
	scheduler.lastTick = first.Add(-time.Second)
	// A later due slot must not be persisted past the failed slot: restart uses
	// the latest durable execution as its cursor.
	now := first.Add(time.Minute + 30*time.Second)
	scheduler.clock = func() time.Time { return now }
	if err := scheduler.processTick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.executionCount() != 0 {
		t.Fatal("advanced durable cursor past failed insertion")
	}
	now = now.Add(time.Second)
	if err := scheduler.processTick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.executionCount() != 2 {
		t.Fatalf("expected both due slots after recovery, got %d", store.executionCount())
	}
}

func TestScheduler_RestartRecoversMiddleInsertFailure(t *testing.T) {
	first := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	store := &transientInsertStore{mockStore: newMockStore(), failAt: first.Add(time.Minute)}
	store.addJob(domain.Job{ID: uuid.New(), Namespace: "test", CreatedAt: first.Add(-time.Second)}, domain.Schedule{CronExpression: "* * * * *", Timezone: "UTC"})
	now := first.Add(2*time.Minute + 30*time.Second)
	makeScheduler := func() *Scheduler {
		s := New(Config{TickInterval: time.Second}, store, reliabilityParser{}, &mockEmitter{})
		s.clock = func() time.Time { return now }
		return s
	}
	if err := makeScheduler().processTick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.executionCount() != 1 {
		t.Fatalf("persisted past failed middle slot: %d rows", store.executionCount())
	}
	// A fresh scheduler has no in-memory cursor and resumes from durable history.
	if err := makeScheduler().processTick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.executionCount() != 3 {
		t.Fatalf("restart lost due slots: %d rows", store.executionCount())
	}
}

func TestScheduler_ImpossibleCalendarProducesNoExecutions(t *testing.T) {
	store := newMockStore()
	now := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	job := domain.Job{ID: uuid.New(), Namespace: "test", CreatedAt: now.Add(-time.Hour)}
	scheduler := New(Config{TickInterval: time.Second}, store, reliabilityParser{}, &mockEmitter{})
	result, err := scheduler.processJobWithProgress(context.Background(), domain.JobWithSchedule{Job: job, Schedule: domain.Schedule{CronExpression: "0 0 31 2 *", Timezone: "UTC"}}, now.Add(-time.Minute), now)
	if err != nil {
		t.Fatal(err)
	}
	if result.triggered != 0 || store.executionCount() != 0 {
		t.Fatalf("impossible cron produced %d triggers and %d rows", result.triggered, store.executionCount())
	}
}

func TestScheduler_CatchUpNeverPredatesJobCreation(t *testing.T) {
	store := newMockStore()
	created := time.Date(2026, 1, 1, 12, 0, 30, 0, time.UTC)
	job := domain.Job{ID: uuid.New(), Namespace: "test", CreatedAt: created}
	scheduler := New(Config{TickInterval: time.Second}, store, reliabilityParser{}, &mockEmitter{})
	result, err := scheduler.processJobWithProgress(context.Background(), domain.JobWithSchedule{Job: job, Schedule: domain.Schedule{CronExpression: "* * * * *", Timezone: "UTC"}}, created.Add(-2*time.Hour), created.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if result.triggered != 1 {
		t.Fatalf("new job got %d triggers, want only the next minute", result.triggered)
	}
	for _, execution := range store.executions {
		if execution.ScheduledAt.Before(created) {
			t.Fatalf("execution %s predates job creation %s", execution.ScheduledAt, created)
		}
	}
}

func TestScheduler_FutureCreationTimeKeepsClockSkewFallback(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 30, 0, time.UTC)
	lastTick := now.Add(-time.Minute)
	jws := domain.JobWithSchedule{Job: domain.Job{CreatedAt: now.Add(10 * time.Minute)}}
	scheduler := New(Config{TickInterval: time.Minute}, newMockStore(), reliabilityParser{}, &mockEmitter{})
	if got := scheduler.windowStart(context.Background(), jws, lastTick, now); !got.Equal(lastTick) {
		t.Fatalf("future creation time moved active cursor: got %s want %s", got, lastTick)
	}
}
