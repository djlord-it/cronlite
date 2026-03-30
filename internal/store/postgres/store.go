package postgres

import (
	"context"
	"database/sql"
	"time"

	"github.com/google/uuid"

	"github.com/djlord-it/easy-cron/internal/api"
	"github.com/djlord-it/easy-cron/internal/dispatcher"
	"github.com/djlord-it/easy-cron/internal/domain"
	"github.com/djlord-it/easy-cron/internal/scheduler"
)

// Store implements scheduler.Store and dispatcher.Store using PostgreSQL.
type Store struct {
	db        *sql.DB
	opTimeout time.Duration
}

// New creates a new PostgreSQL store with the given database connection.
// opTimeout specifies the maximum duration for individual DB operations.
// If opTimeout is 0, no timeout is applied (not recommended for production).
func New(db *sql.DB, opTimeout time.Duration) *Store {
	return &Store{db: db, opTimeout: opTimeout}
}

// withTimeout returns a context with the store's operation timeout applied.
// If the parent context already has a deadline earlier than the timeout,
// the parent's deadline is preserved. The returned cancel function must be called.
func (s *Store) withTimeout(parent context.Context) (context.Context, context.CancelFunc) {
	if s.opTimeout <= 0 {
		return parent, func() {}
	}
	return context.WithTimeout(parent, s.opTimeout)
}

// GetEnabledJobs returns enabled jobs with their schedules, paginated by limit and offset.
func (s *Store) GetEnabledJobs(ctx context.Context, limit, offset int) ([]domain.JobWithSchedule, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	rows, err := s.db.QueryContext(ctx, queryGetEnabledJobs, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []domain.JobWithSchedule
	for rows.Next() {
		var jws domain.JobWithSchedule
		var timeoutMs int64

		err := rows.Scan(
			&jws.Job.ID,
			&jws.Job.Namespace,
			&jws.Job.Name,
			&jws.Job.Enabled,
			&jws.Job.ScheduleID,
			&jws.Job.Delivery.Type,
			&jws.Job.Delivery.WebhookURL,
			&jws.Job.Delivery.Secret,
			&timeoutMs,
			&jws.Job.Analytics.Enabled,
			&jws.Job.Analytics.RetentionSeconds,
			&jws.Job.CreatedAt,
			&jws.Job.UpdatedAt,
			&jws.Schedule.ID,
			&jws.Schedule.CronExpression,
			&jws.Schedule.Timezone,
			&jws.Schedule.CreatedAt,
			&jws.Schedule.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		jws.Job.Delivery.Timeout = time.Duration(timeoutMs) * time.Millisecond
		result = append(result, jws)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return result, nil
}

// InsertExecution inserts a new execution record.
// Returns scheduler.ErrDuplicateExecution if (job_id, scheduled_at) already exists.
func (s *Store) InsertExecution(ctx context.Context, exec domain.Execution) error {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	_, err := s.db.ExecContext(ctx, queryInsertExecution,
		exec.ID,
		exec.JobID,
		string(exec.Namespace),
		exec.ScheduledAt,
		exec.FiredAt,
		string(exec.Status),
		exec.CreatedAt,
	)
	if err != nil {
		if isDuplicateKeyError(err) {
			return scheduler.ErrDuplicateExecution
		}
		return err
	}
	return nil
}

// GetJobByID returns a job by its ID.
func (s *Store) GetJobByID(ctx context.Context, jobID uuid.UUID) (domain.Job, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	var job domain.Job
	var timeoutMs int64

	err := s.db.QueryRowContext(ctx, queryGetJobByID, jobID).Scan(
		&job.ID,
		&job.Namespace,
		&job.Name,
		&job.Enabled,
		&job.ScheduleID,
		&job.Delivery.Type,
		&job.Delivery.WebhookURL,
		&job.Delivery.Secret,
		&timeoutMs,
		&job.Analytics.Enabled,
		&job.Analytics.RetentionSeconds,
		&job.CreatedAt,
		&job.UpdatedAt,
	)
	if err != nil {
		return domain.Job{}, err
	}
	job.Delivery.Timeout = time.Duration(timeoutMs) * time.Millisecond
	return job, nil
}

// InsertDeliveryAttempt inserts a new delivery attempt record.
func (s *Store) InsertDeliveryAttempt(ctx context.Context, attempt domain.DeliveryAttempt) error {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	_, err := s.db.ExecContext(ctx, queryInsertDeliveryAttempt,
		attempt.ID,
		attempt.ExecutionID,
		attempt.Attempt,
		attempt.StatusCode,
		attempt.Error,
		attempt.StartedAt,
		attempt.FinishedAt,
	)
	return err
}

// UpdateExecutionStatus updates the status of an execution.
// Returns dispatcher.ErrStatusTransitionDenied if the execution is already in a terminal state.
// This uses an atomic UPDATE with WHERE clause to prevent TOCTOU race conditions.
func (s *Store) UpdateExecutionStatus(ctx context.Context, executionID uuid.UUID, status domain.ExecutionStatus) error {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	// Single atomic update with guard in WHERE clause.
	// PostgreSQL acquires row lock before evaluating WHERE,
	// ensuring serialized access under concurrency.
	result, err := s.db.ExecContext(ctx, queryUpdateExecutionStatus, string(status), executionID)
	if err != nil {
		return err
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}

	if rowsAffected == 0 {
		// Either: (a) execution not found, or (b) already in terminal state.
		// Distinguish by checking if the row exists.
		var currentStatus string
		err := s.db.QueryRowContext(ctx, queryGetExecutionStatus, executionID).Scan(&currentStatus)
		if err == sql.ErrNoRows {
			return sql.ErrNoRows
		}
		if err != nil {
			return err
		}
		// Row exists but wasn't updated => terminal state
		return dispatcher.ErrStatusTransitionDenied
	}

	return nil
}

// isDuplicateKeyError checks if the error is a PostgreSQL unique violation.
func isDuplicateKeyError(err error) bool {
	if err == nil {
		return false
	}
	// PostgreSQL unique violation error code is 23505
	// Check error message for common patterns from both lib/pq and pgx
	errStr := err.Error()
	return contains(errStr, "23505") || contains(errStr, "unique constraint") || contains(errStr, "duplicate key")
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// CreateJob creates a new job with its schedule in a transaction.
func (s *Store) CreateJob(ctx context.Context, job domain.Job, schedule domain.Schedule) error {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx, queryInsertSchedule,
		schedule.ID,
		schedule.CronExpression,
		schedule.Timezone,
		schedule.CreatedAt,
		schedule.UpdatedAt,
	)
	if err != nil {
		return err
	}

	_, err = tx.ExecContext(ctx, queryInsertJob,
		job.ID,
		string(job.Namespace),
		job.Name,
		job.Enabled,
		job.ScheduleID,
		string(job.Delivery.Type),
		job.Delivery.WebhookURL,
		job.Delivery.Secret,
		job.Delivery.Timeout.Milliseconds(),
		job.Analytics.Enabled,
		job.Analytics.RetentionSeconds,
		job.CreatedAt,
		job.UpdatedAt,
	)
	if err != nil {
		return err
	}

	return tx.Commit()
}

// ListJobs returns jobs for a namespace with their schedules, paginated by limit and offset.
func (s *Store) ListJobs(ctx context.Context, ns domain.Namespace, limit, offset int) ([]domain.JobWithSchedule, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	rows, err := s.db.QueryContext(ctx, queryListJobs, string(ns), limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []domain.JobWithSchedule
	for rows.Next() {
		var jws domain.JobWithSchedule
		var timeoutMs int64

		err := rows.Scan(
			&jws.Job.ID,
			&jws.Job.Namespace,
			&jws.Job.Name,
			&jws.Job.Enabled,
			&jws.Job.ScheduleID,
			&jws.Job.Delivery.Type,
			&jws.Job.Delivery.WebhookURL,
			&jws.Job.Delivery.Secret,
			&timeoutMs,
			&jws.Job.Analytics.Enabled,
			&jws.Job.Analytics.RetentionSeconds,
			&jws.Job.CreatedAt,
			&jws.Job.UpdatedAt,
			&jws.Schedule.ID,
			&jws.Schedule.CronExpression,
			&jws.Schedule.Timezone,
			&jws.Schedule.CreatedAt,
			&jws.Schedule.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		jws.Job.Delivery.Timeout = time.Duration(timeoutMs) * time.Millisecond
		result = append(result, jws)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return result, nil
}

// ListExecutions returns executions for a job, paginated by limit and offset.
func (s *Store) ListExecutions(ctx context.Context, jobID uuid.UUID, limit, offset int) ([]domain.Execution, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	rows, err := s.db.QueryContext(ctx, queryListExecutions, jobID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []domain.Execution
	for rows.Next() {
		var exec domain.Execution
		var status string

		err := rows.Scan(
			&exec.ID,
			&exec.JobID,
			&exec.Namespace,
			&exec.ScheduledAt,
			&exec.FiredAt,
			&status,
			&exec.CreatedAt,
		)
		if err != nil {
			return nil, err
		}
		exec.Status = domain.ExecutionStatus(status)
		result = append(result, exec)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return result, nil
}

func (s *Store) DeleteJob(ctx context.Context, jobID uuid.UUID, ns domain.Namespace) error {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	var deletedID uuid.UUID
	err := s.db.QueryRowContext(ctx, queryDeleteJob, jobID, string(ns)).Scan(&deletedID)
	if err != nil {
		if err == sql.ErrNoRows {
			return sql.ErrNoRows
		}
		return err
	}
	return nil
}

// GetOrphanedExecutions returns executions that are stuck in 'emitted' status
// and were created before the given threshold time.
// Results are ordered by created_at ASC (oldest first) and limited to maxResults.
func (s *Store) GetOrphanedExecutions(ctx context.Context, olderThan time.Time, maxResults int) ([]domain.Execution, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	rows, err := s.db.QueryContext(ctx, queryGetOrphanedExecutions, olderThan, maxResults)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []domain.Execution
	for rows.Next() {
		var exec domain.Execution
		var status string

		err := rows.Scan(
			&exec.ID,
			&exec.JobID,
			&exec.Namespace,
			&exec.ScheduledAt,
			&exec.FiredAt,
			&status,
			&exec.CreatedAt,
		)
		if err != nil {
			return nil, err
		}
		exec.Status = domain.ExecutionStatus(status)
		result = append(result, exec)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return result, nil
}

// DequeueExecution atomically claims one emitted execution by transitioning it
// to in_progress with a claimed_at timestamp. Returns nil, nil if no work available.
// Uses SELECT FOR UPDATE SKIP LOCKED to prevent double-claim under concurrency.
func (s *Store) DequeueExecution(ctx context.Context) (*domain.Execution, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var exec domain.Execution
	var status string

	err = tx.QueryRowContext(ctx, queryDequeueExecution).Scan(
		&exec.ID,
		&exec.JobID,
		&exec.Namespace,
		&exec.ScheduledAt,
		&exec.FiredAt,
		&status,
		&exec.CreatedAt,
	)
	if err == sql.ErrNoRows {
		// No work available — commit to release any advisory locks and return nil
		tx.Commit()
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	exec.Status = domain.ExecutionStatus(status)

	// Atomically transition to in_progress with claim timestamp
	_, err = tx.ExecContext(ctx, queryClaimExecution, exec.ID)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	exec.Status = domain.ExecutionStatusInProgress
	return &exec, nil
}

// RequeueStaleExecutions resets in_progress executions older than olderThan back
// to emitted status. Uses a CTE with FOR UPDATE SKIP LOCKED to avoid interfering
// with active DequeueExecution transactions.
func (s *Store) RequeueStaleExecutions(ctx context.Context, olderThan time.Time, limit int) (int, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	result, err := s.db.ExecContext(ctx, queryRequeueStaleExecutions, olderThan, limit)
	if err != nil {
		return 0, err
	}

	n, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}

	return int(n), nil
}

// Compile-time interface assertions
var (
	_ scheduler.Store  = (*Store)(nil)
	_ dispatcher.Store = (*Store)(nil)
	_ api.Store        = (*Store)(nil)
)
