package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/djlord-it/easy-cron/internal/domain"
)

// Pagination defaults and limits.
const (
	DefaultLimit = 100
	MaxLimit     = 1000
)

type Store interface {
	CreateJob(ctx context.Context, job domain.Job, schedule domain.Schedule) error
	ListJobs(ctx context.Context, ns domain.Namespace, limit, offset int) ([]domain.JobWithSchedule, error)
	ListExecutions(ctx context.Context, jobID uuid.UUID, limit, offset int) ([]domain.Execution, error)
	DeleteJob(ctx context.Context, jobID uuid.UUID, ns domain.Namespace) error
}

// HealthChecker provides database health status for the /health endpoint.
type HealthChecker interface {
	PingContext(ctx context.Context) error
}

type LegacyHandler struct {
	store     Store
	namespace domain.Namespace // single-tenant for now
	db        HealthChecker
}

func NewHandler(store Store, ns domain.Namespace) *LegacyHandler {
	return &LegacyHandler{store: store, namespace: ns}
}

// WithHealthChecker sets the database health checker for verbose /health responses.
func (h *LegacyHandler) WithHealthChecker(db HealthChecker) *LegacyHandler {
	h.db = db
	return h
}

func (h *LegacyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	switch {
	case path == "/health" && r.Method == http.MethodGet:
		h.health(w, r)

	case path == "/jobs" && r.Method == http.MethodPost:
		h.createJob(w, r)

	case path == "/jobs" && r.Method == http.MethodGet:
		h.listJobs(w, r)

	case strings.HasSuffix(path, "/executions") && r.Method == http.MethodGet:
		h.listExecutions(w, r)

	case strings.HasPrefix(path, "/jobs/") && r.Method == http.MethodDelete:
		h.deleteJob(w, r)

	default:
		writeError(w, http.StatusNotFound, "not found")
	}
}

// LegacyHealthResponse represents the /health endpoint response.
type LegacyHealthResponse struct {
	Status     string            `json:"status"`
	Components map[string]string `json:"components,omitempty"`
}

func (h *LegacyHandler) health(w http.ResponseWriter, r *http.Request) {
	// Check if verbose mode requested via ?verbose=true
	verbose := r.URL.Query().Get("verbose") == "true"

	if !verbose || h.db == nil {
		// Simple health check - just return ok
		writeJSON(w, http.StatusOK, LegacyHealthResponse{Status: "ok"})
		return
	}

	// Verbose health check - check all components
	resp := LegacyHealthResponse{
		Status:     "ok",
		Components: make(map[string]string),
	}

	// Check database connectivity with timeout
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	if err := h.db.PingContext(ctx); err != nil {
		resp.Status = "degraded"
		log.Printf("api: health check database unhealthy: %v", err)
		resp.Components["database"] = "unhealthy"
	} else {
		resp.Components["database"] = "healthy"
	}

	// Return appropriate status code based on health
	statusCode := http.StatusOK
	if resp.Status == "degraded" {
		statusCode = http.StatusServiceUnavailable
	}

	writeJSON(w, statusCode, resp)
}

// maxRequestBodySize is the maximum allowed request body size (1MB).
const maxRequestBodySize = 1 << 20

func (h *LegacyHandler) createJob(w http.ResponseWriter, r *http.Request) {
	// Limit request body size to prevent DoS via large payloads
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)

	var req LegacyCreateJobRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		// Check if error is due to body size limit
		if err.Error() == "http: request body too large" {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}

	if err := validateCreateJob(req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	timeout := 30 * time.Second
	if req.WebhookTimeout > 0 {
		timeout = time.Duration(req.WebhookTimeout) * time.Second
	}

	now := time.Now().UTC()
	jobID := uuid.New()
	scheduleID := uuid.New()

	job := domain.Job{
		ID:        jobID,
		Namespace: h.namespace,
		Name:      req.Name,
		Enabled:   true,
		ScheduleID: scheduleID,
		Delivery: domain.DeliveryConfig{
			Type:       domain.DeliveryTypeWebhook,
			WebhookURL: req.WebhookURL,
			Secret:     req.WebhookSecret,
			Timeout:    timeout,
		},
		Analytics: parseAnalyticsConfig(req.Analytics),
		CreatedAt: now,
		UpdatedAt: now,
	}

	schedule := domain.Schedule{
		ID:             scheduleID,
		CronExpression: req.CronExpression,
		Timezone:       req.Timezone,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	if err := h.store.CreateJob(r.Context(), job, schedule); err != nil {
		log.Printf("api: create job error: %v", err)
		writeError(w, http.StatusInternalServerError, "failed to create job")
		return
	}

	resp := JobResponse{
		ID:             job.ID.String(),
		Namespace:      job.Namespace.String(),
		Name:           job.Name,
		Enabled:        job.Enabled,
		CronExpression: schedule.CronExpression,
		Timezone:       schedule.Timezone,
		WebhookURL:     job.Delivery.WebhookURL,
		CreatedAt:      formatTime(job.CreatedAt),
	}

	writeJSON(w, http.StatusCreated, resp)
}

func (h *LegacyHandler) listJobs(w http.ResponseWriter, r *http.Request) {
	limit, offset, err := parsePagination(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	jobs, err := h.store.ListJobs(r.Context(), h.namespace, limit, offset)
	if err != nil {
		log.Printf("api: list jobs error: %v", err)
		writeError(w, http.StatusInternalServerError, "failed to list jobs")
		return
	}

	resp := ListJobsResponse{Jobs: make([]JobResponse, len(jobs))}
	for i, jws := range jobs {
		resp.Jobs[i] = JobResponse{
			ID:             jws.Job.ID.String(),
			Namespace:      jws.Job.Namespace.String(),
			Name:           jws.Job.Name,
			Enabled:        jws.Job.Enabled,
			CronExpression: jws.Schedule.CronExpression,
			Timezone:       jws.Schedule.Timezone,
			WebhookURL:     jws.Job.Delivery.WebhookURL,
			CreatedAt:      formatTime(jws.Job.CreatedAt),
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

func (h *LegacyHandler) listExecutions(w http.ResponseWriter, r *http.Request) {
	// Extract job ID from path: /jobs/{id}/executions
	path := r.URL.Path
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 3 || parts[0] != "jobs" || parts[2] != "executions" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	jobID, err := uuid.Parse(parts[1])
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid job id")
		return
	}

	limit, offset, err := parsePagination(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	executions, err := h.store.ListExecutions(r.Context(), jobID, limit, offset)
	if err != nil {
		log.Printf("api: list executions error: %v", err)
		writeError(w, http.StatusInternalServerError, "failed to list executions")
		return
	}

	resp := ListExecutionsResponse{Executions: make([]ExecutionResponse, len(executions))}
	for i, exec := range executions {
		resp.Executions[i] = ExecutionResponse{
			ID:          exec.ID.String(),
			JobID:       exec.JobID.String(),
			ScheduledAt: formatTime(exec.ScheduledAt),
			FiredAt:     formatTime(exec.FiredAt),
			Status:      string(exec.Status),
			CreatedAt:   formatTime(exec.CreatedAt),
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

func (h *LegacyHandler) deleteJob(w http.ResponseWriter, r *http.Request) {
	// Extract job ID from path: /jobs/{id}
	path := r.URL.Path
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 2 || parts[0] != "jobs" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	jobID, err := uuid.Parse(parts[1])
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid job id")
		return
	}

	if err := h.store.DeleteJob(r.Context(), jobID, h.namespace); err != nil {
		log.Printf("api: delete job error: %v", err)
		if err == sql.ErrNoRows {
			writeError(w, http.StatusNotFound, "job not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to delete job")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("api: json encode error: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, ErrorResponse{Error: msg})
}

// parseAnalyticsConfig converts a validated AnalyticsRequest to domain config.
// If analytics is nil, returns a disabled config.
func parseAnalyticsConfig(a *AnalyticsRequest) domain.AnalyticsConfig {
	if a == nil {
		return domain.AnalyticsConfig{}
	}
	return domain.AnalyticsConfig{
		Enabled:          true,
		RetentionSeconds: a.RetentionSeconds,
	}
}

// parsePagination extracts and validates limit/offset query parameters.
// Returns DefaultLimit if limit is not specified, and 0 for offset if not specified.
// Returns an error if limit exceeds MaxLimit or if values are negative/invalid.
func parsePagination(r *http.Request) (limit, offset int, err error) {
	limit = DefaultLimit
	offset = 0

	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		limit, err = strconv.Atoi(limitStr)
		if err != nil {
			return 0, 0, err
		}
		if limit < 0 {
			return 0, 0, strconv.ErrRange
		}
		if limit > MaxLimit {
			return 0, 0, &limitExceededError{max: MaxLimit}
		}
		if limit == 0 {
			limit = DefaultLimit
		}
	}

	if offsetStr := r.URL.Query().Get("offset"); offsetStr != "" {
		offset, err = strconv.Atoi(offsetStr)
		if err != nil {
			return 0, 0, err
		}
		if offset < 0 {
			return 0, 0, strconv.ErrRange
		}
	}

	return limit, offset, nil
}

type limitExceededError struct {
	max int
}

func (e *limitExceededError) Error() string {
	return "limit exceeds maximum of " + strconv.Itoa(e.max)
}
