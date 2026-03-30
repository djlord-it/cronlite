package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/djlord-it/easy-cron/internal/domain"
)

// mockHandlerStore implements api.Store for handler tests.
type mockHandlerStore struct {
	mu sync.Mutex

	createJobFn        func(ctx context.Context, job domain.Job, schedule domain.Schedule) error
	getEnabledJobsFn   func(ctx context.Context, limit, offset int) ([]domain.JobWithSchedule, error)
	listJobsFn         func(ctx context.Context, filter domain.JobFilter) ([]domain.Job, error)
	listExecutionsFn   func(ctx context.Context, filter domain.ExecutionFilter) ([]domain.Execution, error)
	deleteJobFn        func(ctx context.Context, jobID uuid.UUID, ns domain.Namespace) error
}

func (s *mockHandlerStore) CreateJob(ctx context.Context, job domain.Job, schedule domain.Schedule) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.createJobFn != nil {
		return s.createJobFn(ctx, job, schedule)
	}
	return nil
}

func (s *mockHandlerStore) GetEnabledJobs(ctx context.Context, limit, offset int) ([]domain.JobWithSchedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.getEnabledJobsFn != nil {
		return s.getEnabledJobsFn(ctx, limit, offset)
	}
	return nil, nil
}

func (s *mockHandlerStore) ListJobs(ctx context.Context, filter domain.JobFilter) ([]domain.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listJobsFn != nil {
		return s.listJobsFn(ctx, filter)
	}
	return nil, nil
}

func (s *mockHandlerStore) ListExecutions(ctx context.Context, filter domain.ExecutionFilter) ([]domain.Execution, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listExecutionsFn != nil {
		return s.listExecutionsFn(ctx, filter)
	}
	return nil, nil
}

func (s *mockHandlerStore) DeleteJob(ctx context.Context, jobID uuid.UUID, ns domain.Namespace) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deleteJobFn != nil {
		return s.deleteJobFn(ctx, jobID, ns)
	}
	return nil
}

// mockHealthChecker implements HealthChecker for handler tests.
type mockHealthChecker struct {
	mu     sync.Mutex
	pingFn func(ctx context.Context) error
}

func (m *mockHealthChecker) PingContext(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pingFn != nil {
		return m.pingFn(ctx)
	}
	return nil
}

func newTestHandler(store *mockHandlerStore) *LegacyHandler {
	return NewHandler(store, domain.Namespace("default"))
}

// --- CreateJob Tests ---

func TestHandler_CreateJob_Success(t *testing.T) {
	store := &mockHandlerStore{}
	handler := newTestHandler(store)

	body := `{
		"name": "test-job",
		"cron_expression": "0 * * * *",
		"timezone": "UTC",
		"webhook_url": "https://example.com/webhook"
	}`

	req := httptest.NewRequest(http.MethodPost, "/jobs", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp JobResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if resp.Name != "test-job" {
		t.Errorf("Name = %q, want test-job", resp.Name)
	}
	if !resp.Enabled {
		t.Error("Enabled should be true")
	}
	if resp.CronExpression != "0 * * * *" {
		t.Errorf("CronExpression = %q, want '0 * * * *'", resp.CronExpression)
	}
	if resp.Timezone != "UTC" {
		t.Errorf("Timezone = %q, want UTC", resp.Timezone)
	}
	if resp.WebhookURL != "https://example.com/webhook" {
		t.Errorf("WebhookURL = %q, want https://example.com/webhook", resp.WebhookURL)
	}
	if resp.ID == "" {
		t.Error("ID should not be empty")
	}
}

func TestHandler_CreateJob_ValidationError(t *testing.T) {
	store := &mockHandlerStore{}
	handler := newTestHandler(store)

	// Missing name
	body := `{"cron_expression": "0 * * * *", "timezone": "UTC", "webhook_url": "https://example.com"}`

	req := httptest.NewRequest(http.MethodPost, "/jobs", strings.NewReader(body))
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}

	var resp ErrorResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if !strings.Contains(resp.Error, "name") {
		t.Errorf("error should mention name: %q", resp.Error)
	}
}

func TestHandler_CreateJob_InvalidJSON(t *testing.T) {
	store := &mockHandlerStore{}
	handler := newTestHandler(store)

	req := httptest.NewRequest(http.MethodPost, "/jobs", strings.NewReader("{invalid"))
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHandler_CreateJob_StoreError(t *testing.T) {
	store := &mockHandlerStore{
		createJobFn: func(ctx context.Context, job domain.Job, schedule domain.Schedule) error {
			return errors.New("database error")
		},
	}
	handler := newTestHandler(store)

	body := `{
		"name": "test-job",
		"cron_expression": "0 * * * *",
		"timezone": "UTC",
		"webhook_url": "https://example.com/webhook"
	}`

	req := httptest.NewRequest(http.MethodPost, "/jobs", strings.NewReader(body))
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", w.Code)
	}
}

func TestHandler_CreateJob_BodyTooLarge(t *testing.T) {
	store := &mockHandlerStore{}
	handler := newTestHandler(store)

	// Create body larger than 1MB
	largeBody := strings.Repeat("a", 1<<20+1)

	req := httptest.NewRequest(http.MethodPost, "/jobs", strings.NewReader(largeBody))
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusRequestEntityTooLarge && w.Code != http.StatusBadRequest {
		t.Errorf("expected 413 or 400, got %d", w.Code)
	}
}

// --- ListJobs Tests ---

func TestHandler_ListJobs_Success(t *testing.T) {
	now := time.Now().UTC()
	store := &mockHandlerStore{
		getEnabledJobsFn: func(ctx context.Context, limit, offset int) ([]domain.JobWithSchedule, error) {
			return []domain.JobWithSchedule{
				{
					Job: domain.Job{
						ID:        uuid.MustParse("11111111-1111-1111-1111-111111111111"),
						Namespace: "default",
						Name:      "job-1",
						Enabled:   true,
						Delivery:  domain.DeliveryConfig{WebhookURL: "https://example.com/1"},
						CreatedAt: now,
					},
					Schedule: domain.Schedule{
						CronExpression: "0 * * * *",
						Timezone:       "UTC",
					},
				},
			}, nil
		},
	}
	handler := newTestHandler(store)

	req := httptest.NewRequest(http.MethodGet, "/jobs", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var resp ListJobsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if len(resp.Jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(resp.Jobs))
	}
	if resp.Jobs[0].Name != "job-1" {
		t.Errorf("Name = %q, want job-1", resp.Jobs[0].Name)
	}
}

func TestHandler_ListJobs_Empty(t *testing.T) {
	store := &mockHandlerStore{
		getEnabledJobsFn: func(ctx context.Context, limit, offset int) ([]domain.JobWithSchedule, error) {
			return []domain.JobWithSchedule{}, nil
		},
	}
	handler := newTestHandler(store)

	req := httptest.NewRequest(http.MethodGet, "/jobs", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	// Verify response is empty array, not null
	var resp ListJobsResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Jobs == nil {
		t.Error("Jobs should be empty array, not null")
	}
	if len(resp.Jobs) != 0 {
		t.Errorf("expected 0 jobs, got %d", len(resp.Jobs))
	}
}

func TestHandler_ListJobs_StoreError(t *testing.T) {
	store := &mockHandlerStore{
		getEnabledJobsFn: func(ctx context.Context, limit, offset int) ([]domain.JobWithSchedule, error) {
			return nil, errors.New("db error")
		},
	}
	handler := newTestHandler(store)

	req := httptest.NewRequest(http.MethodGet, "/jobs", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", w.Code)
	}
}

// --- ListExecutions Tests ---

func TestHandler_ListExecutions_Success(t *testing.T) {
	now := time.Now().UTC()
	jobID := uuid.MustParse("22222222-2222-2222-2222-222222222222")

	store := &mockHandlerStore{
		listExecutionsFn: func(ctx context.Context, filter domain.ExecutionFilter) ([]domain.Execution, error) {
			if filter.JobID != jobID {
				t.Errorf("jobID = %v, want %v", filter.JobID, jobID)
			}
			return []domain.Execution{
				{
					ID:          uuid.New(),
					JobID:       jobID,
					ScheduledAt: now,
					FiredAt:     now,
					Status:      domain.ExecutionStatusDelivered,
					CreatedAt:   now,
				},
			}, nil
		},
	}
	handler := newTestHandler(store)

	req := httptest.NewRequest(http.MethodGet, "/jobs/"+jobID.String()+"/executions", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp ListExecutionsResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Executions) != 1 {
		t.Fatalf("expected 1 execution, got %d", len(resp.Executions))
	}
}

func TestHandler_ListExecutions_InvalidID(t *testing.T) {
	store := &mockHandlerStore{}
	handler := newTestHandler(store)

	req := httptest.NewRequest(http.MethodGet, "/jobs/bad-id/executions", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

// --- DeleteJob Tests ---

func TestHandler_DeleteJob_Success(t *testing.T) {
	jobID := uuid.MustParse("33333333-3333-3333-3333-333333333333")
	store := &mockHandlerStore{
		deleteJobFn: func(ctx context.Context, jID uuid.UUID, ns domain.Namespace) error {
			if jID != jobID {
				t.Errorf("jobID = %v, want %v", jID, jobID)
			}
			return nil
		},
	}
	handler := newTestHandler(store)

	req := httptest.NewRequest(http.MethodDelete, "/jobs/"+jobID.String(), nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandler_DeleteJob_NotFound(t *testing.T) {
	store := &mockHandlerStore{
		deleteJobFn: func(ctx context.Context, jID uuid.UUID, ns domain.Namespace) error {
			return domain.ErrJobNotFound
		},
	}
	handler := newTestHandler(store)

	jobID := uuid.New()
	req := httptest.NewRequest(http.MethodDelete, "/jobs/"+jobID.String(), nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestHandler_DeleteJob_InvalidID(t *testing.T) {
	store := &mockHandlerStore{}
	handler := newTestHandler(store)

	req := httptest.NewRequest(http.MethodDelete, "/jobs/bad-id", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHandler_DeleteJob_StoreError(t *testing.T) {
	store := &mockHandlerStore{
		deleteJobFn: func(ctx context.Context, jID uuid.UUID, ns domain.Namespace) error {
			return errors.New("db error")
		},
	}
	handler := newTestHandler(store)

	jobID := uuid.New()
	req := httptest.NewRequest(http.MethodDelete, "/jobs/"+jobID.String(), nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", w.Code)
	}
}

// --- Health Tests ---

func TestHandler_Health_Simple(t *testing.T) {
	store := &mockHandlerStore{}
	handler := newTestHandler(store)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var resp LegacyHealthResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Status != "ok" {
		t.Errorf("Status = %q, want ok", resp.Status)
	}
}

func TestHandler_Health_Verbose_Healthy(t *testing.T) {
	store := &mockHandlerStore{}
	db := &mockHealthChecker{}
	handler := newTestHandler(store).WithHealthChecker(db)

	req := httptest.NewRequest(http.MethodGet, "/health?verbose=true", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var resp LegacyHealthResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Status != "ok" {
		t.Errorf("Status = %q, want ok", resp.Status)
	}
	if resp.Components["database"] != "healthy" {
		t.Errorf("database = %q, want healthy", resp.Components["database"])
	}
}

func TestHandler_Health_Verbose_Unhealthy(t *testing.T) {
	store := &mockHandlerStore{}
	db := &mockHealthChecker{
		pingFn: func(ctx context.Context) error {
			return errors.New("connection refused")
		},
	}
	handler := newTestHandler(store).WithHealthChecker(db)

	req := httptest.NewRequest(http.MethodGet, "/health?verbose=true", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}

	var resp LegacyHealthResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Status != "degraded" {
		t.Errorf("Status = %q, want degraded", resp.Status)
	}
	if strings.Contains(resp.Components["database"], "connection refused") {
		t.Error("database component should not leak error details")
	}
	if resp.Components["database"] != "unhealthy" {
		t.Errorf("database = %q, want 'unhealthy'", resp.Components["database"])
	}
}

// --- Routing Tests ---

func TestHandler_NotFound(t *testing.T) {
	store := &mockHandlerStore{}
	handler := newTestHandler(store)

	req := httptest.NewRequest(http.MethodGet, "/nonexistent", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestHandler_CreateJob_WithAnalytics(t *testing.T) {
	store := &mockHandlerStore{}
	handler := newTestHandler(store)

	body := `{
		"name": "analytics-job",
		"cron_expression": "0 * * * *",
		"timezone": "UTC",
		"webhook_url": "https://example.com/webhook",
		"analytics": {"retention_seconds": 86400}
	}`

	req := httptest.NewRequest(http.MethodPost, "/jobs", strings.NewReader(body))
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandler_CreateJob_WithWebhookSecret(t *testing.T) {
	store := &mockHandlerStore{}
	handler := newTestHandler(store)

	body := `{
		"name": "secret-job",
		"cron_expression": "0 * * * *",
		"timezone": "UTC",
		"webhook_url": "https://example.com/webhook",
		"webhook_secret": "my-secret-key"
	}`

	req := httptest.NewRequest(http.MethodPost, "/jobs", strings.NewReader(body))
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
}
