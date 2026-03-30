package service

import (
	"github.com/djlord-it/easy-cron/internal/cron"
	"github.com/djlord-it/easy-cron/internal/domain"
)

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
