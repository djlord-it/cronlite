package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"

	"github.com/djlord-it/cronlite/internal/dispatcher"
	"github.com/djlord-it/cronlite/internal/domain"
	"github.com/djlord-it/cronlite/internal/scheduler"
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

// ── Job Repository ────────────────────────────────────────────────────────────

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
		jws, err := scanJobWithScheduleRow(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, jws)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return result, nil
}

// InsertJob creates a new job with its schedule in a transaction.
// Alias for CreateJob to satisfy the domain.JobRepository interface.
func (s *Store) InsertJob(ctx context.Context, job domain.Job, schedule domain.Schedule) error {
	return s.CreateJob(ctx, job, schedule)
}

// CreateJob creates a new job with its schedule in a transaction.
func (s *Store) CreateJob(ctx context.Context, job domain.Job, schedule domain.Schedule) error {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()

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

// GetJob returns a job by its ID.
func (s *Store) GetJob(ctx context.Context, id uuid.UUID) (domain.Job, error) {
	return s.GetJobByID(ctx, id)
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
		if err == sql.ErrNoRows {
			return domain.Job{}, domain.ErrJobNotFound
		}
		return domain.Job{}, err
	}
	job.Delivery.Timeout = time.Duration(timeoutMs) * time.Millisecond
	return job, nil
}

// GetJobWithSchedule returns a job and its schedule by job ID.
func (s *Store) GetJobWithSchedule(ctx context.Context, id uuid.UUID) (domain.Job, domain.Schedule, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	var job domain.Job
	var sched domain.Schedule
	var timeoutMs int64

	err := s.db.QueryRowContext(ctx, queryGetJobWithSchedule, id).Scan(
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
		&sched.ID,
		&sched.CronExpression,
		&sched.Timezone,
		&sched.CreatedAt,
		&sched.UpdatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return domain.Job{}, domain.Schedule{}, domain.ErrJobNotFound
		}
		return domain.Job{}, domain.Schedule{}, err
	}
	job.Delivery.Timeout = time.Duration(timeoutMs) * time.Millisecond
	return job, sched, nil
}

// GetJobWithScheduleScoped returns a job and its schedule filtered by both ID and namespace.
// This provides defense-in-depth for API-facing operations.
func (s *Store) GetJobWithScheduleScoped(ctx context.Context, id uuid.UUID, ns domain.Namespace) (domain.Job, domain.Schedule, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	var job domain.Job
	var sched domain.Schedule
	var timeoutMs int64

	err := s.db.QueryRowContext(ctx, queryGetJobWithScheduleScoped, id, string(ns)).Scan(
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
		&sched.ID,
		&sched.CronExpression,
		&sched.Timezone,
		&sched.CreatedAt,
		&sched.UpdatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return domain.Job{}, domain.Schedule{}, domain.ErrJobNotFound
		}
		return domain.Job{}, domain.Schedule{}, err
	}
	job.Delivery.Timeout = time.Duration(timeoutMs) * time.Millisecond
	return job, sched, nil
}

// ListJobs returns jobs matching the filter. It supports the full JobFilter
// including namespace, tags, enabled, and name substring filtering.
// This method satisfies the domain.JobRepository interface.
func (s *Store) ListJobs(ctx context.Context, filter domain.JobFilter) ([]domain.Job, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	filter.ListParams = filter.ListParams.WithDefaults()

	// Build dynamic WHERE clause
	var conditions []string
	var args []interface{}
	argIdx := 1

	conditions = append(conditions, fmt.Sprintf("j.namespace = $%d", argIdx))
	args = append(args, string(filter.Namespace))
	argIdx++

	if filter.Enabled != nil {
		conditions = append(conditions, fmt.Sprintf("j.enabled = $%d", argIdx))
		args = append(args, *filter.Enabled)
		argIdx++
	}

	if filter.Name != "" {
		conditions = append(conditions, fmt.Sprintf("j.name ILIKE $%d", argIdx))
		args = append(args, "%"+filter.Name+"%")
		argIdx++
	}

	// Tag filters: each tag adds an EXISTS subquery
	for _, tag := range filter.Tags {
		conditions = append(conditions, fmt.Sprintf(
			"EXISTS (SELECT 1 FROM tags t WHERE t.job_id = j.id AND t.key = $%d AND t.value = $%d)",
			argIdx, argIdx+1,
		))
		args = append(args, tag.Key, tag.Value)
		argIdx += 2
	}

	where := strings.Join(conditions, " AND ")

	query := fmt.Sprintf(`
SELECT
    j.id, j.namespace, j.name, j.enabled, j.schedule_id,
    j.delivery_type, j.webhook_url, j.secret, j.timeout_ms,
    j.analytics_enabled, j.analytics_retention_seconds,
    j.created_at, j.updated_at
FROM jobs j
WHERE %s
ORDER BY j.created_at DESC
LIMIT $%d OFFSET $%d
`, where, argIdx, argIdx+1)

	args = append(args, filter.Limit, filter.Offset)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []domain.Job
	for rows.Next() {
		job, err := scanJobRow(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, job)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return result, nil
}

// UpdateJob updates a job by id and namespace.
func (s *Store) UpdateJob(ctx context.Context, job domain.Job) error {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	result, err := s.db.ExecContext(ctx, queryUpdateJob,
		job.Name,
		job.Enabled,
		string(job.Delivery.Type),
		job.Delivery.WebhookURL,
		job.Delivery.Secret,
		job.Delivery.Timeout.Milliseconds(),
		job.Analytics.Enabled,
		job.Analytics.RetentionSeconds,
		job.UpdatedAt,
		job.ID,
		string(job.Namespace),
	)
	if err != nil {
		return err
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return domain.ErrJobNotFound
	}
	return nil
}

// DeleteJob cascade-deletes a job by id and namespace.
func (s *Store) DeleteJob(ctx context.Context, jobID uuid.UUID, ns domain.Namespace) error {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	var deletedID uuid.UUID
	err := s.db.QueryRowContext(ctx, queryDeleteJob, jobID, string(ns)).Scan(&deletedID)
	if err != nil {
		if err == sql.ErrNoRows {
			return domain.ErrJobNotFound
		}
		return err
	}
	return nil
}

// ── Schedule Repository ───────────────────────────────────────────────────────

// InsertSchedule inserts a new schedule.
func (s *Store) InsertSchedule(ctx context.Context, schedule domain.Schedule) error {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	_, err := s.db.ExecContext(ctx, queryInsertSchedule,
		schedule.ID,
		schedule.CronExpression,
		schedule.Timezone,
		schedule.CreatedAt,
		schedule.UpdatedAt,
	)
	return err
}

// GetSchedule returns a schedule by its ID.
func (s *Store) GetSchedule(ctx context.Context, id uuid.UUID) (domain.Schedule, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	var sched domain.Schedule
	err := s.db.QueryRowContext(ctx, queryGetSchedule, id).Scan(
		&sched.ID,
		&sched.CronExpression,
		&sched.Timezone,
		&sched.CreatedAt,
		&sched.UpdatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return domain.Schedule{}, sql.ErrNoRows
		}
		return domain.Schedule{}, err
	}
	return sched, nil
}

// UpdateSchedule updates an existing schedule.
func (s *Store) UpdateSchedule(ctx context.Context, schedule domain.Schedule) error {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	_, err := s.db.ExecContext(ctx, queryUpdateSchedule,
		schedule.CronExpression,
		schedule.Timezone,
		schedule.UpdatedAt,
		schedule.ID,
	)
	return err
}

// ── Execution Repository ──────────────────────────────────────────────────────

// InsertExecution inserts a new execution record.
// Returns scheduler.ErrDuplicateExecution if (job_id, scheduled_at) already exists.
func (s *Store) InsertExecution(ctx context.Context, exec domain.Execution) error {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	_, err := s.db.ExecContext(ctx, queryInsertExecution,
		exec.ID,
		exec.JobID,
		string(exec.Namespace),
		string(exec.TriggerType),
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

// GetExecution returns an execution by its ID.
func (s *Store) GetExecution(ctx context.Context, id uuid.UUID) (domain.Execution, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	exec, err := scanSingleExecution(s.db.QueryRowContext(ctx, queryGetExecution, id))
	if err != nil {
		if err == sql.ErrNoRows {
			return domain.Execution{}, domain.ErrExecutionNotFound
		}
		return domain.Execution{}, err
	}
	return exec, nil
}

// GetRecentExecutions returns the most recent executions for a job.
func (s *Store) GetRecentExecutions(ctx context.Context, jobID uuid.UUID, limit int) ([]domain.Execution, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	rows, err := s.db.QueryContext(ctx, queryGetRecentExecutions, jobID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanExecutionRows(rows)
}

// ListExecutions returns executions matching the filter with dynamic WHERE.
// This method satisfies the domain.ExecutionRepository interface.
func (s *Store) ListExecutions(ctx context.Context, filter domain.ExecutionFilter) ([]domain.Execution, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	filter.ListParams = filter.ListParams.WithDefaults()

	var conditions []string
	var args []interface{}
	argIdx := 1

	if filter.JobID != uuid.Nil {
		conditions = append(conditions, fmt.Sprintf("job_id = $%d", argIdx))
		args = append(args, filter.JobID)
		argIdx++
	}

	if !filter.Namespace.IsZero() {
		conditions = append(conditions, fmt.Sprintf("namespace = $%d", argIdx))
		args = append(args, string(filter.Namespace))
		argIdx++
	}

	if filter.Status != nil {
		conditions = append(conditions, fmt.Sprintf("status = $%d", argIdx))
		args = append(args, string(*filter.Status))
		argIdx++
	}

	if filter.TriggerType != nil {
		conditions = append(conditions, fmt.Sprintf("trigger_type = $%d", argIdx))
		args = append(args, *filter.TriggerType)
		argIdx++
	}

	if filter.Since != nil {
		conditions = append(conditions, fmt.Sprintf("created_at >= $%d", argIdx))
		args = append(args, *filter.Since)
		argIdx++
	}

	if filter.Until != nil {
		conditions = append(conditions, fmt.Sprintf("created_at <= $%d", argIdx))
		args = append(args, *filter.Until)
		argIdx++
	}

	where := "TRUE"
	if len(conditions) > 0 {
		where = strings.Join(conditions, " AND ")
	}

	query := fmt.Sprintf(`
SELECT id, job_id, namespace, trigger_type, scheduled_at, fired_at, status, acknowledged_at, created_at
FROM executions
WHERE %s
ORDER BY created_at DESC
LIMIT $%d OFFSET $%d
`, where, argIdx, argIdx+1)

	args = append(args, filter.Limit, filter.Offset)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanExecutionRows(rows)
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
	defer func() {
		_ = tx.Rollback()
	}()

	exec, err := scanSingleExecution(tx.QueryRowContext(ctx, queryDequeueExecution))
	if err == sql.ErrNoRows {
		// No work available -- commit to release any advisory locks and return nil
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

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

	return scanExecutionRows(rows)
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

// ── Tag Repository ────────────────────────────────────────────────────────────

// UpsertTags upserts tags for a job in a transaction.
func (s *Store) UpsertTags(ctx context.Context, jobID uuid.UUID, tags []domain.Tag) error {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	for _, tag := range tags {
		_, err := tx.ExecContext(ctx, queryUpsertTag, jobID, tag.Key, tag.Value)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

// GetTags returns all tags for a job.
func (s *Store) GetTags(ctx context.Context, jobID uuid.UUID) ([]domain.Tag, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	rows, err := s.db.QueryContext(ctx, queryGetTags, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tags []domain.Tag
	for rows.Next() {
		var tag domain.Tag
		if err := rows.Scan(&tag.Key, &tag.Value); err != nil {
			return nil, err
		}
		tags = append(tags, tag)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return tags, nil
}

// DeleteTags deletes all tags for a job.
func (s *Store) DeleteTags(ctx context.Context, jobID uuid.UUID) error {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	_, err := s.db.ExecContext(ctx, queryDeleteTags, jobID)
	return err
}

// ── API Key Repository ────────────────────────────────────────────────────────

// InsertAPIKey inserts a new API key.
func (s *Store) InsertAPIKey(ctx context.Context, key domain.APIKey) error {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	_, err := s.db.ExecContext(ctx, queryInsertAPIKey,
		key.ID,
		string(key.Namespace),
		key.TokenHash,
		key.Label,
		key.Enabled,
		key.CreatedAt,
	)
	if err != nil {
		if isDuplicateKeyError(err) {
			return domain.ErrDuplicateAPIKey
		}
		return err
	}
	return nil
}

// GetKeyByTokenHash returns an enabled API key by its token hash.
func (s *Store) GetKeyByTokenHash(ctx context.Context, tokenHash string) (domain.APIKey, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	var key domain.APIKey
	err := s.db.QueryRowContext(ctx, queryGetKeyByTokenHash, tokenHash).Scan(
		&key.ID,
		&key.Namespace,
		&key.TokenHash,
		&key.Label,
		&key.Enabled,
		&key.CreatedAt,
		&key.LastUsedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return domain.APIKey{}, domain.ErrAPIKeyNotFound
		}
		return domain.APIKey{}, err
	}
	return key, nil
}

// ListKeys returns API keys for a namespace, paginated.
func (s *Store) ListKeys(ctx context.Context, ns domain.Namespace, params domain.ListParams) ([]domain.APIKey, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	params = params.WithDefaults()

	rows, err := s.db.QueryContext(ctx, queryListKeys, string(ns), params.Limit, params.Offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var keys []domain.APIKey
	for rows.Next() {
		var key domain.APIKey
		err := rows.Scan(
			&key.ID,
			&key.Namespace,
			&key.TokenHash,
			&key.Label,
			&key.Enabled,
			&key.CreatedAt,
			&key.LastUsedAt,
		)
		if err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return keys, nil
}

// DeleteKey deletes an API key by id and namespace.
func (s *Store) DeleteKey(ctx context.Context, id uuid.UUID, ns domain.Namespace) error {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	result, err := s.db.ExecContext(ctx, queryDeleteKey, id, string(ns))
	if err != nil {
		return err
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return domain.ErrAPIKeyNotFound
	}
	return nil
}

// UpdateLastUsedAt updates last_used_at for a batch of API key IDs.
func (s *Store) UpdateLastUsedAt(ctx context.Context, ids []uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}

	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	// Convert uuid slice to string slice for pq.Array
	strs := make([]string, len(ids))
	for i, id := range ids {
		strs[i] = id.String()
	}

	_, err := s.db.ExecContext(ctx, queryUpdateLastUsedAt, pq.Array(strs))
	return err
}

// ── Delivery Attempt Repository ───────────────────────────────────────────────

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

// GetAttempts returns all delivery attempts for an execution.
func (s *Store) GetAttempts(ctx context.Context, executionID uuid.UUID) ([]domain.DeliveryAttempt, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	rows, err := s.db.QueryContext(ctx, queryGetDeliveryAttempts, executionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var attempts []domain.DeliveryAttempt
	for rows.Next() {
		var a domain.DeliveryAttempt
		err := rows.Scan(
			&a.ID,
			&a.ExecutionID,
			&a.Attempt,
			&a.StatusCode,
			&a.Error,
			&a.StartedAt,
			&a.FinishedAt,
		)
		if err != nil {
			return nil, err
		}
		attempts = append(attempts, a)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return attempts, nil
}

// ── Scan helpers ──────────────────────────────────────────────────────────────

// scanner abstracts *sql.Row and *sql.Rows for scan helpers.
type scanner interface {
	Scan(dest ...interface{}) error
}

// scanJobRow scans a single job row (no schedule join).
func scanJobRow(row scanner) (domain.Job, error) {
	var job domain.Job
	var timeoutMs int64

	err := row.Scan(
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

// scanJobWithScheduleRow scans a joined job+schedule row.
func scanJobWithScheduleRow(row scanner) (domain.JobWithSchedule, error) {
	var jws domain.JobWithSchedule
	var timeoutMs int64

	err := row.Scan(
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
		return domain.JobWithSchedule{}, err
	}
	jws.Job.Delivery.Timeout = time.Duration(timeoutMs) * time.Millisecond
	return jws, nil
}

// scanSingleExecution scans a single execution row (from QueryRow).
func scanSingleExecution(row *sql.Row) (domain.Execution, error) {
	var exec domain.Execution
	var status string
	var triggerType string

	err := row.Scan(
		&exec.ID,
		&exec.JobID,
		&exec.Namespace,
		&triggerType,
		&exec.ScheduledAt,
		&exec.FiredAt,
		&status,
		&exec.AcknowledgedAt,
		&exec.CreatedAt,
	)
	if err != nil {
		return domain.Execution{}, err
	}
	exec.Status = domain.ExecutionStatus(status)
	exec.TriggerType = domain.TriggerType(triggerType)
	return exec, nil
}

// scanExecutionRows scans multiple execution rows.
func scanExecutionRows(rows *sql.Rows) ([]domain.Execution, error) {
	var result []domain.Execution
	for rows.Next() {
		var exec domain.Execution
		var status string
		var triggerType string

		err := rows.Scan(
			&exec.ID,
			&exec.JobID,
			&exec.Namespace,
			&triggerType,
			&exec.ScheduledAt,
			&exec.FiredAt,
			&status,
			&exec.AcknowledgedAt,
			&exec.CreatedAt,
		)
		if err != nil {
			return nil, err
		}
		exec.Status = domain.ExecutionStatus(status)
		exec.TriggerType = domain.TriggerType(triggerType)
		result = append(result, exec)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return result, nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// isDuplicateKeyError checks if the error is a PostgreSQL unique violation (code 23505).
// Primary path: typed assertion on *pq.Error via errors.As (handles wrapped errors).
// Fallback: string matching for non-pq drivers or unusual wrapping.
func isDuplicateKeyError(err error) bool {
	if err == nil {
		return false
	}
	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		return pqErr.Code == "23505"
	}
	// Fallback for wrapped or non-pq errors.
	errStr := err.Error()
	return containsSubstring(errStr, "23505") || containsSubstring(errStr, "unique constraint") || containsSubstring(errStr, "duplicate key")
}

func containsSubstring(s, substr string) bool {
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

// Compile-time interface assertions
var (
	_ scheduler.Store  = (*Store)(nil)
	_ dispatcher.Store = (*Store)(nil)

	_ domain.JobRepository             = (*Store)(nil)
	_ domain.ScheduleRepository        = (*Store)(nil)
	_ domain.ExecutionRepository       = (*Store)(nil)
	_ domain.TagRepository             = (*Store)(nil)
	_ domain.APIKeyRepository          = (*Store)(nil)
	_ domain.DeliveryAttemptRepository = (*Store)(nil)
)
