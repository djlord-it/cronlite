package dispatcher

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/djlord-it/easy-cron/internal/domain"
)

// mockStore tracks execution status transitions and enforces terminal state guards.
type mockStore struct {
	mu               sync.Mutex
	jobs             map[uuid.UUID]domain.Job
	executionStatus  map[uuid.UUID]domain.ExecutionStatus
	deliveryAttempts []domain.DeliveryAttempt
	statusUpdates    []statusUpdate
	dequeueResults   []*domain.Execution // for DB poll mode
}

type statusUpdate struct {
	ExecutionID uuid.UUID
	Status      domain.ExecutionStatus
	Denied      bool
}

func newMockStore() *mockStore {
	return &mockStore{
		jobs:            make(map[uuid.UUID]domain.Job),
		executionStatus: make(map[uuid.UUID]domain.ExecutionStatus),
	}
}

func (s *mockStore) GetJobByID(ctx context.Context, jobID uuid.UUID) (domain.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[jobID]
	if !ok {
		return domain.Job{}, errors.New("job not found")
	}
	return job, nil
}

func (s *mockStore) InsertDeliveryAttempt(ctx context.Context, attempt domain.DeliveryAttempt) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deliveryAttempts = append(s.deliveryAttempts, attempt)
	return nil
}

func (s *mockStore) UpdateExecutionStatus(ctx context.Context, executionID uuid.UUID, status domain.ExecutionStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	currentStatus := s.executionStatus[executionID]

	// Enforce terminal state guard
	if currentStatus == domain.ExecutionStatusDelivered || currentStatus == domain.ExecutionStatusFailed {
		s.statusUpdates = append(s.statusUpdates, statusUpdate{
			ExecutionID: executionID,
			Status:      status,
			Denied:      true,
		})
		return ErrStatusTransitionDenied
	}

	s.executionStatus[executionID] = status
	s.statusUpdates = append(s.statusUpdates, statusUpdate{
		ExecutionID: executionID,
		Status:      status,
		Denied:      false,
	})
	return nil
}

func (s *mockStore) DequeueExecution(ctx context.Context) (*domain.Execution, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.dequeueResults) == 0 {
		return nil, nil
	}
	result := s.dequeueResults[0]
	s.dequeueResults = s.dequeueResults[1:]
	return result, nil
}

func (s *mockStore) setExecutionStatus(id uuid.UUID, status domain.ExecutionStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.executionStatus[id] = status
}

func (s *mockStore) getExecutionStatus(id uuid.UUID) domain.ExecutionStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.executionStatus[id]
}

func (s *mockStore) getStatusUpdates() []statusUpdate {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]statusUpdate, len(s.statusUpdates))
	copy(result, s.statusUpdates)
	return result
}

func (s *mockStore) getAttemptCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.deliveryAttempts)
}

func (s *mockStore) addJob(job domain.Job) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs[job.ID] = job
}

// mockSender simulates webhook delivery with configurable results.
type mockSender struct {
	mu      sync.Mutex
	results []WebhookResult
	index   int
	calls   int
}

func (s *mockSender) Send(ctx context.Context, req WebhookRequest) WebhookResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.index < len(s.results) {
		result := s.results[s.index]
		s.index++
		return result
	}
	// Default: success
	return WebhookResult{StatusCode: 200, Duration: 10 * time.Millisecond}
}

func (s *mockSender) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// TestDispatcher_TerminalState_DeliveredCannotRegress verifies that once an
// execution is marked as delivered, it cannot be changed to any other status.
func TestDispatcher_TerminalState_DeliveredCannotRegress(t *testing.T) {
	store := newMockStore()
	sender := &mockSender{results: []WebhookResult{{StatusCode: 200}}}

	jobID := uuid.New()
	executionID := uuid.New()

	store.addJob(domain.Job{
		ID:        jobID,
		Namespace: domain.Namespace("test-ns"),
		Name:      "test-job",
		Enabled:   true,
		Delivery: domain.DeliveryConfig{
			Type:       "webhook",
			WebhookURL: "http://example.com/webhook",
			Timeout:    30 * time.Second,
		},
	})

	// Pre-set execution as delivered (simulating already processed)
	store.setExecutionStatus(executionID, domain.ExecutionStatusDelivered)

	disp := New(store, sender)

	event := domain.TriggerEvent{
		ExecutionID: executionID,
		JobID:       jobID,
		Namespace:   domain.Namespace("test-ns"),
		ScheduledAt: time.Now().UTC(),
		FiredAt:     time.Now().UTC(),
	}

	ctx := context.Background()
	err := disp.Dispatch(ctx, event)

	// Should succeed (idempotent handling)
	if err != nil {
		t.Fatalf("dispatch should succeed on replay: %v", err)
	}

	// Status should still be delivered
	if store.getExecutionStatus(executionID) != domain.ExecutionStatusDelivered {
		t.Error("execution status should remain delivered")
	}

	// Should have recorded the denied transition
	updates := store.getStatusUpdates()
	if len(updates) != 1 {
		t.Fatalf("expected 1 status update attempt, got %d", len(updates))
	}
	if !updates[0].Denied {
		t.Error("status update should have been denied")
	}
}

// TestDispatcher_TerminalState_FailedCannotRegress verifies that once an
// execution is marked as failed, it cannot be changed to any other status.
func TestDispatcher_TerminalState_FailedCannotRegress(t *testing.T) {
	store := newMockStore()
	sender := &mockSender{results: []WebhookResult{{StatusCode: 200}}}

	jobID := uuid.New()
	executionID := uuid.New()

	store.addJob(domain.Job{
		ID:        jobID,
		Namespace: domain.Namespace("test-ns"),
		Name:      "test-job",
		Enabled:   true,
		Delivery: domain.DeliveryConfig{
			Type:       "webhook",
			WebhookURL: "http://example.com/webhook",
			Timeout:    30 * time.Second,
		},
	})

	// Pre-set execution as failed
	store.setExecutionStatus(executionID, domain.ExecutionStatusFailed)

	disp := New(store, sender)

	event := domain.TriggerEvent{
		ExecutionID: executionID,
		JobID:       jobID,
		Namespace:   domain.Namespace("test-ns"),
		ScheduledAt: time.Now().UTC(),
		FiredAt:     time.Now().UTC(),
	}

	ctx := context.Background()
	err := disp.Dispatch(ctx, event)

	// Should succeed (idempotent handling)
	if err != nil {
		t.Fatalf("dispatch should succeed on replay: %v", err)
	}

	// Status should still be failed
	if store.getExecutionStatus(executionID) != domain.ExecutionStatusFailed {
		t.Error("execution status should remain failed")
	}
}

// TestDispatcher_RetryBounded verifies that retry attempts are bounded
// to exactly maxAttempts (4).
func TestDispatcher_RetryBounded(t *testing.T) {
	store := newMockStore()

	// All attempts fail with retryable error
	sender := &mockSender{results: []WebhookResult{
		{StatusCode: 500}, // Attempt 1: retryable
		{StatusCode: 500}, // Attempt 2: retryable
		{StatusCode: 500}, // Attempt 3: retryable
		{StatusCode: 500}, // Attempt 4: retryable
		{StatusCode: 500}, // Should never reach this
	}}

	jobID := uuid.New()
	executionID := uuid.New()

	store.addJob(domain.Job{
		ID:        jobID,
		Namespace: domain.Namespace("test-ns"),
		Name:      "test-job",
		Enabled:   true,
		Delivery: domain.DeliveryConfig{
			Type:       "webhook",
			WebhookURL: "http://example.com/webhook",
			Timeout:    30 * time.Second,
		},
	})

	// Use zero backoff for fast test
	disp := New(store, sender)
	disp.backoff = []time.Duration{0, 0, 0, 0}

	event := domain.TriggerEvent{
		ExecutionID: executionID,
		JobID:       jobID,
		Namespace:   domain.Namespace("test-ns"),
		ScheduledAt: time.Now().UTC(),
		FiredAt:     time.Now().UTC(),
	}

	ctx := context.Background()
	_ = disp.Dispatch(ctx, event)

	// Should have exactly 4 webhook calls (maxAttempts)
	if sender.callCount() != 4 {
		t.Errorf("expected exactly 4 webhook calls, got %d", sender.callCount())
	}

	// Should have exactly 4 delivery attempts recorded
	if store.getAttemptCount() != 4 {
		t.Errorf("expected exactly 4 delivery attempts, got %d", store.getAttemptCount())
	}

	// Execution should be marked failed
	if store.getExecutionStatus(executionID) != domain.ExecutionStatusFailed {
		t.Error("execution should be marked failed after max retries")
	}
}

// TestDispatcher_NonRetryableStopsImmediately verifies that non-retryable
// errors (4xx except 429) stop retry immediately.
func TestDispatcher_NonRetryableStopsImmediately(t *testing.T) {
	store := newMockStore()

	// First attempt returns non-retryable 404
	sender := &mockSender{results: []WebhookResult{
		{StatusCode: 404}, // Non-retryable
		{StatusCode: 200}, // Should never reach
	}}

	jobID := uuid.New()
	executionID := uuid.New()

	store.addJob(domain.Job{
		ID:        jobID,
		Namespace: domain.Namespace("test-ns"),
		Name:      "test-job",
		Enabled:   true,
		Delivery: domain.DeliveryConfig{
			Type:       "webhook",
			WebhookURL: "http://example.com/webhook",
			Timeout:    30 * time.Second,
		},
	})

	disp := New(store, sender)
	disp.backoff = []time.Duration{0, 0, 0, 0}

	event := domain.TriggerEvent{
		ExecutionID: executionID,
		JobID:       jobID,
		Namespace:   domain.Namespace("test-ns"),
		ScheduledAt: time.Now().UTC(),
		FiredAt:     time.Now().UTC(),
	}

	ctx := context.Background()
	_ = disp.Dispatch(ctx, event)

	// Should have only 1 webhook call (non-retryable)
	if sender.callCount() != 1 {
		t.Errorf("expected 1 webhook call for non-retryable error, got %d", sender.callCount())
	}

	// Execution should be marked failed
	if store.getExecutionStatus(executionID) != domain.ExecutionStatusFailed {
		t.Error("execution should be marked failed for non-retryable error")
	}
}

// TestDispatcher_429IsRetryable verifies that 429 (rate limit) is retryable.
func TestDispatcher_429IsRetryable(t *testing.T) {
	store := newMockStore()

	sender := &mockSender{results: []WebhookResult{
		{StatusCode: 429}, // Retryable
		{StatusCode: 200}, // Success on retry
	}}

	jobID := uuid.New()
	executionID := uuid.New()

	store.addJob(domain.Job{
		ID:        jobID,
		Namespace: domain.Namespace("test-ns"),
		Name:      "test-job",
		Enabled:   true,
		Delivery: domain.DeliveryConfig{
			Type:       "webhook",
			WebhookURL: "http://example.com/webhook",
			Timeout:    30 * time.Second,
		},
	})

	disp := New(store, sender)
	disp.backoff = []time.Duration{0, 0, 0, 0}

	event := domain.TriggerEvent{
		ExecutionID: executionID,
		JobID:       jobID,
		Namespace:   domain.Namespace("test-ns"),
		ScheduledAt: time.Now().UTC(),
		FiredAt:     time.Now().UTC(),
	}

	ctx := context.Background()
	_ = disp.Dispatch(ctx, event)

	// Should have 2 webhook calls (retry after 429)
	if sender.callCount() != 2 {
		t.Errorf("expected 2 webhook calls (429 is retryable), got %d", sender.callCount())
	}

	// Execution should be marked delivered
	if store.getExecutionStatus(executionID) != domain.ExecutionStatusDelivered {
		t.Error("execution should be marked delivered after successful retry")
	}
}

// TestDispatcher_MaxAttemptsConstant verifies the maxAttempts constant is exactly 4.
func TestDispatcher_MaxAttemptsConstant(t *testing.T) {
	if maxAttempts != 4 {
		t.Errorf("maxAttempts must be 4, got %d", maxAttempts)
	}
}

// mockCircuitBreaker implements CircuitBreaker for testing.
type mockCircuitBreaker struct {
	mu           sync.Mutex
	allowErr     map[string]error
	successCalls []string
	failureCalls []string
}

func newMockCircuitBreaker() *mockCircuitBreaker {
	return &mockCircuitBreaker{
		allowErr: make(map[string]error),
	}
}

func (m *mockCircuitBreaker) Allow(url string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.allowErr[url]
}

func (m *mockCircuitBreaker) RecordSuccess(url string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.successCalls = append(m.successCalls, url)
}

func (m *mockCircuitBreaker) RecordFailure(url string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failureCalls = append(m.failureCalls, url)
}

func (m *mockCircuitBreaker) setOpen(url string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.allowErr[url] = errors.New("circuit breaker is open")
}

func TestDispatch_CircuitOpen_DefersWithoutMarkingFailed(t *testing.T) {
	store := newMockStore()
	sender := &mockSender{results: []WebhookResult{}}
	cb := newMockCircuitBreaker()

	jobID := uuid.New()
	execID := uuid.New()
	webhookURL := "http://failing.example.com/hook"
	store.addJob(domain.Job{
		ID:        jobID,
		Namespace: domain.Namespace("test-ns"),
		Name:      "test-job",
		Enabled:   true,
		Delivery: domain.DeliveryConfig{
			Type:       "webhook",
			WebhookURL: webhookURL,
			Timeout:    30 * time.Second,
		},
	})
	cb.setOpen(webhookURL)

	disp := New(store, sender).WithCircuitBreaker(cb)

	event := domain.TriggerEvent{
		JobID:       jobID,
		ExecutionID: execID,
		ScheduledAt: time.Now(),
		FiredAt:     time.Now(),
	}

	err := disp.Dispatch(context.Background(), event)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	// Sender should NOT have been called
	if sender.callCount() != 0 {
		t.Errorf("expected 0 send calls, got %d", sender.callCount())
	}

	// Execution status should NOT have been updated (stays emitted/in_progress,
	// allowing retry when circuit closes)
	updates := store.getStatusUpdates()
	if len(updates) != 0 {
		t.Errorf("expected no status updates when circuit is open, got %d: %v", len(updates), updates)
	}
}

func TestDispatch_CircuitClosed_RecordsSuccess(t *testing.T) {
	store := newMockStore()
	sender := &mockSender{results: []WebhookResult{
		{StatusCode: 200},
	}}
	cb := newMockCircuitBreaker()

	jobID := uuid.New()
	execID := uuid.New()
	webhookURL := "http://ok.example.com/hook"
	store.addJob(domain.Job{
		ID:        jobID,
		Namespace: domain.Namespace("test-ns"),
		Name:      "test-job",
		Enabled:   true,
		Delivery: domain.DeliveryConfig{
			Type:       "webhook",
			WebhookURL: webhookURL,
			Timeout:    30 * time.Second,
		},
	})

	disp := New(store, sender).WithCircuitBreaker(cb)

	event := domain.TriggerEvent{
		JobID:       jobID,
		ExecutionID: execID,
		ScheduledAt: time.Now(),
		FiredAt:     time.Now(),
	}

	err := disp.Dispatch(context.Background(), event)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	cb.mu.Lock()
	defer cb.mu.Unlock()
	if len(cb.successCalls) != 1 || cb.successCalls[0] != webhookURL {
		t.Errorf("expected RecordSuccess(%q), got %v", webhookURL, cb.successCalls)
	}
}

func TestDispatch_CircuitClosed_RecordsFailure(t *testing.T) {
	store := newMockStore()
	sender := &mockSender{results: []WebhookResult{
		{StatusCode: 500, Error: errors.New("server error")},
		{StatusCode: 500, Error: errors.New("server error")},
		{StatusCode: 500, Error: errors.New("server error")},
		{StatusCode: 500, Error: errors.New("server error")},
	}}
	cb := newMockCircuitBreaker()

	jobID := uuid.New()
	execID := uuid.New()
	webhookURL := "http://fail.example.com/hook"
	store.addJob(domain.Job{
		ID:        jobID,
		Namespace: domain.Namespace("test-ns"),
		Name:      "test-job",
		Enabled:   true,
		Delivery: domain.DeliveryConfig{
			Type:       "webhook",
			WebhookURL: webhookURL,
			Timeout:    30 * time.Second,
		},
	})

	disp := New(store, sender).WithCircuitBreaker(cb)
	disp.backoff = []time.Duration{0, 0, 0, 0}

	event := domain.TriggerEvent{
		JobID:       jobID,
		ExecutionID: execID,
		ScheduledAt: time.Now(),
		FiredAt:     time.Now(),
	}

	err := disp.Dispatch(context.Background(), event)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	cb.mu.Lock()
	defer cb.mu.Unlock()
	if len(cb.failureCalls) != 1 || cb.failureCalls[0] != webhookURL {
		t.Errorf("expected RecordFailure(%q), got %v", webhookURL, cb.failureCalls)
	}
}

func TestDispatch_NilBreaker_Works(t *testing.T) {
	store := newMockStore()
	sender := &mockSender{results: []WebhookResult{
		{StatusCode: 200},
	}}

	jobID := uuid.New()
	execID := uuid.New()
	store.addJob(domain.Job{
		ID:        jobID,
		Namespace: domain.Namespace("test-ns"),
		Name:      "test-job",
		Enabled:   true,
		Delivery: domain.DeliveryConfig{
			Type:       "webhook",
			WebhookURL: "http://example.com/hook",
			Timeout:    30 * time.Second,
		},
	})

	disp := New(store, sender) // no WithCircuitBreaker

	event := domain.TriggerEvent{
		JobID:       jobID,
		ExecutionID: execID,
		ScheduledAt: time.Now(),
		FiredAt:     time.Now(),
	}

	err := disp.Dispatch(context.Background(), event)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestRunDBPoll_ProcessesExecution(t *testing.T) {
	store := newMockStore()
	sender := &mockSender{results: []WebhookResult{
		{StatusCode: 200},
	}}

	jobID := uuid.New()
	execID := uuid.New()
	store.addJob(domain.Job{
		ID:        jobID,
		Namespace: domain.Namespace("test-ns"),
		Name:      "test-job",
		Enabled:   true,
		Delivery: domain.DeliveryConfig{
			Type:       "webhook",
			WebhookURL: "http://example.com/hook",
			Timeout:    30 * time.Second,
		},
	})

	store.mu.Lock()
	store.dequeueResults = []*domain.Execution{{
		ID:          execID,
		JobID:       jobID,
		Namespace:   domain.Namespace("test-ns"),
		ScheduledAt: time.Now(),
		FiredAt:     time.Now(),
		Status:      domain.ExecutionStatusInProgress,
		CreatedAt:   time.Now(),
	}}
	store.mu.Unlock()

	disp := New(store, sender)
	disp.backoff = []time.Duration{0, 0, 0, 0}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	disp.RunDBPoll(ctx, 50*time.Millisecond, 1)

	store.mu.Lock()
	status := store.executionStatus[execID]
	store.mu.Unlock()
	if status != domain.ExecutionStatusDelivered {
		t.Errorf("expected delivered, got %v", status)
	}
}

func TestRunDBPoll_SleepsWhenNoWork(t *testing.T) {
	store := newMockStore()
	sender := &mockSender{}

	disp := New(store, sender)

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	start := time.Now()
	disp.RunDBPoll(ctx, 50*time.Millisecond, 1)
	elapsed := time.Since(start)

	if elapsed < 100*time.Millisecond {
		t.Errorf("expected to run for ~150ms, exited after %v", elapsed)
	}

	if sender.callCount() > 0 {
		t.Errorf("expected 0 send calls, got %d", sender.callCount())
	}
}

func TestRunDBPoll_MultipleWorkers(t *testing.T) {
	store := newMockStore()
	sender := &mockSender{results: []WebhookResult{
		{StatusCode: 200},
		{StatusCode: 200},
	}}

	jobID := uuid.New()
	store.addJob(domain.Job{
		ID:        jobID,
		Namespace: domain.Namespace("test-ns"),
		Name:      "test-job",
		Enabled:   true,
		Delivery: domain.DeliveryConfig{
			Type:       "webhook",
			WebhookURL: "http://example.com/hook",
			Timeout:    30 * time.Second,
		},
	})

	exec1 := &domain.Execution{
		ID: uuid.New(), JobID: jobID, Namespace: domain.Namespace("test-ns"),
		ScheduledAt: time.Now(), FiredAt: time.Now(),
		Status: domain.ExecutionStatusInProgress, CreatedAt: time.Now(),
	}
	exec2 := &domain.Execution{
		ID: uuid.New(), JobID: jobID, Namespace: domain.Namespace("test-ns"),
		ScheduledAt: time.Now(), FiredAt: time.Now(),
		Status: domain.ExecutionStatusInProgress, CreatedAt: time.Now(),
	}
	store.mu.Lock()
	store.dequeueResults = []*domain.Execution{exec1, exec2}
	store.mu.Unlock()

	disp := New(store, sender)
	disp.backoff = []time.Duration{0, 0, 0, 0}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	disp.RunDBPoll(ctx, 50*time.Millisecond, 2)

	store.mu.Lock()
	s1 := store.executionStatus[exec1.ID]
	s2 := store.executionStatus[exec2.ID]
	store.mu.Unlock()
	if s1 != domain.ExecutionStatusDelivered {
		t.Errorf("exec1: expected delivered, got %v", s1)
	}
	if s2 != domain.ExecutionStatusDelivered {
		t.Errorf("exec2: expected delivered, got %v", s2)
	}
}

func TestClassifyStatusForMetrics(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		err        error
		want       string
	}{
		{"200 success", 200, nil, "2xx"},
		{"201 created", 201, nil, "2xx"},
		{"299 upper bound 2xx", 299, nil, "2xx"},
		{"400 bad request", 400, nil, "4xx"},
		{"404 not found", 404, nil, "4xx"},
		{"500 internal", 500, nil, "5xx"},
		{"503 unavailable", 503, nil, "5xx"},
		{"100 informational maps to other_error", 100, nil, "other_error"},
		{"timeout context deadline exceeded", 0, errors.New("context deadline exceeded"), "timeout"},
		{"timeout waiting for response", 0, errors.New("Timeout waiting for response"), "timeout"},
		{"connection refused", 0, errors.New("connection refused"), "connection_error"},
		{"dial tcp no such host", 0, errors.New("dial tcp: no such host"), "connection_error"},
		{"network is unreachable", 0, errors.New("network is unreachable"), "connection_error"},
		{"random error maps to other_error", 0, errors.New("some random error"), "other_error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyStatusForMetrics(tt.statusCode, tt.err)
			if got != tt.want {
				t.Errorf("classifyStatusForMetrics(%d, %v) = %q, want %q",
					tt.statusCode, tt.err, got, tt.want)
			}
		})
	}
}

func TestContainsInsensitive(t *testing.T) {
	tests := []struct {
		name   string
		s      string
		substr string
		want   bool
	}{
		{"lowercase match in mixed case", "Hello World", "hello", true},
		{"uppercase match in mixed case", "Hello World", "WORLD", true},
		{"substr longer than s", "abc", "abcd", false},
		{"empty s non-empty substr", "", "a", false},
		{"non-empty s empty substr", "abc", "", true},
		{"case-insensitive connection refused", "Connection Refused", "connection refused", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := containsInsensitive(tt.s, tt.substr)
			if got != tt.want {
				t.Errorf("containsInsensitive(%q, %q) = %v, want %v",
					tt.s, tt.substr, got, tt.want)
			}
		})
	}
}
