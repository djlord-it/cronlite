package domain

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Repository interfaces — implemented by store/postgres/

type JobRepository interface {
	InsertJob(ctx context.Context, job Job, schedule Schedule) error
	GetJob(ctx context.Context, id uuid.UUID) (Job, error)
	// GetJobWithSchedule retrieves a job by ID without namespace filtering.
	// Used by system components (scheduler, reconciler) that operate cross-namespace.
	GetJobWithSchedule(ctx context.Context, id uuid.UUID) (Job, Schedule, error)
	// GetJobWithScheduleScoped retrieves a job by ID filtered by namespace at the SQL level.
	// Used by the service layer for API-facing operations (defense-in-depth).
	GetJobWithScheduleScoped(ctx context.Context, id uuid.UUID, ns Namespace) (Job, Schedule, error)
	ListJobs(ctx context.Context, filter JobFilter) ([]Job, error)
	UpdateJob(ctx context.Context, job Job) error
	DeleteJob(ctx context.Context, id uuid.UUID, ns Namespace) error
	GetEnabledJobs(ctx context.Context, limit, offset int) ([]JobWithSchedule, error)
}

type ScheduleRepository interface {
	InsertSchedule(ctx context.Context, schedule Schedule) error
	GetSchedule(ctx context.Context, id uuid.UUID) (Schedule, error)
	UpdateSchedule(ctx context.Context, schedule Schedule) error
}

type ExecutionRepository interface {
	InsertExecution(ctx context.Context, exec Execution) error
	GetExecution(ctx context.Context, id uuid.UUID) (Execution, error)
	ListExecutions(ctx context.Context, filter ExecutionFilter) ([]Execution, error)
	GetRecentExecutions(ctx context.Context, jobID uuid.UUID, limit int) ([]Execution, error)
	UpdateExecutionStatus(ctx context.Context, id uuid.UUID, status ExecutionStatus) error
	DequeueExecution(ctx context.Context) (*Execution, error)
	GetOrphanedExecutions(ctx context.Context, olderThan time.Time, maxResults int) ([]Execution, error)
	RequeueStaleExecutions(ctx context.Context, olderThan time.Time, limit int) (int, error)
}

type TagRepository interface {
	UpsertTags(ctx context.Context, jobID uuid.UUID, tags []Tag) error
	GetTags(ctx context.Context, jobID uuid.UUID) ([]Tag, error)
	DeleteTags(ctx context.Context, jobID uuid.UUID) error
}

type APIKeyRepository interface {
	InsertAPIKey(ctx context.Context, key APIKey) error
	GetKeyByTokenHash(ctx context.Context, tokenHash string) (APIKey, error)
	ListKeys(ctx context.Context, ns Namespace, params ListParams) ([]APIKey, error)
	DeleteKey(ctx context.Context, id uuid.UUID, ns Namespace) error
	UpdateLastUsedAt(ctx context.Context, ids []uuid.UUID) error
}

type DeliveryAttemptRepository interface {
	InsertDeliveryAttempt(ctx context.Context, attempt DeliveryAttempt) error
	GetAttempts(ctx context.Context, executionID uuid.UUID) ([]DeliveryAttempt, error)
}

// Composite type used by scheduler
type JobWithSchedule struct {
	Job      Job
	Schedule Schedule
}
