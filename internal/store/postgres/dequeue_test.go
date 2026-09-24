package postgres

import (
	"context"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/djlord-it/cronlite/internal/domain"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
)

func TestDequeueExecutionEmptyQueueUsesOneStatement(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery(regexp.QuoteMeta(queryDequeueExecution)).WillReturnRows(sqlmock.NewRows([]string{
		"id", "job_id", "namespace", "trigger_type", "scheduled_at", "fired_at", "status", "acknowledged_at", "created_at",
	}))
	exec, err := New(db, 0).DequeueExecution(context.Background())
	if err != nil || exec != nil {
		t.Fatalf("want empty queue, got execution=%v error=%v", exec, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDequeueExecutionIntegration(t *testing.T) {
	dsn := os.Getenv("CRONLITE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set CRONLITE_TEST_DATABASE_URL to run real Postgres integration tests")
	}
	db, cleanup := openIsolatedPostgresSchema(t, dsn)
	defer cleanup()
	store := New(db, 5*time.Second)
	ctx, now := context.Background(), time.Now().UTC().Truncate(time.Microsecond)
	schedule := domain.Schedule{ID: uuid.New(), CronExpression: "* * * * *", Timezone: "UTC", CreatedAt: now, UpdatedAt: now}
	job := domain.Job{
		ID: uuid.New(), Namespace: "tenant-A", Name: "dequeue test", Enabled: true, ScheduleID: schedule.ID,
		Delivery:  domain.DeliveryConfig{Type: domain.DeliveryTypeWebhook, WebhookURL: "https://example.com/hook", Timeout: time.Second},
		Analytics: domain.AnalyticsConfig{RetentionSeconds: domain.DefaultRetentionSeconds}, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateJob(ctx, job, schedule); err != nil {
		t.Fatal(err)
	}
	want := domain.Execution{ID: uuid.New(), JobID: job.ID, Namespace: job.Namespace, TriggerType: domain.TriggerTypeManual,
		ScheduledAt: now, FiredAt: now, Status: domain.ExecutionStatusEmitted, CreatedAt: now}
	if err := store.InsertExecution(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := store.DequeueExecution(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ID != want.ID || got.Status != domain.ExecutionStatusInProgress {
		t.Fatalf("claimed execution = %+v", got)
	}
	got, err = store.DequeueExecution(ctx)
	if err != nil || got != nil {
		t.Fatalf("second dequeue = %+v, %v; want empty", got, err)
	}
}

func TestDequeueExecutionClaimsOneRow(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	id, jobID, now := uuid.New(), uuid.New(), time.Now().UTC()
	rows := sqlmock.NewRows([]string{
		"id", "job_id", "namespace", "trigger_type", "scheduled_at", "fired_at", "status", "acknowledged_at", "created_at",
	}).AddRow(id.String(), jobID.String(), "tenant-A", "manual", now, now, "in_progress", nil, now)
	mock.ExpectQuery(regexp.QuoteMeta(queryDequeueExecution)).WillReturnRows(rows)
	exec, err := New(db, 0).DequeueExecution(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if exec == nil || exec.ID != id || exec.Status != domain.ExecutionStatusInProgress {
		t.Fatalf("want claimed execution %s, got %+v", id, exec)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
