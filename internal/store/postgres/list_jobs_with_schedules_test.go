package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/djlord-it/cronlite/internal/domain"
	"github.com/google/uuid"
)

func TestListJobsWithSchedulesReturnsScheduleData(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	jobID := uuid.New()
	scheduleID := uuid.New()
	rows := sqlmock.NewRows([]string{
		"id", "namespace", "name", "enabled", "schedule_id",
		"delivery_type", "webhook_url", "secret", "timeout_ms",
		"analytics_enabled", "analytics_retention_seconds", "created_at", "updated_at",
		"schedule_id", "cron_expression", "timezone", "schedule_created_at", "schedule_updated_at",
	}).AddRow(
		jobID, "team", "daily-report", true, scheduleID,
		"webhook", "https://example.com/hook", "", int64(30000),
		false, 86400, now, now,
		scheduleID, "0 9 * * *", "America/Toronto", now, now,
	)

	mock.ExpectQuery(`(?s)FROM jobs j.*JOIN schedules s.*j.namespace = \$1`).
		WithArgs("team", 25, 0).
		WillReturnRows(rows)

	store := New(db, time.Second)
	got, err := store.ListJobsWithSchedules(context.Background(), domain.JobFilter{
		Namespace:  "team",
		ListParams: domain.ListParams{Limit: 25},
	})
	if err != nil {
		t.Fatalf("ListJobsWithSchedules: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected one row, got %d", len(got))
	}
	if got[0].Schedule.CronExpression != "0 9 * * *" || got[0].Schedule.Timezone != "America/Toronto" {
		t.Fatalf("schedule missing from row: %#v", got[0].Schedule)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
