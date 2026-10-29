package migrate

import (
	"context"
	"database/sql"
	"fmt"
)

// querier is satisfied by *sql.Conn and *sql.Tx.
type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// probe reports whether a migration's defining object is present in the
// current schema. Names are resolved via current_schema(), never "public".
type probe func(ctx context.Context, q querier) (bool, error)

func relationExists(name string) probe {
	return func(ctx context.Context, q querier) (bool, error) {
		var ok bool
		err := q.QueryRowContext(ctx,
			`SELECT to_regclass(format('%I.%I', current_schema(), $1::text)) IS NOT NULL`,
			name).Scan(&ok)
		return ok, err
	}
}

func columnExists(table, column string) probe {
	return func(ctx context.Context, q querier) (bool, error) {
		var ok bool
		err := q.QueryRowContext(ctx, `
SELECT EXISTS (
    SELECT 1 FROM pg_attribute
    WHERE attrelid = to_regclass(format('%I.%I', current_schema(), $1::text))
      AND attname = $2 AND attnum > 0 AND NOT attisdropped
)`, table, column).Scan(&ok)
		return ok, err
	}
}

// columnDropped reports that table exists and column does not. Requiring
// the table keeps a database that stopped before 004 from looking like
// 005 was applied.
func columnDropped(table, column string) probe {
	return func(ctx context.Context, q querier) (bool, error) {
		tbl, err := relationExists(table)(ctx, q)
		if err != nil || !tbl {
			return false, err
		}
		col, err := columnExists(table, column)(ctx, q)
		return !col, err
	}
}

// probes maps migrations 1–12 to the check that detects them.
var probes = map[int]probe{
	1:  relationExists("jobs"),
	2:  relationExists("idx_jobs_enabled"),
	3:  columnExists("executions", "claimed_at"),
	4:  relationExists("api_keys"),
	5:  columnDropped("api_keys", "scopes"),
	6:  relationExists("idx_executions_in_progress_claimed_at"),
	7:  relationExists("admin_sessions"),
	8:  columnExists("admin_sessions", "absolute_expires_at"),
	9:  relationExists("idx_executions_emitted_created_at"),
	10: relationExists("admin_bulk_batches"),
	11: columnExists("executions", "claim_generation"),
	12: columnExists("api_keys", "tailnet_login"),
}

// runProbe runs the probe for version; ok is false when no probe exists.
func runProbe(ctx context.Context, q querier, version int) (present, ok bool, err error) {
	p, ok := probes[version]
	if !ok {
		return false, false, nil
	}
	present, err = p(ctx, q)
	if err != nil {
		return false, true, fmt.Errorf("probe migration %d: %w", version, err)
	}
	return present, true, nil
}
