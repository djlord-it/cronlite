//go:build integration

package migrate

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/djlord-it/cronlite/schema"
	_ "github.com/lib/pq"
)

// initdbOrder is the docker-compose.yml /docker-entrypoint-initdb.d mount order.
var initdbOrder = []int{1, 2, 3, 4, 5, 6, 11, 7, 8, 9, 10, 12}

// newTestDB creates a fresh database for one test and returns its URL.
func newTestDB(t *testing.T) string {
	t.Helper()
	adminURL := os.Getenv("MIGRATE_TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("MIGRATE_TEST_DATABASE_URL is not set; skipping PostgreSQL migrate integration tests")
	}
	admin, err := sql.Open("postgres", adminURL)
	if err != nil {
		t.Fatalf("open admin db: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close() })

	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("rand: %v", err)
	}
	name := "cronlite_migrate_" + hex.EncodeToString(buf)
	ctx := context.Background()
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() {
		if _, err := admin.ExecContext(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			t.Errorf("drop database %s: %v", name, err)
		}
	})

	u, err := url.Parse(adminURL)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	u.Path = "/" + name
	return u.String()
}

func openDB(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func loadAll(t *testing.T) []Migration {
	t.Helper()
	ms, err := Load(schema.FS)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return ms
}

func byVersion(ms []Migration) map[int]Migration {
	out := make(map[int]Migration, len(ms))
	for _, m := range ms {
		out[m.Version] = m
	}
	return out
}

// handApply executes the raw migration files, as initdb or an operator would.
func handApply(t *testing.T, db *sql.DB, ms []Migration, versions ...int) {
	t.Helper()
	bv := byVersion(ms)
	for _, v := range versions {
		m, ok := bv[v]
		if !ok {
			t.Fatalf("no migration %d", v)
		}
		if _, err := db.ExecContext(context.Background(), m.SQL); err != nil {
			t.Fatalf("hand-apply %d: %v", v, err)
		}
	}
}

func seq(from, to int) []int {
	var out []int
	for i := from; i <= to; i++ {
		out = append(out, i)
	}
	return out
}

func opts() Options {
	return Options{LockTimeout: 10 * time.Second}
}

type trackingRow struct {
	Version   int
	Checksum  string
	Baselined bool
}

func trackingRows(t *testing.T, db *sql.DB) []trackingRow {
	t.Helper()
	var exists bool
	if err := db.QueryRow(`SELECT to_regclass('cronlite_schema_migrations') IS NOT NULL`).Scan(&exists); err != nil {
		t.Fatalf("probe tracking table: %v", err)
	}
	if !exists {
		return nil
	}
	rows, err := db.Query(`SELECT version, checksum, baselined FROM cronlite_schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatalf("query tracking rows: %v", err)
	}
	defer rows.Close()
	var out []trackingRow
	for rows.Next() {
		var r trackingRow
		if err := rows.Scan(&r.Version, &r.Checksum, &r.Baselined); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}

func regclassExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var ok bool
	if err := db.QueryRow(`SELECT to_regclass($1) IS NOT NULL`, name).Scan(&ok); err != nil {
		t.Fatalf("to_regclass(%s): %v", name, err)
	}
	return ok
}

func emptyInts(s []int) bool { return len(s) == 0 }

func TestRunEmptyDatabase(t *testing.T) {
	db := openDB(t, newTestDB(t))
	ms := loadAll(t)

	res, err := Run(context.Background(), db, ms, opts())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !reflect.DeepEqual(res.Applied, seq(1, 12)) {
		t.Fatalf("Applied = %v, want 1..12", res.Applied)
	}
	if !emptyInts(res.Baselined) {
		t.Fatalf("Baselined = %v, want empty", res.Baselined)
	}
	rows := trackingRows(t, db)
	if len(rows) != 12 {
		t.Fatalf("tracking rows = %d, want 12", len(rows))
	}
	bv := byVersion(ms)
	for _, r := range rows {
		if r.Baselined {
			t.Errorf("version %d baselined = true, want false", r.Version)
		}
		if r.Checksum != bv[r.Version].Checksum {
			t.Errorf("version %d checksum = %s, want %s", r.Version, r.Checksum, bv[r.Version].Checksum)
		}
	}
}

func TestRunIsIdempotent(t *testing.T) {
	db := openDB(t, newTestDB(t))
	ms := loadAll(t)
	if _, err := Run(context.Background(), db, ms, opts()); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	res, err := Run(context.Background(), db, ms, opts())
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if !emptyInts(res.Applied) || !emptyInts(res.Baselined) {
		t.Fatalf("second Run = %+v, want empty", res)
	}
}

func TestRunConcurrent(t *testing.T) {
	dsn := newTestDB(t)
	db1, db2 := openDB(t, dsn), openDB(t, dsn)
	ms := loadAll(t)

	var wg sync.WaitGroup
	results := make([]Result, 2)
	errs := make([]error, 2)
	for i, db := range []*sql.DB{db1, db2} {
		wg.Add(1)
		go func(i int, db *sql.DB) {
			defer wg.Done()
			results[i], errs[i] = Run(context.Background(), db, ms, opts())
		}(i, db)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("Run %d: %v", i, err)
		}
	}
	union := append(append([]int{}, results[0].Applied...), results[1].Applied...)
	sort.Ints(union)
	if !reflect.DeepEqual(union, seq(1, 12)) {
		t.Fatalf("union of Applied = %v (a=%v b=%v), want exactly 1..12", union, results[0].Applied, results[1].Applied)
	}
}

func TestRunBaselinesInitdbDatabase(t *testing.T) {
	db := openDB(t, newTestDB(t))
	ms := loadAll(t)
	handApply(t, db, ms, initdbOrder...)

	res, err := Run(context.Background(), db, ms, opts())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !reflect.DeepEqual(res.Baselined, seq(1, 12)) {
		t.Fatalf("Baselined = %v, want 1..12", res.Baselined)
	}
	if !emptyInts(res.Applied) {
		t.Fatalf("Applied = %v, want empty", res.Applied)
	}
	for _, r := range trackingRows(t, db) {
		if !r.Baselined {
			t.Errorf("version %d baselined = false, want true", r.Version)
		}
	}
}

func TestRunRejectsGap(t *testing.T) {
	db := openDB(t, newTestDB(t))
	ms := loadAll(t)
	handApply(t, db, ms, append(seq(1, 9), 11)...)

	_, err := Run(context.Background(), db, ms, opts())
	if !errors.Is(err, ErrInconsistentSchema) {
		t.Fatalf("Run err = %v, want ErrInconsistentSchema", err)
	}
	if !strings.Contains(err.Error(), "10") {
		t.Fatalf("error %q does not name version 10", err)
	}
	if rows := trackingRows(t, db); len(rows) != 0 {
		t.Fatalf("tracking rows = %v, want none", rows)
	}
}

func TestRunRefusesBeforeApplyingAnything(t *testing.T) {
	db := openDB(t, newTestDB(t))
	ms := loadAll(t)
	handApply(t, db, ms, seq(1, 9)...)

	_, err := Run(context.Background(), db, ms, opts())
	if !errors.Is(err, ErrManualMigrationRequired) {
		t.Fatalf("Run err = %v, want ErrManualMigrationRequired", err)
	}
	if regclassExists(t, db, "admin_bulk_batches") {
		t.Fatal("admin_bulk_batches exists; 010 was applied before refusing")
	}
	for _, want := range []string{"010 (010_job_control_center.sql)", "011 (011_execution_ownership.sql)", "schema/README.md", "Execution ownership migration (011)"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
	if i, j := strings.Index(err.Error(), "010 ("), strings.Index(err.Error(), "011 ("); i < 0 || j < i {
		t.Errorf("error %q does not list 010 before 011", err)
	}
}

func TestRunRefusesManualOnExisting(t *testing.T) {
	db := openDB(t, newTestDB(t))
	ms := loadAll(t)
	handApply(t, db, ms, seq(1, 10)...)

	_, err := Run(context.Background(), db, ms, opts())
	if !errors.Is(err, ErrManualMigrationRequired) {
		t.Fatalf("Run err = %v, want ErrManualMigrationRequired", err)
	}
	for _, want := range []string{"11", "schema/README.md", "Execution ownership migration (011)"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}

	handApply(t, db, ms, 11)
	res, err := Run(context.Background(), db, ms, opts())
	if err != nil {
		t.Fatalf("Run after hand-applying 011: %v", err)
	}
	found := false
	for _, v := range res.Baselined {
		if v == 11 {
			found = true
		}
	}
	if !found {
		t.Fatalf("Baselined = %v, want to contain 11", res.Baselined)
	}
}

func TestRunBaselinesHandAppliedAfterRefusal(t *testing.T) {
	db := openDB(t, newTestDB(t))
	ms := loadAll(t)
	handApply(t, db, ms, seq(1, 7)...)

	_, err := Run(context.Background(), db, ms, opts())
	if !errors.Is(err, ErrManualMigrationRequired) {
		t.Fatalf("first Run err = %v, want ErrManualMigrationRequired", err)
	}

	handApply(t, db, ms, seq(8, 11)...)
	res, err := Run(context.Background(), db, ms, opts())
	if err != nil {
		t.Fatalf("Run after hand-applying 8..11: %v", err)
	}
	if !reflect.DeepEqual(res.Applied, []int{12}) {
		t.Fatalf("Applied = %v, want [12]", res.Applied)
	}
	if !reflect.DeepEqual(res.Baselined, seq(8, 11)) {
		t.Fatalf("Baselined = %v, want 8..11", res.Baselined)
	}
}

func TestRunRejectsGapAfterBaseline(t *testing.T) {
	db := openDB(t, newTestDB(t))
	ms := loadAll(t)
	handApply(t, db, ms, seq(1, 7)...)
	if _, err := Run(context.Background(), db, ms, opts()); !errors.Is(err, ErrManualMigrationRequired) {
		t.Fatalf("first Run err = %v, want ErrManualMigrationRequired", err)
	}

	handApply(t, db, ms, 8, 10)
	_, err := Run(context.Background(), db, ms, opts())
	if !errors.Is(err, ErrInconsistentSchema) {
		t.Fatalf("Run err = %v, want ErrInconsistentSchema", err)
	}
	if !strings.Contains(err.Error(), "migration 9 is missing") {
		t.Fatalf("error %q does not name missing version 9", err)
	}
	for _, r := range trackingRows(t, db) {
		if r.Version > 7 {
			t.Errorf("tracking row %d recorded, want only 1..7", r.Version)
		}
	}
}

func TestRunResumesFreshPartial(t *testing.T) {
	db := openDB(t, newTestDB(t))
	ms := loadAll(t)
	if _, err := Run(context.Background(), db, ms[:5], opts()); err != nil {
		t.Fatalf("partial Run: %v", err)
	}
	res, err := Run(context.Background(), db, ms, opts())
	if err != nil {
		t.Fatalf("full Run: %v", err)
	}
	if !reflect.DeepEqual(res.Applied, seq(6, 12)) {
		t.Fatalf("Applied = %v, want 6..12", res.Applied)
	}
}

func TestRunBrokenMigrationRollsBack(t *testing.T) {
	db := openDB(t, newTestDB(t))
	ms := append(loadAll(t), Migration{Version: 99, Name: "099_broken.sql", SQL: "CREATE TABLE ok99(); SELECT 1/0;", Checksum: "broken"})

	_, err := Run(context.Background(), db, ms, opts())
	if err == nil {
		t.Fatal("Run succeeded, want error")
	}
	if !strings.Contains(err.Error(), "99") {
		t.Fatalf("error %q does not mention 99", err)
	}
	if regclassExists(t, db, "ok99") {
		t.Fatal("ok99 exists; migration 99 was not rolled back")
	}
	got := map[int]bool{}
	for _, r := range trackingRows(t, db) {
		got[r.Version] = true
	}
	if got[99] {
		t.Fatal("tracking row 99 exists")
	}
	for _, v := range seq(1, 12) {
		if !got[v] {
			t.Errorf("tracking row %d missing", v)
		}
	}
}

func TestRunLockTimeout(t *testing.T) {
	db := openDB(t, newTestDB(t))
	ms := loadAll(t)

	ctx := context.Background()
	holder, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	defer holder.Close()
	if _, err := holder.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, lockKey); err != nil {
		t.Fatalf("hold lock: %v", err)
	}
	defer func() { _, _ = holder.ExecContext(ctx, `SELECT pg_advisory_unlock($1)`, lockKey) }()

	start := time.Now()
	_, err = Run(ctx, db, ms, Options{LockTimeout: time.Second})
	elapsed := time.Since(start)
	if !errors.Is(err, ErrLockTimeout) {
		t.Fatalf("Run err = %v, want ErrLockTimeout", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("Run took %v, want < 5s", elapsed)
	}
}

func TestRunChecksumDriftWarns(t *testing.T) {
	db := openDB(t, newTestDB(t))
	ms := loadAll(t)
	if _, err := Run(context.Background(), db, ms, opts()); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if _, err := db.Exec(`UPDATE cronlite_schema_migrations SET checksum='x' WHERE version=1`); err != nil {
		t.Fatalf("update checksum: %v", err)
	}
	var mu sync.Mutex
	var lines []string
	o := opts()
	o.Logf = func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, fmt.Sprintf(format, args...))
	}
	if _, err := Run(context.Background(), db, ms, o); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, l := range lines {
		if strings.Contains(l, "migration 1 (001_initial.sql) checksum mismatch") {
			return
		}
	}
	t.Fatalf("no checksum drift warning for version 1 in %q", lines)
}

func TestStatusOf(t *testing.T) {
	db := openDB(t, newTestDB(t))
	ms := loadAll(t)
	if _, err := Run(context.Background(), db, ms[:5], opts()); err != nil {
		t.Fatalf("partial Run: %v", err)
	}
	st, err := StatusOf(context.Background(), db, ms)
	if err != nil {
		t.Fatalf("StatusOf: %v", err)
	}
	if len(st) != 12 {
		t.Fatalf("len(StatusOf) = %d, want 12", len(st))
	}
	for _, s := range st {
		want := StatePending
		if s.Version <= 5 {
			want = StateApplied
		}
		if s.State != want {
			t.Errorf("version %d state = %q, want %q", s.Version, s.State, want)
		}
	}
}

func TestStatusOfEmptyDatabase(t *testing.T) {
	db := openDB(t, newTestDB(t))
	ms := loadAll(t)
	st, err := StatusOf(context.Background(), db, ms)
	if err != nil {
		t.Fatalf("StatusOf: %v", err)
	}
	for _, s := range st {
		if s.State != StatePending {
			t.Errorf("version %d state = %q, want pending", s.Version, s.State)
		}
	}
}

func TestNeedsBaseline(t *testing.T) {
	db := openDB(t, newTestDB(t))
	ms := loadAll(t)
	ctx := context.Background()

	if got, err := NeedsBaseline(ctx, db); err != nil || got {
		t.Fatalf("empty db: NeedsBaseline = %v, %v; want false, nil", got, err)
	}
	handApply(t, db, ms, seq(1, 9)...)
	if got, err := NeedsBaseline(ctx, db); err != nil || !got {
		t.Fatalf("pre-runner db: NeedsBaseline = %v, %v; want true, nil", got, err)
	}
	if _, err := Run(ctx, db, ms, opts()); !errors.Is(err, ErrManualMigrationRequired) {
		t.Fatalf("Run err = %v", err)
	}
	if got, err := NeedsBaseline(ctx, db); err != nil || got {
		t.Fatalf("tracked db: NeedsBaseline = %v, %v; want false, nil", got, err)
	}
}
