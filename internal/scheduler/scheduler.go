package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"

	"github.com/djlord-it/cronlite/internal/domain"
)

var ErrDuplicateExecution = errors.New("execution already exists")

// Pagination defaults for scheduler job loading.
const (
	DefaultJobPageSize = 100
)

type Store interface {
	GetEnabledJobs(ctx context.Context, limit, offset int) ([]domain.JobWithSchedule, error)
	InsertExecution(ctx context.Context, exec domain.Execution) error
}

type lastScheduledExecutionStore interface {
	GetLastScheduledExecution(ctx context.Context, jobID uuid.UUID) (time.Time, bool, error)
}

type CronParser interface {
	Parse(expression string, timezone string) (CronSchedule, error)
}

type CronSchedule interface {
	Next(after time.Time) time.Time
}

type EventEmitter interface {
	Emit(ctx context.Context, event domain.TriggerEvent) error
}

// MetricsSink defines the interface for recording scheduler metrics.
// All methods must be non-blocking and fire-and-forget.
type MetricsSink interface {
	TickStarted()
	TickCompleted(duration time.Duration, jobsTriggered int, err error)
	TickDrift(drift time.Duration)
}

type Config struct {
	TickInterval    time.Duration
	MaxFiresPerTick int // 0 = use default (1000)
}

type Scheduler struct {
	config   Config
	store    Store
	parser   CronParser
	emitter  EventEmitter
	metrics  MetricsSink // optional, nil = disabled
	clock    func() time.Time
	lastTick time.Time
}

func New(config Config, store Store, parser CronParser, emitter EventEmitter) *Scheduler {
	return &Scheduler{
		config:  config,
		store:   store,
		parser:  parser,
		emitter: emitter,
		clock:   time.Now,
	}
}

// WithMetrics attaches a metrics sink to the scheduler.
func (s *Scheduler) WithMetrics(sink MetricsSink) *Scheduler {
	s.metrics = sink
	return s
}

func (s *Scheduler) Run(ctx context.Context) error {
	ticker := time.NewTicker(s.config.TickInterval)
	defer ticker.Stop()

	log.Printf("scheduler: started, tick=%s", s.config.TickInterval)
	startedAt := s.clock().UTC()
	s.lastTick = time.Time{}
	expectedNext := startedAt.Add(s.config.TickInterval)

	for {
		select {
		case <-ctx.Done():
			log.Println("scheduler: stopped")
			return ctx.Err()
		case tickTime := <-ticker.C:
			// Record tick drift before processing
			if s.metrics != nil {
				drift := tickTime.Sub(expectedNext)
				s.metrics.TickDrift(drift)
			}
			expectedNext = tickTime.Add(s.config.TickInterval)

			if err := s.processTick(ctx); err != nil {
				log.Printf("scheduler: tick error: %v", err)
			}
		}
	}
}

func (s *Scheduler) processTick(ctx context.Context) error {
	if s.metrics != nil {
		s.metrics.TickStarted()
	}

	start := s.clock()
	now := start.UTC()
	jobsTriggered := 0

	// Paginate through all enabled jobs to avoid loading unbounded data into memory.
	offset := 0
	for {
		jobs, err := s.store.GetEnabledJobs(ctx, DefaultJobPageSize, offset)
		if err != nil {
			if s.metrics != nil {
				s.metrics.TickCompleted(s.clock().Sub(start), jobsTriggered, err)
			}
			return fmt.Errorf("get jobs: %w", err)
		}

		for i := range jobs {
			triggered, jobErr := s.processJob(ctx, jobs[i], s.lastTick, now)
			jobsTriggered += triggered
			if jobErr != nil {
				log.Printf("scheduler: job=%s namespace=%s error: %v", jobs[i].Job.ID, jobs[i].Job.Namespace, jobErr)
			}
		}

		// If we got fewer than requested, we've reached the end.
		if len(jobs) < DefaultJobPageSize {
			break
		}
		offset += len(jobs)
	}

	s.lastTick = now

	if s.metrics != nil {
		s.metrics.TickCompleted(s.clock().Sub(start), jobsTriggered, nil)
	}

	return nil
}

func (s *Scheduler) processJob(ctx context.Context, jws domain.JobWithSchedule, lastTick, now time.Time) (int, error) {
	job := jws.Job
	schedule := jws.Schedule
	windowStart := s.windowStart(ctx, jws, lastTick, now)

	tz := schedule.Timezone
	if tz == "" {
		tz = "UTC"
	}

	loc, err := time.LoadLocation(tz)
	if err != nil {
		return 0, fmt.Errorf("load tz %s: %w", tz, err)
	}

	lastTickInTZ := windowStart.In(loc)
	nowInTZ := now.In(loc)

	cronSched, err := s.parser.Parse(schedule.CronExpression, tz)
	if err != nil {
		return 0, fmt.Errorf("parse cron: %w", err)
	}

	// Loop through all due times since last tick
	maxIterations := s.config.MaxFiresPerTick
	if maxIterations <= 0 {
		maxIterations = 1000
	}
	t := cronSched.Next(lastTickInTZ)
	triggered := 0

	for i := 0; i < maxIterations && !t.After(nowInTZ); i++ {
		scheduledAtUTC := t.UTC().Truncate(time.Minute)

		if err := s.emitExecution(ctx, job, scheduledAtUTC, now); err != nil {
			log.Printf("scheduler: job=%s namespace=%s at %s error: %v", job.ID, job.Namespace, scheduledAtUTC.Format(time.RFC3339), err)
		} else {
			triggered++
		}

		t = cronSched.Next(t)
	}

	return triggered, nil
}

func (s *Scheduler) windowStart(ctx context.Context, jws domain.JobWithSchedule, lastTick, now time.Time) time.Time {
	if !lastTick.IsZero() {
		return lastTick
	}

	cursor := jws.Job.CreatedAt.UTC()
	if cursor.IsZero() || cursor.After(now) {
		cursor = now.Add(-s.tickInterval())
	}

	store, ok := s.store.(lastScheduledExecutionStore)
	if !ok {
		return cursor
	}

	lastScheduledAt, found, err := store.GetLastScheduledExecution(ctx, jws.Job.ID)
	if err != nil {
		log.Printf("scheduler: job=%s namespace=%s last scheduled lookup failed: %v", jws.Job.ID, jws.Job.Namespace, err)
		return cursor
	}
	if found && lastScheduledAt.After(cursor) {
		return lastScheduledAt.UTC()
	}

	return cursor
}

func (s *Scheduler) tickInterval() time.Duration {
	if s.config.TickInterval > 0 {
		return s.config.TickInterval
	}
	return time.Minute
}

func (s *Scheduler) emitExecution(ctx context.Context, job domain.Job, scheduledAt, now time.Time) error {
	executionID := uuid.New()

	execution := domain.Execution{
		ID:          executionID,
		JobID:       job.ID,
		Namespace:   job.Namespace,
		TriggerType: domain.TriggerTypeScheduled,
		ScheduledAt: scheduledAt,
		FiredAt:     now,
		Status:      domain.ExecutionStatusEmitted,
		CreatedAt:   now,
	}

	if err := s.store.InsertExecution(ctx, execution); err != nil {
		if errors.Is(err, ErrDuplicateExecution) {
			// Idempotent: execution already exists, skip silently.
			// This is expected on scheduler restart or overlapping ticks.
			return nil
		}
		return fmt.Errorf("insert execution: %w", err)
	}

	event := domain.TriggerEvent{
		ExecutionID: executionID,
		JobID:       job.ID,
		Namespace:   job.Namespace,
		ScheduledAt: scheduledAt,
		FiredAt:     now,
		CreatedAt:   now,
	}

	if err := s.emitter.Emit(ctx, event); err != nil {
		// CRITICAL: Execution record exists in DB but event was NOT delivered to dispatcher.
		// This execution is now ORPHANED and will not be retried automatically.
		// Operators should monitor for executions stuck in 'emitted' status.
		log.Printf("scheduler: ORPHAN execution=%s job=%s scheduled_at=%s emit failed: %v",
			executionID, job.ID, scheduledAt.Format(time.RFC3339), err)
		return fmt.Errorf("emit: %w", err)
	}

	log.Printf("scheduler: emitted execution=%s job=%s scheduled_at=%s", executionID, job.ID, scheduledAt.Format(time.RFC3339))
	return nil
}
