package leaderelection

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// mockMetrics records calls to MetricsSink for verification.
type mockMetrics struct {
	mu       sync.Mutex
	calls    []string
	acquired int
	losses   map[string]int
}

func newMockMetrics() *mockMetrics {
	return &mockMetrics{losses: make(map[string]int)}
}

func (m *mockMetrics) LeaderStatusChanged(isLeader bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if isLeader {
		m.calls = append(m.calls, "status:true")
	} else {
		m.calls = append(m.calls, "status:false")
	}
}

func (m *mockMetrics) LeaderAcquired() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.acquired++
	m.calls = append(m.calls, "acquired")
}

func (m *mockMetrics) LeaderLost(reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.losses[reason]++
	m.calls = append(m.calls, "lost:"+reason)
}

func (m *mockMetrics) getCalls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]string, len(m.calls))
	copy(result, m.calls)
	return result
}

func (m *mockMetrics) getLossCount(reason string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.losses[reason]
}

func newMockDB(t *testing.T) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	return db, mock
}

func expectTCPKeepalive(mock sqlmock.Sqlmock) {
	mock.ExpectExec("SET tcp_keepalives_idle").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("SET tcp_keepalives_interval").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("SET tcp_keepalives_count").WillReturnResult(sqlmock.NewResult(0, 0))
}

func TestElector_AcquiresLock_CallsBothCallbacks(t *testing.T) {
	db, mock := newMockDB(t)
	defer db.Close()

	expectTCPKeepalive(mock)
	rows := sqlmock.NewRows([]string{"pg_try_advisory_lock"}).AddRow(true)
	mock.ExpectQuery("SELECT pg_try_advisory_lock").WithArgs(int64(728379)).WillReturnRows(rows)

	var electedCalled, demotedCalled bool
	var mu sync.Mutex

	// Use a long heartbeat so ctx.Done() fires before the first ping tick.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	elector := New(db, 728379, time.Second, 500*time.Millisecond,
		func(leaderCtx context.Context) {
			mu.Lock()
			electedCalled = true
			mu.Unlock()
			<-leaderCtx.Done()
		},
		func() {
			mu.Lock()
			demotedCalled = true
			mu.Unlock()
		},
	)

	reason := elector.runOnce(ctx)

	if reason != "shutdown" {
		t.Errorf("expected reason 'shutdown', got %q", reason)
	}

	mu.Lock()
	defer mu.Unlock()
	if !electedCalled {
		t.Error("onElected was not called")
	}
	if !demotedCalled {
		t.Error("onDemoted was not called")
	}
}

func TestElector_LockHeldByAnother(t *testing.T) {
	db, mock := newMockDB(t)
	defer db.Close()

	expectTCPKeepalive(mock)
	rows := sqlmock.NewRows([]string{"pg_try_advisory_lock"}).AddRow(false)
	mock.ExpectQuery("SELECT pg_try_advisory_lock").WithArgs(int64(728379)).WillReturnRows(rows)

	var electedCalled, demotedCalled bool

	elector := New(db, 728379, time.Second, time.Second,
		func(ctx context.Context) { electedCalled = true },
		func() { demotedCalled = true },
	)

	reason := elector.runOnce(context.Background())

	if reason != "" {
		t.Errorf("expected empty reason, got %q", reason)
	}
	if electedCalled {
		t.Error("onElected should not be called when lock is held by another")
	}
	if demotedCalled {
		t.Error("onDemoted should not be called when lock is held by another")
	}
}

func TestElector_ConnFailure_EmitsErrorMetric(t *testing.T) {
	db, _ := newMockDB(t)
	db.Close() // Close DB to force Conn() failure

	metrics := newMockMetrics()

	elector := New(db, 728379, time.Second, time.Second,
		func(ctx context.Context) {},
		func() {},
	)
	elector.metrics = metrics

	reason := elector.runOnce(context.Background())

	if reason != "" {
		t.Errorf("expected empty reason, got %q", reason)
	}
	if metrics.getLossCount("error") != 1 {
		t.Errorf("expected 1 error metric, got %d", metrics.getLossCount("error"))
	}
}

func TestElector_QueryFailure_EmitsErrorMetric(t *testing.T) {
	db, mock := newMockDB(t)
	defer db.Close()

	expectTCPKeepalive(mock)
	mock.ExpectQuery("SELECT pg_try_advisory_lock").
		WithArgs(int64(728379)).
		WillReturnError(errors.New("connection reset"))

	metrics := newMockMetrics()

	elector := New(db, 728379, time.Second, time.Second,
		func(ctx context.Context) {},
		func() {},
	)
	elector.metrics = metrics

	reason := elector.runOnce(context.Background())

	if reason != "" {
		t.Errorf("expected empty reason, got %q", reason)
	}
	if metrics.getLossCount("error") != 1 {
		t.Errorf("expected 1 error metric, got %d", metrics.getLossCount("error"))
	}
}

func TestElector_PingFailure_ReturnsConnLost(t *testing.T) {
	db, mock := newMockDB(t)
	defer db.Close()

	expectTCPKeepalive(mock)
	rows := sqlmock.NewRows([]string{"pg_try_advisory_lock"}).AddRow(true)
	mock.ExpectQuery("SELECT pg_try_advisory_lock").WithArgs(int64(728379)).WillReturnRows(rows)
	// First ping fails immediately — simulates dead connection.
	mock.ExpectPing().WillReturnError(errors.New("broken pipe"))

	demotedCalled := false

	elector := New(db, 728379, time.Second, 10*time.Millisecond,
		func(leaderCtx context.Context) { <-leaderCtx.Done() },
		func() { demotedCalled = true },
	)

	reason := elector.runOnce(context.Background())

	if reason != "conn_lost" {
		t.Errorf("expected reason 'conn_lost', got %q", reason)
	}
	if !demotedCalled {
		t.Error("onDemoted should be called after connection loss")
	}
}

func TestElector_ContextCancellation_ReturnsShutdown(t *testing.T) {
	db, mock := newMockDB(t)
	defer db.Close()

	expectTCPKeepalive(mock)
	rows := sqlmock.NewRows([]string{"pg_try_advisory_lock"}).AddRow(true)
	mock.ExpectQuery("SELECT pg_try_advisory_lock").WithArgs(int64(728379)).WillReturnRows(rows)
	// No pings expected — heartbeat interval is long, context cancels first.

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	elector := New(db, 728379, time.Second, 500*time.Millisecond,
		func(leaderCtx context.Context) { <-leaderCtx.Done() },
		func() {},
	)

	reason := elector.runOnce(ctx)

	if reason != "shutdown" {
		t.Errorf("expected reason 'shutdown', got %q", reason)
	}
}

func TestElector_MetricsRecorded_FullSequence(t *testing.T) {
	db, mock := newMockDB(t)
	defer db.Close()

	expectTCPKeepalive(mock)
	rows := sqlmock.NewRows([]string{"pg_try_advisory_lock"}).AddRow(true)
	mock.ExpectQuery("SELECT pg_try_advisory_lock").WithArgs(int64(728379)).WillReturnRows(rows)

	metrics := newMockMetrics()

	// Long heartbeat so ctx.Done() fires first → clean "shutdown" reason.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	elector := New(db, 728379, time.Second, 500*time.Millisecond,
		func(leaderCtx context.Context) { <-leaderCtx.Done() },
		func() {},
	)
	elector.metrics = metrics

	elector.runOnce(ctx)

	calls := metrics.getCalls()

	// Expected: status:true → acquired → status:false → lost:shutdown
	expected := []string{"status:true", "acquired", "status:false", "lost:shutdown"}
	if len(calls) != len(expected) {
		t.Fatalf("expected %d metric calls, got %d: %v", len(expected), len(calls), calls)
	}
	for i, want := range expected {
		if calls[i] != want {
			t.Errorf("metric call[%d]: want %q, got %q", i, want, calls[i])
		}
	}
}

func TestElector_OnDemotedPanic_Recovered(t *testing.T) {
	db, mock := newMockDB(t)
	defer db.Close()

	expectTCPKeepalive(mock)
	rows := sqlmock.NewRows([]string{"pg_try_advisory_lock"}).AddRow(true)
	mock.ExpectQuery("SELECT pg_try_advisory_lock").WithArgs(int64(728379)).WillReturnRows(rows)

	metrics := newMockMetrics()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	elector := New(db, 728379, time.Second, 500*time.Millisecond,
		func(leaderCtx context.Context) { <-leaderCtx.Done() },
		func() { panic("simulated crash in onDemoted") },
	)
	elector.metrics = metrics

	reason := elector.runOnce(ctx)

	// Process should NOT have crashed. Panic overrides the reason.
	if reason != "panic" {
		t.Errorf("expected reason 'panic', got %q", reason)
	}
	if metrics.getLossCount("panic") != 1 {
		t.Errorf("expected 1 panic loss metric, got %d", metrics.getLossCount("panic"))
	}
}
