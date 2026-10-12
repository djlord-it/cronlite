package dispatcher

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/djlord-it/cronlite/internal/circuitbreaker"
	"github.com/djlord-it/cronlite/internal/domain"
	"github.com/google/uuid"
)

type ownershipTestStore struct {
	*mockStore
	ttl        time.Duration
	claimDelay time.Duration
	renew      func(context.Context, domain.Execution) error
	insert     func(context.Context, domain.DeliveryAttempt) error
}

func (s *ownershipTestStore) ExecutionLeaseDuration() time.Duration { return s.ttl }
func (s *ownershipTestStore) ClaimExecution(ctx context.Context, id uuid.UUID, ns domain.Namespace) (*domain.Execution, error) {
	time.Sleep(s.claimDelay)
	return s.mockStore.ClaimExecution(ctx, id, ns)
}
func (s *ownershipTestStore) RenewExecutionClaim(ctx context.Context, e domain.Execution) error {
	if s.renew != nil {
		return s.renew(ctx, e)
	}
	return s.mockStore.RenewExecutionClaim(ctx, e)
}
func (s *ownershipTestStore) InsertDeliveryAttempt(ctx context.Context, a domain.DeliveryAttempt) error {
	if s.insert != nil {
		return s.insert(ctx, a)
	}
	return s.mockStore.InsertDeliveryAttempt(ctx, a)
}

type ownershipTestSender func(context.Context, WebhookRequest) WebhookResult

func (s ownershipTestSender) Send(ctx context.Context, req WebhookRequest) WebhookResult {
	return s(ctx, req)
}

type ownershipTestAnalytics func(context.Context)

func (s ownershipTestAnalytics) Record(ctx context.Context, _ domain.TriggerEvent, _ domain.AnalyticsConfig) {
	s(ctx)
}

func ownershipTestSetup(ttl time.Duration) (*ownershipTestStore, domain.TriggerEvent) {
	store := &ownershipTestStore{mockStore: newMockStore(), ttl: ttl}
	jobID := uuid.New()
	store.addJob(domain.Job{ID: jobID, Namespace: "test", Delivery: domain.DeliveryConfig{WebhookURL: "https://example.com"}, Analytics: domain.AnalyticsConfig{Enabled: true}})
	return store, domain.TriggerEvent{ExecutionID: uuid.New(), JobID: jobID, Namespace: "test", ScheduledAt: time.Now()}
}
func ownershipWait(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("ownership gate not reached")
	}
}
func ownershipResult(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(time.Second):
		t.Fatal("ownership work did not return")
		return nil
	}
}

func TestOwnershipDelayedClaimCannotSend(t *testing.T) {
	store, event := ownershipTestSetup(40 * time.Millisecond)
	store.claimDelay = 80 * time.Millisecond
	var sends atomic.Int64
	err := New(store, ownershipTestSender(func(context.Context, WebhookRequest) WebhookResult {
		sends.Add(1)
		return WebhookResult{StatusCode: 200}
	})).Dispatch(context.Background(), event)
	if !errors.Is(err, domain.ErrExecutionClaimLost) || sends.Load() != 0 {
		t.Fatalf("late claim admitted: error=%v sends=%d", err, sends.Load())
	}
}

func TestOwnershipBlockedRenewalCancelsHTTPAndRetainsSlot(t *testing.T) {
	store, event := ownershipTestSetup(120 * time.Millisecond)
	blocked, release, sending := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var renews atomic.Int64
	store.renew = func(ctx context.Context, e domain.Execution) error {
		if renews.Add(1) == 1 {
			return store.mockStore.RenewExecutionClaim(ctx, e)
		}
		close(blocked)
		<-release // deliberate noncooperative transport
		return nil
	}
	cancelled := make(chan struct{})
	sender := ownershipTestSender(func(ctx context.Context, _ WebhookRequest) WebhookResult {
		close(sending)
		<-ctx.Done()
		close(cancelled)
		return WebhookResult{Error: ctx.Err()}
	})
	done := make(chan error, 1)
	go func() { done <- New(store, sender).Dispatch(context.Background(), event) }()
	ownershipWait(t, sending)
	ownershipWait(t, blocked)
	ownershipWait(t, cancelled)
	select {
	case <-done:
		t.Fatal("slot freed while renewal still blocked")
	default:
	}
	close(release)
	if err := ownershipResult(t, done); !errors.Is(err, domain.ErrExecutionClaimLost) {
		t.Fatalf("blocked renewal: %v", err)
	}
	if renews.Load() != 2 || store.getExecutionStatus(event.ExecutionID) != domain.ExecutionStatusInProgress {
		t.Fatal("replacement renewals or false terminal state")
	}
}

func TestOwnershipUncertainRenewalStopsDelivery(t *testing.T) {
	store, event := ownershipTestSetup(120 * time.Millisecond)
	uncertain := errors.New("commit may have succeeded; response lost")
	var renews atomic.Int64
	store.renew = func(ctx context.Context, e domain.Execution) error {
		if renews.Add(1) == 1 {
			return store.mockStore.RenewExecutionClaim(ctx, e)
		}
		return uncertain
	}
	sender := ownershipTestSender(func(ctx context.Context, _ WebhookRequest) WebhookResult {
		<-ctx.Done()
		return WebhookResult{Error: ctx.Err()}
	})
	err := New(store, sender).Dispatch(context.Background(), event)
	if !errors.Is(err, uncertain) || len(store.getStatusUpdates()) != 0 {
		t.Fatalf("uncertain ownership completed: %v", err)
	}
}

func TestOwnershipLostDuringBackoffStopsRetryAndReleasesProbe(t *testing.T) {
	store, event := ownershipTestSetup(time.Minute)
	breaker := circuitbreaker.New(1, 0)
	breaker.RecordFailure("https://example.com")
	var calls atomic.Int64
	sender := ownershipTestSender(func(context.Context, WebhookRequest) WebhookResult {
		calls.Add(1)
		store.mu.Lock()
		store.generation[event.ExecutionID]++
		store.mu.Unlock()
		return WebhookResult{StatusCode: 500}
	})
	d := New(store, sender).WithCircuitBreaker(breaker)
	d.backoff = []time.Duration{0, 10 * time.Millisecond}
	err := d.Dispatch(context.Background(), event)
	if !errors.Is(err, domain.ErrExecutionClaimLost) || calls.Load() != 1 {
		t.Fatalf("stale retry: sends=%d error=%v", calls.Load(), err)
	}
	// All three permits remain available after abandoned admission.
	for i := 0; i < circuitbreaker.DefaultHalfOpenProbes; i++ {
		cancel, err := breaker.AllowWithCancel("https://example.com")
		if err != nil {
			t.Fatal("probe leaked", err)
		}
		t.Cleanup(cancel)
	}
}

func TestOwnershipSlowAnalyticsRemainsBestEffort(t *testing.T) {
	store, event := ownershipTestSetup(200 * time.Millisecond)
	analytics := ownershipTestAnalytics(func(ctx context.Context) { <-ctx.Done() })
	var sends atomic.Int64
	err := New(store, ownershipTestSender(func(context.Context, WebhookRequest) WebhookResult {
		sends.Add(1)
		return WebhookResult{StatusCode: 200}
	})).WithAnalytics(analytics).Dispatch(context.Background(), event)
	if err != nil || sends.Load() != 1 || store.getExecutionStatus(event.ExecutionID) != domain.ExecutionStatusDelivered {
		t.Fatalf("optional analytics revoked delivery: %v sends=%d", err, sends.Load())
	}
}

func TestOwnershipAuditDeadlineAfterSuccessRemainsRecoverable(t *testing.T) {
	store, event := ownershipTestSetup(200 * time.Millisecond)
	store.insert = func(ctx context.Context, _ domain.DeliveryAttempt) error { <-ctx.Done(); return ctx.Err() }
	breaker := circuitbreaker.New(1, 0)
	breaker.RecordFailure("https://example.com")
	err := New(store, ownershipTestSender(func(context.Context, WebhookRequest) WebhookResult { return WebhookResult{StatusCode: 200} })).WithCircuitBreaker(breaker).Dispatch(context.Background(), event)
	if !errors.Is(err, context.DeadlineExceeded) || store.getExecutionStatus(event.ExecutionID) != domain.ExecutionStatusInProgress {
		t.Fatalf("audit timeout: %v", err)
	}
	if err := breaker.Allow("https://example.com"); err != nil {
		t.Fatal("HTTP success did not release circuit", err)
	}
}

func TestOwnershipLateRenewalResponseCannotResurrect(t *testing.T) {
	store, event := ownershipTestSetup(80 * time.Millisecond)
	execution, err := store.ClaimExecution(context.Background(), event.ExecutionID, event.Namespace)
	if err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	store.renew = func(context.Context, domain.Execution) error { close(started); <-release; return nil }
	lease := newExecutionLease(context.Background(), store, *execution, time.Now())
	done := make(chan error, 1)
	go func() { done <- lease.renew(lease.ctx) }()
	ownershipWait(t, started)
	<-lease.ctx.Done()
	close(release)
	if err := ownershipResult(t, done); !errors.Is(err, domain.ErrExecutionClaimLost) {
		t.Fatalf("late response accepted: %v", err)
	}
	lease.close()
}

type stalledOwnershipStore struct {
	*sustainedStore
	mu      sync.Mutex
	seen    map[uuid.UUID]bool
	blocked chan struct{}
	release chan struct{}
}

func (s *stalledOwnershipStore) ExecutionLeaseDuration() time.Duration { return 100 * time.Millisecond }
func (s *stalledOwnershipStore) RenewExecutionClaim(ctx context.Context, e domain.Execution) error {
	s.mu.Lock()
	first := !s.seen[e.ID]
	s.seen[e.ID] = true
	s.mu.Unlock()
	if first {
		return ctx.Err()
	}
	s.blocked <- struct{}{}
	<-s.release
	return nil
}

func TestOwnershipStalledRenewalsRemainBoundedByDeliverySlots(t *testing.T) {
	base := runtime.NumGoroutine()
	store := &stalledOwnershipStore{sustainedStore: &sustainedStore{job: domain.Job{ID: uuid.New(), Namespace: "stall", Delivery: domain.DeliveryConfig{WebhookURL: "https://example.com"}}}, seen: make(map[uuid.UUID]bool), blocked: make(chan struct{}, 32), release: make(chan struct{})}
	store.remaining.Store(100)
	sender := &sustainedSender{}
	sender.block.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { New(store, sender).RunDBPoll(ctx, time.Millisecond, 2); close(done) }()
	for i := 0; i < 8; i++ {
		ownershipWait(t, store.blocked)
	}
	time.Sleep(150 * time.Millisecond)
	if store.remaining.Load() != 92 || sender.peak.Load() > 8 || len(store.blocked) != 0 {
		t.Fatalf("stuck resources expanded: remaining=%d peak=%d extra=%d", store.remaining.Load(), sender.peak.Load(), len(store.blocked))
	}
	stalled := runtime.NumGoroutine()
	cancel()
	select {
	case <-done:
		t.Fatal("released occupied slots while driver still blocked")
	default:
	}
	close(store.release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("released driver did not finish")
	}
	t.Logf("8 retained slots, 8 stuck renewals, no further claims; goroutines baseline=%d stalled=%d final=%d", base, stalled, runtime.NumGoroutine())
}
