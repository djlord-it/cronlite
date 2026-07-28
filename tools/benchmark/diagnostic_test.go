package main

import (
	"context"
	"database/sql"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestDiagnosticExecutionIncludesClaimAndAttempts(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SET TRANSACTION READ ONLY")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT[[:space:]]+e.id::text.+e.claimed_at").
		WithArgs("exec-1").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "job_id", "scheduled_at", "fired_at", "created_at", "claimed_at", "status",
		}).AddRow("exec-1", "job-1", now, now.Add(time.Second), now.Add(2*time.Second), now.Add(3*time.Second), "delivered"))
	mock.ExpectQuery("SELECT[[:space:]]+id::text.+FROM delivery_attempts").
		WithArgs("exec-1").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "attempt", "status_code", "error", "started_at", "finished_at",
		}).AddRow("attempt-1", 1, 204, "", now.Add(4*time.Second), now.Add(5*time.Second)))
	mock.ExpectCommit()

	got, err := newDiagnosticCollector(db).execution(context.Background(), "exec-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ExecutionID != "exec-1" || got.ClaimedAt == nil || len(got.Attempts) != 1 {
		t.Fatalf("diagnostic execution = %+v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDiagnosticExecutionRollsBackWhenQueryFails(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SET TRANSACTION READ ONLY")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT[[:space:]]+e.id::text").WithArgs("missing").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()

	if _, err := newDiagnosticCollector(db).execution(context.Background(), "missing"); err == nil {
		t.Fatal("expected query error")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
