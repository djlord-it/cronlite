package main

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/lib/pq"
)

const chaosExecutionsQuery = `
SELECT
	id::text,
	job_id::text,
	scheduled_at,
	created_at,
	status,
	lease_until
FROM executions
WHERE job_id = ANY($1::uuid[])
ORDER BY job_id, scheduled_at
`

const chaosQueueDepthQuery = `
SELECT count(*)
FROM executions
WHERE job_id = ANY($1::uuid[])
  AND status IN ('emitted', 'in_progress')
`

const chaosStuckQuery = `
SELECT id::text
FROM executions
WHERE job_id = ANY($1::uuid[])
  AND (
	(status = 'in_progress' AND lease_until < clock_timestamp() - $2 * INTERVAL '1 millisecond')
	OR (status IN ('emitted', 'in_progress') AND created_at < clock_timestamp() - $3 * INTERVAL '1 millisecond')
  )
ORDER BY created_at
`

type ChaosExecutionRow struct {
	ID          string     `json:"id"`
	JobID       string     `json:"job_id"`
	ScheduledAt time.Time  `json:"scheduled_at"`
	CreatedAt   time.Time  `json:"created_at"`
	Status      string     `json:"status"`
	LeaseUntil  *time.Time `json:"lease_until,omitempty"`
}

type chaosDiagnosticReader interface {
	chaosExecutions(ctx context.Context, jobIDs []string) ([]ChaosExecutionRow, error)
	chaosQueueDepth(ctx context.Context, jobIDs []string) (int, error)
	chaosStuck(ctx context.Context, jobIDs []string, leaseGrace, ageLimit time.Duration) ([]string, error)
}

var _ chaosDiagnosticReader = (*diagnosticCollector)(nil)

func (c *diagnosticCollector) chaosExecutions(ctx context.Context, jobIDs []string) ([]ChaosExecutionRow, error) {
	if len(jobIDs) == 0 {
		return nil, nil
	}
	var result []ChaosExecutionRow
	err := c.readOnly(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, chaosExecutionsQuery, pq.Array(jobIDs))
		if err != nil {
			return fmt.Errorf("query chaos executions: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var row ChaosExecutionRow
			if err := rows.Scan(&row.ID, &row.JobID, &row.ScheduledAt, &row.CreatedAt, &row.Status, &row.LeaseUntil); err != nil {
				return fmt.Errorf("scan chaos execution: %w", err)
			}
			result = append(result, row)
		}
		return rows.Err()
	})
	return result, err
}

func (c *diagnosticCollector) chaosQueueDepth(ctx context.Context, jobIDs []string) (int, error) {
	if len(jobIDs) == 0 {
		return 0, nil
	}
	var depth int
	err := c.readOnly(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, chaosQueueDepthQuery, pq.Array(jobIDs)).Scan(&depth); err != nil {
			return fmt.Errorf("query chaos queue depth: %w", err)
		}
		return nil
	})
	return depth, err
}

func (c *diagnosticCollector) chaosStuck(
	ctx context.Context,
	jobIDs []string,
	leaseGrace time.Duration,
	ageLimit time.Duration,
) ([]string, error) {
	if len(jobIDs) == 0 {
		return nil, nil
	}
	var ids []string
	err := c.readOnly(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, chaosStuckQuery, pq.Array(jobIDs), leaseGrace.Milliseconds(), ageLimit.Milliseconds())
		if err != nil {
			return fmt.Errorf("query chaos stuck executions: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return fmt.Errorf("scan chaos stuck execution: %w", err)
			}
			ids = append(ids, id)
		}
		return rows.Err()
	})
	return ids, err
}

// readOnly runs fn in a read-only transaction, matching execution's pattern.
func (c *diagnosticCollector) readOnly(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin read-only diagnostic transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "SET TRANSACTION READ ONLY"); err != nil {
		return fmt.Errorf("set diagnostic transaction read-only: %w", err)
	}
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit diagnostic transaction: %w", err)
	}
	return nil
}
