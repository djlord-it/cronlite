package postgres

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/djlord-it/cronlite/internal/dispatcher"
	"github.com/djlord-it/cronlite/internal/domain"
	"github.com/google/uuid"
)

type ownershipFixture struct {
	db        *sql.DB
	store     *Store
	execution domain.Execution
	event     domain.TriggerEvent
}

func newOwnershipFixture(t *testing.T, ttl time.Duration, endpoint string) ownershipFixture {
	t.Helper()
	dsn := os.Getenv("CRONLITE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("CRONLITE_TEST_DATABASE_URL required for real PostgreSQL ownership tests")
	}
	base, cleanup := openIsolatedPostgresSchema(t, dsn)
	t.Cleanup(cleanup)
	var schema string
	if err := base.QueryRow("SELECT current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db, err := sql.Open("postgres", u.String())
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(8)
	t.Cleanup(func() { db.Close() })
	store := New(db, 5*time.Second).WithExecutionLease(ttl)
	now := time.Now().UTC()
	schedule := domain.Schedule{ID: uuid.New(), CronExpression: "* * * * *", Timezone: "UTC", CreatedAt: now, UpdatedAt: now}
	job := domain.Job{ID: uuid.New(), Namespace: "ownership", Name: "ownership", Enabled: true, ScheduleID: schedule.ID, Delivery: domain.DeliveryConfig{Type: domain.DeliveryTypeWebhook, WebhookURL: endpoint, Secret: "ownership-secret", Timeout: 120 * time.Second}, Analytics: domain.AnalyticsConfig{RetentionSeconds: domain.DefaultRetentionSeconds}, CreatedAt: now, UpdatedAt: now}
	if err := store.CreateJob(context.Background(), job, schedule); err != nil {
		t.Fatal(err)
	}
	execution := domain.Execution{ID: uuid.New(), JobID: job.ID, Namespace: job.Namespace, TriggerType: domain.TriggerTypeManual, ScheduledAt: now, FiredAt: now, CreatedAt: now, Status: domain.ExecutionStatusEmitted}
	if err := store.InsertExecution(context.Background(), execution); err != nil {
		t.Fatal(err)
	}
	return ownershipFixture{db, store, execution, domain.TriggerEvent{ExecutionID: execution.ID, JobID: job.ID, Namespace: job.Namespace, ScheduledAt: now, FiredAt: now, CreatedAt: now}}
}

func (f ownershipFixture) claim(t *testing.T) domain.Execution {
	t.Helper()
	execution, err := f.store.DequeueExecution(context.Background())
	if err != nil || execution == nil {
		t.Fatalf("claim=%v error=%v", execution, err)
	}
	return *execution
}

func ownershipEventually(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(time.Millisecond)
	}
}

func (f ownershipFixture) expire(t *testing.T) {
	t.Helper()
	if _, err := f.db.Exec("UPDATE executions SET lease_until=clock_timestamp()-INTERVAL '1 second' WHERE id=$1", f.execution.ID); err != nil {
		t.Fatal(err)
	}
	n, err := f.store.RequeueExpiredExecutions(context.Background(), 1)
	if err != nil || n != 1 {
		t.Fatalf("requeue=%d error=%v", n, err)
	}
}

func TestOwnershipConcurrentClaimsIntegration(t *testing.T) {
	for _, mode := range []string{"single", "batch", "direct"} {
		t.Run(mode, func(t *testing.T) {
			f := newOwnershipFixture(t, time.Minute, "https://example.com")
			if mode == "direct" {
				if claim, err := f.store.ClaimExecution(context.Background(), f.execution.ID, "wrong"); err != nil || claim != nil {
					t.Fatalf("namespace crossed: %v %v", claim, err)
				}
			}
			var wg sync.WaitGroup
			var claimed atomic.Int64
			start := make(chan struct{})
			for i := 0; i < 24; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					switch mode {
					case "single":
						e, err := f.store.DequeueExecution(context.Background())
						if err != nil {
							t.Error(err)
						}
						if e != nil {
							claimed.Add(1)
						}
					case "batch":
						es, err := f.store.DequeueExecutions(context.Background(), 3)
						if err != nil {
							t.Error(err)
						}
						claimed.Add(int64(len(es)))
					case "direct":
						e, err := f.store.ClaimExecution(context.Background(), f.execution.ID, f.execution.Namespace)
						if err != nil {
							t.Error(err)
						}
						if e != nil {
							claimed.Add(1)
						}
					}
				}()
			}
			close(start)
			wg.Wait()
			if claimed.Load() != 1 {
				t.Fatalf("competing claims: %d", claimed.Load())
			}
		})
	}
}

func TestOwnershipStaleCompletionAndClockSkewIntegration(t *testing.T) {
	f := newOwnershipFixture(t, time.Minute, "https://example.com")
	old := f.claim(t)
	if old.ClaimGeneration != 1 || old.ClaimedAt == nil || old.LeaseUntil == nil {
		t.Fatalf("ownership missing: %+v", old)
	}
	// An arbitrary caller clock cannot make a valid PostgreSQL lease stale.
	n, err := f.store.RequeueStaleExecutions(context.Background(), time.Now().Add(100*365*24*time.Hour), 1)
	if err != nil || n != 0 {
		t.Fatalf("clock skew reclaimed live owner: %d %v", n, err)
	}
	f.expire(t)
	current := f.claim(t)
	if current.ClaimGeneration <= old.ClaimGeneration {
		t.Fatal("generation reused")
	}
	for _, status := range []domain.ExecutionStatus{domain.ExecutionStatusFailed, domain.ExecutionStatusDelivered} {
		if err := f.store.CompleteExecution(context.Background(), old, status); !errors.Is(err, domain.ErrExecutionClaimLost) {
			t.Fatalf("old completion=%v", err)
		}
	}
	if err := f.store.RenewExecutionClaim(context.Background(), old); !errors.Is(err, domain.ErrExecutionClaimLost) {
		t.Fatalf("old renewal=%v", err)
	}
	if err := f.store.UpdateExecutionStatus(context.Background(), old.ID, domain.ExecutionStatusFailed); !errors.Is(err, domain.ErrExecutionClaimLost) {
		t.Fatalf("unfenced completion=%v", err)
	}
	if err := f.store.CompleteExecution(context.Background(), current, domain.ExecutionStatusDelivered); err != nil {
		t.Fatal(err)
	}
}

func TestOwnershipExpiryCheckedAfterLockWaitIntegration(t *testing.T) {
	for _, operation := range []string{"renew", "complete"} {
		t.Run(operation, func(t *testing.T) {
			f := newOwnershipFixture(t, 200*time.Millisecond, "https://example.com")
			owner := f.claim(t)
			tx, err := f.db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback() }()
			if _, err := tx.Exec("SELECT id FROM executions WHERE id=$1 FOR UPDATE", owner.ID); err != nil {
				t.Fatal(err)
			}
			result := make(chan error, 1)
			query := queryRenewExecutionClaim
			if operation == "renew" {
				go func() { result <- f.store.RenewExecutionClaim(context.Background(), owner) }()
			} else {
				query = queryCompleteExecution
				go func() {
					result <- f.store.CompleteExecution(context.Background(), owner, domain.ExecutionStatusDelivered)
				}()
			}
			ownershipEventually(t, time.Second, func() bool {
				var waiting bool
				err := f.db.QueryRow("SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE wait_event_type='Lock' AND query=$1)", query).Scan(&waiting)
				if err != nil {
					t.Fatal(err)
				}
				return waiting
			})
			ownershipEventually(t, time.Second, func() bool {
				var expired bool
				err := f.db.QueryRow("SELECT lease_until<=clock_timestamp() FROM executions WHERE id=$1", owner.ID).Scan(&expired)
				if err != nil {
					t.Fatal(err)
				}
				return expired
			})
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			if err := <-result; !errors.Is(err, domain.ErrExecutionClaimLost) {
				t.Fatalf("expired ownership resurrected: %v", err)
			}
			var expiry time.Time
			if err := f.db.QueryRow("SELECT lease_until FROM executions WHERE id=$1", owner.ID).Scan(&expiry); err != nil {
				t.Fatal(err)
			}
			if !expiry.Equal(*owner.LeaseUntil) {
				t.Fatal("expiry changed after lock wait")
			}
		})
	}
}

func TestOwnershipClaimUsesFreshTransactionTimeIntegration(t *testing.T) {
	f := newOwnershipFixture(t, 80*time.Millisecond, "https://example.com")
	tx, err := f.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	var began time.Time
	if err := tx.QueryRow("SELECT now()").Scan(&began); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	owner, err := scanExecution(tx.QueryRow(queryDequeueExecution, f.store.leaseDuration.Milliseconds()), true)
	if err != nil {
		t.Fatal(err)
	}
	if owner.LeaseUntil.Sub(began) < 160*time.Millisecond {
		t.Fatal("claim used stale transaction clock")
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestOwnershipLegacyMigrationAndRecoveryIntegration(t *testing.T) {
	f := newOwnershipFixture(t, time.Minute, "https://example.com")
	if _, err := f.db.Exec("UPDATE executions SET status='in_progress', claimed_at=NULL WHERE id=$1", f.execution.ID); err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../../../schema/011_execution_ownership.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(string(migration)); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	n, err := f.store.RequeueExpiredExecutions(context.Background(), 1)
	if err != nil || n != 1 {
		t.Fatalf("legacy recovery: %d %v", n, err)
	}
	orphans, err := f.store.GetOrphanedExecutions(context.Background(), time.Now().Add(-19*time.Minute), 1)
	if err != nil || len(orphans) != 1 {
		t.Fatalf("channel legacy recovery delayed: %v %v", orphans, err)
	}
	owner := f.claim(t)
	if owner.ClaimGeneration < 1 {
		t.Fatal("legacy claim unfenced")
	}
	if err := f.store.CompleteExecution(context.Background(), owner, domain.ExecutionStatusDelivered); err != nil {
		t.Fatal(err)
	}
	n, err = f.store.RequeueExpiredExecutions(context.Background(), 1)
	if err != nil || n != 0 {
		t.Fatal("terminal row recovered")
	}
}

func TestOwnershipPoolContentionLatencyIntegration(t *testing.T) {
	f := newOwnershipFixture(t, domain.DefaultExecutionLease, "https://example.com")
	owner := f.claim(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var timings []time.Duration
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				start := time.Now()
				err := f.store.RenewExecutionClaim(context.Background(), owner)
				elapsed := time.Since(start)
				if err != nil {
					t.Error(err)
				}
				mu.Lock()
				timings = append(timings, elapsed)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	sort.Slice(timings, func(i, j int) bool { return timings[i] < timings[j] })
	t.Logf("640 serialized renewals, 32 callers, 8 connections: p50=%s p95=%s p99=%s max=%s", timings[len(timings)/2], timings[len(timings)*95/100], timings[len(timings)*99/100], timings[len(timings)-1])
}

func verifyOwnershipHTTP(t *testing.T, r *http.Request) {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Error(err)
	}
	if !dispatcher.VerifySignature("ownership-secret", body, r.Header.Get("X-CronLite-Signature")) {
		t.Error("HMAC changed")
	}
	if r.Header.Get("X-CronLite-Execution-ID") == "" || r.Header.Get("X-CronLite-Event-ID") == "" {
		t.Error("identity missing")
	}
}

func TestOwnershipConcurrentHTTPDispatchIntegration(t *testing.T) {
	arrived := make(chan struct{}, 1)
	release := make(chan struct{})
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		verifyOwnershipHTTP(t, r)
		calls.Add(1)
		arrived <- struct{}{}
		<-release
		w.WriteHeader(200)
	}))
	defer server.Close()
	f := newOwnershipFixture(t, time.Minute, server.URL)
	d := dispatcher.New(f.store, dispatcher.NewHTTPWebhookSender())
	done := make(chan error, 2)
	go func() { done <- d.Dispatch(context.Background(), f.event) }()
	<-arrived
	go func() { done <- d.Dispatch(context.Background(), f.event) }()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("competing HTTP claims")
	}
}

func TestOwnershipLongHTTPRenewalAndShutdownIntegration(t *testing.T) {
	started := make(chan struct{})
	stopped := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		verifyOwnershipHTTP(t, r)
		close(started)
		<-r.Context().Done()
		close(stopped)
	}))
	defer server.Close()
	f := newOwnershipFixture(t, 240*time.Millisecond, server.URL)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- dispatcher.New(f.store, dispatcher.NewHTTPWebhookSender()).Dispatch(ctx, f.event) }()
	<-started
	var initial time.Time
	if err := f.db.QueryRow("SELECT lease_until FROM executions WHERE id=$1", f.execution.ID).Scan(&initial); err != nil {
		t.Fatal(err)
	}
	time.Sleep(600 * time.Millisecond) // several full leases while the request stays active
	n, err := f.store.RequeueExpiredExecutions(context.Background(), 1)
	if err != nil || n != 0 {
		t.Fatal("live long webhook reclaimed")
	}
	var renewed time.Time
	if err := f.db.QueryRow("SELECT lease_until FROM executions WHERE id=$1", f.execution.ID).Scan(&renewed); err != nil {
		t.Fatal(err)
	}
	if !renewed.After(initial) {
		t.Fatal("long webhook did not renew")
	}
	cancel()
	if err := <-done; err == nil {
		t.Fatal("cancellation reported success")
	}
	<-stopped
	ownershipEventually(t, time.Second, func() bool {
		n, err := f.store.RequeueExpiredExecutions(context.Background(), 1)
		if err != nil {
			t.Fatal(err)
		}
		return n == 1
	})
	if owner := f.claim(t); owner.ClaimGeneration < 2 {
		t.Fatal("cancelled generation reused")
	}
}

func TestOwnershipOldHTTPResultCannotCompleteNewClaimIntegration(t *testing.T) {
	for _, oldStatus := range []int{200, 400} {
		t.Run(http.StatusText(oldStatus), func(t *testing.T) {
			oldArrived, newArrived := make(chan struct{}), make(chan struct{})
			oldRelease, newRelease := make(chan struct{}), make(chan struct{})
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				verifyOwnershipHTTP(t, r)
				if calls.Add(1) == 1 {
					close(oldArrived)
					<-oldRelease
					w.WriteHeader(oldStatus)
				} else {
					close(newArrived)
					<-newRelease
					w.WriteHeader(200)
				}
			}))
			defer server.Close()
			f := newOwnershipFixture(t, time.Minute, server.URL)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			oldDone, newDone := make(chan struct{}), make(chan struct{})
			oldCompleted := make(chan error, 1)
			go func() {
				dispatcher.New(ownershipObservedCompletionStore{f.store, oldCompleted}, dispatcher.NewHTTPWebhookSender()).RunDBPoll(ctx, time.Millisecond, 1)
				close(oldDone)
			}()
			<-oldArrived
			f.expire(t)
			go func() {
				dispatcher.New(f.store, dispatcher.NewHTTPWebhookSender()).RunDBPoll(ctx, time.Millisecond, 1)
				close(newDone)
			}()
			<-newArrived
			close(oldRelease)
			if err := <-oldCompleted; !errors.Is(err, domain.ErrExecutionClaimLost) {
				t.Fatalf("old HTTP completion accepted: %v", err)
			}
			ownershipEventually(t, time.Second, func() bool {
				var n int
				err := f.db.QueryRow("SELECT COUNT(*) FROM delivery_attempts WHERE execution_id=$1 AND claim_generation=1", f.execution.ID).Scan(&n)
				if err != nil {
					t.Fatal(err)
				}
				return n == 1
			})
			state, err := f.store.GetExecutionScoped(context.Background(), f.execution.ID, f.execution.Namespace)
			if err != nil || state.Status != domain.ExecutionStatusInProgress {
				t.Fatalf("old HTTP result mutated new claim: %+v %v", state, err)
			}
			close(newRelease)
			ownershipEventually(t, time.Second, func() bool {
				state, err := f.store.GetExecutionScoped(context.Background(), f.execution.ID, f.execution.Namespace)
				if err != nil {
					t.Fatal(err)
				}
				return state.Status == domain.ExecutionStatusDelivered
			})
			cancel()
			<-oldDone
			<-newDone
		})
	}
}

type ownershipObservedCompletionStore struct {
	*Store
	completed chan error
}

func (s ownershipObservedCompletionStore) CompleteExecution(ctx context.Context, e domain.Execution, status domain.ExecutionStatus) error {
	err := s.Store.CompleteExecution(ctx, e, status)
	s.completed <- err
	return err
}

type ownershipLostCompletionStore struct{ *Store }

func (s ownershipLostCompletionStore) CompleteExecution(context.Context, domain.Execution, domain.ExecutionStatus) error {
	return errors.New("injected lost terminal persistence")
}

func TestOwnershipHTTP200PersistenceGapRemainsAtLeastOnceIntegration(t *testing.T) {
	var calls atomic.Int64
	var idsMu sync.Mutex
	var executionIDs, attemptIDs []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		verifyOwnershipHTTP(t, r)
		calls.Add(1)
		idsMu.Lock()
		executionIDs = append(executionIDs, r.Header.Get("X-CronLite-Execution-ID"))
		attemptIDs = append(attemptIDs, r.Header.Get("X-CronLite-Event-ID"))
		idsMu.Unlock()
		w.WriteHeader(200)
	}))
	defer server.Close()
	f := newOwnershipFixture(t, time.Minute, server.URL)
	if err := dispatcher.New(ownershipLostCompletionStore{f.store}, dispatcher.NewHTTPWebhookSender()).Dispatch(context.Background(), f.event); err == nil {
		t.Fatal("expected injected persistence failure")
	}
	f.expire(t)
	if err := dispatcher.New(f.store, dispatcher.NewHTTPWebhookSender()).Dispatch(context.Background(), f.event); err != nil {
		t.Fatal(err)
	}
	idsMu.Lock()
	defer idsMu.Unlock()
	if calls.Load() != 2 || executionIDs[0] != executionIDs[1] || attemptIDs[0] == attemptIDs[1] {
		t.Fatal("at-least-once identity changed")
	}
	attempts, err := f.store.GetAttempts(context.Background(), f.execution.ID)
	if err != nil || len(attempts) != 2 {
		t.Fatalf("attempt history: %v %v", attempts, err)
	}
	seen := map[int64]bool{}
	for _, attempt := range attempts {
		seen[attempt.ClaimGeneration] = true
	}
	if !seen[1] || !seen[2] {
		t.Fatal("claim generation not returned in audit evidence")
	}
}

func TestOwnershipRenewalStalledInPostgresCancelsHTTPIntegration(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		verifyOwnershipHTTP(t, r)
		close(started)
		<-r.Context().Done()
		close(cancelled)
	}))
	defer server.Close()
	f := newOwnershipFixture(t, 400*time.Millisecond, server.URL)
	done := make(chan error, 1)
	go func() {
		done <- dispatcher.New(f.store, dispatcher.NewHTTPWebhookSender()).Dispatch(context.Background(), f.event)
	}()
	<-started
	tx, err := f.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec("SELECT id FROM executions WHERE id=$1 FOR UPDATE", f.execution.ID); err != nil {
		t.Fatal(err)
	}
	ownershipEventually(t, time.Second, func() bool {
		var waiting bool
		err := f.db.QueryRow("SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE query=$1 AND wait_event_type='Lock')", queryRenewExecutionClaim).Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		return waiting
	})
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("stalled SQL prevented HTTP cancellation")
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("stalled ownership reported success")
		}
	case <-time.After(time.Second):
		t.Fatal("cooperative PostgreSQL call did not stop")
	}
	n, err := f.store.RequeueExpiredExecutions(context.Background(), 1)
	if err != nil || n != 1 {
		t.Fatalf("stalled execution unrecoverable: %d %v", n, err)
	}
}

func TestOwnershipRenewCompleteReclaimRaceIntegration(t *testing.T) {
	f := newOwnershipFixture(t, time.Minute, "https://example.com")
	for i := 0; i < 40; i++ {
		if i > 0 {
			f.execution.ID = uuid.New()
			f.execution.ScheduledAt = time.Now()
			if err := f.store.InsertExecution(context.Background(), f.execution); err != nil {
				t.Fatal(err)
			}
		}
		owner := f.claim(t)
		if _, err := f.db.Exec("UPDATE executions SET lease_until=clock_timestamp()+INTERVAL '10 milliseconds' WHERE id=$1", owner.ID); err != nil {
			t.Fatal(err)
		}
		time.Sleep(8 * time.Millisecond)
		var wg sync.WaitGroup
		var renewed, completed, requeueErr error
		var requeued int
		start := make(chan struct{})
		wg.Add(3)
		go func() { defer wg.Done(); <-start; renewed = f.store.RenewExecutionClaim(context.Background(), owner) }()
		go func() {
			defer wg.Done()
			<-start
			completed = f.store.CompleteExecution(context.Background(), owner, domain.ExecutionStatusDelivered)
		}()
		go func() {
			defer wg.Done()
			<-start
			requeued, requeueErr = f.store.RequeueExpiredExecutions(context.Background(), 1)
		}()
		close(start)
		wg.Wait()
		if requeueErr != nil {
			t.Fatal(requeueErr)
		}
		if requeued == 1 && (renewed == nil || completed == nil) {
			t.Fatalf("expired and valid ownership both won: renew=%v complete=%v", renewed, completed)
		}
		state, err := f.store.GetExecutionScoped(context.Background(), owner.ID, owner.Namespace)
		if err != nil {
			t.Fatal(err)
		}
		if completed == nil && state.Status != domain.ExecutionStatusDelivered {
			t.Fatal("completion overwritten")
		}
		if state.Status != domain.ExecutionStatusDelivered {
			if state.Status == domain.ExecutionStatusInProgress {
				f.expire(t)
			}
			current := f.claim(t)
			if err := f.store.CompleteExecution(context.Background(), current, domain.ExecutionStatusDelivered); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestOwnershipMigrationPreservesPopulatedLegacyTablesIntegration(t *testing.T) {
	f := newOwnershipFixture(t, time.Minute, "https://example.com")
	if _, err := f.db.Exec("UPDATE executions SET status='delivered', acknowledged_at=clock_timestamp() WHERE id=$1", f.execution.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.store.InsertDeliveryAttempt(context.Background(), domain.DeliveryAttempt{ID: uuid.New(), ExecutionID: f.execution.ID, Attempt: 1, StatusCode: 200, StartedAt: time.Now(), FinishedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	for i, status := range []domain.ExecutionStatus{domain.ExecutionStatusEmitted, domain.ExecutionStatusInProgress} {
		execution := f.execution
		execution.ID = uuid.New()
		execution.ScheduledAt = execution.ScheduledAt.Add(time.Duration(i+1) * time.Second)
		execution.Status = status
		if err := f.store.InsertExecution(context.Background(), execution); err != nil {
			t.Fatal(err)
		}
	}
	// Restore just this isolated populated fixture to the actual pre-011 schema.
	if _, err := f.db.Exec("ALTER TABLE executions DROP COLUMN claim_generation, DROP COLUMN lease_until; ALTER TABLE delivery_attempts DROP COLUMN claim_generation"); err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../../../schema/011_execution_ownership.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(string(migration)); err != nil {
		t.Fatal(err)
	}
	var defaulted int
	if err := f.db.QueryRow("SELECT count(*) FROM executions WHERE claim_generation=0 AND lease_until IS NULL").Scan(&defaulted); err != nil || defaulted != 3 {
		t.Fatalf("legacy data changed: %d %v", defaulted, err)
	}
	terminal, err := f.store.GetExecutionScoped(context.Background(), f.execution.ID, f.execution.Namespace)
	if err != nil || terminal.Status != domain.ExecutionStatusDelivered || terminal.AcknowledgedAt == nil {
		t.Fatal("legacy terminal/ack changed")
	}
	attempts, err := f.store.GetAttempts(context.Background(), f.execution.ID)
	if err != nil || len(attempts) != 1 || attempts[0].ClaimGeneration != 0 || attempts[0].StatusCode != 200 {
		t.Fatal("legacy evidence changed")
	}
	n, err := f.store.RequeueExpiredExecutions(context.Background(), 1)
	if err != nil || n != 1 {
		t.Fatal("legacy active row not recovered")
	}
}

type ownershipDelayedRenewStore struct {
	*Store
	calls     atomic.Int64
	committed chan struct{}
	release   chan struct{}
}

func (s *ownershipDelayedRenewStore) RenewExecutionClaim(ctx context.Context, e domain.Execution) error {
	err := s.Store.RenewExecutionClaim(ctx, e)
	if err == nil && s.calls.Add(1) == 2 {
		close(s.committed)
		<-s.release
	}
	return err
}

func TestOwnershipCommittedRenewalWithDelayedResponseCannotReviveIntegration(t *testing.T) {
	started, cancelled := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		verifyOwnershipHTTP(t, r)
		close(started)
		<-r.Context().Done()
		close(cancelled)
	}))
	defer server.Close()
	f := newOwnershipFixture(t, time.Second, server.URL)
	store := &ownershipDelayedRenewStore{Store: f.store, committed: make(chan struct{}), release: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		done <- dispatcher.New(store, dispatcher.NewHTTPWebhookSender()).Dispatch(context.Background(), f.event)
	}()
	<-started
	<-store.committed
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("unacknowledged renewal extended local work")
	}
	var leaseStillLive bool
	if err := f.db.QueryRow("SELECT lease_until>clock_timestamp() FROM executions WHERE id=$1", f.execution.ID).Scan(&leaseStillLive); err != nil || !leaseStillLive {
		t.Fatal("renewal commit was not observed", err)
	}
	close(store.release)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("late response revived work")
		}
	case <-time.After(time.Second):
		t.Fatal("late response cleanup stuck")
	}
	ownershipEventually(t, time.Second, func() bool {
		n, err := f.store.RequeueExpiredExecutions(context.Background(), 1)
		if err != nil {
			t.Fatal(err)
		}
		return n == 1
	})
}
