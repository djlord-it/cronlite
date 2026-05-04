package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/djlord-it/cronlite/internal/cron"
	"github.com/djlord-it/cronlite/internal/domain"
	"github.com/djlord-it/cronlite/internal/service"
	"github.com/google/uuid"
)

// ── Mock repositories ────────────────────────────────────────────────────────

type mockJobRepo struct {
	insertJobFn          func(ctx context.Context, job domain.Job, schedule domain.Schedule) error
	getJobFn             func(ctx context.Context, id uuid.UUID) (domain.Job, error)
	getJobWithScheduleFn func(ctx context.Context, id uuid.UUID) (domain.Job, domain.Schedule, error)
	listJobsFn           func(ctx context.Context, filter domain.JobFilter) ([]domain.Job, error)
	updateJobFn          func(ctx context.Context, job domain.Job) error
	deleteJobFn          func(ctx context.Context, id uuid.UUID, ns domain.Namespace) error
	getEnabledJobsFn     func(ctx context.Context, limit int, afterID uuid.UUID) ([]domain.JobWithSchedule, error)
}

func (m *mockJobRepo) InsertJob(ctx context.Context, job domain.Job, schedule domain.Schedule) error {
	if m.insertJobFn != nil {
		return m.insertJobFn(ctx, job, schedule)
	}
	return nil
}
func (m *mockJobRepo) GetJob(ctx context.Context, id uuid.UUID) (domain.Job, error) {
	if m.getJobFn != nil {
		return m.getJobFn(ctx, id)
	}
	return domain.Job{}, nil
}
func (m *mockJobRepo) GetJobWithSchedule(ctx context.Context, id uuid.UUID) (domain.Job, domain.Schedule, error) {
	if m.getJobWithScheduleFn != nil {
		return m.getJobWithScheduleFn(ctx, id)
	}
	return domain.Job{}, domain.Schedule{}, nil
}
func (m *mockJobRepo) GetJobWithScheduleScoped(ctx context.Context, id uuid.UUID, ns domain.Namespace) (domain.Job, domain.Schedule, error) {
	if m.getJobWithScheduleFn != nil {
		return m.getJobWithScheduleFn(ctx, id)
	}
	return domain.Job{}, domain.Schedule{}, nil
}
func (m *mockJobRepo) ListJobs(ctx context.Context, filter domain.JobFilter) ([]domain.Job, error) {
	if m.listJobsFn != nil {
		return m.listJobsFn(ctx, filter)
	}
	return nil, nil
}
func (m *mockJobRepo) UpdateJob(ctx context.Context, job domain.Job) error {
	if m.updateJobFn != nil {
		return m.updateJobFn(ctx, job)
	}
	return nil
}
func (m *mockJobRepo) DeleteJob(ctx context.Context, id uuid.UUID, ns domain.Namespace) error {
	if m.deleteJobFn != nil {
		return m.deleteJobFn(ctx, id, ns)
	}
	return nil
}
func (m *mockJobRepo) GetEnabledJobs(ctx context.Context, limit int, afterID uuid.UUID) ([]domain.JobWithSchedule, error) {
	if m.getEnabledJobsFn != nil {
		return m.getEnabledJobsFn(ctx, limit, afterID)
	}
	return nil, nil
}

type mockScheduleRepo struct {
	insertScheduleFn func(ctx context.Context, schedule domain.Schedule) error
	getScheduleFn    func(ctx context.Context, id uuid.UUID) (domain.Schedule, error)
	updateScheduleFn func(ctx context.Context, schedule domain.Schedule) error
}

func (m *mockScheduleRepo) InsertSchedule(ctx context.Context, schedule domain.Schedule) error {
	if m.insertScheduleFn != nil {
		return m.insertScheduleFn(ctx, schedule)
	}
	return nil
}
func (m *mockScheduleRepo) GetSchedule(ctx context.Context, id uuid.UUID) (domain.Schedule, error) {
	if m.getScheduleFn != nil {
		return m.getScheduleFn(ctx, id)
	}
	return domain.Schedule{}, nil
}
func (m *mockScheduleRepo) UpdateSchedule(ctx context.Context, schedule domain.Schedule) error {
	if m.updateScheduleFn != nil {
		return m.updateScheduleFn(ctx, schedule)
	}
	return nil
}

type mockExecRepo struct {
	insertExecutionFn       func(ctx context.Context, exec domain.Execution) error
	getExecutionFn          func(ctx context.Context, id uuid.UUID) (domain.Execution, error)
	getExecutionScopedFn    func(ctx context.Context, id uuid.UUID, ns domain.Namespace) (domain.Execution, error)
	listExecutionsFn        func(ctx context.Context, filter domain.ExecutionFilter) ([]domain.Execution, error)
	listPendingAckFn        func(ctx context.Context, ns domain.Namespace, jobID *uuid.UUID, limit int) ([]domain.Execution, error)
	ackExecutionFn          func(ctx context.Context, id uuid.UUID, ns domain.Namespace) error
	getRecentExecutionsFn   func(ctx context.Context, jobID uuid.UUID, limit int) ([]domain.Execution, error)
	updateExecutionStatusFn func(ctx context.Context, id uuid.UUID, status domain.ExecutionStatus) error
	dequeueExecutionFn      func(ctx context.Context) (*domain.Execution, error)
	getOrphanedExecFn       func(ctx context.Context, olderThan time.Time, maxResults int) ([]domain.Execution, error)
	requeueStaleExecFn      func(ctx context.Context, olderThan time.Time, limit int) (int, error)
}

func (m *mockExecRepo) InsertExecution(ctx context.Context, exec domain.Execution) error {
	if m.insertExecutionFn != nil {
		return m.insertExecutionFn(ctx, exec)
	}
	return nil
}
func (m *mockExecRepo) GetExecution(ctx context.Context, id uuid.UUID) (domain.Execution, error) {
	if m.getExecutionFn != nil {
		return m.getExecutionFn(ctx, id)
	}
	return domain.Execution{}, nil
}
func (m *mockExecRepo) GetExecutionScoped(ctx context.Context, id uuid.UUID, ns domain.Namespace) (domain.Execution, error) {
	if m.getExecutionScopedFn != nil {
		return m.getExecutionScopedFn(ctx, id, ns)
	}
	return domain.Execution{}, domain.ErrExecutionNotFound
}
func (m *mockExecRepo) ListExecutions(ctx context.Context, filter domain.ExecutionFilter) ([]domain.Execution, error) {
	if m.listExecutionsFn != nil {
		return m.listExecutionsFn(ctx, filter)
	}
	return nil, nil
}
func (m *mockExecRepo) ListPendingAck(ctx context.Context, ns domain.Namespace, jobID *uuid.UUID, limit int) ([]domain.Execution, error) {
	if m.listPendingAckFn != nil {
		return m.listPendingAckFn(ctx, ns, jobID, limit)
	}
	return nil, nil
}
func (m *mockExecRepo) AckExecution(ctx context.Context, id uuid.UUID, ns domain.Namespace) error {
	if m.ackExecutionFn != nil {
		return m.ackExecutionFn(ctx, id, ns)
	}
	return nil
}
func (m *mockExecRepo) GetRecentExecutions(ctx context.Context, jobID uuid.UUID, limit int) ([]domain.Execution, error) {
	if m.getRecentExecutionsFn != nil {
		return m.getRecentExecutionsFn(ctx, jobID, limit)
	}
	return nil, nil
}
func (m *mockExecRepo) UpdateExecutionStatus(ctx context.Context, id uuid.UUID, status domain.ExecutionStatus) error {
	if m.updateExecutionStatusFn != nil {
		return m.updateExecutionStatusFn(ctx, id, status)
	}
	return nil
}
func (m *mockExecRepo) DequeueExecution(ctx context.Context) (*domain.Execution, error) {
	if m.dequeueExecutionFn != nil {
		return m.dequeueExecutionFn(ctx)
	}
	return nil, nil
}
func (m *mockExecRepo) GetOrphanedExecutions(ctx context.Context, olderThan time.Time, maxResults int) ([]domain.Execution, error) {
	if m.getOrphanedExecFn != nil {
		return m.getOrphanedExecFn(ctx, olderThan, maxResults)
	}
	return nil, nil
}
func (m *mockExecRepo) RequeueStaleExecutions(ctx context.Context, olderThan time.Time, limit int) (int, error) {
	if m.requeueStaleExecFn != nil {
		return m.requeueStaleExecFn(ctx, olderThan, limit)
	}
	return 0, nil
}

type mockTagRepo struct {
	upsertTagsFn func(ctx context.Context, jobID uuid.UUID, tags []domain.Tag) error
	getTagsFn    func(ctx context.Context, jobID uuid.UUID) ([]domain.Tag, error)
	deleteTagsFn func(ctx context.Context, jobID uuid.UUID) error
}

func (m *mockTagRepo) UpsertTags(ctx context.Context, jobID uuid.UUID, tags []domain.Tag) error {
	if m.upsertTagsFn != nil {
		return m.upsertTagsFn(ctx, jobID, tags)
	}
	return nil
}
func (m *mockTagRepo) GetTags(ctx context.Context, jobID uuid.UUID) ([]domain.Tag, error) {
	if m.getTagsFn != nil {
		return m.getTagsFn(ctx, jobID)
	}
	return nil, nil
}
func (m *mockTagRepo) DeleteTags(ctx context.Context, jobID uuid.UUID) error {
	if m.deleteTagsFn != nil {
		return m.deleteTagsFn(ctx, jobID)
	}
	return nil
}

type mockAPIKeyRepoV2 struct {
	insertAPIKeyFn   func(ctx context.Context, key domain.APIKey) error
	getKeyByHashFn   func(ctx context.Context, tokenHash string) (domain.APIKey, error)
	listKeysFn       func(ctx context.Context, ns domain.Namespace, params domain.ListParams) ([]domain.APIKey, error)
	deleteKeyFn      func(ctx context.Context, id uuid.UUID, ns domain.Namespace) error
	updateLastUsedFn func(ctx context.Context, ids []uuid.UUID) error
}

func (m *mockAPIKeyRepoV2) InsertAPIKey(ctx context.Context, key domain.APIKey) error {
	if m.insertAPIKeyFn != nil {
		return m.insertAPIKeyFn(ctx, key)
	}
	return nil
}
func (m *mockAPIKeyRepoV2) GetKeyByTokenHash(ctx context.Context, tokenHash string) (domain.APIKey, error) {
	if m.getKeyByHashFn != nil {
		return m.getKeyByHashFn(ctx, tokenHash)
	}
	return domain.APIKey{}, nil
}
func (m *mockAPIKeyRepoV2) ListKeys(ctx context.Context, ns domain.Namespace, params domain.ListParams) ([]domain.APIKey, error) {
	if m.listKeysFn != nil {
		return m.listKeysFn(ctx, ns, params)
	}
	return nil, nil
}
func (m *mockAPIKeyRepoV2) DeleteKey(ctx context.Context, id uuid.UUID, ns domain.Namespace) error {
	if m.deleteKeyFn != nil {
		return m.deleteKeyFn(ctx, id, ns)
	}
	return nil
}
func (m *mockAPIKeyRepoV2) UpdateLastUsedAt(ctx context.Context, ids []uuid.UUID) error {
	if m.updateLastUsedFn != nil {
		return m.updateLastUsedFn(ctx, ids)
	}
	return nil
}

type mockAttemptRepo struct {
	insertAttemptFn func(ctx context.Context, attempt domain.DeliveryAttempt) error
	getAttemptsFn   func(ctx context.Context, executionID uuid.UUID) ([]domain.DeliveryAttempt, error)
}

func (m *mockAttemptRepo) InsertDeliveryAttempt(ctx context.Context, attempt domain.DeliveryAttempt) error {
	if m.insertAttemptFn != nil {
		return m.insertAttemptFn(ctx, attempt)
	}
	return nil
}
func (m *mockAttemptRepo) GetAttempts(ctx context.Context, executionID uuid.UUID) ([]domain.DeliveryAttempt, error) {
	if m.getAttemptsFn != nil {
		return m.getAttemptsFn(ctx, executionID)
	}
	return nil, nil
}

// ── Mock HealthChecker ───────────────────────────────────────────────────────

type mockHealthCheckerV2 struct {
	pingErr error
}

func (m *mockHealthCheckerV2) PingContext(ctx context.Context) error {
	return m.pingErr
}

type mockEmitterV2 struct {
	lastEvent domain.TriggerEvent
	called    bool
}

func (m *mockEmitterV2) Emit(ctx context.Context, event domain.TriggerEvent) error {
	m.called = true
	m.lastEvent = event
	return nil
}

// ── Helpers ──────────────────────────────────────────────────────────────────

func ctxWithNS(ns string) context.Context {
	return domain.NamespaceToContext(context.Background(), domain.Namespace(ns))
}

func newTestServer(jr *mockJobRepo, sr *mockScheduleRepo, er *mockExecRepo, tr *mockTagRepo, kr *mockAPIKeyRepoV2, ar *mockAttemptRepo) *ServerImpl {
	if jr == nil {
		jr = &mockJobRepo{}
	}
	if sr == nil {
		sr = &mockScheduleRepo{}
	}
	if er == nil {
		er = &mockExecRepo{}
	}
	if tr == nil {
		tr = &mockTagRepo{}
	}
	if kr == nil {
		kr = &mockAPIKeyRepoV2{}
	}
	if ar == nil {
		ar = &mockAttemptRepo{}
	}
	svc := service.NewJobService(jr, sr, er, tr, kr, ar, cron.NewParser())
	return NewServerImpl(svc)
}

func boolPtr(b bool) *bool    { return &b }
func strPtr(s string) *string { return &s }
func intPtr(i int) *int       { return &i }

// fixedJob returns a domain.Job with sensible defaults for testing.
func fixedJob(id uuid.UUID, ns string) domain.Job {
	now := time.Now().UTC()
	return domain.Job{
		ID:        id,
		Namespace: domain.Namespace(ns),
		Name:      "test-job",
		Enabled:   true,
		Delivery: domain.DeliveryConfig{
			Type:       domain.DeliveryTypeWebhook,
			WebhookURL: "https://example.com/hook",
			Timeout:    30 * time.Second,
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// fixedSchedule returns a domain.Schedule with sensible defaults for testing.
func fixedSchedule(id uuid.UUID) domain.Schedule {
	now := time.Now().UTC()
	return domain.Schedule{
		ID:             id,
		CronExpression: "*/5 * * * *",
		Timezone:       "UTC",
		CreatedAt:      now,
		UpdatedAt:      now,
	}
}

// ── Health Tests ─────────────────────────────────────────────────────────────

func TestGetHealth_Simple(t *testing.T) {
	srv := newTestServer(nil, nil, nil, nil, nil, nil)
	ctx := context.Background()

	resp, err := srv.GetHealth(ctx, GetHealthRequestObject{
		Params: GetHealthParams{Verbose: nil},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, ok := resp.(GetHealth200JSONResponse)
	if !ok {
		t.Fatalf("expected GetHealth200JSONResponse, got %T", resp)
	}
	if got.Status != "ok" {
		t.Fatalf("expected status %q, got %q", "ok", got.Status)
	}
	if got.Database != nil {
		t.Fatalf("expected Database to be nil, got %v", *got.Database)
	}
}

func TestGetHealth_VerboseHealthy(t *testing.T) {
	srv := newTestServer(nil, nil, nil, nil, nil, nil)
	srv.WithHealthChecker(&mockHealthCheckerV2{pingErr: nil})
	ctx := context.Background()

	resp, err := srv.GetHealth(ctx, GetHealthRequestObject{
		Params: GetHealthParams{Verbose: boolPtr(true)},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, ok := resp.(GetHealth200JSONResponse)
	if !ok {
		t.Fatalf("expected GetHealth200JSONResponse, got %T", resp)
	}
	if got.Status != "ok" {
		t.Fatalf("expected status %q, got %q", "ok", got.Status)
	}
	if got.Database == nil {
		t.Fatal("expected Database to be set")
	}
	if *got.Database != "healthy" {
		t.Fatalf("expected database %q, got %q", "healthy", *got.Database)
	}
}

func TestGetHealth_VerboseUnhealthy(t *testing.T) {
	srv := newTestServer(nil, nil, nil, nil, nil, nil)
	srv.WithHealthChecker(&mockHealthCheckerV2{pingErr: errors.New("connection refused")})
	ctx := context.Background()

	resp, err := srv.GetHealth(ctx, GetHealthRequestObject{
		Params: GetHealthParams{Verbose: boolPtr(true)},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, ok := resp.(GetHealth503JSONResponse)
	if !ok {
		t.Fatalf("expected GetHealth503JSONResponse, got %T", resp)
	}
	if got.Status != "degraded" {
		t.Fatalf("expected status %q, got %q", "degraded", got.Status)
	}
	if got.Database == nil {
		t.Fatal("expected Database to be set")
	}
	if *got.Database != "unhealthy" {
		t.Fatalf("expected database %q, got %q", "unhealthy", *got.Database)
	}

	w := httptest.NewRecorder()
	if err := resp.VisitGetHealthResponse(w); err != nil {
		t.Fatalf("unexpected response write error: %v", err)
	}
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected HTTP status %d, got %d", http.StatusServiceUnavailable, w.Code)
	}
}

func TestGetHealth_VerboseNoChecker(t *testing.T) {
	srv := newTestServer(nil, nil, nil, nil, nil, nil)
	// db is nil — no WithHealthChecker call
	ctx := context.Background()

	resp, err := srv.GetHealth(ctx, GetHealthRequestObject{
		Params: GetHealthParams{Verbose: boolPtr(true)},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, ok := resp.(GetHealth200JSONResponse)
	if !ok {
		t.Fatalf("expected GetHealth200JSONResponse, got %T", resp)
	}
	if got.Status != "ok" {
		t.Fatalf("expected status %q, got %q", "ok", got.Status)
	}
	if got.Database != nil {
		t.Fatalf("expected Database to be nil when checker is nil, got %v", *got.Database)
	}
}

// ── CreateJob Tests ──────────────────────────────────────────────────────────

func TestCreateJob_HappyPath(t *testing.T) {
	srv := newTestServer(nil, nil, nil, nil, nil, nil)
	ctx := ctxWithNS("t1")

	resp, err := srv.CreateJob(ctx, CreateJobRequestObject{
		Body: &CreateJobRequest{
			Name:           "my-job",
			CronExpression: "*/5 * * * *",
			Timezone:       "UTC",
			WebhookUrl:     "https://example.com/hook",
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, ok := resp.(CreateJob201JSONResponse)
	if !ok {
		t.Fatalf("expected CreateJob201JSONResponse, got %T", resp)
	}
	if got.Name != "my-job" {
		t.Fatalf("expected name %q, got %q", "my-job", got.Name)
	}
	if got.CronExpression != "*/5 * * * *" {
		t.Fatalf("expected cron %q, got %q", "*/5 * * * *", got.CronExpression)
	}
	if got.Namespace != "t1" {
		t.Fatalf("expected namespace %q, got %q", "t1", got.Namespace)
	}
	if !got.Enabled {
		t.Fatal("expected Enabled to be true")
	}
}

func TestCreateJob_InvalidCron(t *testing.T) {
	srv := newTestServer(nil, nil, nil, nil, nil, nil)
	ctx := ctxWithNS("t1")

	resp, err := srv.CreateJob(ctx, CreateJobRequestObject{
		Body: &CreateJobRequest{
			Name:           "bad-cron",
			CronExpression: "not-a-cron",
			Timezone:       "UTC",
			WebhookUrl:     "https://example.com/hook",
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, ok := resp.(CreateJob422JSONResponse); !ok {
		t.Fatalf("expected CreateJob422JSONResponse, got %T", resp)
	}
}

// ── ListJobs Tests ───────────────────────────────────────────────────────────

func TestListJobs_HappyPath(t *testing.T) {
	jobID := uuid.New()
	jr := &mockJobRepo{
		listJobsFn: func(ctx context.Context, filter domain.JobFilter) ([]domain.Job, error) {
			return []domain.Job{fixedJob(jobID, "t1")}, nil
		},
	}
	srv := newTestServer(jr, nil, nil, nil, nil, nil)
	ctx := ctxWithNS("t1")

	resp, err := srv.ListJobs(ctx, ListJobsRequestObject{
		Params: ListJobsParams{},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, ok := resp.(ListJobs200JSONResponse)
	if !ok {
		t.Fatalf("expected ListJobs200JSONResponse, got %T", resp)
	}
	if len(got.Jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(got.Jobs))
	}
	if got.Jobs[0].Name != "test-job" {
		t.Fatalf("expected job name %q, got %q", "test-job", got.Jobs[0].Name)
	}
}

// ── GetJob Tests ─────────────────────────────────────────────────────────────

func TestGetJob_HappyPath(t *testing.T) {
	jobID := uuid.New()
	schedID := uuid.New()
	jr := &mockJobRepo{
		getJobWithScheduleFn: func(ctx context.Context, id uuid.UUID) (domain.Job, domain.Schedule, error) {
			j := fixedJob(jobID, "t1")
			j.ScheduleID = schedID
			return j, fixedSchedule(schedID), nil
		},
	}
	tr := &mockTagRepo{
		getTagsFn: func(ctx context.Context, jID uuid.UUID) ([]domain.Tag, error) {
			return []domain.Tag{{Key: "env", Value: "prod"}}, nil
		},
	}
	er := &mockExecRepo{
		getRecentExecutionsFn: func(ctx context.Context, jID uuid.UUID, limit int) ([]domain.Execution, error) {
			return nil, nil
		},
	}
	srv := newTestServer(jr, nil, er, tr, nil, nil)
	ctx := ctxWithNS("t1")

	resp, err := srv.GetJob(ctx, GetJobRequestObject{Id: jobID})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, ok := resp.(GetJob200JSONResponse)
	if !ok {
		t.Fatalf("expected GetJob200JSONResponse, got %T", resp)
	}
	if got.Name != "test-job" {
		t.Fatalf("expected name %q, got %q", "test-job", got.Name)
	}
	if got.Tags == nil {
		t.Fatal("expected tags to be set")
	}
	if (*got.Tags)["env"] != "prod" {
		t.Fatalf("expected tag env=prod, got %v", *got.Tags)
	}
}

func TestGetJob_NotFound(t *testing.T) {
	jr := &mockJobRepo{
		getJobWithScheduleFn: func(ctx context.Context, id uuid.UUID) (domain.Job, domain.Schedule, error) {
			return domain.Job{}, domain.Schedule{}, errors.New("not found")
		},
	}
	srv := newTestServer(jr, nil, nil, nil, nil, nil)
	ctx := ctxWithNS("t1")

	resp, err := srv.GetJob(ctx, GetJobRequestObject{Id: uuid.New()})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, ok := resp.(GetJob404JSONResponse); !ok {
		t.Fatalf("expected GetJob404JSONResponse, got %T", resp)
	}
}

// ── UpdateJob Tests ──────────────────────────────────────────────────────────

func TestUpdateJob_HappyPath(t *testing.T) {
	jobID := uuid.New()
	schedID := uuid.New()
	jr := &mockJobRepo{
		getJobWithScheduleFn: func(ctx context.Context, id uuid.UUID) (domain.Job, domain.Schedule, error) {
			j := fixedJob(jobID, "t1")
			j.ScheduleID = schedID
			return j, fixedSchedule(schedID), nil
		},
	}
	srv := newTestServer(jr, nil, nil, nil, nil, nil)
	ctx := ctxWithNS("t1")

	newName := "updated-name"
	resp, err := srv.UpdateJob(ctx, UpdateJobRequestObject{
		Id: jobID,
		Body: &UpdateJobRequest{
			Name: &newName,
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, ok := resp.(UpdateJob200JSONResponse)
	if !ok {
		t.Fatalf("expected UpdateJob200JSONResponse, got %T", resp)
	}
	if got.Name != "updated-name" {
		t.Fatalf("expected name %q, got %q", "updated-name", got.Name)
	}
}

func TestUpdateJob_NotFound(t *testing.T) {
	jr := &mockJobRepo{
		getJobWithScheduleFn: func(ctx context.Context, id uuid.UUID) (domain.Job, domain.Schedule, error) {
			return domain.Job{}, domain.Schedule{}, errors.New("not found")
		},
	}
	srv := newTestServer(jr, nil, nil, nil, nil, nil)
	ctx := ctxWithNS("t1")

	resp, err := srv.UpdateJob(ctx, UpdateJobRequestObject{
		Id:   uuid.New(),
		Body: &UpdateJobRequest{Name: strPtr("x")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, ok := resp.(UpdateJob404JSONResponse); !ok {
		t.Fatalf("expected UpdateJob404JSONResponse, got %T", resp)
	}
}

func TestUpdateJob_InvalidCron(t *testing.T) {
	jobID := uuid.New()
	schedID := uuid.New()
	jr := &mockJobRepo{
		getJobWithScheduleFn: func(ctx context.Context, id uuid.UUID) (domain.Job, domain.Schedule, error) {
			j := fixedJob(jobID, "t1")
			j.ScheduleID = schedID
			return j, fixedSchedule(schedID), nil
		},
	}
	srv := newTestServer(jr, nil, nil, nil, nil, nil)
	ctx := ctxWithNS("t1")

	resp, err := srv.UpdateJob(ctx, UpdateJobRequestObject{
		Id: jobID,
		Body: &UpdateJobRequest{
			CronExpression: strPtr("invalid-cron"),
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, ok := resp.(UpdateJob422JSONResponse); !ok {
		t.Fatalf("expected UpdateJob422JSONResponse, got %T", resp)
	}
}

// ── DeleteJob Tests ──────────────────────────────────────────────────────────

func TestDeleteJob_HappyPath(t *testing.T) {
	srv := newTestServer(nil, nil, nil, nil, nil, nil)
	ctx := ctxWithNS("t1")

	resp, err := srv.DeleteJob(ctx, DeleteJobRequestObject{Id: uuid.New()})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, ok := resp.(DeleteJob204Response); !ok {
		t.Fatalf("expected DeleteJob204Response, got %T", resp)
	}
}

func TestDeleteJob_NotFound(t *testing.T) {
	jr := &mockJobRepo{
		deleteJobFn: func(ctx context.Context, id uuid.UUID, ns domain.Namespace) error {
			return domain.ErrJobNotFound
		},
	}
	srv := newTestServer(jr, nil, nil, nil, nil, nil)
	ctx := ctxWithNS("t1")

	resp, err := srv.DeleteJob(ctx, DeleteJobRequestObject{Id: uuid.New()})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, ok := resp.(DeleteJob404JSONResponse); !ok {
		t.Fatalf("expected DeleteJob404JSONResponse, got %T", resp)
	}
}

// ── PauseJob Tests ───────────────────────────────────────────────────────────

func TestPauseJob_HappyPath(t *testing.T) {
	jobID := uuid.New()
	jr := &mockJobRepo{
		getJobWithScheduleFn: func(ctx context.Context, id uuid.UUID) (domain.Job, domain.Schedule, error) {
			return fixedJob(jobID, "t1"), fixedSchedule(uuid.New()), nil
		},
	}
	srv := newTestServer(jr, nil, nil, nil, nil, nil)
	ctx := ctxWithNS("t1")

	resp, err := srv.PauseJob(ctx, PauseJobRequestObject{Id: jobID})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, ok := resp.(PauseJob200JSONResponse)
	if !ok {
		t.Fatalf("expected PauseJob200JSONResponse, got %T", resp)
	}
	if got.Enabled {
		t.Fatal("expected Enabled to be false after pause")
	}
}

func TestPauseJob_NotFound(t *testing.T) {
	jr := &mockJobRepo{
		getJobWithScheduleFn: func(ctx context.Context, id uuid.UUID) (domain.Job, domain.Schedule, error) {
			return domain.Job{}, domain.Schedule{}, errors.New("not found")
		},
	}
	srv := newTestServer(jr, nil, nil, nil, nil, nil)
	ctx := ctxWithNS("t1")

	resp, err := srv.PauseJob(ctx, PauseJobRequestObject{Id: uuid.New()})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, ok := resp.(PauseJob404JSONResponse); !ok {
		t.Fatalf("expected PauseJob404JSONResponse, got %T", resp)
	}
}

// ── ResumeJob Tests ──────────────────────────────────────────────────────────

func TestResumeJob_HappyPath(t *testing.T) {
	jobID := uuid.New()
	jr := &mockJobRepo{
		getJobWithScheduleFn: func(ctx context.Context, id uuid.UUID) (domain.Job, domain.Schedule, error) {
			j := fixedJob(jobID, "t1")
			j.Enabled = false // job is paused
			return j, fixedSchedule(uuid.New()), nil
		},
	}
	srv := newTestServer(jr, nil, nil, nil, nil, nil)
	ctx := ctxWithNS("t1")

	resp, err := srv.ResumeJob(ctx, ResumeJobRequestObject{Id: jobID})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, ok := resp.(ResumeJob200JSONResponse)
	if !ok {
		t.Fatalf("expected ResumeJob200JSONResponse, got %T", resp)
	}
	if !got.Enabled {
		t.Fatal("expected Enabled to be true after resume")
	}
}

func TestResumeJob_NotFound(t *testing.T) {
	jr := &mockJobRepo{
		getJobWithScheduleFn: func(ctx context.Context, id uuid.UUID) (domain.Job, domain.Schedule, error) {
			return domain.Job{}, domain.Schedule{}, errors.New("not found")
		},
	}
	srv := newTestServer(jr, nil, nil, nil, nil, nil)
	ctx := ctxWithNS("t1")

	resp, err := srv.ResumeJob(ctx, ResumeJobRequestObject{Id: uuid.New()})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, ok := resp.(ResumeJob404JSONResponse); !ok {
		t.Fatalf("expected ResumeJob404JSONResponse, got %T", resp)
	}
}

// ── TriggerJob Tests ─────────────────────────────────────────────────────────

func TestTriggerJob_HappyPath(t *testing.T) {
	jobID := uuid.New()
	jr := &mockJobRepo{
		getJobWithScheduleFn: func(ctx context.Context, id uuid.UUID) (domain.Job, domain.Schedule, error) {
			return fixedJob(jobID, "t1"), fixedSchedule(uuid.New()), nil
		},
	}
	srv := newTestServer(jr, nil, nil, nil, nil, nil)
	ctx := ctxWithNS("t1")

	resp, err := srv.TriggerJob(ctx, TriggerJobRequestObject{Id: jobID})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, ok := resp.(TriggerJob201JSONResponse)
	if !ok {
		t.Fatalf("expected TriggerJob201JSONResponse, got %T", resp)
	}
	if got.JobId != jobID {
		t.Fatalf("expected job ID %v, got %v", jobID, got.JobId)
	}
	if got.TriggerType != ExecutionTriggerTypeManual {
		t.Fatalf("expected trigger type %q, got %q", ExecutionTriggerTypeManual, got.TriggerType)
	}
}

func TestTriggerJob_EmitsEventWhenEmitterAttached(t *testing.T) {
	jobID := uuid.New()
	jr := &mockJobRepo{
		getJobWithScheduleFn: func(ctx context.Context, id uuid.UUID) (domain.Job, domain.Schedule, error) {
			return fixedJob(jobID, "t1"), fixedSchedule(uuid.New()), nil
		},
	}
	er := &mockExecRepo{}
	emitter := &mockEmitterV2{}
	svc := service.NewJobService(jr, &mockScheduleRepo{}, er, &mockTagRepo{}, &mockAPIKeyRepoV2{}, &mockAttemptRepo{}, cron.NewParser()).
		WithEmitter(emitter)
	srv := NewServerImpl(svc)

	resp, err := srv.TriggerJob(ctxWithNS("t1"), TriggerJobRequestObject{Id: jobID})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, ok := resp.(TriggerJob201JSONResponse)
	if !ok {
		t.Fatalf("expected TriggerJob201JSONResponse, got %T", resp)
	}
	if !emitter.called {
		t.Fatal("expected manual trigger to emit dispatch event")
	}
	if emitter.lastEvent.ExecutionID != got.Id {
		t.Fatalf("expected emitted execution ID %v, got %v", got.Id, emitter.lastEvent.ExecutionID)
	}
	if emitter.lastEvent.JobID != jobID {
		t.Fatalf("expected emitted job ID %v, got %v", jobID, emitter.lastEvent.JobID)
	}
	if emitter.lastEvent.Namespace != "t1" {
		t.Fatalf("expected emitted namespace t1, got %q", emitter.lastEvent.Namespace)
	}
}

func TestTriggerJob_NotFound(t *testing.T) {
	jr := &mockJobRepo{
		getJobWithScheduleFn: func(ctx context.Context, id uuid.UUID) (domain.Job, domain.Schedule, error) {
			return domain.Job{}, domain.Schedule{}, errors.New("not found")
		},
	}
	srv := newTestServer(jr, nil, nil, nil, nil, nil)
	ctx := ctxWithNS("t1")

	resp, err := srv.TriggerJob(ctx, TriggerJobRequestObject{Id: uuid.New()})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, ok := resp.(TriggerJob404JSONResponse); !ok {
		t.Fatalf("expected TriggerJob404JSONResponse, got %T", resp)
	}
}

func TestTriggerJob_Disabled(t *testing.T) {
	jobID := uuid.New()
	jr := &mockJobRepo{
		getJobWithScheduleFn: func(ctx context.Context, id uuid.UUID) (domain.Job, domain.Schedule, error) {
			j := fixedJob(jobID, "t1")
			j.Enabled = false
			return j, fixedSchedule(uuid.New()), nil
		},
	}
	srv := newTestServer(jr, nil, nil, nil, nil, nil)
	ctx := ctxWithNS("t1")

	resp, err := srv.TriggerJob(ctx, TriggerJobRequestObject{Id: jobID})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, ok := resp.(TriggerJob409JSONResponse); !ok {
		t.Fatalf("expected TriggerJob409JSONResponse, got %T", resp)
	}
}

// ── GetNextRun Tests ─────────────────────────────────────────────────────────

func TestGetNextRun_HappyPath(t *testing.T) {
	jobID := uuid.New()
	schedID := uuid.New()
	jr := &mockJobRepo{
		getJobWithScheduleFn: func(ctx context.Context, id uuid.UUID) (domain.Job, domain.Schedule, error) {
			j := fixedJob(jobID, "t1")
			j.ScheduleID = schedID
			return j, fixedSchedule(schedID), nil
		},
	}
	srv := newTestServer(jr, nil, nil, nil, nil, nil)
	ctx := ctxWithNS("t1")

	resp, err := srv.GetNextRun(ctx, GetNextRunRequestObject{Id: jobID})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, ok := resp.(GetNextRun200JSONResponse)
	if !ok {
		t.Fatalf("expected GetNextRun200JSONResponse, got %T", resp)
	}
	if len(got.NextRuns) != 5 {
		t.Fatalf("expected 5 next runs, got %d", len(got.NextRuns))
	}
	if got.CronExpression != "*/5 * * * *" {
		t.Fatalf("expected cron %q, got %q", "*/5 * * * *", got.CronExpression)
	}
}

func TestGetNextRun_NotFound(t *testing.T) {
	jr := &mockJobRepo{
		getJobWithScheduleFn: func(ctx context.Context, id uuid.UUID) (domain.Job, domain.Schedule, error) {
			return domain.Job{}, domain.Schedule{}, errors.New("not found")
		},
	}
	srv := newTestServer(jr, nil, nil, nil, nil, nil)
	ctx := ctxWithNS("t1")

	resp, err := srv.GetNextRun(ctx, GetNextRunRequestObject{Id: uuid.New()})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, ok := resp.(GetNextRun404JSONResponse); !ok {
		t.Fatalf("expected GetNextRun404JSONResponse, got %T", resp)
	}
}

// ── ResolveSchedule Tests ────────────────────────────────────────────────────

func TestResolveSchedule_CronPassthrough(t *testing.T) {
	srv := newTestServer(nil, nil, nil, nil, nil, nil)
	ctx := ctxWithNS("t1")

	resp, err := srv.ResolveSchedule(ctx, ResolveScheduleRequestObject{
		Body: &ResolveScheduleRequest{
			Description: "*/10 * * * *",
			Timezone:    nil, // default to UTC
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, ok := resp.(ResolveSchedule200JSONResponse)
	if !ok {
		t.Fatalf("expected ResolveSchedule200JSONResponse, got %T", resp)
	}
	if got.CronExpression != "*/10 * * * *" {
		t.Fatalf("expected cron %q, got %q", "*/10 * * * *", got.CronExpression)
	}
	if got.Timezone != "UTC" {
		t.Fatalf("expected timezone %q, got %q", "UTC", got.Timezone)
	}
	if len(got.NextRuns) != 5 {
		t.Fatalf("expected 5 next runs, got %d", len(got.NextRuns))
	}
}

func TestResolveSchedule_InvalidTimezone(t *testing.T) {
	srv := newTestServer(nil, nil, nil, nil, nil, nil)
	ctx := ctxWithNS("t1")

	resp, err := srv.ResolveSchedule(ctx, ResolveScheduleRequestObject{
		Body: &ResolveScheduleRequest{
			Description: "*/10 * * * *",
			Timezone:    strPtr("Not/A/Timezone"),
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, ok := resp.(ResolveSchedule422JSONResponse); !ok {
		t.Fatalf("expected ResolveSchedule422JSONResponse, got %T", resp)
	}
}

// ── Execution Tests ──────────────────────────────────────────────────────────

func TestListExecutions_HappyPath(t *testing.T) {
	jobID := uuid.New()
	execID := uuid.New()
	now := time.Now().UTC()
	er := &mockExecRepo{
		listExecutionsFn: func(ctx context.Context, filter domain.ExecutionFilter) ([]domain.Execution, error) {
			return []domain.Execution{
				{
					ID:          execID,
					JobID:       jobID,
					Namespace:   domain.Namespace("t1"),
					TriggerType: domain.TriggerTypeScheduled,
					ScheduledAt: now,
					FiredAt:     now,
					Status:      domain.ExecutionStatusDelivered,
					CreatedAt:   now,
				},
			}, nil
		},
	}
	srv := newTestServer(nil, nil, er, nil, nil, nil)
	ctx := ctxWithNS("t1")

	resp, err := srv.ListExecutions(ctx, ListExecutionsRequestObject{
		Id:     jobID,
		Params: ListExecutionsParams{},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, ok := resp.(ListExecutions200JSONResponse)
	if !ok {
		t.Fatalf("expected ListExecutions200JSONResponse, got %T", resp)
	}
	if len(got.Executions) != 1 {
		t.Fatalf("expected 1 execution, got %d", len(got.Executions))
	}
	if got.Executions[0].Id != execID {
		t.Fatalf("expected execution ID %v, got %v", execID, got.Executions[0].Id)
	}
}

func TestGetExecution_HappyPath(t *testing.T) {
	execID := uuid.New()
	jobID := uuid.New()
	now := time.Now().UTC()
	er := &mockExecRepo{
		getExecutionScopedFn: func(ctx context.Context, id uuid.UUID, ns domain.Namespace) (domain.Execution, error) {
			return domain.Execution{
				ID:          execID,
				JobID:       jobID,
				Namespace:   ns,
				TriggerType: domain.TriggerTypeManual,
				ScheduledAt: now,
				FiredAt:     now,
				Status:      domain.ExecutionStatusEmitted,
				CreatedAt:   now,
			}, nil
		},
	}
	srv := newTestServer(nil, nil, er, nil, nil, nil)
	ctx := ctxWithNS("t1")

	resp, err := srv.GetExecution(ctx, GetExecutionRequestObject{Id: execID})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, ok := resp.(GetExecution200JSONResponse)
	if !ok {
		t.Fatalf("expected GetExecution200JSONResponse, got %T", resp)
	}
	if got.Id != execID {
		t.Fatalf("expected execution ID %v, got %v", execID, got.Id)
	}
	if got.Status != ExecutionStatusEmitted {
		t.Fatalf("expected status %q, got %q", ExecutionStatusEmitted, got.Status)
	}
}

func TestGetExecution_NotFound(t *testing.T) {
	er := &mockExecRepo{
		getExecutionScopedFn: func(ctx context.Context, id uuid.UUID, ns domain.Namespace) (domain.Execution, error) {
			return domain.Execution{}, domain.ErrExecutionNotFound
		},
	}
	srv := newTestServer(nil, nil, er, nil, nil, nil)
	ctx := ctxWithNS("t1")

	resp, err := srv.GetExecution(ctx, GetExecutionRequestObject{Id: uuid.New()})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, ok := resp.(GetExecution404JSONResponse); !ok {
		t.Fatalf("expected GetExecution404JSONResponse, got %T", resp)
	}
}

func TestListPendingAck_HappyPath(t *testing.T) {
	jobID := uuid.New()
	execID := uuid.New()
	now := time.Now().UTC()
	limit := 2

	var capturedNS domain.Namespace
	var capturedJobID *uuid.UUID
	var capturedLimit int
	er := &mockExecRepo{
		listPendingAckFn: func(ctx context.Context, ns domain.Namespace, requestedJobID *uuid.UUID, limit int) ([]domain.Execution, error) {
			capturedNS = ns
			capturedJobID = requestedJobID
			capturedLimit = limit
			return []domain.Execution{
				{
					ID:          execID,
					JobID:       jobID,
					Namespace:   ns,
					TriggerType: domain.TriggerTypeScheduled,
					ScheduledAt: now,
					FiredAt:     now,
					Status:      domain.ExecutionStatusDelivered,
					CreatedAt:   now,
				},
			}, nil
		},
	}
	srv := newTestServer(nil, nil, er, nil, nil, nil)

	resp, err := srv.ListPendingAck(ctxWithNS("t1"), ListPendingAckRequestObject{
		Params: ListPendingAckParams{
			Limit: &limit,
			JobId: &jobID,
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, ok := resp.(ListPendingAck200JSONResponse)
	if !ok {
		t.Fatalf("expected ListPendingAck200JSONResponse, got %T", resp)
	}
	if len(got.Executions) != 1 {
		t.Fatalf("expected 1 execution, got %d", len(got.Executions))
	}
	if got.Executions[0].Id != execID {
		t.Fatalf("expected execution ID %v, got %v", execID, got.Executions[0].Id)
	}
	if capturedNS != "t1" {
		t.Fatalf("expected namespace t1, got %q", capturedNS)
	}
	if capturedJobID == nil || *capturedJobID != jobID {
		t.Fatalf("expected job_id filter %v, got %v", jobID, capturedJobID)
	}
	if capturedLimit != limit {
		t.Fatalf("expected limit %d, got %d", limit, capturedLimit)
	}
}

func TestAckExecution_HappyPath(t *testing.T) {
	execID := uuid.New()
	var capturedID uuid.UUID
	var capturedNS domain.Namespace
	er := &mockExecRepo{
		ackExecutionFn: func(ctx context.Context, id uuid.UUID, ns domain.Namespace) error {
			capturedID = id
			capturedNS = ns
			return nil
		},
	}
	srv := newTestServer(nil, nil, er, nil, nil, nil)

	resp, err := srv.AckExecution(ctxWithNS("t1"), AckExecutionRequestObject{Id: execID})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := resp.(AckExecution204Response); !ok {
		t.Fatalf("expected AckExecution204Response, got %T", resp)
	}
	if capturedID != execID {
		t.Fatalf("expected execution ID %v, got %v", execID, capturedID)
	}
	if capturedNS != "t1" {
		t.Fatalf("expected namespace t1, got %q", capturedNS)
	}
}

func TestAckExecution_NotFound(t *testing.T) {
	er := &mockExecRepo{
		ackExecutionFn: func(ctx context.Context, id uuid.UUID, ns domain.Namespace) error {
			return domain.ErrExecutionNotFound
		},
	}
	srv := newTestServer(nil, nil, er, nil, nil, nil)

	resp, err := srv.AckExecution(ctxWithNS("t1"), AckExecutionRequestObject{Id: uuid.New()})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := resp.(AckExecution404JSONResponse); !ok {
		t.Fatalf("expected AckExecution404JSONResponse, got %T", resp)
	}
}

// ── API Key Tests ────────────────────────────────────────────────────────────

func TestCreateAPIKey_HappyPath(t *testing.T) {
	srv := newTestServer(nil, nil, nil, nil, nil, nil)
	ctx := ctxWithNS("t1")

	resp, err := srv.CreateAPIKey(ctx, CreateAPIKeyRequestObject{
		Body: &CreateAPIKeyRequest{
			Label:     "test-key",
			Namespace: "t1",
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, ok := resp.(CreateAPIKey201JSONResponse)
	if !ok {
		t.Fatalf("expected CreateAPIKey201JSONResponse, got %T", resp)
	}
	if got.Label != "test-key" {
		t.Fatalf("expected label %q, got %q", "test-key", got.Label)
	}
	if got.Namespace != "t1" {
		t.Fatalf("expected namespace %q, got %q", "t1", got.Namespace)
	}
	if got.Token == "" {
		t.Fatal("expected token to be non-empty")
	}
}

func TestCreateAPIKey_UsesAuthenticatedNamespace(t *testing.T) {
	srv := newTestServer(nil, nil, nil, nil, nil, nil)

	resp, err := srv.CreateAPIKey(ctxWithNS("caller-ns"), CreateAPIKeyRequestObject{
		Body: &CreateAPIKeyRequest{
			Label:     "test-key",
			Namespace: "other-ns",
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, ok := resp.(CreateAPIKey201JSONResponse)
	if !ok {
		t.Fatalf("expected CreateAPIKey201JSONResponse, got %T", resp)
	}
	if got.Namespace != "caller-ns" {
		t.Fatalf("expected namespace %q, got %q", "caller-ns", got.Namespace)
	}
}

func TestListAPIKeys_HappyPath(t *testing.T) {
	keyID := uuid.New()
	now := time.Now().UTC()
	kr := &mockAPIKeyRepoV2{
		listKeysFn: func(ctx context.Context, ns domain.Namespace, params domain.ListParams) ([]domain.APIKey, error) {
			return []domain.APIKey{
				{
					ID:        keyID,
					Namespace: domain.Namespace("t1"),
					Label:     "my-key",
					Enabled:   true,
					CreatedAt: now,
				},
			}, nil
		},
	}
	srv := newTestServer(nil, nil, nil, nil, kr, nil)
	ctx := ctxWithNS("t1")

	resp, err := srv.ListAPIKeys(ctx, ListAPIKeysRequestObject{
		Params: ListAPIKeysParams{},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, ok := resp.(ListAPIKeys200JSONResponse)
	if !ok {
		t.Fatalf("expected ListAPIKeys200JSONResponse, got %T", resp)
	}
	if len(got.Keys) != 1 {
		t.Fatalf("expected 1 key, got %d", len(got.Keys))
	}
	if got.Keys[0].Label != "my-key" {
		t.Fatalf("expected label %q, got %q", "my-key", got.Keys[0].Label)
	}
}

func TestDeleteAPIKey_HappyPath(t *testing.T) {
	srv := newTestServer(nil, nil, nil, nil, nil, nil)
	ctx := ctxWithNS("t1")

	resp, err := srv.DeleteAPIKey(ctx, DeleteAPIKeyRequestObject{Id: uuid.New()})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, ok := resp.(DeleteAPIKey204Response); !ok {
		t.Fatalf("expected DeleteAPIKey204Response, got %T", resp)
	}
}

func TestDeleteAPIKey_NotFound(t *testing.T) {
	kr := &mockAPIKeyRepoV2{
		deleteKeyFn: func(ctx context.Context, id uuid.UUID, ns domain.Namespace) error {
			return domain.ErrAPIKeyNotFound
		},
	}
	srv := newTestServer(nil, nil, nil, nil, kr, nil)
	ctx := ctxWithNS("t1")

	resp, err := srv.DeleteAPIKey(ctx, DeleteAPIKeyRequestObject{Id: uuid.New()})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, ok := resp.(DeleteAPIKey404JSONResponse); !ok {
		t.Fatalf("expected DeleteAPIKey404JSONResponse, got %T", resp)
	}
}

// ── Conversion Helper Tests ──────────────────────────────────────────────────

func TestApiTagsToDomain(t *testing.T) {
	tests := []struct {
		name string
		in   *Tag
		want int // expected length; -1 means nil
	}{
		{"nil input", nil, -1},
		{"empty map", &Tag{}, 0},
		{"populated", &Tag{"env": "prod", "team": "backend"}, 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := apiTagsToDomain(tt.in)
			if tt.want == -1 {
				if got != nil {
					t.Fatalf("expected nil, got %v", got)
				}
				return
			}
			if len(got) != tt.want {
				t.Fatalf("expected %d tags, got %d", tt.want, len(got))
			}
		})
	}
}

func TestDomainTagsToAPI(t *testing.T) {
	tests := []struct {
		name    string
		in      []domain.Tag
		wantNil bool
		wantLen int
	}{
		{"nil input", nil, true, 0},
		{"empty slice", []domain.Tag{}, true, 0},
		{"populated", []domain.Tag{{Key: "env", Value: "prod"}, {Key: "team", Value: "backend"}}, false, 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := domainTagsToAPI(tt.in)
			if tt.wantNil {
				if got != nil {
					t.Fatalf("expected nil, got %v", *got)
				}
				return
			}
			if got == nil {
				t.Fatal("expected non-nil result")
			}
			if len(*got) != tt.wantLen {
				t.Fatalf("expected %d entries, got %d", tt.wantLen, len(*got))
			}
		})
	}
}

func TestListParamsFromQuery(t *testing.T) {
	tests := []struct {
		name       string
		limit      *int
		offset     *int
		wantLimit  int
		wantOffset int
	}{
		{"nil params", nil, nil, 0, 0},
		{"limit only", intPtr(50), nil, 50, 0},
		{"both set", intPtr(25), intPtr(10), 25, 10},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := listParamsFromQuery(tt.limit, tt.offset)
			if got.Limit != tt.wantLimit {
				t.Fatalf("expected limit %d, got %d", tt.wantLimit, got.Limit)
			}
			if got.Offset != tt.wantOffset {
				t.Fatalf("expected offset %d, got %d", tt.wantOffset, got.Offset)
			}
		})
	}
}

// ── ListJobs Filter Tests ────────────────────────────────────────────────────

func TestListJobs_WithFilters(t *testing.T) {
	var capturedFilter domain.JobFilter
	jr := &mockJobRepo{
		listJobsFn: func(ctx context.Context, filter domain.JobFilter) ([]domain.Job, error) {
			capturedFilter = filter
			return []domain.Job{fixedJob(uuid.New(), "t1")}, nil
		},
	}
	srv := newTestServer(jr, nil, nil, nil, nil, nil)
	ctx := ctxWithNS("t1")

	tags := []string{"env:prod", "team:backend"}
	resp, err := srv.ListJobs(ctx, ListJobsRequestObject{
		Params: ListJobsParams{
			Enabled: boolPtr(true),
			Name:    strPtr("test"),
			Tag:     &tags,
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, ok := resp.(ListJobs200JSONResponse); !ok {
		t.Fatalf("expected ListJobs200JSONResponse, got %T", resp)
	}

	// Verify Enabled filter
	if capturedFilter.Enabled == nil {
		t.Fatal("expected Enabled filter to be set")
	}
	if *capturedFilter.Enabled != true {
		t.Fatalf("expected Enabled=true, got %v", *capturedFilter.Enabled)
	}

	// Verify Name filter
	if capturedFilter.Name != "test" {
		t.Fatalf("expected Name=%q, got %q", "test", capturedFilter.Name)
	}

	// Verify Tags filter
	if len(capturedFilter.Tags) != 2 {
		t.Fatalf("expected 2 tags, got %d", len(capturedFilter.Tags))
	}
	tagMap := make(map[string]string)
	for _, tag := range capturedFilter.Tags {
		tagMap[tag.Key] = tag.Value
	}
	if tagMap["env"] != "prod" {
		t.Fatalf("expected tag env=prod, got env=%s", tagMap["env"])
	}
	if tagMap["team"] != "backend" {
		t.Fatalf("expected tag team=backend, got team=%s", tagMap["team"])
	}
}

// ── UpdateJob Timeout & Tags Tests ───────────────────────────────────────────

func TestUpdateJob_WithTimeoutAndTags(t *testing.T) {
	jobID := uuid.New()
	schedID := uuid.New()
	jr := &mockJobRepo{
		getJobWithScheduleFn: func(ctx context.Context, id uuid.UUID) (domain.Job, domain.Schedule, error) {
			j := fixedJob(jobID, "t1")
			j.ScheduleID = schedID
			return j, fixedSchedule(schedID), nil
		},
	}
	srv := newTestServer(jr, nil, nil, nil, nil, nil)
	ctx := ctxWithNS("t1")

	timeoutSec := 60
	tags := Tag{"env": "staging", "team": "platform"}
	resp, err := srv.UpdateJob(ctx, UpdateJobRequestObject{
		Id: jobID,
		Body: &UpdateJobRequest{
			WebhookTimeoutSeconds: &timeoutSec,
			Tags:                  &tags,
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, ok := resp.(UpdateJob200JSONResponse)
	if !ok {
		t.Fatalf("expected UpdateJob200JSONResponse, got %T", resp)
	}
	if got.Id != jobID {
		t.Fatalf("expected job ID %v, got %v", jobID, got.Id)
	}
}

// ── ListExecutions Filter Tests ──────────────────────────────────────────────

func TestListExecutions_WithFilters(t *testing.T) {
	jobID := uuid.New()
	execID := uuid.New()
	now := time.Now().UTC()

	var capturedFilter domain.ExecutionFilter
	er := &mockExecRepo{
		listExecutionsFn: func(ctx context.Context, filter domain.ExecutionFilter) ([]domain.Execution, error) {
			capturedFilter = filter
			return []domain.Execution{
				{
					ID:          execID,
					JobID:       jobID,
					Namespace:   domain.Namespace("t1"),
					TriggerType: domain.TriggerTypeScheduled,
					ScheduledAt: now,
					FiredAt:     now,
					Status:      domain.ExecutionStatusDelivered,
					CreatedAt:   now,
				},
			}, nil
		},
	}
	srv := newTestServer(nil, nil, er, nil, nil, nil)
	ctx := ctxWithNS("t1")

	status := ListExecutionsParamsStatus("delivered")
	triggerType := ListExecutionsParamsTriggerType("scheduled")
	since := now.Add(-24 * time.Hour)
	until := now

	resp, err := srv.ListExecutions(ctx, ListExecutionsRequestObject{
		Id: jobID,
		Params: ListExecutionsParams{
			Status:      &status,
			TriggerType: &triggerType,
			Since:       &since,
			Until:       &until,
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, ok := resp.(ListExecutions200JSONResponse)
	if !ok {
		t.Fatalf("expected ListExecutions200JSONResponse, got %T", resp)
	}
	if len(got.Executions) != 1 {
		t.Fatalf("expected 1 execution, got %d", len(got.Executions))
	}

	// Verify Status filter
	if capturedFilter.Status == nil {
		t.Fatal("expected Status filter to be set")
	}
	if string(*capturedFilter.Status) != "delivered" {
		t.Fatalf("expected Status=%q, got %q", "delivered", string(*capturedFilter.Status))
	}

	// Verify TriggerType filter
	if capturedFilter.TriggerType == nil {
		t.Fatal("expected TriggerType filter to be set")
	}
	if *capturedFilter.TriggerType != "scheduled" {
		t.Fatalf("expected TriggerType=%q, got %q", "scheduled", *capturedFilter.TriggerType)
	}

	// Verify Since filter
	if capturedFilter.Since == nil {
		t.Fatal("expected Since filter to be set")
	}
	if !capturedFilter.Since.Equal(since) {
		t.Fatalf("expected Since=%v, got %v", since, *capturedFilter.Since)
	}

	// Verify Until filter
	if capturedFilter.Until == nil {
		t.Fatal("expected Until filter to be set")
	}
	if !capturedFilter.Until.Equal(until) {
		t.Fatalf("expected Until=%v, got %v", until, *capturedFilter.Until)
	}
}
