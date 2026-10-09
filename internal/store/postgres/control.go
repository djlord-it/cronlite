package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/djlord-it/cronlite/internal/domain"
	"github.com/google/uuid"
)

func fleetWhere(f domain.JobFilter) (string, []any) {
	conditions := []string{"j.namespace = $1"}
	args := []any{string(f.Namespace)}
	if f.Enabled != nil {
		args = append(args, *f.Enabled)
		conditions = append(conditions, fmt.Sprintf("j.enabled = $%d", len(args)))
	}
	if f.Name != "" {
		args = append(args, "%"+escapeLike(f.Name)+"%")
		conditions = append(conditions, fmt.Sprintf("j.name ILIKE $%d", len(args)))
	}
	for _, tag := range f.Tags {
		args = append(args, tag.Key, tag.Value)
		conditions = append(conditions, fmt.Sprintf("EXISTS (SELECT 1 FROM tags t WHERE t.job_id=j.id AND t.key=$%d AND t.value=$%d)", len(args)-1, len(args)))
	}
	return strings.Join(conditions, " AND "), args
}
func escapeLike(v string) string {
	return strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(v)
}

// Counts and rows share one read snapshot, including when the requested page is empty.
func (s *Store) Fleet(ctx context.Context, f domain.JobFilter) (domain.Fleet, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	result := domain.Fleet{}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback() }()
	where, args := fleetWhere(f)
	// Evaluate the matching predicate once per namespace job, alongside fleet
	// counts. Avoid a second aggregate scan on every keystroke. The namespace
	// index bounds the scan; only the requested page loads schedule/job details.
	if err := tx.QueryRowContext(ctx, `SELECT count(*), count(*) FILTER (WHERE j.enabled), count(*) FILTER (WHERE NOT j.enabled), count(*) FILTER (WHERE `+where+`) FROM jobs j WHERE j.namespace=$1`, args...).Scan(&result.Total, &result.Active, &result.Paused, &result.Matched); err != nil {
		return result, err
	}
	f.ListParams = f.WithDefaults()
	query := fmt.Sprintf(`SELECT j.id,j.namespace,j.name,j.enabled,j.schedule_id,j.delivery_type,j.webhook_url,j.secret,j.timeout_ms,j.analytics_enabled,j.analytics_retention_seconds,j.created_at,j.updated_at,
 s.id,s.cron_expression,s.timezone,s.created_at,s.updated_at FROM jobs j JOIN schedules s ON s.id=j.schedule_id WHERE %s ORDER BY j.created_at DESC,j.id DESC LIMIT $%d OFFSET $%d`, where, len(args)+1, len(args)+2)
	args = append(args, f.Limit, f.Offset)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		row, scanErr := scanJobWithScheduleRow(rows)
		if scanErr != nil {
			rows.Close()
			return result, scanErr
		}
		result.Jobs = append(result.Jobs, row)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	return result, tx.Commit()
}

func (s *Store) Runs(ctx context.Context, f domain.ExecutionFilter) (domain.RunsPage, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	result := domain.RunsPage{}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback() }()
	conditions := []string{"e.namespace=$1", "j.namespace=$1", "e.created_at >= $2", "e.created_at <= $3"}
	args := []any{string(f.Namespace), f.Since, f.Until}
	if f.Status != nil {
		args = append(args, string(*f.Status))
		conditions = append(conditions, fmt.Sprintf("e.status=$%d", len(args)))
	}
	if f.TriggerType != nil {
		args = append(args, *f.TriggerType)
		conditions = append(conditions, fmt.Sprintf("e.trigger_type=$%d", len(args)))
	}
	if f.JobID != uuid.Nil {
		args = append(args, f.JobID)
		conditions = append(conditions, fmt.Sprintf("e.job_id=$%d", len(args)))
	}
	where := strings.Join(conditions, " AND ")
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM executions e JOIN jobs j ON j.id=e.job_id WHERE `+where, args...).Scan(&result.Total); err != nil {
		return result, err
	}
	f.ListParams = f.WithDefaults()
	// Page before lateral evidence queries: at most 26 execution IDs in the UI.
	query := fmt.Sprintf(`WITH page AS (SELECT e.*,j.name FROM executions e JOIN jobs j ON j.id=e.job_id WHERE %s ORDER BY e.created_at DESC,e.id DESC LIMIT $%d OFFSET $%d)
 SELECT p.id,p.job_id,p.namespace,p.trigger_type,p.scheduled_at,p.fired_at,p.status,p.acknowledged_at,p.created_at,p.claimed_at,p.name,
 (SELECT count(*) FROM delivery_attempts a WHERE a.execution_id=p.id),coalesce(a.status_code,0),coalesce(a.error,''),a.finished_at
 FROM page p LEFT JOIN LATERAL (SELECT status_code,error,finished_at FROM delivery_attempts WHERE execution_id=p.id ORDER BY finished_at DESC,id DESC LIMIT 1) a ON true ORDER BY p.created_at DESC,p.id DESC`, where, len(args)+1, len(args)+2)
	args = append(args, f.Limit, f.Offset)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var row domain.RunEvidence
		var finished sql.NullTime
		e := &row.Execution
		if err := rows.Scan(&e.ID, &e.JobID, &e.Namespace, &e.TriggerType, &e.ScheduledAt, &e.FiredAt, &e.Status, &e.AcknowledgedAt, &e.CreatedAt, &e.ClaimedAt, &row.JobName, &row.Attempts, &row.LastStatusCode, &row.LastError, &finished); err != nil {
			rows.Close()
			return result, err
		}
		if finished.Valid {
			row.LastFinishedAt = finished.Time
		}
		result.Runs = append(result.Runs, row)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	return result, tx.Commit()
}

func authorizedActor(ctx context.Context, tx *sql.Tx, ns domain.Namespace, actor uuid.UUID) error {
	var id uuid.UUID
	// Lock against revocation/deletion while a mutation is committing.
	err := tx.QueryRowContext(ctx, `SELECT id FROM api_keys WHERE id=$1 AND namespace=$2 AND enabled FOR SHARE`, actor, string(ns)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ErrAPIKeyNotFound
	}
	return err
}
func (s *Store) ListSavedViews(ctx context.Context, ns domain.Namespace, actor uuid.UUID) ([]domain.SavedView, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `SELECT v.id,v.name,v.query,v.created_at FROM admin_saved_views v JOIN api_keys k ON k.id=v.api_key_id AND k.namespace=v.namespace AND k.enabled WHERE v.api_key_id=$1 AND v.namespace=$2 ORDER BY v.name LIMIT 20`, actor, string(ns))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.SavedView
	for rows.Next() {
		var v domain.SavedView
		if err := rows.Scan(&v.ID, &v.Name, &v.Query, &v.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, v)
	}
	return result, rows.Err()
}
func (s *Store) SaveView(ctx context.Context, ns domain.Namespace, actor uuid.UUID, name, query string) error {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := authorizedActor(ctx, tx, ns, actor); err != nil {
		return err
	}
	// Serialize quota checks for this API key across server instances.
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 10))`, actor.String()); err != nil {
		return err
	}
	var count int
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT count(*),coalesce(bool_or(name=$3),false) FROM admin_saved_views WHERE api_key_id=$1 AND namespace=$2`, actor, string(ns), name).Scan(&count, &exists); err != nil {
		return err
	}
	if count >= 20 && !exists {
		return domain.ErrSavedViewLimit
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO admin_saved_views(id,api_key_id,namespace,name,query) VALUES($1,$2,$3,$4,$5) ON CONFLICT(api_key_id,namespace,name) DO UPDATE SET query=excluded.query`, uuid.New(), actor, string(ns), name, query)
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) DeleteView(ctx context.Context, ns domain.Namespace, actor, id uuid.UUID) error {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	result, err := s.db.ExecContext(ctx, `DELETE FROM admin_saved_views v USING api_keys k WHERE v.id=$1 AND v.api_key_id=$2 AND v.namespace=$3 AND k.id=v.api_key_id AND k.namespace=v.namespace AND k.enabled`, id, actor, string(ns))
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return domain.ErrBulkNotFound
	}
	return nil
}

func (s *Store) PreviewBulk(ctx context.Context, ns domain.Namespace, actor uuid.UUID, action string, ids []uuid.UUID) (domain.BulkBatch, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	batch := domain.BulkBatch{ID: uuid.New(), Action: action}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return batch, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := authorizedActor(ctx, tx, ns, actor); err != nil {
		return batch, err
	}
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 11))`, actor.String()); err != nil {
		return batch, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM admin_bulk_batches WHERE api_key_id=$1 AND namespace=$2 AND confirmed_at IS NULL AND expires_at<=now()`, actor, string(ns)); err != nil {
		return batch, err
	}
	var pending int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM admin_bulk_batches WHERE api_key_id=$1 AND namespace=$2 AND confirmed_at IS NULL`, actor, string(ns)).Scan(&pending); err != nil {
		return batch, err
	}
	if pending >= 20 {
		return batch, domain.ErrInvalidControlInput
	}
	// Sort lock order to avoid deadlocks between overlapping selections.
	sorted := append([]uuid.UUID(nil), ids...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].String() < sorted[j].String() })
	for _, id := range sorted {
		item := domain.BulkItem{ID: id}
		err = tx.QueryRowContext(ctx, `SELECT name,enabled,updated_at FROM jobs WHERE id=$1 AND namespace=$2`, id, string(ns)).Scan(&item.Name, &item.Enabled, &item.UpdatedAt)
		if errors.Is(err, sql.ErrNoRows) {
			return batch, domain.ErrJobNotFound
		}
		if err != nil {
			return batch, err
		}
		batch.Items = append(batch.Items, item)
	}
	raw, err := json.Marshal(batch.Items)
	if err != nil {
		return batch, err
	}
	err = tx.QueryRowContext(ctx, `INSERT INTO admin_bulk_batches(id,api_key_id,namespace,action,items,expires_at) VALUES($1,$2,$3,$4,$5,now()+interval '10 minutes') RETURNING created_at,expires_at`, batch.ID, actor, string(ns), action, string(raw)).Scan(&batch.CreatedAt, &batch.ExpiresAt)
	if err != nil {
		return batch, err
	}
	return batch, tx.Commit()
}
func scanBulk(row interface{ Scan(...any) error }) (domain.BulkBatch, error) {
	var batch domain.BulkBatch
	var raw []byte
	err := row.Scan(&batch.ID, &batch.Action, &raw, &batch.CreatedAt, &batch.ExpiresAt, &batch.ConfirmedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return batch, domain.ErrBulkNotFound
	}
	if err != nil {
		return batch, err
	}
	err = json.Unmarshal(raw, &batch.Items)
	return batch, err
}

const bulkColumns = `b.id,b.action,b.items,b.created_at,b.expires_at,b.confirmed_at`

func (s *Store) GetBulk(ctx context.Context, ns domain.Namespace, actor, id uuid.UUID) (domain.BulkBatch, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	return scanBulk(s.db.QueryRowContext(ctx, `SELECT `+bulkColumns+` FROM admin_bulk_batches b JOIN api_keys k ON k.id=b.api_key_id AND k.namespace=b.namespace AND k.enabled WHERE b.id=$1 AND b.api_key_id=$2 AND b.namespace=$3 AND (b.confirmed_at IS NOT NULL OR b.expires_at>now())`, id, actor, string(ns)))
}
func (s *Store) ListBulk(ctx context.Context, ns domain.Namespace, actor uuid.UUID) ([]domain.BulkBatch, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `SELECT `+bulkColumns+` FROM admin_bulk_batches b JOIN api_keys k ON k.id=b.api_key_id AND k.namespace=b.namespace AND k.enabled WHERE b.api_key_id=$1 AND b.namespace=$2 AND b.confirmed_at IS NOT NULL ORDER BY b.created_at DESC,b.id DESC LIMIT 20`, actor, string(ns))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var batches []domain.BulkBatch
	for rows.Next() {
		batch, e := scanBulk(rows)
		if e != nil {
			return nil, e
		}
		batches = append(batches, batch)
	}
	return batches, rows.Err()
}
func (s *Store) ConfirmBulk(ctx context.Context, ns domain.Namespace, actor, id uuid.UUID) (domain.BulkBatch, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.BulkBatch{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := authorizedActor(ctx, tx, ns, actor); err != nil {
		return domain.BulkBatch{}, err
	}
	batch, err := scanBulk(tx.QueryRowContext(ctx, `SELECT `+bulkColumns+` FROM admin_bulk_batches b WHERE b.id=$1 AND b.api_key_id=$2 AND b.namespace=$3 AND (b.confirmed_at IS NOT NULL OR b.expires_at>now()) FOR UPDATE`, id, actor, string(ns)))
	if err != nil {
		return batch, err
	}
	if batch.ConfirmedAt != nil {
		return batch, tx.Commit()
	}
	for i := range batch.Items {
		item := &batch.Items[i]
		if _, err := tx.ExecContext(ctx, `SAVEPOINT bulk_item`); err != nil {
			return batch, err
		}
		var enabled bool
		var updated time.Time
		err = tx.QueryRowContext(ctx, `SELECT enabled,updated_at FROM jobs WHERE id=$1 AND namespace=$2 FOR UPDATE`, item.ID, string(ns)).Scan(&enabled, &updated)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			item.Result = "unavailable"
		case err != nil:
			item.Result = "failed"
		case !updated.Equal(item.UpdatedAt) || enabled != item.Enabled:
			item.Result = "conflict"
		case enabled == (batch.Action == "resume"):
			item.Result = "unchanged"
		default:
			_, err = tx.ExecContext(ctx, `UPDATE jobs SET enabled=$1,updated_at=clock_timestamp() WHERE id=$2 AND namespace=$3`, batch.Action == "resume", item.ID, string(ns))
			if err != nil {
				item.Result = "failed"
			} else {
				item.Result = "applied"
			}
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			if _, rollbackErr := tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT bulk_item`); rollbackErr != nil {
				return batch, rollbackErr
			}
		}
		if _, err := tx.ExecContext(ctx, `RELEASE SAVEPOINT bulk_item`); err != nil {
			return batch, err
		}
	}
	raw, err := json.Marshal(batch.Items)
	if err != nil {
		return batch, err
	}
	var confirmed time.Time
	err = tx.QueryRowContext(ctx, `UPDATE admin_bulk_batches SET items=$1,confirmed_at=clock_timestamp() WHERE id=$2 RETURNING confirmed_at`, string(raw), id).Scan(&confirmed)
	if err != nil {
		return batch, err
	}
	batch.ConfirmedAt = &confirmed
	return batch, tx.Commit()
}

// SetJobEnabled changes only fleet state; concurrent configuration edits cannot
// be overwritten by a pause/resume operation's stale job snapshot.
func (s *Store) SetJobEnabled(ctx context.Context, id uuid.UUID, ns domain.Namespace, enabled bool) (domain.Job, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	row := s.db.QueryRowContext(ctx, `UPDATE jobs SET enabled=$1,updated_at=clock_timestamp() WHERE id=$2 AND namespace=$3 RETURNING id,namespace,name,enabled,schedule_id,delivery_type,webhook_url,secret,timeout_ms,analytics_enabled,analytics_retention_seconds,created_at,updated_at`, enabled, id, string(ns))
	job, err := scanJobRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return job, domain.ErrJobNotFound
	}
	return job, err
}
