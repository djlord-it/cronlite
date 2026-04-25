package mcp

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/djlord-it/cronlite/internal/cron"
	"github.com/djlord-it/cronlite/internal/domain"
	"github.com/djlord-it/cronlite/internal/service"
	"github.com/google/uuid"
)

// ── Mock repositories ───────────────────────────────────────────────────────

type mockJobRepo struct {
	insertJobFn          func(ctx context.Context, job domain.Job, schedule domain.Schedule) error
	getJobFn             func(ctx context.Context, id uuid.UUID) (domain.Job, error)
	getJobWithScheduleFn func(ctx context.Context, id uuid.UUID) (domain.Job, domain.Schedule, error)
	listJobsFn           func(ctx context.Context, filter domain.JobFilter) ([]domain.Job, error)
	updateJobFn          func(ctx context.Context, job domain.Job) error
	deleteJobFn          func(ctx context.Context, id uuid.UUID, ns domain.Namespace) error
	getEnabledJobsFn     func(ctx context.Context, limit, offset int) ([]domain.JobWithSchedule, error)
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

func (m *mockJobRepo) GetEnabledJobs(ctx context.Context, limit, offset int) ([]domain.JobWithSchedule, error) {
	if m.getEnabledJobsFn != nil {
		return m.getEnabledJobsFn(ctx, limit, offset)
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

type mockExecutionRepo struct {
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

func (m *mockExecutionRepo) InsertExecution(ctx context.Context, exec domain.Execution) error {
	if m.insertExecutionFn != nil {
		return m.insertExecutionFn(ctx, exec)
	}
	return nil
}

func (m *mockExecutionRepo) GetExecution(ctx context.Context, id uuid.UUID) (domain.Execution, error) {
	if m.getExecutionFn != nil {
		return m.getExecutionFn(ctx, id)
	}
	return domain.Execution{}, nil
}

func (m *mockExecutionRepo) GetExecutionScoped(ctx context.Context, id uuid.UUID, ns domain.Namespace) (domain.Execution, error) {
	if m.getExecutionScopedFn != nil {
		return m.getExecutionScopedFn(ctx, id, ns)
	}
	return domain.Execution{}, domain.ErrExecutionNotFound
}

func (m *mockExecutionRepo) ListExecutions(ctx context.Context, filter domain.ExecutionFilter) ([]domain.Execution, error) {
	if m.listExecutionsFn != nil {
		return m.listExecutionsFn(ctx, filter)
	}
	return nil, nil
}

func (m *mockExecutionRepo) ListPendingAck(ctx context.Context, ns domain.Namespace, jobID *uuid.UUID, limit int) ([]domain.Execution, error) {
	if m.listPendingAckFn != nil {
		return m.listPendingAckFn(ctx, ns, jobID, limit)
	}
	return nil, nil
}

func (m *mockExecutionRepo) AckExecution(ctx context.Context, id uuid.UUID, ns domain.Namespace) error {
	if m.ackExecutionFn != nil {
		return m.ackExecutionFn(ctx, id, ns)
	}
	return nil
}

func (m *mockExecutionRepo) GetRecentExecutions(ctx context.Context, jobID uuid.UUID, limit int) ([]domain.Execution, error) {
	if m.getRecentExecutionsFn != nil {
		return m.getRecentExecutionsFn(ctx, jobID, limit)
	}
	return nil, nil
}

func (m *mockExecutionRepo) UpdateExecutionStatus(ctx context.Context, id uuid.UUID, status domain.ExecutionStatus) error {
	if m.updateExecutionStatusFn != nil {
		return m.updateExecutionStatusFn(ctx, id, status)
	}
	return nil
}

func (m *mockExecutionRepo) DequeueExecution(ctx context.Context) (*domain.Execution, error) {
	if m.dequeueExecutionFn != nil {
		return m.dequeueExecutionFn(ctx)
	}
	return nil, nil
}

func (m *mockExecutionRepo) GetOrphanedExecutions(ctx context.Context, olderThan time.Time, maxResults int) ([]domain.Execution, error) {
	if m.getOrphanedExecFn != nil {
		return m.getOrphanedExecFn(ctx, olderThan, maxResults)
	}
	return nil, nil
}

func (m *mockExecutionRepo) RequeueStaleExecutions(ctx context.Context, olderThan time.Time, limit int) (int, error) {
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

// mockAPIKeyRepo is defined in auth_test.go (same package).

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

// ── Test helpers ────────────────────────────────────────────────────────────

func newTestService(jr *mockJobRepo, sr *mockScheduleRepo, er *mockExecutionRepo, tr *mockTagRepo) *service.JobService {
	if jr == nil {
		jr = &mockJobRepo{}
	}
	if sr == nil {
		sr = &mockScheduleRepo{}
	}
	if er == nil {
		er = &mockExecutionRepo{}
	}
	if tr == nil {
		tr = &mockTagRepo{}
	}
	akr := &mockAPIKeyRepo{
		getKeyByHashFn: func(_ context.Context, _ string) (domain.APIKey, error) {
			return domain.APIKey{}, errors.New("no key")
		},
	}
	return service.NewJobService(jr, sr, er, tr, akr, &mockAttemptRepo{}, cron.NewParser())
}

func ctxWithNS(ns string) context.Context {
	return domain.NamespaceToContext(context.Background(), domain.Namespace(ns))
}

func resultText(t *testing.T, result *mcpgo.CallToolResult) string {
	t.Helper()
	if result == nil {
		t.Fatal("result is nil")
	}
	if len(result.Content) == 0 {
		t.Fatal("result.Content is empty")
	}
	tc, ok := result.Content[0].(mcpgo.TextContent)
	if !ok {
		t.Fatalf("result.Content[0] is %T, want mcpgo.TextContent", result.Content[0])
	}
	return tc.Text
}

// fixedJob returns a deterministic Job for testing.
func fixedJob() domain.Job {
	return domain.Job{
		ID:         uuid.MustParse("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"),
		Namespace:  domain.Namespace("t1"),
		Name:       "my-job",
		Enabled:    true,
		ScheduleID: uuid.MustParse("11111111-2222-3333-4444-555555555555"),
		Delivery: domain.DeliveryConfig{
			Type:       domain.DeliveryTypeWebhook,
			WebhookURL: "https://example.com/hook",
			Timeout:    30 * time.Second,
		},
		CreatedAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

// fixedSchedule returns a deterministic Schedule for testing.
func fixedSchedule() domain.Schedule {
	return domain.Schedule{
		ID:             uuid.MustParse("11111111-2222-3333-4444-555555555555"),
		CronExpression: "*/5 * * * *",
		Timezone:       "UTC",
		CreatedAt:      time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt:      time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

// fixedExecution returns a deterministic Execution for testing.
func fixedExecution() domain.Execution {
	return domain.Execution{
		ID:          uuid.MustParse("cccccccc-dddd-eeee-ffff-aaaaaaaaaaaa"),
		JobID:       uuid.MustParse("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"),
		Namespace:   domain.Namespace("t1"),
		TriggerType: domain.TriggerTypeManual,
		ScheduledAt: time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC),
		FiredAt:     time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC),
		Status:      domain.ExecutionStatusEmitted,
		CreatedAt:   time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC),
	}
}

// ── handleCreateJob ─────────────────────────────────────────────────────────

func TestHandleCreateJob_HappyPath(t *testing.T) {
	svc := newTestService(nil, nil, nil, nil)
	handler := handleCreateJob(svc)

	req := mcpgo.CallToolRequest{
		Params: mcpgo.CallToolParams{
			Name: "create-job",
			Arguments: map[string]any{
				"name":            "test-job",
				"cron_expression": "*/5 * * * *",
				"timezone":        "UTC",
				"webhook_url":     "https://example.com/hook",
			},
		},
	}

	result, err := handler(ctxWithNS("t1"), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error: %s", resultText(t, result))
	}

	text := resultText(t, result)
	if !strings.Contains(text, "id") {
		t.Errorf("result text should contain \"id\", got: %s", text)
	}
	if !strings.Contains(text, "test-job") {
		t.Errorf("result text should contain \"test-job\", got: %s", text)
	}
}

func TestHandleCreateJob_MissingName(t *testing.T) {
	svc := newTestService(nil, nil, nil, nil)
	handler := handleCreateJob(svc)

	req := mcpgo.CallToolRequest{
		Params: mcpgo.CallToolParams{
			Name: "create-job",
			Arguments: map[string]any{
				"cron_expression": "*/5 * * * *",
				"timezone":        "UTC",
				"webhook_url":     "https://example.com/hook",
			},
		},
	}

	result, err := handler(ctxWithNS("t1"), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected IsError=true")
	}

	text := resultText(t, result)
	if !strings.Contains(text, "Missing required parameter: name") {
		t.Errorf("expected missing name error, got: %s", text)
	}
}

func TestHandleCreateJob_ServiceError(t *testing.T) {
	svc := newTestService(nil, nil, nil, nil)
	handler := handleCreateJob(svc)

	req := mcpgo.CallToolRequest{
		Params: mcpgo.CallToolParams{
			Name: "create-job",
			Arguments: map[string]any{
				"name":            "test-job",
				"cron_expression": "INVALID",
				"timezone":        "UTC",
				"webhook_url":     "https://example.com/hook",
			},
		},
	}

	result, err := handler(ctxWithNS("t1"), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected IsError=true for invalid cron expression")
	}

	text := resultText(t, result)
	if !strings.Contains(text, "Invalid cron expression") {
		t.Errorf("expected cron error, got: %s", text)
	}
}

// ── handleListJobs ──────────────────────────────────────────────────────────

func TestHandleListJobs_HappyPath(t *testing.T) {
	jr := &mockJobRepo{
		listJobsFn: func(_ context.Context, _ domain.JobFilter) ([]domain.Job, error) {
			j1 := fixedJob()
			j2 := fixedJob()
			j2.ID = uuid.MustParse("bbbbbbbb-cccc-dddd-eeee-ffffffffffff")
			j2.Name = "other-job"
			return []domain.Job{j1, j2}, nil
		},
	}
	svc := newTestService(jr, nil, nil, nil)
	handler := handleListJobs(svc)

	req := mcpgo.CallToolRequest{
		Params: mcpgo.CallToolParams{
			Name:      "list-jobs",
			Arguments: map[string]any{},
		},
	}

	result, err := handler(ctxWithNS("t1"), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error: %s", resultText(t, result))
	}

	text := resultText(t, result)
	if !strings.Contains(text, `"count": 2`) {
		t.Errorf("expected count 2 in result, got: %s", text)
	}
}

func TestHandleListJobs_Empty(t *testing.T) {
	jr := &mockJobRepo{
		listJobsFn: func(_ context.Context, _ domain.JobFilter) ([]domain.Job, error) {
			return nil, nil
		},
	}
	svc := newTestService(jr, nil, nil, nil)
	handler := handleListJobs(svc)

	req := mcpgo.CallToolRequest{
		Params: mcpgo.CallToolParams{
			Name:      "list-jobs",
			Arguments: map[string]any{},
		},
	}

	result, err := handler(ctxWithNS("t1"), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error: %s", resultText(t, result))
	}

	text := resultText(t, result)
	if !strings.Contains(text, `"count": 0`) {
		t.Errorf("expected count 0 in result, got: %s", text)
	}
}

// ── handleGetJob ────────────────────────────────────────────────────────────

func TestHandleGetJob_HappyPath(t *testing.T) {
	jobID := fixedJob().ID

	jr := &mockJobRepo{
		getJobWithScheduleFn: func(_ context.Context, _ uuid.UUID) (domain.Job, domain.Schedule, error) {
			return fixedJob(), fixedSchedule(), nil
		},
	}
	tr := &mockTagRepo{
		getTagsFn: func(_ context.Context, _ uuid.UUID) ([]domain.Tag, error) {
			return []domain.Tag{{Key: "env", Value: "prod"}}, nil
		},
	}
	er := &mockExecutionRepo{
		getRecentExecutionsFn: func(_ context.Context, _ uuid.UUID, _ int) ([]domain.Execution, error) {
			return []domain.Execution{fixedExecution()}, nil
		},
	}
	svc := newTestService(jr, nil, er, tr)
	handler := handleGetJob(svc)

	req := mcpgo.CallToolRequest{
		Params: mcpgo.CallToolParams{
			Name: "get-job",
			Arguments: map[string]any{
				"id": jobID.String(),
			},
		},
	}

	result, err := handler(ctxWithNS("t1"), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error: %s", resultText(t, result))
	}

	text := resultText(t, result)
	if !strings.Contains(text, "my-job") {
		t.Errorf("expected job name in result, got: %s", text)
	}
	if !strings.Contains(text, "env") {
		t.Errorf("expected tag key in result, got: %s", text)
	}
	if !strings.Contains(text, "recent_executions") {
		t.Errorf("expected recent_executions in result, got: %s", text)
	}
}

func TestHandleGetJob_InvalidUUID(t *testing.T) {
	svc := newTestService(nil, nil, nil, nil)
	handler := handleGetJob(svc)

	req := mcpgo.CallToolRequest{
		Params: mcpgo.CallToolParams{
			Name: "get-job",
			Arguments: map[string]any{
				"id": "not-a-uuid",
			},
		},
	}

	result, err := handler(ctxWithNS("t1"), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected IsError=true")
	}

	text := resultText(t, result)
	if !strings.Contains(text, "Invalid job ID") {
		t.Errorf("expected invalid ID error, got: %s", text)
	}
}

func TestHandleGetJob_NotFound(t *testing.T) {
	jr := &mockJobRepo{
		getJobWithScheduleFn: func(_ context.Context, _ uuid.UUID) (domain.Job, domain.Schedule, error) {
			return domain.Job{}, domain.Schedule{}, errors.New("not found")
		},
	}
	svc := newTestService(jr, nil, nil, nil)
	handler := handleGetJob(svc)

	req := mcpgo.CallToolRequest{
		Params: mcpgo.CallToolParams{
			Name: "get-job",
			Arguments: map[string]any{
				"id": uuid.New().String(),
			},
		},
	}

	result, err := handler(ctxWithNS("t1"), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected IsError=true")
	}

	text := resultText(t, result)
	if !strings.Contains(text, "Job not found") {
		t.Errorf("expected not found error, got: %s", text)
	}
}

// ── handleUpdateJob ─────────────────────────────────────────────────────────

func TestHandleUpdateJob_HappyPath(t *testing.T) {
	job := fixedJob()
	schedule := fixedSchedule()

	jr := &mockJobRepo{
		getJobWithScheduleFn: func(_ context.Context, _ uuid.UUID) (domain.Job, domain.Schedule, error) {
			return job, schedule, nil
		},
	}
	svc := newTestService(jr, nil, nil, nil)
	handler := handleUpdateJob(svc)

	req := mcpgo.CallToolRequest{
		Params: mcpgo.CallToolParams{
			Name: "update-job",
			Arguments: map[string]any{
				"id":   job.ID.String(),
				"name": "updated-name",
			},
		},
	}

	result, err := handler(ctxWithNS("t1"), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error: %s", resultText(t, result))
	}

	text := resultText(t, result)
	if !strings.Contains(text, "updated-name") {
		t.Errorf("expected updated name in result, got: %s", text)
	}
}

func TestHandleUpdateJob_MissingID(t *testing.T) {
	svc := newTestService(nil, nil, nil, nil)
	handler := handleUpdateJob(svc)

	req := mcpgo.CallToolRequest{
		Params: mcpgo.CallToolParams{
			Name: "update-job",
			Arguments: map[string]any{
				"name": "updated-name",
			},
		},
	}

	result, err := handler(ctxWithNS("t1"), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected IsError=true")
	}

	text := resultText(t, result)
	if !strings.Contains(text, "Missing required parameter: id") {
		t.Errorf("expected missing id error, got: %s", text)
	}
}

// ── handleDeleteJob ─────────────────────────────────────────────────────────

func TestHandleDeleteJob_HappyPath(t *testing.T) {
	jobID := fixedJob().ID

	svc := newTestService(nil, nil, nil, nil)
	handler := handleDeleteJob(svc)

	req := mcpgo.CallToolRequest{
		Params: mcpgo.CallToolParams{
			Name: "delete-job",
			Arguments: map[string]any{
				"id": jobID.String(),
			},
		},
	}

	result, err := handler(ctxWithNS("t1"), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error: %s", resultText(t, result))
	}

	text := resultText(t, result)
	if !strings.Contains(text, "deleted successfully") {
		t.Errorf("expected deleted message, got: %s", text)
	}
}

func TestHandleDeleteJob_NotFound(t *testing.T) {
	jr := &mockJobRepo{
		deleteJobFn: func(_ context.Context, _ uuid.UUID, _ domain.Namespace) error {
			return domain.ErrJobNotFound
		},
	}
	svc := newTestService(jr, nil, nil, nil)
	handler := handleDeleteJob(svc)

	req := mcpgo.CallToolRequest{
		Params: mcpgo.CallToolParams{
			Name: "delete-job",
			Arguments: map[string]any{
				"id": uuid.New().String(),
			},
		},
	}

	result, err := handler(ctxWithNS("t1"), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected IsError=true")
	}

	text := resultText(t, result)
	if !strings.Contains(text, "Job not found") {
		t.Errorf("expected not found error, got: %s", text)
	}
}

// ── handlePauseJob ──────────────────────────────────────────────────────────

func TestHandlePauseJob_HappyPath(t *testing.T) {
	job := fixedJob()
	schedule := fixedSchedule()

	jr := &mockJobRepo{
		getJobWithScheduleFn: func(_ context.Context, _ uuid.UUID) (domain.Job, domain.Schedule, error) {
			return job, schedule, nil
		},
	}
	svc := newTestService(jr, nil, nil, nil)
	handler := handlePauseJob(svc)

	req := mcpgo.CallToolRequest{
		Params: mcpgo.CallToolParams{
			Name: "pause-job",
			Arguments: map[string]any{
				"id": job.ID.String(),
			},
		},
	}

	result, err := handler(ctxWithNS("t1"), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error: %s", resultText(t, result))
	}

	text := resultText(t, result)
	if !strings.Contains(text, "paused successfully") {
		t.Errorf("expected paused message, got: %s", text)
	}
}

func TestHandlePauseJob_NotFound(t *testing.T) {
	jr := &mockJobRepo{
		getJobWithScheduleFn: func(_ context.Context, _ uuid.UUID) (domain.Job, domain.Schedule, error) {
			return domain.Job{}, domain.Schedule{}, errors.New("not found")
		},
	}
	svc := newTestService(jr, nil, nil, nil)
	handler := handlePauseJob(svc)

	req := mcpgo.CallToolRequest{
		Params: mcpgo.CallToolParams{
			Name: "pause-job",
			Arguments: map[string]any{
				"id": uuid.New().String(),
			},
		},
	}

	result, err := handler(ctxWithNS("t1"), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected IsError=true")
	}

	text := resultText(t, result)
	if !strings.Contains(text, "Job not found") {
		t.Errorf("expected not found error, got: %s", text)
	}
}

// ── handleResumeJob ─────────────────────────────────────────────────────────

func TestHandleResumeJob_HappyPath(t *testing.T) {
	job := fixedJob()
	job.Enabled = false
	schedule := fixedSchedule()

	jr := &mockJobRepo{
		getJobWithScheduleFn: func(_ context.Context, _ uuid.UUID) (domain.Job, domain.Schedule, error) {
			return job, schedule, nil
		},
	}
	svc := newTestService(jr, nil, nil, nil)
	handler := handleResumeJob(svc)

	req := mcpgo.CallToolRequest{
		Params: mcpgo.CallToolParams{
			Name: "resume-job",
			Arguments: map[string]any{
				"id": job.ID.String(),
			},
		},
	}

	result, err := handler(ctxWithNS("t1"), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error: %s", resultText(t, result))
	}

	text := resultText(t, result)
	if !strings.Contains(text, "resumed successfully") {
		t.Errorf("expected resumed message, got: %s", text)
	}
}

func TestHandleResumeJob_NotFound(t *testing.T) {
	jr := &mockJobRepo{
		getJobWithScheduleFn: func(_ context.Context, _ uuid.UUID) (domain.Job, domain.Schedule, error) {
			return domain.Job{}, domain.Schedule{}, errors.New("not found")
		},
	}
	svc := newTestService(jr, nil, nil, nil)
	handler := handleResumeJob(svc)

	req := mcpgo.CallToolRequest{
		Params: mcpgo.CallToolParams{
			Name: "resume-job",
			Arguments: map[string]any{
				"id": uuid.New().String(),
			},
		},
	}

	result, err := handler(ctxWithNS("t1"), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected IsError=true")
	}

	text := resultText(t, result)
	if !strings.Contains(text, "Job not found") {
		t.Errorf("expected not found error, got: %s", text)
	}
}

// ── handleTriggerJob ────────────────────────────────────────────────────────

func TestHandleTriggerJob_HappyPath(t *testing.T) {
	job := fixedJob()
	job.Enabled = true
	schedule := fixedSchedule()

	jr := &mockJobRepo{
		getJobWithScheduleFn: func(_ context.Context, _ uuid.UUID) (domain.Job, domain.Schedule, error) {
			return job, schedule, nil
		},
	}
	svc := newTestService(jr, nil, nil, nil)
	handler := handleTriggerJob(svc)

	req := mcpgo.CallToolRequest{
		Params: mcpgo.CallToolParams{
			Name: "trigger-job",
			Arguments: map[string]any{
				"id": job.ID.String(),
			},
		},
	}

	result, err := handler(ctxWithNS("t1"), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error: %s", resultText(t, result))
	}

	text := resultText(t, result)
	if !strings.Contains(text, "id") {
		t.Errorf("expected execution id in result, got: %s", text)
	}
	if !strings.Contains(text, "manual") {
		t.Errorf("expected trigger_type manual in result, got: %s", text)
	}
}

func TestHandleTriggerJob_Disabled(t *testing.T) {
	job := fixedJob()
	job.Enabled = false
	schedule := fixedSchedule()

	jr := &mockJobRepo{
		getJobWithScheduleFn: func(_ context.Context, _ uuid.UUID) (domain.Job, domain.Schedule, error) {
			return job, schedule, nil
		},
	}
	svc := newTestService(jr, nil, nil, nil)
	handler := handleTriggerJob(svc)

	req := mcpgo.CallToolRequest{
		Params: mcpgo.CallToolParams{
			Name: "trigger-job",
			Arguments: map[string]any{
				"id": job.ID.String(),
			},
		},
	}

	result, err := handler(ctxWithNS("t1"), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected IsError=true for disabled job")
	}

	text := resultText(t, result)
	if !strings.Contains(text, "disabled") {
		t.Errorf("expected disabled error, got: %s", text)
	}
}

// ── handleNextRun ───────────────────────────────────────────────────────────

func TestHandleNextRun_HappyPath(t *testing.T) {
	job := fixedJob()
	schedule := fixedSchedule()

	jr := &mockJobRepo{
		getJobWithScheduleFn: func(_ context.Context, _ uuid.UUID) (domain.Job, domain.Schedule, error) {
			return job, schedule, nil
		},
	}
	svc := newTestService(jr, nil, nil, nil)
	handler := handleNextRun(svc)

	req := mcpgo.CallToolRequest{
		Params: mcpgo.CallToolParams{
			Name: "next-run",
			Arguments: map[string]any{
				"id": job.ID.String(),
			},
		},
	}

	result, err := handler(ctxWithNS("t1"), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error: %s", resultText(t, result))
	}

	text := resultText(t, result)
	if !strings.Contains(text, "next_runs") {
		t.Errorf("expected next_runs in result, got: %s", text)
	}
	if !strings.Contains(text, "next_run_at") {
		t.Errorf("expected next_run_at in result, got: %s", text)
	}
}

func TestHandleNextRun_NotFound(t *testing.T) {
	jr := &mockJobRepo{
		getJobWithScheduleFn: func(_ context.Context, _ uuid.UUID) (domain.Job, domain.Schedule, error) {
			return domain.Job{}, domain.Schedule{}, errors.New("not found")
		},
	}
	svc := newTestService(jr, nil, nil, nil)
	handler := handleNextRun(svc)

	req := mcpgo.CallToolRequest{
		Params: mcpgo.CallToolParams{
			Name: "next-run",
			Arguments: map[string]any{
				"id": uuid.New().String(),
			},
		},
	}

	result, err := handler(ctxWithNS("t1"), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected IsError=true")
	}

	text := resultText(t, result)
	if !strings.Contains(text, "Job not found") {
		t.Errorf("expected not found error, got: %s", text)
	}
}

// ── handleResolveSchedule ───────────────────────────────────────────────────

func TestHandleResolveSchedule_Cron(t *testing.T) {
	svc := newTestService(nil, nil, nil, nil)
	handler := handleResolveSchedule(svc)

	req := mcpgo.CallToolRequest{
		Params: mcpgo.CallToolParams{
			Name: "resolve-schedule",
			Arguments: map[string]any{
				"description": "*/5 * * * *",
				"timezone":    "UTC",
			},
		},
	}

	result, err := handler(ctxWithNS("t1"), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error: %s", resultText(t, result))
	}

	text := resultText(t, result)
	if !strings.Contains(text, "*/5 * * * *") {
		t.Errorf("expected cron expression in result, got: %s", text)
	}
	if !strings.Contains(text, "next_runs") {
		t.Errorf("expected next_runs in result, got: %s", text)
	}
}

func TestHandleResolveSchedule_NaturalLanguage(t *testing.T) {
	svc := newTestService(nil, nil, nil, nil)
	handler := handleResolveSchedule(svc)

	req := mcpgo.CallToolRequest{
		Params: mcpgo.CallToolParams{
			Name: "resolve-schedule",
			Arguments: map[string]any{
				"description": "every hour",
				"timezone":    "UTC",
			},
		},
	}

	result, err := handler(ctxWithNS("t1"), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error: %s", resultText(t, result))
	}

	text := resultText(t, result)
	if !strings.Contains(text, "cron_expression") {
		t.Errorf("expected cron_expression in result, got: %s", text)
	}
}

func TestHandleResolveSchedule_MissingDesc(t *testing.T) {
	svc := newTestService(nil, nil, nil, nil)
	handler := handleResolveSchedule(svc)

	req := mcpgo.CallToolRequest{
		Params: mcpgo.CallToolParams{
			Name:      "resolve-schedule",
			Arguments: map[string]any{},
		},
	}

	result, err := handler(ctxWithNS("t1"), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected IsError=true")
	}

	text := resultText(t, result)
	if !strings.Contains(text, "Missing required parameter: description") {
		t.Errorf("expected missing description error, got: %s", text)
	}
}

// ── parseTags ───────────────────────────────────────────────────────────────

func TestParseTags(t *testing.T) {
	t.Run("nil input", func(t *testing.T) {
		got := parseTags(nil)
		if got != nil {
			t.Errorf("expected nil, got %v", got)
		}
	})

	t.Run("non-map input", func(t *testing.T) {
		got := parseTags("not a map")
		if got != nil {
			t.Errorf("expected nil, got %v", got)
		}
	})

	t.Run("valid map", func(t *testing.T) {
		input := map[string]any{
			"env":  "prod",
			"team": "backend",
		}
		got := parseTags(input)
		if len(got) != 2 {
			t.Fatalf("expected 2 tags, got %d", len(got))
		}
		// Build a lookup map since iteration order is not guaranteed.
		tagMap := make(map[string]string)
		for _, tag := range got {
			tagMap[tag.Key] = tag.Value
		}
		if tagMap["env"] != "prod" {
			t.Errorf("expected env=prod, got env=%s", tagMap["env"])
		}
		if tagMap["team"] != "backend" {
			t.Errorf("expected team=backend, got team=%s", tagMap["team"])
		}
	})
}

// ── executionToMap ──────────────────────────────────────────────────────────

func TestExecutionToMap(t *testing.T) {
	t.Run("without acknowledged_at", func(t *testing.T) {
		exec := fixedExecution()
		m := executionToMap(exec)

		if m["id"] != exec.ID.String() {
			t.Errorf("id = %v, want %s", m["id"], exec.ID.String())
		}
		if m["job_id"] != exec.JobID.String() {
			t.Errorf("job_id = %v, want %s", m["job_id"], exec.JobID.String())
		}
		if m["trigger_type"] != string(exec.TriggerType) {
			t.Errorf("trigger_type = %v, want %s", m["trigger_type"], exec.TriggerType)
		}
		if m["status"] != string(exec.Status) {
			t.Errorf("status = %v, want %s", m["status"], exec.Status)
		}
		if _, ok := m["acknowledged_at"]; ok {
			t.Error("expected no acknowledged_at key when nil")
		}
	})

	t.Run("with acknowledged_at", func(t *testing.T) {
		exec := fixedExecution()
		ack := time.Date(2025, 1, 1, 12, 5, 0, 0, time.UTC)
		exec.AcknowledgedAt = &ack

		m := executionToMap(exec)

		got, ok := m["acknowledged_at"]
		if !ok {
			t.Fatal("expected acknowledged_at key")
		}
		want := ack.Format(time.RFC3339)
		if got != want {
			t.Errorf("acknowledged_at = %v, want %s", got, want)
		}
	})
}

// ── handleCreateJob — optional params ────────────────────────────────────────

func TestHandleCreateJob_AllOptionalParams(t *testing.T) {
	svc := newTestService(nil, nil, nil, nil)
	handler := handleCreateJob(svc)

	req := mcpgo.CallToolRequest{
		Params: mcpgo.CallToolParams{
			Name: "create-job",
			Arguments: map[string]any{
				"name":                    "full-job",
				"cron_expression":         "*/5 * * * *",
				"timezone":                "UTC",
				"webhook_url":             "https://example.com/hook",
				"webhook_secret":          "s3cret",
				"webhook_timeout_seconds": float64(60),
				"tags":                    map[string]any{"env": "prod"},
			},
		},
	}

	result, err := handler(ctxWithNS("t1"), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error: %s", resultText(t, result))
	}

	text := resultText(t, result)
	if !strings.Contains(text, "full-job") {
		t.Errorf("expected job name in result, got: %s", text)
	}
	if !strings.Contains(text, "env") {
		t.Errorf("expected tag key in result, got: %s", text)
	}
	if !strings.Contains(text, "prod") {
		t.Errorf("expected tag value in result, got: %s", text)
	}
}

// ── handleListJobs — filter params ──────────────────────────────────────────

func TestHandleListJobs_WithFilters(t *testing.T) {
	var capturedFilter domain.JobFilter
	jr := &mockJobRepo{
		listJobsFn: func(_ context.Context, filter domain.JobFilter) ([]domain.Job, error) {
			capturedFilter = filter
			j := fixedJob()
			j.Name = "test-filtered"
			return []domain.Job{j}, nil
		},
	}
	svc := newTestService(jr, nil, nil, nil)
	handler := handleListJobs(svc)

	req := mcpgo.CallToolRequest{
		Params: mcpgo.CallToolParams{
			Name: "list-jobs",
			Arguments: map[string]any{
				"name":    "test",
				"enabled": true,
			},
		},
	}

	result, err := handler(ctxWithNS("t1"), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error: %s", resultText(t, result))
	}

	text := resultText(t, result)
	if !strings.Contains(text, `"count": 1`) {
		t.Errorf("expected count 1 in result, got: %s", text)
	}

	// Verify filter.Name was set
	if capturedFilter.Name != "test" {
		t.Fatalf("expected filter.Name=%q, got %q", "test", capturedFilter.Name)
	}

	// Verify filter.Enabled was set
	if capturedFilter.Enabled == nil {
		t.Fatal("expected filter.Enabled to be set")
	}
	if *capturedFilter.Enabled != true {
		t.Fatalf("expected filter.Enabled=true, got %v", *capturedFilter.Enabled)
	}
}

// ── handleUpdateJob — all optional params ───────────────────────────────────

func TestHandleUpdateJob_AllParams(t *testing.T) {
	job := fixedJob()
	schedule := fixedSchedule()

	jr := &mockJobRepo{
		getJobWithScheduleFn: func(_ context.Context, _ uuid.UUID) (domain.Job, domain.Schedule, error) {
			return job, schedule, nil
		},
	}
	svc := newTestService(jr, nil, nil, nil)
	handler := handleUpdateJob(svc)

	req := mcpgo.CallToolRequest{
		Params: mcpgo.CallToolParams{
			Name: "update-job",
			Arguments: map[string]any{
				"id":                      job.ID.String(),
				"name":                    "renamed-job",
				"cron_expression":         "0 * * * *",
				"timezone":                "America/New_York",
				"webhook_url":             "https://example.com/new-hook",
				"webhook_secret":          "new-secret",
				"webhook_timeout_seconds": float64(90),
				"tags":                    map[string]any{"env": "staging", "team": "ops"},
			},
		},
	}

	result, err := handler(ctxWithNS("t1"), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error: %s", resultText(t, result))
	}

	text := resultText(t, result)
	if !strings.Contains(text, "renamed-job") {
		t.Errorf("expected updated name in result, got: %s", text)
	}
	if !strings.Contains(text, "0 * * * *") {
		t.Errorf("expected updated cron expression in result, got: %s", text)
	}
}
