package migrate

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"time"
)

// lockKey is the advisory lock held while migrating ("CRONMIGR"). It must
// differ from the default LEADER_LOCK_KEY (728379).
const lockKey int64 = 0x43524f4e4d494752

// defaultLockTimeout applies when Options.LockTimeout is zero.
const defaultLockTimeout = 2 * time.Minute

// lockPollInterval is how often pg_try_advisory_lock is retried.
const lockPollInterval = 100 * time.Millisecond

const trackingTable = "cronlite_schema_migrations"

var (
	// ErrManualMigrationRequired means a pending migration must be applied
	// by an operator because the database predates the runner.
	ErrManualMigrationRequired = errors.New("manual migration required")
	// ErrInconsistentSchema means the detected schema objects do not form a
	// contiguous prefix of migrations, so the database cannot be baselined.
	ErrInconsistentSchema = errors.New("inconsistent schema")
	// ErrLockTimeout means the migration advisory lock was not acquired in time.
	ErrLockTimeout = errors.New("timed out waiting for migration lock")
)

// Options configures Run.
type Options struct {
	// LockTimeout bounds how long Run waits for the migration lock.
	// Zero means 2m.
	LockTimeout time.Duration
	// Logf receives warnings (e.g. checksum drift). Nil discards them.
	Logf func(format string, args ...any)
}

// Result lists the versions Run applied and the versions it recorded as
// already present (baselined) without executing them.
type Result struct {
	Applied   []int
	Baselined []int
}

// State is a migration's state in the tracking table.
type State string

const (
	StateApplied   State = "applied"
	StateBaselined State = "baselined"
	StatePending   State = "pending"
)

// Status describes one migration's state.
type Status struct {
	Version int
	Name    string
	State   State
}

type recorded struct {
	checksum  string
	baselined bool
}

// Run applies pending migrations in version order while holding a
// PostgreSQL advisory lock. On a database created before the runner existed
// (no tracking rows, but "jobs" exists) it first baselines the migrations
// already present.
func Run(ctx context.Context, db *sql.DB, ms []Migration, opts Options) (Result, error) {
	var res Result
	logf := opts.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		return res, fmt.Errorf("migrate: open connection: %w", err)
	}
	defer conn.Close()

	if err := acquireLock(ctx, conn, opts.LockTimeout); err != nil {
		return res, err
	}
	defer releaseLock(conn)

	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS `+trackingTable+` (
    version    INTEGER PRIMARY KEY,
    name       TEXT NOT NULL,
    checksum   TEXT NOT NULL,
    baselined  BOOLEAN NOT NULL DEFAULT false,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
)`); err != nil {
		return res, fmt.Errorf("migrate: create tracking table: %w", err)
	}

	rec, err := readRecorded(ctx, conn)
	if err != nil {
		return res, err
	}

	for _, m := range ms {
		if r, ok := rec[m.Version]; ok && r.checksum != m.Checksum {
			logf("migrate: migration %d (%s) checksum mismatch: recorded %s, embedded %s; continuing",
				m.Version, m.Name, r.checksum, m.Checksum)
		}
	}

	if len(rec) == 0 {
		baselined, err := baseline(ctx, conn, ms)
		if err != nil {
			return res, err
		}
		res.Baselined = append(res.Baselined, baselined...)
		if rec, err = readRecorded(ctx, conn); err != nil {
			return res, err
		}
	}

	anyBaselined := false
	for _, r := range rec {
		if r.baselined {
			anyBaselined = true
			break
		}
	}

	var pending []Migration
	for _, m := range ms {
		if _, ok := rec[m.Version]; !ok {
			pending = append(pending, m)
		}
	}

	// On a baselined database the operator may have hand-applied pending
	// migrations (e.g. after a refusal). Record the contiguous run of them
	// that probes present as baselined, so they are never re-executed.
	// Refusal is then decided on what remains, before anything is applied.
	if anyBaselined {
		handApplied, err := presentPrefix(ctx, conn, pending)
		if err != nil {
			return res, err
		}
		if err := recordBaselined(ctx, conn, handApplied); err != nil {
			return res, err
		}
		for _, m := range handApplied {
			logf("migrate: migration %d (%s) was applied by hand; recorded as baselined", m.Version, m.Name)
			res.Baselined = append(res.Baselined, m.Version)
		}
		pending = pending[len(handApplied):]

		for i, m := range pending {
			if m.ManualOnExisting {
				var list []string
				for _, p := range pending[:i+1] {
					list = append(list, fmt.Sprintf("%03d (%s)", p.Version, p.Name))
				}
				return res, fmt.Errorf("%w: migration %d (%s) must be applied by hand on an existing database; "+
					"apply migrations %s by hand, in order, following \"Execution ownership migration (011)\" in schema/README.md, then rerun",
					ErrManualMigrationRequired, m.Version, m.Name, strings.Join(list, ", "))
			}
		}
	}

	for _, m := range pending {
		if err := apply(ctx, conn, m); err != nil {
			return res, err
		}
		res.Applied = append(res.Applied, m.Version)
	}
	return res, nil
}

// NeedsBaseline reports whether the database predates the runner: the
// tracking table does not exist yet but the application's jobs table does, so
// the next run would baseline the existing schema. It is read-only.
func NeedsBaseline(ctx context.Context, db *sql.DB) (bool, error) {
	var tracking, jobs bool
	if err := db.QueryRowContext(ctx,
		`SELECT to_regclass(format('%I.%I', current_schema(), $1::text)) IS NOT NULL,
		        to_regclass(format('%I.%I', current_schema(), 'jobs')) IS NOT NULL`,
		trackingTable).Scan(&tracking, &jobs); err != nil {
		return false, fmt.Errorf("migrate: probe baseline state: %w", err)
	}
	return !tracking && jobs, nil
}

// StatusOf reports each migration's state without taking the lock or
// creating the tracking table.
func StatusOf(ctx context.Context, db *sql.DB, ms []Migration) ([]Status, error) {
	var exists bool
	if err := db.QueryRowContext(ctx,
		`SELECT to_regclass(format('%I.%I', current_schema(), $1::text)) IS NOT NULL`,
		trackingTable).Scan(&exists); err != nil {
		return nil, fmt.Errorf("migrate: probe tracking table: %w", err)
	}
	rec := map[int]recorded{}
	if exists {
		var err error
		if rec, err = readRecorded(ctx, db); err != nil {
			return nil, err
		}
	}
	out := make([]Status, 0, len(ms))
	for _, m := range ms {
		st := StatePending
		if r, ok := rec[m.Version]; ok {
			st = StateApplied
			if r.baselined {
				st = StateBaselined
			}
		}
		out = append(out, Status{Version: m.Version, Name: m.Name, State: st})
	}
	return out, nil
}

func acquireLock(ctx context.Context, conn *sql.Conn, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = defaultLockTimeout
	}
	deadline := time.Now().Add(timeout)
	for {
		var got bool
		if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, lockKey).Scan(&got); err != nil {
			// The server may have granted the lock before the error (e.g. ctx
			// cancelled mid-query); discard the session so it never returns
			// to the pool holding the lock.
			discardConn(conn)
			return fmt.Errorf("migrate: acquire lock: %w", err)
		}
		if got {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("migrate: lock %d not acquired after %s: %w", lockKey, timeout, ErrLockTimeout)
		}
		wait := min(lockPollInterval, time.Until(deadline))
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return fmt.Errorf("migrate: acquire lock: %w", ctx.Err())
		case <-t.C:
		}
	}
}

// releaseLock unlocks; if that fails the connection is discarded so a
// session-level lock never returns to the pool.
func releaseLock(conn *sql.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_unlock($1)`, lockKey); err != nil {
		discardConn(conn)
	}
}

// discardConn marks conn bad so database/sql closes it instead of
// returning it to the pool.
func discardConn(conn *sql.Conn) {
	_ = conn.Raw(func(any) error { return driver.ErrBadConn })
}

type rowsQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func readRecorded(ctx context.Context, q rowsQuerier) (map[int]recorded, error) {
	rows, err := q.QueryContext(ctx, `SELECT version, checksum, baselined FROM `+trackingTable)
	if err != nil {
		return nil, fmt.Errorf("migrate: read tracking table: %w", err)
	}
	defer rows.Close()
	out := map[int]recorded{}
	for rows.Next() {
		var v int
		var r recorded
		if err := rows.Scan(&v, &r.checksum, &r.baselined); err != nil {
			return nil, fmt.Errorf("migrate: read tracking table: %w", err)
		}
		out[v] = r
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("migrate: read tracking table: %w", err)
	}
	return out, nil
}

// baseline records the migrations already present in a pre-runner
// database. It does nothing unless "jobs" exists. The present migrations
// must form a contiguous prefix 1..k.
func baseline(ctx context.Context, conn *sql.Conn, ms []Migration) ([]int, error) {
	hasJobs, _, err := runProbe(ctx, conn, 1)
	if err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	if !hasJobs {
		return nil, nil
	}

	present, err := presentPrefix(ctx, conn, ms)
	if err != nil {
		return nil, err
	}
	if err := recordBaselined(ctx, conn, present); err != nil {
		return nil, err
	}
	versions := make([]int, 0, len(present))
	for _, m := range present {
		versions = append(versions, m.Version)
	}
	return versions, nil
}

// presentPrefix probes ms in order and returns the leading run whose
// objects are present; the result is always ms[:k]. A migration without a
// probe cannot be detected and ends the run. If a migration after the run
// probes present, the schema has a gap and ErrInconsistentSchema names the
// first missing version.
func presentPrefix(ctx context.Context, conn *sql.Conn, ms []Migration) ([]Migration, error) {
	k := len(ms)
	for i, m := range ms {
		ok, _, err := runProbe(ctx, conn, m.Version)
		if err != nil {
			return nil, fmt.Errorf("migrate: %w", err)
		}
		if !ok {
			if k == len(ms) {
				k = i
			}
			continue
		}
		if k != len(ms) {
			missing := ms[k].Version
			return nil, fmt.Errorf("%w: migration %d is present but migration %d is missing; "+
				"apply migration %d by hand before running migrations",
				ErrInconsistentSchema, m.Version, missing, missing)
		}
	}
	return ms[:k], nil
}

func recordBaselined(ctx context.Context, conn *sql.Conn, ms []Migration) error {
	if len(ms) == 0 {
		return nil
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("migrate: begin baseline: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, m := range ms {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO `+trackingTable+`(version, name, checksum, baselined) VALUES ($1, $2, $3, true)`,
			m.Version, m.Name, m.Checksum); err != nil {
			return fmt.Errorf("migrate: baseline migration %d: %w", m.Version, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("migrate: commit baseline: %w", err)
	}
	return nil
}

func apply(ctx context.Context, conn *sql.Conn, m Migration) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("migrate: migration %d (%s): begin: %w", m.Version, m.Name, err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, stripTxControl(m.SQL)); err != nil {
		return fmt.Errorf("migrate: migration %d (%s) failed: %w", m.Version, m.Name, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO `+trackingTable+`(version, name, checksum) VALUES ($1, $2, $3)`,
		m.Version, m.Name, m.Checksum); err != nil {
		return fmt.Errorf("migrate: migration %d (%s): record: %w", m.Version, m.Name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("migrate: migration %d (%s): commit: %w", m.Version, m.Name, err)
	}
	return nil
}
