package main

import (
	"context"
	"reflect"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func newChaosMock(t *testing.T) (*diagnosticCollector, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return newDiagnosticCollector(db), mock
}

func expectReadOnly(mock sqlmock.Sqlmock) {
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SET TRANSACTION READ ONLY")).
		WillReturnResult(sqlmock.NewResult(0, 0))
}

func TestChaosExecutionsReadOnlyAndFiltered(t *testing.T) {
	collector, mock := newChaosMock(t)
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	expectReadOnly(mock)
	mock.ExpectQuery(regexp.QuoteMeta("= ANY($1::uuid[])")).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "job_id", "scheduled_at", "created_at", "status", "lease_until"}).
			AddRow("e1", "j1", now, now.Add(time.Second), "in_progress", now.Add(5*time.Second)).
			AddRow("e2", "j1", now.Add(time.Minute), now.Add(time.Minute), "delivered", nil))
	mock.ExpectCommit()

	rows, err := collector.chaosExecutions(context.Background(), []string{"j1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].LeaseUntil == nil || rows[1].LeaseUntil != nil || rows[1].Status != "delivered" {
		t.Fatalf("rows = %+v", rows)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestChaosQueueDepthCountsPendingStatuses(t *testing.T) {
	collector, mock := newChaosMock(t)
	expectReadOnly(mock)
	mock.ExpectQuery(regexp.QuoteMeta("status IN ('emitted', 'in_progress')")).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(7))
	mock.ExpectCommit()

	depth, err := collector.chaosQueueDepth(context.Background(), []string{"j1"})
	if err != nil || depth != 7 {
		t.Fatalf("depth=%d err=%v", depth, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestChaosStuckUsesLeaseAndAgeLimits(t *testing.T) {
	collector, mock := newChaosMock(t)
	expectReadOnly(mock)
	mock.ExpectQuery(regexp.QuoteMeta("lease_until < clock_timestamp() - $2 * INTERVAL '1 millisecond'")).
		WithArgs(sqlmock.AnyArg(), int64(8000), int64(19*60*1000+1000)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("e9"))
	mock.ExpectCommit()

	ids, err := collector.chaosStuck(context.Background(), []string{"j1"}, 8*time.Second, 19*time.Minute+time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids, []string{"e9"}) {
		t.Fatalf("ids = %v", ids)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestChaosQueriesEmptyJobIDs(t *testing.T) {
	collector, mock := newChaosMock(t)
	rows, err := collector.chaosExecutions(context.Background(), nil)
	if err != nil || rows != nil {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	depth, err := collector.chaosQueueDepth(context.Background(), nil)
	if err != nil || depth != 0 {
		t.Fatalf("depth=%d err=%v", depth, err)
	}
	ids, err := collector.chaosStuck(context.Background(), nil, time.Second, time.Second)
	if err != nil || ids != nil {
		t.Fatalf("ids=%v err=%v", ids, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("database was queried: %v", err)
	}
}
