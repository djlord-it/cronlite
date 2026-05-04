package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"

	"github.com/djlord-it/cronlite/internal/domain"
)

func TestUpdateJobAggregate_RollsBackWhenTagUpsertFails(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	store := New(db, 0)
	now := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
	jobID := uuid.New()
	scheduleID := uuid.New()
	job := domain.Job{
		ID:         jobID,
		Namespace:  domain.Namespace("tenant-1"),
		Name:       "updated",
		Enabled:    true,
		ScheduleID: scheduleID,
		Delivery: domain.DeliveryConfig{
			Type:       domain.DeliveryTypeWebhook,
			WebhookURL: "https://example.com/hook",
			Secret:     "secret",
			Timeout:    30 * time.Second,
		},
		Analytics: domain.AnalyticsConfig{RetentionSeconds: domain.DefaultRetentionSeconds},
		UpdatedAt: now,
	}
	schedule := domain.Schedule{
		ID:             scheduleID,
		CronExpression: "*/10 * * * *",
		Timezone:       "UTC",
		UpdatedAt:      now,
	}
	tags := []domain.Tag{{Key: "env", Value: "prod"}}
	tagErr := errors.New("tag insert failed")

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE schedules SET").
		WithArgs(schedule.CronExpression, schedule.Timezone, schedule.UpdatedAt, schedule.ID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE jobs SET").
		WithArgs(
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
		).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("DELETE FROM tags").WithArgs(job.ID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO tags").WithArgs(job.ID, tags[0].Key, tags[0].Value).WillReturnError(tagErr)
	mock.ExpectRollback()

	err = store.UpdateJobAggregate(context.Background(), job, schedule, &tags)
	if err == nil {
		t.Fatal("expected tag upsert error")
	}
	if !strings.Contains(err.Error(), tagErr.Error()) {
		t.Fatalf("expected error %q, got %v", tagErr, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}
