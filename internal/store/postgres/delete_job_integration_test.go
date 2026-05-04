package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/lib/pq"

	"github.com/djlord-it/cronlite/internal/domain"
)

func TestDeleteJob_Integration_NamespaceScopedCascade(t *testing.T) {
	dsn := os.Getenv("CRONLITE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set CRONLITE_TEST_DATABASE_URL to run real Postgres integration tests")
	}

	ctx := context.Background()
	db, cleanup := openIsolatedPostgresSchema(t, dsn)
	defer cleanup()

	store := New(db, 5*time.Second)
	now := time.Now().UTC().Truncate(time.Microsecond)

	schedule := domain.Schedule{
		ID:             uuid.New(),
		CronExpression: "*/5 * * * *",
		Timezone:       "UTC",
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	job := domain.Job{
		ID:         uuid.New(),
		Namespace:  domain.Namespace("tenant-b"),
		Name:       "tenant-b job",
		Enabled:    true,
		ScheduleID: schedule.ID,
		Delivery: domain.DeliveryConfig{
			Type:       domain.DeliveryTypeWebhook,
			WebhookURL: "https://example.com/hook",
			Secret:     "secret",
			Timeout:    10 * time.Second,
		},
		Analytics: domain.AnalyticsConfig{RetentionSeconds: domain.DefaultRetentionSeconds},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := store.CreateJob(ctx, job, schedule); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	exec := domain.Execution{
		ID:          uuid.New(),
		JobID:       job.ID,
		Namespace:   job.Namespace,
		TriggerType: domain.TriggerTypeScheduled,
		ScheduledAt: now.Add(time.Minute),
		FiredAt:     now.Add(time.Minute),
		Status:      domain.ExecutionStatusDelivered,
		CreatedAt:   now,
	}
	if err := store.InsertExecution(ctx, exec); err != nil {
		t.Fatalf("InsertExecution: %v", err)
	}
	if err := store.InsertDeliveryAttempt(ctx, domain.DeliveryAttempt{
		ID:          uuid.New(),
		ExecutionID: exec.ID,
		Attempt:     1,
		StatusCode:  204,
		StartedAt:   now,
		FinishedAt:  now.Add(time.Second),
	}); err != nil {
		t.Fatalf("InsertDeliveryAttempt: %v", err)
	}
	if err := store.UpsertTags(ctx, job.ID, []domain.Tag{{Key: "env", Value: "prod"}}); err != nil {
		t.Fatalf("UpsertTags: %v", err)
	}

	err := store.DeleteJob(ctx, job.ID, domain.Namespace("tenant-a"))
	if !errors.Is(err, domain.ErrJobNotFound) {
		t.Fatalf("cross-namespace DeleteJob error = %v, want %v", err, domain.ErrJobNotFound)
	}
	assertRowCount(t, db, "jobs", "id = $1", job.ID, 1)
	assertRowCount(t, db, "schedules", "id = $1", schedule.ID, 1)
	assertRowCount(t, db, "executions", "id = $1", exec.ID, 1)
	assertRowCount(t, db, "delivery_attempts", "execution_id = $1", exec.ID, 1)
	assertRowCount(t, db, "tags", "job_id = $1", job.ID, 1)

	if err := store.DeleteJob(ctx, job.ID, job.Namespace); err != nil {
		t.Fatalf("same-namespace DeleteJob: %v", err)
	}
	assertRowCount(t, db, "jobs", "id = $1", job.ID, 0)
	assertRowCount(t, db, "schedules", "id = $1", schedule.ID, 0)
	assertRowCount(t, db, "executions", "id = $1", exec.ID, 0)
	assertRowCount(t, db, "delivery_attempts", "execution_id = $1", exec.ID, 0)
	assertRowCount(t, db, "tags", "job_id = $1", job.ID, 0)
}

func openIsolatedPostgresSchema(t *testing.T, dsn string) (*sql.DB, func()) {
	t.Helper()

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	db.SetMaxOpenConns(1)

	schema := "cronlite_test_" + strings.ReplaceAll(uuid.NewString(), "-", "_")
	if _, err := db.Exec(`CREATE SCHEMA ` + schema); err != nil {
		db.Close()
		t.Fatalf("create schema: %v", err)
	}
	cleanup := func() {
		_, _ = db.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`)
		_ = db.Close()
	}

	if _, err := db.Exec(`SET search_path TO ` + schema); err != nil {
		cleanup()
		t.Fatalf("set search_path: %v", err)
	}
	for _, file := range []string{
		"001_initial.sql",
		"002_add_indexes.sql",
		"003_add_claimed_at.sql",
		"004_agent_platform.sql",
		"005_drop_scopes.sql",
		"006_add_claimed_at_index.sql",
	} {
		sqlBytes, err := os.ReadFile(filepath.Join("..", "..", "..", "schema", file))
		if err != nil {
			cleanup()
			t.Fatalf("read migration %s: %v", file, err)
		}
		if _, err := db.Exec(string(sqlBytes)); err != nil {
			cleanup()
			t.Fatalf("apply migration %s: %v", file, err)
		}
	}

	return db, cleanup
}

func assertRowCount(t *testing.T, db *sql.DB, table, where string, arg any, want int) {
	t.Helper()

	var got int
	query := fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE %s", table, where)
	if err := db.QueryRow(query, arg).Scan(&got); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	if got != want {
		t.Fatalf("count %s where %s = %d, want %d", table, where, got, want)
	}
}
