package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/djlord-it/cronlite/internal/cron"
	"github.com/djlord-it/cronlite/internal/domain"
	"github.com/google/uuid"
)

// --- Mock repositories ---

type mockJobRepo struct {
	insertJobFn                func(ctx context.Context, job domain.Job, schedule domain.Schedule) error
	getJobFn                   func(ctx context.Context, id uuid.UUID) (domain.Job, error)
	getJobWithScheduleFn       func(ctx context.Context, id uuid.UUID) (domain.Job, domain.Schedule, error)
	getJobWithScheduleScopedFn func(ctx context.Context, id uuid.UUID, ns domain.Namespace) (domain.Job, domain.Schedule, error)
	listJobsFn                 func(ctx context.Context, filter domain.JobFilter) ([]domain.Job, error)
	updateJobFn                func(ctx context.Context, job domain.Job) error
	deleteJobFn                func(ctx context.Context, id uuid.UUID, ns domain.Namespace) error
	getEnabledJobsFn           func(ctx context.Context, limit, offset int) ([]domain.JobWithSchedule, error)

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

func (m *mockJobRepo) GetJobWithScheduleScoped(ctx context.Context, id uuid.UUID, ns domain.Namespace) (domain.Job, domain.Schedule, error) {
	if m.getJobWithScheduleScopedFn != nil {
		return m.getJobWithScheduleScopedFn(ctx, id, ns)
	}
	// Delegate to unscoped for backward compatibility with existing tests.
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
	getExecutionScopedFn    func(ctx context.Context, id uuid.UUID, ns domain.Namespace) (domain.Execution, error)
	listExecutionsFn        func(ctx context.Context, filter domain.ExecutionFilter) ([]domain.Execution, error)
	listPendingAckFn        func(ctx context.Context, ns domain.Namespace, jobID *uuid.UUID, limit int) ([]domain.Execution, error)
	ackExecutionFn          func(ctx context.Context, id uuid.UUID, ns domain.Namespace) error
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

type mockAPIKeyRepo struct {
	insertAPIKeyFn   func(ctx context.Context, key domain.APIKey) error
	getKeyByHashFn   func(ctx context.Context, tokenHash string) (domain.APIKey, error)
	listKeysFn       func(ctx context.Context, ns domain.Namespace, params domain.ListParams) ([]domain.APIKey, error)
	deleteKeyFn      func(ctx context.Context, id uuid.UUID, ns domain.Namespace) error
	updateLastUsedFn func(ctx context.Context, ids []uuid.UUID) error
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

func TestCreateJob_InvalidWebhookURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{"loopback ipv4", "http://127.0.0.1/hook"},
		{"uppercase localhost", "http://LOCALHOST/hook"},
		{"unspecified ipv4", "http://0.0.0.0/hook"},
		{"unspecified ipv6", "http://[::]/hook"},
		{"ipv6 link-local with zone", "http://[fe80::1%25eth0]/hook"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newTestService(nil)

			_, _, err := svc.CreateJob(ctxWithNS("t1"), CreateJobInput{
				Name:           "bad-webhook",
				CronExpression: "*/5 * * * *",
				Timezone:       "UTC",
				WebhookURL:     tt.url,
			})

			if !errors.Is(err, domain.ErrInvalidWebhookURL) {
				t.Errorf("expected ErrInvalidWebhookURL, got %v", err)
			}
		})
	}
}

func TestGetJob_WrongNamespace(t *testing.T) {
	jobID := uuid.New()
	jobRepo := &mockJobRepo{
		getJobWithScheduleScopedFn: func(_ context.Context, id uuid.UUID, ns domain.Namespace) (domain.Job, domain.Schedule, error) {
			// Simulate SQL-level namespace filtering: wrong namespace returns not found.
			if ns != "tenant-A" {
				return domain.Job{}, domain.Schedule{}, domain.ErrJobNotFound
			}
			return domain.Job{ID: jobID, Namespace: "tenant-A"}, domain.Schedule{}, nil
		},
	}

	svc := newTestService(jobRepo)

	// Request with a different namespace — SQL filter rejects it.
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

// --- ListJobs tests ---

func TestListJobs_HappyPath(t *testing.T) {
	var capturedFilter domain.JobFilter
	jobRepo := &mockJobRepo{
		listJobsFn: func(_ context.Context, filter domain.JobFilter) ([]domain.Job, error) {
			capturedFilter = filter
			return []domain.Job{
				{ID: uuid.New(), Namespace: "t1", Name: "job-1"},
				{ID: uuid.New(), Namespace: "t1", Name: "job-2"},
			}, nil
		},
	}
	svc := newTestService(jobRepo)

	jobs, err := svc.ListJobs(ctxWithNS("t1"), domain.JobFilter{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(jobs) != 2 {
		t.Errorf("expected 2 jobs, got %d", len(jobs))
	}
	if capturedFilter.Namespace != "t1" {
		t.Errorf("expected namespace 't1' on filter, got %q", capturedFilter.Namespace)
	}
	if capturedFilter.Limit <= 0 {
		t.Error("expected ListParams defaults to be applied")
	}
}

func TestListJobs_NoNamespace(t *testing.T) {
	svc := newTestService(nil)

	_, err := svc.ListJobs(context.Background(), domain.JobFilter{})
	if !errors.Is(err, domain.ErrNamespaceRequired) {
		t.Errorf("expected ErrNamespaceRequired, got %v", err)
	}
}

// --- UpdateJob tests ---

func TestUpdateJob_HappyPath(t *testing.T) {
	jobID := uuid.New()
	schedID := uuid.New()
	jobRepo := &mockJobRepo{
		getJobWithScheduleFn: func(_ context.Context, id uuid.UUID) (domain.Job, domain.Schedule, error) {
			return domain.Job{
				ID: jobID, Namespace: "t1", Name: "old-name", ScheduleID: schedID, Enabled: true,
				Delivery: domain.DeliveryConfig{WebhookURL: "https://old.com", Timeout: 30 * time.Second},
			}, domain.Schedule{ID: schedID, CronExpression: "*/5 * * * *", Timezone: "UTC"}, nil
		},
	}
	svc := newTestService(jobRepo)

	newName := "new-name"
	job, sched, err := svc.UpdateJob(ctxWithNS("t1"), jobID, UpdateJobInput{Name: &newName})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if job.Name != "new-name" {
		t.Errorf("expected name 'new-name', got %q", job.Name)
	}
	if sched.CronExpression != "*/5 * * * *" {
		t.Errorf("expected cron unchanged, got %q", sched.CronExpression)
	}
	if jobRepo.lastUpdatedJob.Name != "new-name" {
		t.Errorf("expected repo to receive name 'new-name', got %q", jobRepo.lastUpdatedJob.Name)
	}
}

func TestUpdateJob_ScheduleChanged(t *testing.T) {
	jobID := uuid.New()
	schedID := uuid.New()
	var scheduleUpdated bool
	jobRepo := &mockJobRepo{
		getJobWithScheduleFn: func(_ context.Context, id uuid.UUID) (domain.Job, domain.Schedule, error) {
			return domain.Job{
				ID: jobID, Namespace: "t1", Name: "my-job", ScheduleID: schedID, Enabled: true,
				Delivery: domain.DeliveryConfig{WebhookURL: "https://example.com", Timeout: 30 * time.Second},
			}, domain.Schedule{ID: schedID, CronExpression: "*/5 * * * *", Timezone: "UTC"}, nil
		},
	}
	schedRepo := &mockScheduleRepo{
		updateScheduleFn: func(_ context.Context, schedule domain.Schedule) error {
			scheduleUpdated = true
			if schedule.CronExpression != "*/10 * * * *" {
				t.Errorf("expected cron '*/10 * * * *', got %q", schedule.CronExpression)
			}
			return nil
		},
	}
	svc := newTestServiceFull(jobRepo, schedRepo, nil, nil, nil, nil)

	newCron := "*/10 * * * *"
	_, sched, err := svc.UpdateJob(ctxWithNS("t1"), jobID, UpdateJobInput{CronExpression: &newCron})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !scheduleUpdated {
		t.Error("expected schedule to be updated")
	}
	if sched.CronExpression != "*/10 * * * *" {
		t.Errorf("expected new cron '*/10 * * * *', got %q", sched.CronExpression)
	}
}

func TestUpdateJob_InvalidNewCron(t *testing.T) {
	jobID := uuid.New()
	schedID := uuid.New()
	jobRepo := &mockJobRepo{
		getJobWithScheduleFn: func(_ context.Context, id uuid.UUID) (domain.Job, domain.Schedule, error) {
			return domain.Job{
				ID: jobID, Namespace: "t1", Name: "my-job", ScheduleID: schedID, Enabled: true,
				Delivery: domain.DeliveryConfig{WebhookURL: "https://example.com", Timeout: 30 * time.Second},
			}, domain.Schedule{ID: schedID, CronExpression: "*/5 * * * *", Timezone: "UTC"}, nil
		},
	}
	svc := newTestService(jobRepo)

	badCron := "not valid cron"
	_, _, err := svc.UpdateJob(ctxWithNS("t1"), jobID, UpdateJobInput{CronExpression: &badCron})
	if !errors.Is(err, domain.ErrInvalidCronExpression) {
		t.Errorf("expected ErrInvalidCronExpression, got %v", err)
	}
}

func TestUpdateJob_InvalidWebhookURL(t *testing.T) {
	jobID := uuid.New()
	schedID := uuid.New()
	jobRepo := &mockJobRepo{
		getJobWithScheduleFn: func(_ context.Context, id uuid.UUID) (domain.Job, domain.Schedule, error) {
			return domain.Job{
				ID: jobID, Namespace: "t1", Name: "my-job", ScheduleID: schedID, Enabled: true,
				Delivery: domain.DeliveryConfig{WebhookURL: "https://example.com", Timeout: 30 * time.Second},
			}, domain.Schedule{ID: schedID, CronExpression: "*/5 * * * *", Timezone: "UTC"}, nil
		},
	}
	svc := newTestService(jobRepo)

	badURL := "http://localhost/hook"
	_, _, err := svc.UpdateJob(ctxWithNS("t1"), jobID, UpdateJobInput{WebhookURL: &badURL})
	if !errors.Is(err, domain.ErrInvalidWebhookURL) {
		t.Errorf("expected ErrInvalidWebhookURL, got %v", err)
	}
}

func TestUpdateJob_NoNamespace(t *testing.T) {
	svc := newTestService(nil)

	newName := "whatever"
	_, _, err := svc.UpdateJob(context.Background(), uuid.New(), UpdateJobInput{Name: &newName})
	if !errors.Is(err, domain.ErrNamespaceRequired) {
		t.Errorf("expected ErrNamespaceRequired, got %v", err)
	}
}

func TestUpdateJob_NamespaceMismatch(t *testing.T) {
	jobID := uuid.New()
	jobRepo := &mockJobRepo{
		getJobWithScheduleScopedFn: func(_ context.Context, id uuid.UUID, ns domain.Namespace) (domain.Job, domain.Schedule, error) {
			if ns != "tenant-A" {
				return domain.Job{}, domain.Schedule{}, domain.ErrJobNotFound
			}
			return domain.Job{ID: jobID, Namespace: "tenant-A"}, domain.Schedule{}, nil
		},
	}
	svc := newTestService(jobRepo)

	newName := "new-name"
	_, _, err := svc.UpdateJob(ctxWithNS("tenant-B"), jobID, UpdateJobInput{Name: &newName})
	if !errors.Is(err, domain.ErrJobNotFound) {
		t.Errorf("expected ErrJobNotFound for namespace mismatch, got %v", err)
	}
}

func TestUpdateJob_WithTags(t *testing.T) {
	jobID := uuid.New()
	schedID := uuid.New()
	jobRepo := &mockJobRepo{
		getJobWithScheduleFn: func(_ context.Context, id uuid.UUID) (domain.Job, domain.Schedule, error) {
			return domain.Job{
				ID: jobID, Namespace: "t1", Name: "my-job", ScheduleID: schedID, Enabled: true,
				Delivery: domain.DeliveryConfig{WebhookURL: "https://example.com", Timeout: 30 * time.Second},
			}, domain.Schedule{ID: schedID, CronExpression: "*/5 * * * *", Timezone: "UTC"}, nil
		},
	}

	var deleteCalled, upsertCalled bool
	tagRepo := &mockTagRepo{
		deleteTagsFn: func(_ context.Context, id uuid.UUID) error {
			deleteCalled = true
			if id != jobID {
				t.Errorf("expected delete for job %s, got %s", jobID, id)
			}
			return nil
		},
		upsertTagsFn: func(_ context.Context, id uuid.UUID, tags []domain.Tag) error {
			upsertCalled = true
			if id != jobID {
				t.Errorf("expected upsert for job %s, got %s", jobID, id)
			}
			if len(tags) != 2 {
				t.Errorf("expected 2 tags, got %d", len(tags))
			}
			return nil
		},
	}
	svc := newTestServiceFull(jobRepo, nil, nil, tagRepo, nil, nil)

	newTags := []domain.Tag{{Key: "env", Value: "prod"}, {Key: "team", Value: "backend"}}
	_, _, err := svc.UpdateJob(ctxWithNS("t1"), jobID, UpdateJobInput{Tags: &newTags})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !deleteCalled {
		t.Error("expected DeleteTags to be called")
	}
	if !upsertCalled {
		t.Error("expected UpsertTags to be called")
	}
}

// --- DeleteJob tests ---

func TestDeleteJob_HappyPath(t *testing.T) {
	jobID := uuid.New()
	var capturedID uuid.UUID
	var capturedNS domain.Namespace
	jobRepo := &mockJobRepo{
		deleteJobFn: func(_ context.Context, id uuid.UUID, ns domain.Namespace) error {
			capturedID = id
			capturedNS = ns
			return nil
		},
	}
	svc := newTestService(jobRepo)

	err := svc.DeleteJob(ctxWithNS("t1"), jobID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedID != jobID {
		t.Errorf("expected job ID %s, got %s", jobID, capturedID)
	}
	if capturedNS != "t1" {
		t.Errorf("expected namespace 't1', got %q", capturedNS)
	}
}

func TestDeleteJob_NoNamespace(t *testing.T) {
	svc := newTestService(nil)

	err := svc.DeleteJob(context.Background(), uuid.New())
	if !errors.Is(err, domain.ErrNamespaceRequired) {
		t.Errorf("expected ErrNamespaceRequired, got %v", err)
	}
}

// --- GetJob tests ---

func TestGetJob_HappyPath(t *testing.T) {
	jobID := uuid.New()
	schedID := uuid.New()
	jobRepo := &mockJobRepo{
		getJobWithScheduleFn: func(_ context.Context, id uuid.UUID) (domain.Job, domain.Schedule, error) {
			return domain.Job{
				ID: jobID, Namespace: "t1", Name: "my-job", ScheduleID: schedID, Enabled: true,
			}, domain.Schedule{ID: schedID, CronExpression: "*/5 * * * *", Timezone: "UTC"}, nil
		},
	}
	tagRepo := &mockTagRepo{
		getTagsFn: func(_ context.Context, id uuid.UUID) ([]domain.Tag, error) {
			return []domain.Tag{{Key: "env", Value: "prod"}}, nil
		},
	}
	execRepo := &mockExecutionRepo{
		getRecentExecutionsFn: func(_ context.Context, id uuid.UUID, limit int) ([]domain.Execution, error) {
			return []domain.Execution{
				{ID: uuid.New(), JobID: jobID, Status: domain.ExecutionStatusDelivered},
			}, nil
		},
	}
	svc := newTestServiceFull(jobRepo, nil, execRepo, tagRepo, nil, nil)

	job, sched, tags, execs, err := svc.GetJob(ctxWithNS("t1"), jobID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if job.Name != "my-job" {
		t.Errorf("expected name 'my-job', got %q", job.Name)
	}
	if sched.CronExpression != "*/5 * * * *" {
		t.Errorf("expected cron '*/5 * * * *', got %q", sched.CronExpression)
	}
	if len(tags) != 1 {
		t.Errorf("expected 1 tag, got %d", len(tags))
	}
	if len(execs) != 1 {
		t.Errorf("expected 1 execution, got %d", len(execs))
	}
}

func TestGetJob_NoNamespace(t *testing.T) {
	svc := newTestService(nil)

	_, _, _, _, err := svc.GetJob(context.Background(), uuid.New())
	if !errors.Is(err, domain.ErrNamespaceRequired) {
		t.Errorf("expected ErrNamespaceRequired, got %v", err)
	}
}

// --- GetNextRunTime tests ---

func TestGetNextRunTime_HappyPath(t *testing.T) {
	jobID := uuid.New()
	schedID := uuid.New()
	jobRepo := &mockJobRepo{
		getJobWithScheduleFn: func(_ context.Context, id uuid.UUID) (domain.Job, domain.Schedule, error) {
			return domain.Job{
				ID: jobID, Namespace: "t1", ScheduleID: schedID, Enabled: true,
			}, domain.Schedule{ID: schedID, CronExpression: "*/5 * * * *", Timezone: "UTC"}, nil
		},
	}
	svc := newTestService(jobRepo)

	nextRun, runs, sched, err := svc.GetNextRunTime(ctxWithNS("t1"), jobID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(runs) != 5 {
		t.Errorf("expected 5 run times, got %d", len(runs))
	}
	if nextRun.IsZero() {
		t.Error("expected non-zero next run time")
	}
	if nextRun != runs[0] {
		t.Error("expected first run to match nextRun")
	}
	if sched.CronExpression != "*/5 * * * *" {
		t.Errorf("expected cron '*/5 * * * *', got %q", sched.CronExpression)
	}
	// Verify runs are in ascending order.
	for i := 1; i < len(runs); i++ {
		if !runs[i].After(runs[i-1]) {
			t.Errorf("expected runs[%d] > runs[%d]", i, i-1)
		}
	}
}

func TestGetNextRunTime_NoNamespace(t *testing.T) {
	svc := newTestService(nil)

	_, _, _, err := svc.GetNextRunTime(context.Background(), uuid.New())
	if !errors.Is(err, domain.ErrNamespaceRequired) {
		t.Errorf("expected ErrNamespaceRequired, got %v", err)
	}
}

func TestGetNextRunTime_NotFound(t *testing.T) {
	jobRepo := &mockJobRepo{
		getJobWithScheduleFn: func(_ context.Context, id uuid.UUID) (domain.Job, domain.Schedule, error) {
			return domain.Job{}, domain.Schedule{}, errors.New("not found")
		},
	}
	svc := newTestService(jobRepo)

	_, _, _, err := svc.GetNextRunTime(ctxWithNS("t1"), uuid.New())
	if !errors.Is(err, domain.ErrJobNotFound) {
		t.Errorf("expected ErrJobNotFound, got %v", err)
	}
}

// --- parseNaturalLanguage tests ---

func TestParseNaturalLanguage(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantCron  string
		wantError bool
	}{
		{"every 5 minutes", "every 5 minutes", "*/5 * * * *", false},
		{"every 2 hours", "every 2 hours", "0 */2 * * *", false},
		{"every hour", "every hour", "0 * * * *", false},
		{"hourly", "hourly", "0 * * * *", false},
		{"daily at 9am", "daily at 9am", "0 9 * * *", false},
		{"daily at 2:30pm", "daily at 2:30pm", "30 14 * * *", false},
		{"every weekday at 9:00 am", "every weekday at 9:00 am", "0 9 * * 1-5", false},
		{"every monday at 14:00", "every monday at 14:00", "0 14 * * 1", false},
		{"every saturday at 12pm", "every saturday at 12pm", "0 12 * * 6", false},
		{"every 0 minutes - error", "every 0 minutes", "", true},
		{"every 60 minutes - error", "every 60 minutes", "", true},
		{"every 0 hours - error", "every 0 hours", "", true},
		{"every 24 hours - error", "every 24 hours", "", true},
		{"something invalid - error", "something invalid", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cronExpr, _, err := parseNaturalLanguage(tt.input)
			if tt.wantError {
				if err == nil {
					t.Errorf("expected error for input %q, got nil", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for input %q: %v", tt.input, err)
			}
			if cronExpr != tt.wantCron {
				t.Errorf("input %q: expected cron %q, got %q", tt.input, tt.wantCron, cronExpr)
			}
		})
	}
}

// --- parseTime tests ---

func TestParseTime(t *testing.T) {
	tests := []struct {
		name       string
		hourStr    string
		minuteStr  string
		ampm       string
		wantHour   int
		wantMinute int
	}{
		{"9am", "9", "", "am", 9, 0},
		{"9pm", "9", "", "pm", 21, 0},
		{"12am is midnight", "12", "", "am", 0, 0},
		{"12pm is noon", "12", "", "pm", 12, 0},
		{"3:30pm", "3", "30", "pm", 15, 30},
		{"14:00 24h", "14", "00", "", 14, 0},
		{"0:00 24h", "0", "00", "", 0, 0},
		{"25:00 invalid hour", "25", "00", "", -1, 0},
		{"abc invalid", "abc", "", "", -1, 0},
		{"9:60 invalid minute", "9", "60", "", -1, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotHour, gotMinute := parseTime(tt.hourStr, tt.minuteStr, tt.ampm)
			if gotHour != tt.wantHour {
				t.Errorf("expected hour %d, got %d", tt.wantHour, gotHour)
			}
			if gotMinute != tt.wantMinute {
				t.Errorf("expected minute %d, got %d", tt.wantMinute, gotMinute)
			}
		})
	}
}

// --- CreateJob error-path tests ---

func TestCreateJob_InsertJobError(t *testing.T) {
	insertErr := errors.New("db write failed")
	jobRepo := &mockJobRepo{
		insertJobFn: func(_ context.Context, _ domain.Job, _ domain.Schedule) error {
			return insertErr
		},
	}
	svc := newTestService(jobRepo)

	_, _, err := svc.CreateJob(ctxWithNS("t1"), CreateJobInput{
		Name:           "my-job",
		CronExpression: "*/5 * * * *",
		Timezone:       "UTC",
		WebhookURL:     "https://example.com/hook",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, insertErr) {
		t.Errorf("expected wrapped insertErr, got %v", err)
	}
}

func TestCreateJob_UpsertTagsError(t *testing.T) {
	upsertErr := errors.New("tag write failed")
	tagRepo := &mockTagRepo{
		upsertTagsFn: func(_ context.Context, _ uuid.UUID, _ []domain.Tag) error {
			return upsertErr
		},
	}
	svc := newTestServiceFull(&mockJobRepo{}, nil, nil, tagRepo, nil, nil)

	_, _, err := svc.CreateJob(ctxWithNS("t1"), CreateJobInput{
		Name:           "my-job",
		CronExpression: "*/5 * * * *",
		Timezone:       "UTC",
		WebhookURL:     "https://example.com/hook",
		Tags:           []domain.Tag{{Key: "env", Value: "prod"}},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, upsertErr) {
		t.Errorf("expected wrapped upsertErr, got %v", err)
	}
}

func TestCreateJob_WithCustomTimeout(t *testing.T) {
	jobRepo := &mockJobRepo{}
	svc := newTestService(jobRepo)

	job, _, err := svc.CreateJob(ctxWithNS("t1"), CreateJobInput{
		Name:           "my-job",
		CronExpression: "*/5 * * * *",
		Timezone:       "UTC",
		WebhookURL:     "https://example.com/hook",
		Timeout:        60 * time.Second,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if job.Delivery.Timeout != 60*time.Second {
		t.Errorf("expected timeout 60s, got %v", job.Delivery.Timeout)
	}
}

// --- PauseJob / ResumeJob / TriggerNow no-namespace tests ---

func TestPauseJob_NoNamespace(t *testing.T) {
	svc := newTestService(nil)

	_, err := svc.PauseJob(context.Background(), uuid.New())
	if !errors.Is(err, domain.ErrNamespaceRequired) {
		t.Errorf("expected ErrNamespaceRequired, got %v", err)
	}
}

func TestResumeJob_NoNamespace(t *testing.T) {
	svc := newTestService(nil)

	_, err := svc.ResumeJob(context.Background(), uuid.New())
	if !errors.Is(err, domain.ErrNamespaceRequired) {
		t.Errorf("expected ErrNamespaceRequired, got %v", err)
	}
}

func TestTriggerNow_NoNamespace(t *testing.T) {
	svc := newTestService(nil)

	_, err := svc.TriggerNow(context.Background(), uuid.New())
	if !errors.Is(err, domain.ErrNamespaceRequired) {
		t.Errorf("expected ErrNamespaceRequired, got %v", err)
	}
}

// --- PauseJob / ResumeJob / TriggerNow namespace-mismatch tests ---

func TestPauseJob_NamespaceMismatch(t *testing.T) {
	jobID := uuid.New()
	jobRepo := &mockJobRepo{
		getJobWithScheduleScopedFn: func(_ context.Context, id uuid.UUID, ns domain.Namespace) (domain.Job, domain.Schedule, error) {
			if ns != "tenant-A" {
				return domain.Job{}, domain.Schedule{}, domain.ErrJobNotFound
			}
			return domain.Job{ID: jobID, Namespace: "tenant-A", Enabled: true}, domain.Schedule{}, nil
		},
	}
	svc := newTestService(jobRepo)

	_, err := svc.PauseJob(ctxWithNS("tenant-B"), jobID)
	if !errors.Is(err, domain.ErrJobNotFound) {
		t.Errorf("expected ErrJobNotFound for namespace mismatch, got %v", err)
	}
}

func TestResumeJob_NamespaceMismatch(t *testing.T) {
	jobID := uuid.New()
	jobRepo := &mockJobRepo{
		getJobWithScheduleScopedFn: func(_ context.Context, id uuid.UUID, ns domain.Namespace) (domain.Job, domain.Schedule, error) {
			if ns != "tenant-A" {
				return domain.Job{}, domain.Schedule{}, domain.ErrJobNotFound
			}
			return domain.Job{ID: jobID, Namespace: "tenant-A", Enabled: false}, domain.Schedule{}, nil
		},
	}
	svc := newTestService(jobRepo)

	_, err := svc.ResumeJob(ctxWithNS("tenant-B"), jobID)
	if !errors.Is(err, domain.ErrJobNotFound) {
		t.Errorf("expected ErrJobNotFound for namespace mismatch, got %v", err)
	}
}

func TestTriggerNow_NamespaceMismatch(t *testing.T) {
	jobID := uuid.New()
	jobRepo := &mockJobRepo{
		getJobWithScheduleScopedFn: func(_ context.Context, id uuid.UUID, ns domain.Namespace) (domain.Job, domain.Schedule, error) {
			if ns != "tenant-A" {
				return domain.Job{}, domain.Schedule{}, domain.ErrJobNotFound
			}
			return domain.Job{ID: jobID, Namespace: "tenant-A", Enabled: true}, domain.Schedule{}, nil
		},
	}
	svc := newTestServiceFull(jobRepo, nil, &mockExecutionRepo{}, nil, nil, nil)

	_, err := svc.TriggerNow(ctxWithNS("tenant-B"), jobID)
	if !errors.Is(err, domain.ErrJobNotFound) {
		t.Errorf("expected ErrJobNotFound for namespace mismatch, got %v", err)
	}
}

// --- mockEmitter for TriggerNow emit tests ---

type mockEmitter struct {
	emitFn    func(ctx context.Context, event domain.TriggerEvent) error
	lastEvent domain.TriggerEvent
	called    bool
}

func (m *mockEmitter) Emit(ctx context.Context, event domain.TriggerEvent) error {
	m.called = true
	m.lastEvent = event
	if m.emitFn != nil {
		return m.emitFn(ctx, event)
	}
	return nil
}

func TestTriggerNow_EmitsEvent(t *testing.T) {
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
	emitter := &mockEmitter{}
	svc := newTestServiceFull(jobRepo, nil, execRepo, nil, nil, nil).WithEmitter(emitter)

	exec, err := svc.TriggerNow(ctxWithNS("t1"), jobID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !emitter.called {
		t.Fatal("expected Emit to be called")
	}
	if emitter.lastEvent.ExecutionID != exec.ID {
		t.Errorf("expected emitted ExecutionID %s, got %s", exec.ID, emitter.lastEvent.ExecutionID)
	}
	if emitter.lastEvent.JobID != jobID {
		t.Errorf("expected emitted JobID %s, got %s", jobID, emitter.lastEvent.JobID)
	}
	if emitter.lastEvent.Namespace != "t1" {
		t.Errorf("expected emitted Namespace 't1', got %q", emitter.lastEvent.Namespace)
	}
}

func TestTriggerNow_EmitFailureIsNonFatal(t *testing.T) {
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
	emitter := &mockEmitter{
		emitFn: func(_ context.Context, _ domain.TriggerEvent) error {
			return errors.New("emit failed")
		},
	}
	svc := newTestServiceFull(jobRepo, nil, execRepo, nil, nil, nil).WithEmitter(emitter)

	exec, err := svc.TriggerNow(ctxWithNS("t1"), jobID)
	if err != nil {
		t.Fatalf("TriggerNow should succeed even when Emit fails, got: %v", err)
	}
	if !emitter.called {
		t.Error("expected Emit to be called even though it fails")
	}
	if exec.ID == uuid.Nil {
		t.Error("expected a valid execution ID")
	}
	// Verify the execution was still inserted into the repo.
	if execRepo.lastInsertedExec.ID != exec.ID {
		t.Error("expected execution to be inserted into repo despite emit failure")
	}
}
