package api

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/djlord-it/cronlite/internal/domain"
	"github.com/djlord-it/cronlite/internal/service"
)

// ServerImpl implements StrictServerInterface by delegating to the service layer.
type ServerImpl struct {
	svc *service.JobService
	db  HealthChecker // optional, for verbose health checks
}

// NewServerImpl creates a new ServerImpl backed by the given service.
func NewServerImpl(svc *service.JobService) *ServerImpl {
	return &ServerImpl{svc: svc}
}

// WithHealthChecker sets the database health checker for verbose /health responses.
func (s *ServerImpl) WithHealthChecker(db HealthChecker) *ServerImpl {
	s.db = db
	return s
}

// Compile-time assertion.
var _ StrictServerInterface = (*ServerImpl)(nil)

// ── Health ────────────────────────────────────────────────────────────────────

func (s *ServerImpl) GetHealth(ctx context.Context, request GetHealthRequestObject) (GetHealthResponseObject, error) {
	verbose := request.Params.Verbose != nil && *request.Params.Verbose

	if !verbose || s.db == nil {
		return GetHealth200JSONResponse(HealthResponse{Status: "ok"}), nil
	}

	pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	resp := HealthResponse{Status: "ok"}
	if err := s.db.PingContext(pingCtx); err != nil {
		resp.Status = "degraded"
		dbStatus := "unhealthy"
		resp.Database = &dbStatus
		log.Printf("api: health check database unhealthy: %v", err)
		return GetHealth503JSONResponse(resp), nil
	} else {
		dbStatus := "healthy"
		resp.Database = &dbStatus
	}

	return GetHealth200JSONResponse(resp), nil
}

// ── Jobs ──────────────────────────────────────────────────────────────────────

func (s *ServerImpl) CreateJob(ctx context.Context, request CreateJobRequestObject) (CreateJobResponseObject, error) {
	body := request.Body

	var timeout time.Duration
	if body.WebhookTimeoutSeconds != nil {
		timeout = time.Duration(*body.WebhookTimeoutSeconds) * time.Second
	}

	var secret string
	if body.WebhookSecret != nil {
		secret = *body.WebhookSecret
	}

	input := service.CreateJobInput{
		Name:           body.Name,
		CronExpression: body.CronExpression,
		Timezone:       body.Timezone,
		WebhookURL:     body.WebhookUrl,
		Secret:         secret,
		Timeout:        timeout,
		Tags:           apiTagsToDomain(body.Tags),
	}

	job, schedule, err := s.svc.CreateJob(ctx, input)
	if err != nil {
		he := mapDomainError(err)
		if he.Status == 422 {
			return CreateJob422JSONResponse(newErrorResponse(he)), nil
		}
		return nil, err
	}

	tags := domainTagsToAPI(input.Tags)
	return CreateJob201JSONResponse(JobDetail{
		Id:             job.ID,
		Namespace:      job.Namespace.String(),
		Name:           job.Name,
		Enabled:        job.Enabled,
		CronExpression: schedule.CronExpression,
		Timezone:       schedule.Timezone,
		WebhookUrl:     job.Delivery.WebhookURL,
		Tags:           tags,
		CreatedAt:      job.CreatedAt,
		UpdatedAt:      job.UpdatedAt,
	}), nil
}

func (s *ServerImpl) ListJobs(ctx context.Context, request ListJobsRequestObject) (ListJobsResponseObject, error) {
	params := request.Params
	filter := domain.JobFilter{
		ListParams: listParamsFromQuery(params.Limit, params.Offset),
	}

	if params.Enabled != nil {
		filter.Enabled = params.Enabled
	}
	if params.Name != nil {
		filter.Name = *params.Name
	}
	if params.Tag != nil {
		for _, t := range *params.Tag {
			parts := strings.SplitN(t, ":", 2)
			if len(parts) == 2 {
				filter.Tags = append(filter.Tags, domain.Tag{Key: parts[0], Value: parts[1]})
			}
		}
	}

	jobs, err := s.svc.ListJobs(ctx, filter)
	if err != nil {
		return nil, err
	}

	apiJobs := make([]Job, len(jobs))
	for i := range jobs {
		apiJobs[i] = domainJobToAPI(jobs[i], "", "")
	}

	return ListJobs200JSONResponse{Jobs: apiJobs}, nil
}

func (s *ServerImpl) GetJob(ctx context.Context, request GetJobRequestObject) (GetJobResponseObject, error) {
	job, schedule, tags, executions, err := s.svc.GetJob(ctx, request.Id)
	if err != nil {
		he := mapDomainError(err)
		if he.Status == 404 {
			return GetJob404JSONResponse(newErrorResponse(he)), nil
		}
		return nil, err
	}

	apiExecs := domainExecutionsToAPI(executions)
	apiTags := domainTagsToAPI(tags)

	return GetJob200JSONResponse(JobDetail{
		Id:               job.ID,
		Namespace:        job.Namespace.String(),
		Name:             job.Name,
		Enabled:          job.Enabled,
		CronExpression:   schedule.CronExpression,
		Timezone:         schedule.Timezone,
		WebhookUrl:       job.Delivery.WebhookURL,
		Tags:             apiTags,
		RecentExecutions: &apiExecs,
		CreatedAt:        job.CreatedAt,
		UpdatedAt:        job.UpdatedAt,
	}), nil
}

func (s *ServerImpl) UpdateJob(ctx context.Context, request UpdateJobRequestObject) (UpdateJobResponseObject, error) {
	body := request.Body

	input := service.UpdateJobInput{
		Name:           body.Name,
		CronExpression: body.CronExpression,
		Timezone:       body.Timezone,
		WebhookURL:     body.WebhookUrl,
		Secret:         body.WebhookSecret,
	}

	if body.WebhookTimeoutSeconds != nil {
		d := time.Duration(*body.WebhookTimeoutSeconds) * time.Second
		input.Timeout = &d
	}

	if body.Tags != nil {
		tags := apiTagsToDomain(body.Tags)
		input.Tags = &tags
	}

	job, schedule, err := s.svc.UpdateJob(ctx, request.Id, input)
	if err != nil {
		he := mapDomainError(err)
		switch he.Status {
		case 404:
			return UpdateJob404JSONResponse(newErrorResponse(he)), nil
		case 422:
			return UpdateJob422JSONResponse(newErrorResponse(he)), nil
		}
		return nil, err
	}

	return UpdateJob200JSONResponse(JobDetail{
		Id:             job.ID,
		Namespace:      job.Namespace.String(),
		Name:           job.Name,
		Enabled:        job.Enabled,
		CronExpression: schedule.CronExpression,
		Timezone:       schedule.Timezone,
		WebhookUrl:     job.Delivery.WebhookURL,
		CreatedAt:      job.CreatedAt,
		UpdatedAt:      job.UpdatedAt,
	}), nil
}

func (s *ServerImpl) DeleteJob(ctx context.Context, request DeleteJobRequestObject) (DeleteJobResponseObject, error) {
	err := s.svc.DeleteJob(ctx, request.Id)
	if err != nil {
		he := mapDomainError(err)
		if he.Status == 404 {
			return DeleteJob404JSONResponse(newErrorResponse(he)), nil
		}
		return nil, err
	}
	return DeleteJob204Response{}, nil
}

func (s *ServerImpl) PauseJob(ctx context.Context, request PauseJobRequestObject) (PauseJobResponseObject, error) {
	job, err := s.svc.PauseJob(ctx, request.Id)
	if err != nil {
		he := mapDomainError(err)
		if he.Status == 404 {
			return PauseJob404JSONResponse(newErrorResponse(he)), nil
		}
		return nil, err
	}
	return PauseJob200JSONResponse(domainJobToAPI(job, "", "")), nil
}

func (s *ServerImpl) ResumeJob(ctx context.Context, request ResumeJobRequestObject) (ResumeJobResponseObject, error) {
	job, err := s.svc.ResumeJob(ctx, request.Id)
	if err != nil {
		he := mapDomainError(err)
		if he.Status == 404 {
			return ResumeJob404JSONResponse(newErrorResponse(he)), nil
		}
		return nil, err
	}
	return ResumeJob200JSONResponse(domainJobToAPI(job, "", "")), nil
}

func (s *ServerImpl) TriggerJob(ctx context.Context, request TriggerJobRequestObject) (TriggerJobResponseObject, error) {
	exec, err := s.svc.TriggerNow(ctx, request.Id)
	if err != nil {
		he := mapDomainError(err)
		switch he.Status {
		case 404:
			return TriggerJob404JSONResponse(newErrorResponse(he)), nil
		case 409:
			return TriggerJob409JSONResponse(newErrorResponse(he)), nil
		}
		return nil, err
	}
	return TriggerJob201JSONResponse(domainExecutionToAPI(exec)), nil
}

func (s *ServerImpl) GetNextRun(ctx context.Context, request GetNextRunRequestObject) (GetNextRunResponseObject, error) {
	nextRun, runs, schedule, err := s.svc.GetNextRunTime(ctx, request.Id)
	if err != nil {
		he := mapDomainError(err)
		if he.Status == 404 {
			return GetNextRun404JSONResponse(newErrorResponse(he)), nil
		}
		return nil, err
	}
	return GetNextRun200JSONResponse(NextRunResponse{
		CronExpression: schedule.CronExpression,
		Timezone:       schedule.Timezone,
		NextRunAt:      nextRun,
		NextRuns:       runs,
	}), nil
}

// ── Schedules ─────────────────────────────────────────────────────────────────

func (s *ServerImpl) ResolveSchedule(ctx context.Context, request ResolveScheduleRequestObject) (ResolveScheduleResponseObject, error) {
	body := request.Body

	tz := "UTC"
	if body.Timezone != nil {
		tz = *body.Timezone
	}

	result, err := s.svc.ResolveSchedule(ctx, body.Description, tz)
	if err != nil {
		he := mapDomainError(err)
		if he.Status == 422 {
			return ResolveSchedule422JSONResponse(newErrorResponse(he)), nil
		}
		return nil, err
	}

	return ResolveSchedule200JSONResponse(ResolveScheduleResponse{
		CronExpression: result.CronExpression,
		Description:    result.Description,
		Timezone:       result.Timezone,
		NextRuns:       result.NextRuns,
	}), nil
}

// ── Executions (Phase 2/3 — not yet implemented) ──────────────────────────────

func (s *ServerImpl) ListExecutions(ctx context.Context, request ListExecutionsRequestObject) (ListExecutionsResponseObject, error) {
	params := request.Params
	filter := domain.ExecutionFilter{
		JobID:      request.Id,
		ListParams: listParamsFromQuery(params.Limit, params.Offset),
	}

	if params.Status != nil {
		st := domain.ExecutionStatus(string(*params.Status))
		filter.Status = &st
	}
	if params.TriggerType != nil {
		tt := string(*params.TriggerType)
		filter.TriggerType = &tt
	}
	if params.Since != nil {
		filter.Since = params.Since
	}
	if params.Until != nil {
		filter.Until = params.Until
	}

	execs, err := s.svc.ListExecutions(ctx, filter)
	if err != nil {
		return nil, err
	}

	return ListExecutions200JSONResponse{Executions: domainExecutionsToAPI(execs)}, nil
}

func (s *ServerImpl) GetExecution(ctx context.Context, request GetExecutionRequestObject) (GetExecutionResponseObject, error) {
	exec, _, err := s.svc.GetExecution(ctx, request.Id)
	if err != nil {
		he := mapDomainError(err)
		if he.Status == 404 {
			return GetExecution404JSONResponse(newErrorResponse(he)), nil
		}
		return nil, err
	}
	return GetExecution200JSONResponse(domainExecutionToAPI(exec)), nil
}

func (s *ServerImpl) ListPendingAck(ctx context.Context, request ListPendingAckRequestObject) (ListPendingAckResponseObject, error) {
	params := request.Params
	limit := 0
	if params.Limit != nil {
		limit = *params.Limit
	}

	execs, err := s.svc.ListPendingAck(ctx, params.JobId, limit)
	if err != nil {
		return nil, err
	}

	return ListPendingAck200JSONResponse{Executions: domainExecutionsToAPI(execs)}, nil
}

func (s *ServerImpl) AckExecution(ctx context.Context, request AckExecutionRequestObject) (AckExecutionResponseObject, error) {
	if err := s.svc.AckExecution(ctx, request.Id); err != nil {
		he := mapDomainError(err)
		if he.Status == 404 {
			return AckExecution404JSONResponse(newErrorResponse(he)), nil
		}
		return nil, err
	}
	return AckExecution204Response{}, nil
}

// ── API Keys ──────────────────────────────────────────────────────────────────

func (s *ServerImpl) CreateAPIKey(ctx context.Context, request CreateAPIKeyRequestObject) (CreateAPIKeyResponseObject, error) {
	body := request.Body

	// The namespace for the new key comes from the request body.
	// Override the context namespace with the requested namespace so the
	// service layer sees the target namespace.
	nsCtx := domain.NamespaceToContext(ctx, domain.Namespace(body.Namespace))

	result, err := s.svc.CreateAPIKey(nsCtx, service.CreateAPIKeyInput{
		Label: body.Label,
	})
	if err != nil {
		return nil, err
	}

	return CreateAPIKey201JSONResponse(CreateAPIKeyResponse{
		Id:        result.Key.ID,
		Namespace: result.Key.Namespace.String(),
		Label:     result.Key.Label,
		Token:     result.PlaintextToken,
		CreatedAt: result.Key.CreatedAt,
	}), nil
}

func (s *ServerImpl) ListAPIKeys(ctx context.Context, request ListAPIKeysRequestObject) (ListAPIKeysResponseObject, error) {
	params := request.Params
	lp := listParamsFromQuery(params.Limit, params.Offset)

	keys, err := s.svc.ListAPIKeys(ctx, lp)
	if err != nil {
		return nil, err
	}

	apiKeys := make([]APIKeyInfo, len(keys))
	for i, k := range keys {
		apiKeys[i] = APIKeyInfo{
			Id:         k.ID,
			Namespace:  k.Namespace.String(),
			Label:      k.Label,
			Enabled:    k.Enabled,
			CreatedAt:  k.CreatedAt,
			LastUsedAt: k.LastUsedAt,
		}
	}

	return ListAPIKeys200JSONResponse{Keys: apiKeys}, nil
}

func (s *ServerImpl) DeleteAPIKey(ctx context.Context, request DeleteAPIKeyRequestObject) (DeleteAPIKeyResponseObject, error) {
	err := s.svc.DeleteAPIKey(ctx, request.Id)
	if err != nil {
		he := mapDomainError(err)
		if he.Status == 404 {
			return DeleteAPIKey404JSONResponse(newErrorResponse(he)), nil
		}
		return nil, err
	}
	return DeleteAPIKey204Response{}, nil
}

// ── Conversion helpers ────────────────────────────────────────────────────────

// apiTagsToDomain converts the API Tag map to domain Tag slice.
func apiTagsToDomain(t *Tag) []domain.Tag {
	if t == nil {
		return nil
	}
	var tags []domain.Tag
	for k, v := range *t {
		tags = append(tags, domain.Tag{Key: k, Value: v})
	}
	return tags
}

// domainTagsToAPI converts domain Tag slice to API Tag map pointer.
// Returns nil if the slice is empty.
func domainTagsToAPI(tags []domain.Tag) *Tag {
	if len(tags) == 0 {
		return nil
	}
	t := make(Tag, len(tags))
	for _, tag := range tags {
		t[tag.Key] = tag.Value
	}
	return &t
}

// domainJobToAPI converts a domain.Job to the generated Job type.
// cronExpr and tz are set when the caller has the schedule; otherwise left as "".
func domainJobToAPI(j domain.Job, cronExpr, tz string) Job {
	return Job{
		Id:             j.ID,
		Namespace:      j.Namespace.String(),
		Name:           j.Name,
		Enabled:        j.Enabled,
		CronExpression: cronExpr,
		Timezone:       tz,
		WebhookUrl:     j.Delivery.WebhookURL,
		CreatedAt:      j.CreatedAt,
		UpdatedAt:      j.UpdatedAt,
	}
}

// domainExecutionToAPI converts a domain.Execution to the generated Execution type.
func domainExecutionToAPI(e domain.Execution) Execution {
	return Execution{
		Id:             e.ID,
		JobId:          e.JobID,
		Status:         ExecutionStatus(e.Status),
		TriggerType:    ExecutionTriggerType(e.TriggerType),
		ScheduledAt:    e.ScheduledAt,
		FiredAt:        e.FiredAt,
		AcknowledgedAt: e.AcknowledgedAt,
		CreatedAt:      e.CreatedAt,
	}
}

// domainExecutionsToAPI converts a slice of domain.Execution to the generated slice.
func domainExecutionsToAPI(execs []domain.Execution) []Execution {
	result := make([]Execution, len(execs))
	for i := range execs {
		result[i] = domainExecutionToAPI(execs[i])
	}
	return result
}

// listParamsFromQuery converts optional limit/offset query params to domain.ListParams.
func listParamsFromQuery(limit *int, offset *int) domain.ListParams {
	lp := domain.ListParams{}
	if limit != nil {
		lp.Limit = *limit
	}
	if offset != nil {
		lp.Offset = *offset
	}
	return lp
}
