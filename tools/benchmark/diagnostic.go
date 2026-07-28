package main

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/lib/pq"
)

const diagnosticExecutionQuery = `
SELECT
	e.id::text,
	e.job_id::text,
	e.scheduled_at,
	e.fired_at,
	e.created_at,
	e.claimed_at,
	e.status
FROM executions e
WHERE e.id::text = $1
`

const diagnosticAttemptsQuery = `
SELECT
	id::text,
	attempt,
	status_code,
	error,
	started_at,
	finished_at
FROM delivery_attempts
WHERE execution_id::text = $1
ORDER BY attempt, started_at
`

type diagnosticCollector struct {
	db *sql.DB
}

func newDiagnosticCollector(db *sql.DB) *diagnosticCollector {
	return &diagnosticCollector{db: db}
}

func openDiagnosticCollector(databaseURL string) (*diagnosticCollector, error) {
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open diagnostic database: %w", err)
	}
	return newDiagnosticCollector(db), nil
}

func (c *diagnosticCollector) Close() error {
	return c.db.Close()
}

func (c *diagnosticCollector) ping(ctx context.Context) (Observation, error) {
	started := time.Now()
	err := c.db.PingContext(ctx)
	finished := time.Now()
	observation := Observation{
		Kind:       "database",
		Name:       "postgres_ping",
		StartedAt:  started.UTC(),
		FinishedAt: finished.UTC(),
		Duration:   finished.Sub(started),
		Provenance: ProvenanceDatabase,
	}
	if err != nil {
		observation.Error = err.Error()
		observation.ErrorClass = "database_error"
	}
	return observation, err
}

func (c *diagnosticCollector) execution(
	ctx context.Context,
	executionID string,
) (DiagnosticExecution, error) {
	var result DiagnosticExecution
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return result, fmt.Errorf("begin read-only diagnostic transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, "SET TRANSACTION READ ONLY"); err != nil {
		return result, fmt.Errorf("set diagnostic transaction read-only: %w", err)
	}

	err = tx.QueryRowContext(ctx, diagnosticExecutionQuery, executionID).Scan(
		&result.ExecutionID,
		&result.JobID,
		&result.ScheduledAt,
		&result.FiredAt,
		&result.CreatedAt,
		&result.ClaimedAt,
		&result.Status,
	)
	if err != nil {
		return result, fmt.Errorf("query diagnostic execution: %w", err)
	}
	result.ObservedAt = time.Now().UTC()

	rows, err := tx.QueryContext(ctx, diagnosticAttemptsQuery, executionID)
	if err != nil {
		return result, fmt.Errorf("query diagnostic attempts: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var attempt DiagnosticAttempt
		if err := rows.Scan(
			&attempt.ID,
			&attempt.Attempt,
			&attempt.StatusCode,
			&attempt.Error,
			&attempt.StartedAt,
			&attempt.FinishedAt,
		); err != nil {
			return result, fmt.Errorf("scan diagnostic attempt: %w", err)
		}
		attempt.QueryTime = result.ObservedAt
		result.Attempts = append(result.Attempts, attempt)
	}
	if err := rows.Err(); err != nil {
		return result, fmt.Errorf("iterate diagnostic attempts: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return result, fmt.Errorf("commit diagnostic transaction: %w", err)
	}
	return result, nil
}

type DatabaseResources struct {
	ObservedAt      time.Time `json:"observed_at"`
	ConnectionCount int       `json:"connection_count"`
	DatabaseSize    int64     `json:"database_size_bytes"`
	QueueDepth      int       `json:"queue_depth"`
	InProgress      int       `json:"in_progress"`
	OldestQueueAge  *float64  `json:"oldest_queue_age_ms,omitempty"`
}

func (c *diagnosticCollector) resources(ctx context.Context) (DatabaseResources, error) {
	var result DatabaseResources
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "SET TRANSACTION READ ONLY"); err != nil {
		return result, err
	}
	err = tx.QueryRowContext(ctx, `
SELECT
	(SELECT count(*) FROM pg_stat_activity WHERE datname = current_database()),
	pg_database_size(current_database()),
	(SELECT count(*) FROM executions WHERE status = 'emitted'),
	(SELECT count(*) FROM executions WHERE status = 'in_progress'),
	(SELECT EXTRACT(EPOCH FROM (NOW() - MIN(created_at))) * 1000
	 FROM executions WHERE status = 'emitted')
`).Scan(
		&result.ConnectionCount,
		&result.DatabaseSize,
		&result.QueueDepth,
		&result.InProgress,
		&result.OldestQueueAge,
	)
	if err != nil {
		return result, err
	}
	result.ObservedAt = time.Now().UTC()
	if err := tx.Commit(); err != nil {
		return result, err
	}
	return result, nil
}
