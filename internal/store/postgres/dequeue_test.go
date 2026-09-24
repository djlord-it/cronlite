package postgres

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/djlord-it/cronlite/internal/domain"
	"github.com/google/uuid"
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
