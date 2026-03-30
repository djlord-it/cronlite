package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/djlord-it/easy-cron/internal/cron"
	"github.com/djlord-it/easy-cron/internal/domain"
	"github.com/google/uuid"
)

// --- Mock repositories ---

type mockJobRepo struct {
	insertJobFn          func(ctx context.Context, job domain.Job, schedule domain.Schedule) error
	getJobFn             func(ctx context.Context, id uuid.UUID) (domain.Job, error)
	getJobWithScheduleFn func(ctx context.Context, id uuid.UUID) (domain.Job, domain.Schedule, error)
	listJobsFn           func(ctx context.Context, filter domain.JobFilter) ([]domain.Job, error)
	updateJobFn          func(ctx context.Context, job domain.Job) error
	deleteJobFn          func(ctx context.Context, id uuid.UUID, ns domain.Namespace) error
	getEnabledJobsFn     func(ctx context.Context, limit, offset int) ([]domain.JobWithSchedule, error)

	// capture last call
	lastInsertedJob domain.Job
	lastUpdatedJob  domain.Job
}

func (m *mockJobRepo) InsertJob(ctx context.Context, job domain.Job, schedule domain.Schedule) error {
	m.lastInsertedJob = job
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

func (m *mockJobRepo) ListJobs(ctx context.Context, filter domain.JobFilter) ([]domain.Job, error) {
	if m.listJobsFn != nil {
		return m.listJobsFn(ctx, filter)
	}
	return nil, nil
}

func (m *mockJobRepo) UpdateJob(ctx context.Context, job domain.Job) error {
	m.lastUpdatedJob = job
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
	listExecutionsFn        func(ctx context.Context, filter domain.ExecutionFilter) ([]domain.Execution, error)
	getRecentExecutionsFn   func(ctx context.Context, jobID uuid.UUID, limit int) ([]domain.Execution, error)
	updateExecutionStatusFn func(ctx context.Context, id uuid.UUID, status domain.ExecutionStatus) error
	dequeueExecutionFn      func(ctx context.Context) (*domain.Execution, error)
	getOrphanedExecFn       func(ctx context.Context, olderThan time.Time, maxResults int) ([]domain.Execution, error)
	requeueStaleExecFn      func(ctx context.Context, olderThan time.Time, limit int) (int, error)

	lastInsertedExec domain.Execution
}

func (m *mockExecutionRepo) InsertExecution(ctx context.Context, exec domain.Execution) error {
	m.lastInsertedExec = exec
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

func (m *mockExecutionRepo) ListExecutions(ctx context.Context, filter domain.ExecutionFilter) ([]domain.Execution, error) {
	if m.listExecutionsFn != nil {
		return m.listExecutionsFn(ctx, filter)
	}
	return nil, nil
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

type mockAPIKeyRepo struct {
	insertAPIKeyFn    func(ctx context.Context, key domain.APIKey) error
	getKeyByHashFn    func(ctx context.Context, tokenHash string) (domain.APIKey, error)
	listKeysFn        func(ctx context.Context, ns domain.Namespace, params domain.ListParams) ([]domain.APIKey, error)
	deleteKeyFn       func(ctx context.Context, id uuid.UUID, ns domain.Namespace) error
	updateLastUsedFn  func(ctx context.Context, ids []uuid.UUID) error
}

func (m *mockAPIKeyRepo) InsertAPIKey(ctx context.Context, key domain.APIKey) error {
	if m.insertAPIKeyFn != nil {
		return m.insertAPIKeyFn(ctx, key)
	}
	return nil
}

func (m *mockAPIKeyRepo) GetKeyByTokenHash(ctx context.Context, tokenHash string) (domain.APIKey, error) {
	if m.getKeyByHashFn != nil {
		return m.getKeyByHashFn(ctx, tokenHash)
	}
	return domain.APIKey{}, nil
}

func (m *mockAPIKeyRepo) ListKeys(ctx context.Context, ns domain.Namespace, params domain.ListParams) ([]domain.APIKey, error) {
	if m.listKeysFn != nil {
		return m.listKeysFn(ctx, ns, params)
	}
	return nil, nil
}

func (m *mockAPIKeyRepo) DeleteKey(ctx context.Context, id uuid.UUID, ns domain.Namespace) error {
	if m.deleteKeyFn != nil {
		return m.deleteKeyFn(ctx, id, ns)
	}
	return nil
}

func (m *mockAPIKeyRepo) UpdateLastUsedAt(ctx context.Context, ids []uuid.UUID) error {
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

// --- Helpers ---

func ctxWithNS(ns string) context.Context {
	return domain.NamespaceToContext(context.Background(), domain.Namespace(ns))
}

func newTestService(jobRepo *mockJobRepo) *JobService {
	if jobRepo == nil {
		jobRepo = &mockJobRepo{}
	}
	return NewJobService(
		jobRepo,
		&mockScheduleRepo{},
		&mockExecutionRepo{},
		&mockTagRepo{},
		&mockAPIKeyRepo{},
		&mockAttemptRepo{},
		cron.NewParser(),
	)
}

func newTestServiceFull(
	jobRepo *mockJobRepo,
	schedRepo *mockScheduleRepo,
	execRepo *mockExecutionRepo,
	tagRepo *mockTagRepo,
	apiKeyRepo *mockAPIKeyRepo,
	attemptRepo *mockAttemptRepo,
) *JobService {
	if jobRepo == nil {
		jobRepo = &mockJobRepo{}
	}
	if schedRepo == nil {
		schedRepo = &mockScheduleRepo{}
	}
	if execRepo == nil {
		execRepo = &mockExecutionRepo{}
	}
	if tagRepo == nil {
		tagRepo = &mockTagRepo{}
	}
	if apiKeyRepo == nil {
		apiKeyRepo = &mockAPIKeyRepo{}
	}
	if attemptRepo == nil {
		attemptRepo = &mockAttemptRepo{}
	}
	return NewJobService(jobRepo, schedRepo, execRepo, tagRepo, apiKeyRepo, attemptRepo, cron.NewParser())
}

// --- Tests ---

func TestCreateJob_HappyPath(t *testing.T) {
	jobRepo := &mockJobRepo{}
	svc := newTestService(jobRepo)

	ctx := ctxWithNS("tenant-1")
	job, schedule, err := svc.CreateJob(ctx, CreateJobInput{
		Name:           "my-job",
		CronExpression: "*/5 * * * *",
		Timezone:       "UTC",
		WebhookURL:     "https://example.com/hook",
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if job.Name != "my-job" {
		t.Errorf("expected name 'my-job', got %q", job.Name)
	}
	if job.Namespace != "tenant-1" {
		t.Errorf("expected namespace 'tenant-1', got %q", job.Namespace)
	}
	if !job.Enabled {
		t.Error("expected job to be enabled")
	}
	if job.Delivery.Timeout != 30*time.Second {
		t.Errorf("expected default timeout 30s, got %v", job.Delivery.Timeout)
	}
	if schedule.CronExpression != "*/5 * * * *" {
		t.Errorf("expected cron '*/5 * * * *', got %q", schedule.CronExpression)
	}
	if schedule.Timezone != "UTC" {
		t.Errorf("expected timezone 'UTC', got %q", schedule.Timezone)
	}
	if job.ScheduleID != schedule.ID {
		t.Error("job.ScheduleID should match schedule.ID")
	}
}

func TestCreateJob_NoNamespace(t *testing.T) {
	svc := newTestService(nil)

	_, _, err := svc.CreateJob(context.Background(), CreateJobInput{
		Name:           "my-job",
		CronExpression: "*/5 * * * *",
		Timezone:       "UTC",
		WebhookURL:     "https://example.com/hook",
	})

	if !errors.Is(err, domain.ErrNamespaceRequired) {
		t.Errorf("expected ErrNamespaceRequired, got %v", err)
	}
}

func TestCreateJob_InvalidCron(t *testing.T) {
	svc := newTestService(nil)

	_, _, err := svc.CreateJob(ctxWithNS("t1"), CreateJobInput{
		Name:           "bad-cron",
		CronExpression: "not a cron",
		Timezone:       "UTC",
		WebhookURL:     "https://example.com/hook",
	})

	if !errors.Is(err, domain.ErrInvalidCronExpression) {
		t.Errorf("expected ErrInvalidCronExpression, got %v", err)
	}
}

func TestGetJob_WrongNamespace(t *testing.T) {
	jobID := uuid.New()
	schedID := uuid.New()
	jobRepo := &mockJobRepo{
		getJobWithScheduleFn: func(_ context.Context, id uuid.UUID) (domain.Job, domain.Schedule, error) {
			return domain.Job{
				ID:         jobID,
				Namespace:  "tenant-A",
				ScheduleID: schedID,
			}, domain.Schedule{ID: schedID}, nil
		},
	}

	svc := newTestService(jobRepo)

	// Request with a different namespace.
	ctx := ctxWithNS("tenant-B")
	_, _, _, _, err := svc.GetJob(ctx, jobID)

	if !errors.Is(err, domain.ErrJobNotFound) {
		t.Errorf("expected ErrJobNotFound for namespace mismatch, got %v", err)
	}
}

func TestPauseJob(t *testing.T) {
	jobID := uuid.New()
	jobRepo := &mockJobRepo{
		getJobWithScheduleFn: func(_ context.Context, id uuid.UUID) (domain.Job, domain.Schedule, error) {
			return domain.Job{
				ID:        jobID,
				Namespace: "t1",
				Enabled:   true,
			}, domain.Schedule{}, nil
		},
	}
	svc := newTestService(jobRepo)

	job, err := svc.PauseJob(ctxWithNS("t1"), jobID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if job.Enabled {
		t.Error("expected Enabled=false after pause")
	}
	if jobRepo.lastUpdatedJob.Enabled {
		t.Error("expected repo to receive Enabled=false")
	}
}

func TestResumeJob(t *testing.T) {
	jobID := uuid.New()
	jobRepo := &mockJobRepo{
		getJobWithScheduleFn: func(_ context.Context, id uuid.UUID) (domain.Job, domain.Schedule, error) {
			return domain.Job{
				ID:        jobID,
				Namespace: "t1",
				Enabled:   false,
			}, domain.Schedule{}, nil
		},
	}
	svc := newTestService(jobRepo)

	job, err := svc.ResumeJob(ctxWithNS("t1"), jobID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !job.Enabled {
		t.Error("expected Enabled=true after resume")
	}
	if !jobRepo.lastUpdatedJob.Enabled {
		t.Error("expected repo to receive Enabled=true")
	}
}

func TestTriggerNow_DisabledJob(t *testing.T) {
	jobID := uuid.New()
	jobRepo := &mockJobRepo{
		getJobWithScheduleFn: func(_ context.Context, id uuid.UUID) (domain.Job, domain.Schedule, error) {
			return domain.Job{
				ID:        jobID,
				Namespace: "t1",
				Enabled:   false,
			}, domain.Schedule{}, nil
		},
	}
	svc := newTestServiceFull(jobRepo, nil, &mockExecutionRepo{}, nil, nil, nil)

	_, err := svc.TriggerNow(ctxWithNS("t1"), jobID)
	if !errors.Is(err, domain.ErrJobDisabled) {
		t.Errorf("expected ErrJobDisabled, got %v", err)
	}
}

func TestTriggerNow_HappyPath(t *testing.T) {
	jobID := uuid.New()
	jobRepo := &mockJobRepo{
		getJobWithScheduleFn: func(_ context.Context, id uuid.UUID) (domain.Job, domain.Schedule, error) {
			return domain.Job{
				ID:        jobID,
				Namespace: "t1",
				Enabled:   true,
			}, domain.Schedule{}, nil
		},
	}
	execRepo := &mockExecutionRepo{}
	svc := newTestServiceFull(jobRepo, nil, execRepo, nil, nil, nil)

	exec, err := svc.TriggerNow(ctxWithNS("t1"), jobID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exec.TriggerType != domain.TriggerTypeManual {
		t.Errorf("expected TriggerTypeManual, got %q", exec.TriggerType)
	}
	if exec.Status != domain.ExecutionStatusEmitted {
		t.Errorf("expected status emitted, got %q", exec.Status)
	}
	if exec.JobID != jobID {
		t.Errorf("expected jobID %s, got %s", jobID, exec.JobID)
	}
	if exec.Namespace != "t1" {
		t.Errorf("expected namespace 't1', got %q", exec.Namespace)
	}
	// Verify the execution was passed to the repo.
	if execRepo.lastInsertedExec.ID != exec.ID {
		t.Error("expected execution to be inserted into repo")
	}
}

func TestResolveSchedule_CronPassthrough(t *testing.T) {
	svc := newTestService(nil)

	result, err := svc.ResolveSchedule(context.Background(), "*/10 * * * *", "UTC")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.CronExpression != "*/10 * * * *" {
		t.Errorf("expected '*/10 * * * *', got %q", result.CronExpression)
	}
	if len(result.NextRuns) != 5 {
		t.Errorf("expected 5 next runs, got %d", len(result.NextRuns))
	}
}

func TestResolveSchedule_EveryWeekdayAt9AM(t *testing.T) {
	svc := newTestService(nil)

	result, err := svc.ResolveSchedule(context.Background(), "every weekday at 9am", "UTC")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.CronExpression != "0 9 * * 1-5" {
		t.Errorf("expected '0 9 * * 1-5', got %q", result.CronExpression)
	}
}

func TestResolveSchedule_Every30Minutes(t *testing.T) {
	svc := newTestService(nil)

	result, err := svc.ResolveSchedule(context.Background(), "every 30 minutes", "UTC")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.CronExpression != "*/30 * * * *" {
		t.Errorf("expected '*/30 * * * *', got %q", result.CronExpression)
	}
}

func TestResolveSchedule_InvalidTimezone(t *testing.T) {
	svc := newTestService(nil)

	_, err := svc.ResolveSchedule(context.Background(), "every hour", "Invalid/Zone")
	if !errors.Is(err, domain.ErrInvalidTimezone) {
		t.Errorf("expected ErrInvalidTimezone, got %v", err)
	}
}

func TestResolveSchedule_UnrecognizedInput(t *testing.T) {
	svc := newTestService(nil)

	_, err := svc.ResolveSchedule(context.Background(), "whenever the moon is full", "UTC")
	if !errors.Is(err, domain.ErrScheduleParseFailure) {
		t.Errorf("expected ErrScheduleParseFailure, got %v", err)
	}
}
