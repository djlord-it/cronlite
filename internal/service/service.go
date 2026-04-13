package service

import (
	"context"

	"github.com/djlord-it/cronlite/internal/cron"
	"github.com/djlord-it/cronlite/internal/domain"
)

// EventEmitter sends trigger events to the dispatch pipeline.
type EventEmitter interface {
	Emit(ctx context.Context, event domain.TriggerEvent) error
}

// JobService is the shared domain core. Both REST and MCP transports call it.
// Service methods receive an already-resolved namespace from context (set by
// transport middleware).
type JobService struct {
	jobs       domain.JobRepository
	schedules  domain.ScheduleRepository
	executions domain.ExecutionRepository
	tags       domain.TagRepository
	apiKeys    domain.APIKeyRepository
	attempts   domain.DeliveryAttemptRepository
	parser     *cron.Parser
	emitter    EventEmitter
}

// NewJobService constructs a JobService with all required repository
// dependencies and a cron parser for schedule validation.
func NewJobService(
	jobs domain.JobRepository,
	schedules domain.ScheduleRepository,
	executions domain.ExecutionRepository,
	tags domain.TagRepository,
	apiKeys domain.APIKeyRepository,
	attempts domain.DeliveryAttemptRepository,
	parser *cron.Parser,
) *JobService {
	return &JobService{
		jobs:       jobs,
		schedules:  schedules,
		executions: executions,
		tags:       tags,
		apiKeys:    apiKeys,
		attempts:   attempts,
		parser:     parser,
	}
}

// WithEmitter attaches an EventEmitter so that TriggerNow can push events
// directly to the dispatch pipeline (used in channel mode).
func (s *JobService) WithEmitter(e EventEmitter) *JobService {
	s.emitter = e
	return s
}
