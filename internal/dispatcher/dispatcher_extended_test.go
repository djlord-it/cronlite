package dispatcher

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/djlord-it/cronlite/internal/domain"
)

// TestDispatcher_GetJobError verifies that a store.GetJobByID error
// is propagated correctly.
func TestDispatcher_GetJobError(t *testing.T) {
	store := newMockStore()
	sender := &mockSender{}

	// Don't add any jobs to the store — GetJobByID will fail

	disp := New(store, sender)

	event := domain.TriggerEvent{
		ExecutionID: uuid.New(),
		JobID:       uuid.New(), // non-existent
		Namespace:   domain.Namespace("test-ns"),
		ScheduledAt: time.Now().UTC(),
		FiredAt:     time.Now().UTC(),
	}

	err := disp.Dispatch(context.Background(), event)
	if err == nil {
		t.Error("expected error when job not found")
	}
}

// TestDispatcher_NoWebhookURL verifies that a job with empty webhook URL
// returns an error.
func TestDispatcher_NoWebhookURL(t *testing.T) {
	store := newMockStore()
	sender := &mockSender{}

	jobID := uuid.New()
	store.addJob(domain.Job{
		ID:        jobID,
		Namespace: domain.Namespace("test-ns"),
		Name:      "no-url-job",
		Enabled:   true,
		Delivery:  domain.DeliveryConfig{Type: "webhook", WebhookURL: ""},
	})

	event := domain.TriggerEvent{
		ExecutionID: uuid.New(),
		JobID:       jobID,
		Namespace:   domain.Namespace("test-ns"),
		ScheduledAt: time.Now().UTC(),
		FiredAt:     time.Now().UTC(),
	}

	err := disp(store, sender).Dispatch(context.Background(), event)
	if err == nil {
		t.Error("expected error for empty webhook URL")
	}
}

func disp(store *mockStore, sender *mockSender) *Dispatcher {
	d := New(store, sender)
	d.backoff = []time.Duration{0, 0, 0, 0}
	return d
}

// TestDispatcher_SuccessOnFirstAttempt verifies a successful delivery
// on the first try.
func TestDispatcher_SuccessOnFirstAttempt(t *testing.T) {
	store := newMockStore()
	sender := &mockSender{results: []WebhookResult{{StatusCode: 200, Duration: 10 * time.Millisecond}}}

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

	d := disp(store, sender)

	event := domain.TriggerEvent{
		ExecutionID: executionID,
		JobID:       jobID,
		Namespace:   domain.Namespace("test-ns"),
		ScheduledAt: time.Now().UTC(),
		FiredAt:     time.Now().UTC(),
	}

	err := d.Dispatch(context.Background(), event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if sender.callCount() != 1 {
		t.Errorf("expected 1 webhook call, got %d", sender.callCount())
	}
	if store.getExecutionStatus(executionID) != domain.ExecutionStatusDelivered {
		t.Error("execution should be delivered")
	}
	if store.getAttemptCount() != 1 {
		t.Errorf("expected 1 delivery attempt, got %d", store.getAttemptCount())
	}
}

// mockDispatcherMetrics tracks dispatcher metrics calls.
type mockDispatcherMetrics struct {
	mu                       sync.Mutex
	inFlightIncr             int
	inFlightDecr             int
	attemptCompletedCalls    []attemptCompletedCall
	outcomeCalls             []string
	retryAttemptCalls        []bool
	executionLatencyObserved []float64
}

type attemptCompletedCall struct {
	attempt     int
	statusClass string
	duration    time.Duration
}

func (m *mockDispatcherMetrics) DeliveryAttemptCompleted(attempt int, statusClass string, duration time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.attemptCompletedCalls = append(m.attemptCompletedCalls, attemptCompletedCall{attempt, statusClass, duration})
}

func (m *mockDispatcherMetrics) DeliveryOutcome(outcome string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.outcomeCalls = append(m.outcomeCalls, outcome)
}

func (m *mockDispatcherMetrics) RetryAttempt(retryable bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.retryAttemptCalls = append(m.retryAttemptCalls, retryable)
}

func (m *mockDispatcherMetrics) EventsInFlightIncr() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inFlightIncr++
}

func (m *mockDispatcherMetrics) EventsInFlightDecr() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inFlightDecr++
}

func (m *mockDispatcherMetrics) ExecutionLatencyObserve(latencySeconds float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.executionLatencyObserved = append(m.executionLatencyObserved, latencySeconds)
}

// TestDispatcher_MetricsRecording verifies that metrics are called on successful dispatch.
func TestDispatcher_MetricsRecording(t *testing.T) {
	store := newMockStore()
	sender := &mockSender{results: []WebhookResult{{StatusCode: 200, Duration: 10 * time.Millisecond}}}
	metrics := &mockDispatcherMetrics{}

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

	d := New(store, sender)
	d.backoff = []time.Duration{0, 0, 0, 0}
	d.WithMetrics(metrics)

	event := domain.TriggerEvent{
		ExecutionID: executionID,
		JobID:       jobID,
		Namespace:   domain.Namespace("test-ns"),
		ScheduledAt: time.Now().UTC(),
		FiredAt:     time.Now().UTC(),
	}

	if err := d.Dispatch(context.Background(), event); err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}

	metrics.mu.Lock()
	defer metrics.mu.Unlock()

	if metrics.inFlightIncr != 1 {
		t.Errorf("EventsInFlightIncr = %d, want 1", metrics.inFlightIncr)
	}
	if metrics.inFlightDecr != 1 {
		t.Errorf("EventsInFlightDecr = %d, want 1", metrics.inFlightDecr)
	}
	if len(metrics.attemptCompletedCalls) != 1 {
		t.Errorf("DeliveryAttemptCompleted calls = %d, want 1", len(metrics.attemptCompletedCalls))
	}
	if len(metrics.outcomeCalls) != 1 || metrics.outcomeCalls[0] != "success" {
		t.Errorf("DeliveryOutcome calls = %v, want [success]", metrics.outcomeCalls)
	}
}

// mockAnalyticsSink tracks analytics calls.
type mockAnalyticsSink struct {
	mu    sync.Mutex
	calls int
}

func (m *mockAnalyticsSink) Record(ctx context.Context, event domain.TriggerEvent, config domain.AnalyticsConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
}

// TestDispatcher_AnalyticsBestEffort verifies analytics recording.
func TestDispatcher_AnalyticsBestEffort(t *testing.T) {
	store := newMockStore()
	sender := &mockSender{results: []WebhookResult{{StatusCode: 200}}}
	analytics := &mockAnalyticsSink{}

	jobID := uuid.New()
	store.addJob(domain.Job{
		ID:        jobID,
		Namespace: domain.Namespace("test-ns"),
		Name:      "analytics-job",
		Enabled:   true,
		Delivery: domain.DeliveryConfig{
			Type:       "webhook",
			WebhookURL: "http://example.com/webhook",
			Timeout:    30 * time.Second,
		},
		Analytics: domain.AnalyticsConfig{Enabled: true, RetentionSeconds: 86400},
	})

	d := New(store, sender)
	d.backoff = []time.Duration{0, 0, 0, 0}
	d.WithAnalytics(analytics)

	event := domain.TriggerEvent{
		ExecutionID: uuid.New(),
		JobID:       jobID,
		Namespace:   domain.Namespace("test-ns"),
		ScheduledAt: time.Now().UTC(),
		FiredAt:     time.Now().UTC(),
	}

	if err := d.Dispatch(context.Background(), event); err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}

	analytics.mu.Lock()
	calls := analytics.calls
	analytics.mu.Unlock()

	if calls != 1 {
		t.Errorf("analytics Record should be called once, got %d", calls)
	}
}

// TestDispatcher_BackoffSchedule verifies the default backoff values.
func TestDispatcher_BackoffSchedule(t *testing.T) {
	expected := []time.Duration{0, 30 * time.Second, 2 * time.Minute, 10 * time.Minute}

	if len(defaultBackoff) != len(expected) {
		t.Fatalf("defaultBackoff length = %d, want %d", len(defaultBackoff), len(expected))
	}

	for i, want := range expected {
		if defaultBackoff[i] != want {
			t.Errorf("defaultBackoff[%d] = %v, want %v", i, defaultBackoff[i], want)
		}
	}
}

// TestWebhookResult_IsSuccess verifies success classification.
func TestWebhookResult_IsSuccess(t *testing.T) {
	tests := []struct {
		name   string
		result WebhookResult
		want   bool
	}{
		{"200 OK", WebhookResult{StatusCode: 200}, true},
		{"201 Created", WebhookResult{StatusCode: 201}, true},
		{"204 No Content", WebhookResult{StatusCode: 204}, true},
		{"299 boundary", WebhookResult{StatusCode: 299}, true},
		{"300 redirect", WebhookResult{StatusCode: 300}, false},
		{"400 client error", WebhookResult{StatusCode: 400}, false},
		{"500 server error", WebhookResult{StatusCode: 500}, false},
		{"with error", WebhookResult{StatusCode: 200, Error: errors.New("err")}, false},
		{"zero status with error", WebhookResult{Error: errors.New("connection refused")}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.result.IsSuccess()
			if got != tt.want {
				t.Errorf("IsSuccess() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestWebhookResult_IsRetryable verifies retryable classification.
func TestWebhookResult_IsRetryable(t *testing.T) {
	tests := []struct {
		name   string
		result WebhookResult
		want   bool
	}{
		{"500 server error", WebhookResult{StatusCode: 500}, true},
		{"502 bad gateway", WebhookResult{StatusCode: 502}, true},
		{"503 unavailable", WebhookResult{StatusCode: 503}, true},
		{"429 rate limit", WebhookResult{StatusCode: 429}, true},
		{"network error", WebhookResult{Error: errors.New("connection refused")}, true},
		{"timeout error", WebhookResult{Error: errors.New("context deadline exceeded")}, true},
		{"200 success", WebhookResult{StatusCode: 200}, false},
		{"400 client error", WebhookResult{StatusCode: 400}, false},
		{"404 not found", WebhookResult{StatusCode: 404}, false},
		{"403 forbidden", WebhookResult{StatusCode: 403}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.result.IsRetryable()
			if got != tt.want {
				t.Errorf("IsRetryable() = %v, want %v", got, tt.want)
			}
		})
	}
}
