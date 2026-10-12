package leaderelection

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	_ "github.com/lib/pq"
)

func TestElector_DemotionWaitsForStartupCallback(t *testing.T) {
	db, mock := newMockDB(t)
	defer func() { _ = db.Close() }()
	expectTCPKeepalive(mock)
	mock.ExpectQuery("SELECT pg_try_advisory_lock").WillReturnRows(sqlmock.NewRows([]string{"acquired"}).AddRow(true))
	entered, release, demoted, done := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	elector := New(db, 728379, time.Second, time.Hour,
		func(context.Context) { close(entered); <-release },
		func() { close(demoted) },
	)
	go func() { elector.runOnce(ctx); close(done) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("startup callback did not run")
	}
	cancel()
	select {
	case <-demoted:
		t.Error("demotion ran before startup callback completed")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("election did not stop")
	}
	select {
	case <-demoted:
	default:
		t.Fatal("demotion never ran")
	}
}

// Keep both pools open: sql.Conn.Close alone must not strand a session lock.
func TestElector_CancellationReleasesAdvisoryLock(t *testing.T) {
	dsn := os.Getenv("CRONLITE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("CRONLITE_TEST_DATABASE_URL is required")
	}
	owner, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := owner.Close(); err != nil {
			t.Errorf("close owner database: %v", err)
		}
	}()
	owner.SetMaxOpenConns(1)
	owner.SetMaxIdleConns(1)
	observer, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := observer.Close(); err != nil {
			t.Errorf("close observer database: %v", err)
		}
	}()
	lockKey := time.Now().UnixNano()
	for epoch := 0; epoch < 10; epoch++ {
		ctx, cancel := context.WithCancel(context.Background())
		elected, done := make(chan struct{}), make(chan struct{})
		elector := New(owner, lockKey, time.Second, time.Hour, func(context.Context) { close(elected) }, func() {})
		go func() { elector.runOnce(ctx); close(done) }()
		select {
		case <-elected:
		case <-time.After(3 * time.Second):
			cancel()
			t.Fatal("lock not acquired")
		}
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("election did not stop")
		}
		// Closing a client socket does not await PostgreSQL's processing of EOF.
		var acquired bool
		deadline := time.Now().Add(time.Second)
		for !acquired && time.Now().Before(deadline) {
			if err := observer.QueryRow("SELECT pg_try_advisory_lock($1)", lockKey).Scan(&acquired); err != nil {
				t.Fatal(err)
			}
			if !acquired {
				time.Sleep(10 * time.Millisecond)
			}
		}
		if !acquired {
			t.Fatalf("epoch %d: stopped elector retained advisory lock; pool=%+v", epoch, owner.Stats())
		}
		if _, err := observer.Exec("SELECT pg_advisory_unlock($1)", lockKey); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("owner pool after 10 epochs: %+v", owner.Stats())
}
