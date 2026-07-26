package webadmin

import (
	"bytes"
	"context"
	"errors"
	"html"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/djlord-it/cronlite/internal/domain"
	"github.com/djlord-it/cronlite/internal/service"
	"github.com/google/uuid"
)

type fakeAdminService struct {
	hasKeys         bool
	bootstrapResult service.CreateAPIKeyResult
	bootstrapErr    error
	bootstrapNS     string
	bootstrapLabel  string
	jobs            []domain.JobWithSchedule
	listFilter      domain.JobFilter
	createdInput    service.CreateJobInput
	createdJob      domain.Job
	createdSchedule domain.Schedule
	job             domain.Job
	schedule        domain.Schedule
	tags            []domain.Tag
	executions      []domain.Execution
	executionFilter domain.ExecutionFilter
	attempts        []domain.DeliveryAttempt
	updatedInput    service.UpdateJobInput
	actionID        uuid.UUID
	nextRuns        []time.Time
	err             error
}

func (f *fakeAdminService) HasAnyAPIKeys(context.Context) (bool, error) {
	return f.hasKeys, f.err
}
func (f *fakeAdminService) BootstrapFirstAPIKey(_ context.Context, namespace, label string) (service.CreateAPIKeyResult, error) {
	f.bootstrapNS, f.bootstrapLabel = namespace, label
	return f.bootstrapResult, f.bootstrapErr
}
func (f *fakeAdminService) ListJobsWithSchedules(_ context.Context, filter domain.JobFilter) ([]domain.JobWithSchedule, error) {
	f.listFilter = filter
	return f.jobs, f.err
}
func (f *fakeAdminService) CreateJob(_ context.Context, input service.CreateJobInput) (domain.Job, domain.Schedule, error) {
	f.createdInput = input
	return f.createdJob, f.createdSchedule, f.err
}
func (f *fakeAdminService) GetJob(context.Context, uuid.UUID) (domain.Job, domain.Schedule, []domain.Tag, []domain.Execution, error) {
	return f.job, f.schedule, f.tags, f.executions, f.err
}
func (f *fakeAdminService) UpdateJob(_ context.Context, id uuid.UUID, input service.UpdateJobInput) (domain.Job, domain.Schedule, error) {
	f.actionID, f.updatedInput = id, input
	return f.job, f.schedule, f.err
}
func (f *fakeAdminService) DeleteJob(_ context.Context, id uuid.UUID) error {
	f.actionID = id
	return f.err
}
func (f *fakeAdminService) PauseJob(_ context.Context, id uuid.UUID) (domain.Job, error) {
	f.actionID = id
	return f.job, f.err
}
func (f *fakeAdminService) ResumeJob(_ context.Context, id uuid.UUID) (domain.Job, error) {
	f.actionID = id
	return f.job, f.err
}
func (f *fakeAdminService) TriggerNow(_ context.Context, id uuid.UUID) (domain.Execution, error) {
	f.actionID = id
	return domain.Execution{}, f.err
}
func (f *fakeAdminService) GetNextRunTime(context.Context, uuid.UUID) (time.Time, []time.Time, domain.Schedule, error) {
	var next time.Time
	if len(f.nextRuns) > 0 {
		next = f.nextRuns[0]
	}
	return next, f.nextRuns, f.schedule, f.err
}
func (f *fakeAdminService) ListExecutions(_ context.Context, filter domain.ExecutionFilter) ([]domain.Execution, error) {
	f.executionFilter = filter
	return f.executions, f.err
}
func (f *fakeAdminService) GetExecution(context.Context, uuid.UUID) (domain.Execution, []domain.DeliveryAttempt, error) {
	var execution domain.Execution
	if len(f.executions) > 0 {
		execution = f.executions[0]
	}
	return execution, f.attempts, f.err
}

func newTestHandler(t *testing.T, svc *fakeAdminService, sessions *fakeAdminSessionStore, keys *fakeKeyLookup) http.Handler {
	t.Helper()
	return newTestHandlerWithOptions(t, svc, sessions, keys, false, log.New(io.Discard, "", 0))
}

func newTestHandlerWithOptions(
	t *testing.T,
	svc *fakeAdminService,
	sessions *fakeAdminSessionStore,
	keys *fakeKeyLookup,
	cookieSecure bool,
	logger *log.Logger,
) http.Handler {
	t.Helper()
	if svc == nil {
		svc = &fakeAdminService{hasKeys: true}
	}
	if sessions == nil {
		sessions = &fakeAdminSessionStore{}
	}
	if keys == nil {
		keys = &fakeKeyLookup{}
	}
	handler, err := NewHandler(Config{
		Service:            svc,
		Sessions:           sessions,
		Keys:               keys,
		BootstrapToken:     "install-secret",
		SessionTTL:         12 * time.Hour,
		SessionAbsoluteTTL: 24 * time.Hour,
		CookieSecure:       cookieSecure,
		Now:                func() time.Time { return time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC) },
		Logger:             logger,
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	return handler
}

func TestNewHandlerValidatesSessionAbsoluteTTL(t *testing.T) {
	for _, tt := range []struct {
		name        string
		absoluteTTL time.Duration
	}{
		{name: "zero", absoluteTTL: 0},
		{name: "less than idle TTL", absoluteTTL: 30 * time.Minute},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewHandler(Config{
				Service:            &fakeAdminService{},
				Sessions:           &fakeAdminSessionStore{},
				Keys:               &fakeKeyLookup{},
				SessionTTL:         time.Hour,
				SessionAbsoluteTTL: tt.absoluteTTL,
			})
			if err == nil {
				t.Fatal("expected invalid absolute session TTL to be rejected")
			}
		})
	}
}

func authenticatedRequest(method, target string, form url.Values, sessions *fakeAdminSessionStore) *http.Request {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, target, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	rawSession := "browser-session"
	now := time.Now()
	req.AddCookie(newSessionCookie(rawSession, now, now.Add(12*time.Hour), false))
	sessions.session = domain.AdminSession{
		TokenHash:         service.HashToken(rawSession),
		CSRFToken:         "csrf-token",
		ExpiresAt:         time.Date(2026, 7, 25, 0, 0, 0, 0, time.UTC),
		AbsoluteExpiresAt: time.Date(2026, 7, 26, 0, 0, 0, 0, time.UTC),
	}
	sessions.key = domain.APIKey{ID: uuid.New(), Namespace: "team", Enabled: true}
	return req
}

func TestHandlerAddsSecurityHeadersAndServesEmbeddedCSS(t *testing.T) {
	handler := newTestHandler(t, nil, nil, nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/assets/admin.css", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/css") {
		t.Fatalf("unexpected content type: %q", rec.Header().Get("Content-Type"))
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=86400" {
		t.Fatalf("embedded CSS cache policy = %q, want public, max-age=86400", got)
	}
	if rec.Header().Get("Content-Security-Policy") == "" ||
		rec.Header().Get("X-Content-Type-Options") != "nosniff" ||
		!strings.Contains(rec.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatalf("missing security headers: %#v", rec.Header())
	}
	if strings.Contains(strings.ToLower(rec.Body.String()), "<script") {
		t.Fatal("admin assets must not include JavaScript")
	}
}

func TestLoginDoesNotEchoInvalidAPIKey(t *testing.T) {
	svc := &fakeAdminService{hasKeys: true}
	keys := &fakeKeyLookup{err: domain.ErrAPIKeyNotFound}
	handler := newTestHandler(t, svc, nil, keys)

	getRec := httptest.NewRecorder()
	handler.ServeHTTP(getRec, httptest.NewRequest(http.MethodGet, "/admin/login", nil))
	csrfCookie := getRec.Result().Cookies()[0]
	csrf := extractHiddenCSRF(t, getRec.Body.String())

	form := url.Values{"csrf_token": {csrf}, "api_key": {"ec_do-not-echo"}}
	req := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(csrfCookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "ec_do-not-echo") {
		t.Fatal("invalid API key was echoed into HTML")
	}
}

func TestBootstrapCreatesFirstKeyAndDisplaysItOnce(t *testing.T) {
	generated := "ec_generated-once"
	keyID := uuid.New()
	svc := &fakeAdminService{
		hasKeys: false,
		bootstrapResult: service.CreateAPIKeyResult{
			PlaintextToken: generated,
			Key:            domain.APIKey{ID: keyID, Namespace: "first-team", Enabled: true},
		},
	}
	keys := &fakeKeyLookup{key: svc.bootstrapResult.Key}
	handler := newTestHandler(t, svc, nil, keys)

	getRec := httptest.NewRecorder()
	handler.ServeHTTP(getRec, httptest.NewRequest(http.MethodGet, "/admin/setup", nil))
	csrfCookie := getRec.Result().Cookies()[0]
	csrf := extractHiddenCSRF(t, getRec.Body.String())

	form := url.Values{
		"csrf_token":      {csrf},
		"bootstrap_token": {"install-secret"},
		"namespace":       {"first-team"},
		"label":           {"owner"},
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/setup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(csrfCookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("authenticated HTML must not be cached: %q", rec.Header().Get("Cache-Control"))
	}
	if !strings.Contains(rec.Body.String(), generated) || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("one-time key response is incomplete: headers=%v body=%s", rec.Header(), rec.Body.String())
	}
	if svc.bootstrapNS != "first-team" || svc.bootstrapLabel != "owner" {
		t.Fatalf("unexpected bootstrap input: %q %q", svc.bootstrapNS, svc.bootstrapLabel)
	}
}

func TestJobsListUsesAuthenticatedNamespaceAndSchedule(t *testing.T) {
	sessions := &fakeAdminSessionStore{}
	jobID := uuid.New()
	svc := &fakeAdminService{
		hasKeys: true,
		jobs: []domain.JobWithSchedule{{
			Job:      domain.Job{ID: jobID, Namespace: "team", Name: "daily-report", Enabled: true},
			Schedule: domain.Schedule{CronExpression: "0 9 * * *", Timezone: "America/Toronto"},
		}},
	}
	handler := newTestHandler(t, svc, sessions, nil)
	req := authenticatedRequest(http.MethodGet, "/admin/jobs?name=daily&enabled=true", nil, sessions)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("authenticated HTML must not be cached: %q", rec.Header().Get("Cache-Control"))
	}
	for _, want := range []string{"daily-report", "0 9 * * *", "America/Toronto", "team"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("expected body to contain %q: %s", want, rec.Body.String())
		}
	}
	if !strings.Contains(rec.Body.String(), `value="daily"`) || !strings.Contains(rec.Body.String(), `value="true" selected`) {
		t.Fatalf("filters were not preserved: %s", rec.Body.String())
	}
	if svc.listFilter.Namespace != "team" || svc.listFilter.Name != "daily" || svc.listFilter.Enabled == nil || !*svc.listFilter.Enabled {
		t.Fatalf("namespace/filter not applied: %#v", svc.listFilter)
	}
}

func TestCreateAndDetailPagesRender(t *testing.T) {
	sessions := &fakeAdminSessionStore{}
	jobID := uuid.New()
	executionID := uuid.New()
	nextRun := time.Date(2026, 7, 25, 9, 0, 0, 0, time.UTC)
	svc := &fakeAdminService{
		hasKeys: true,
		job: domain.Job{
			ID: jobID, Namespace: "team", Name: "nightly-report", Enabled: true,
			Delivery: domain.DeliveryConfig{WebhookURL: "https://hooks.example.test/report", Timeout: 30 * time.Second},
		},
		schedule: domain.Schedule{CronExpression: "0 9 * * *", Timezone: "America/Toronto"},
		tags:     []domain.Tag{{Key: "env", Value: "prod"}},
		executions: []domain.Execution{{
			ID: executionID, JobID: jobID, Namespace: "team",
			Status: domain.ExecutionStatusDelivered, TriggerType: domain.TriggerTypeScheduled,
			ScheduledAt: nextRun.Add(-24 * time.Hour), FiredAt: nextRun.Add(-24 * time.Hour),
		}},
		nextRuns: []time.Time{nextRun},
	}
	handler := newTestHandler(t, svc, sessions, nil)

	createRec := httptest.NewRecorder()
	handler.ServeHTTP(
		createRec,
		authenticatedRequest(http.MethodGet, "/admin/jobs/new", nil, sessions),
	)
	if createRec.Code != http.StatusOK {
		t.Fatalf("create page status = %d, want 200: %s", createRec.Code, createRec.Body.String())
	}
	for _, want := range []string{"Create job", `value="UTC"`, `value="30"`} {
		if !strings.Contains(createRec.Body.String(), want) {
			t.Fatalf("create page missing %q: %s", want, createRec.Body.String())
		}
	}

	detailRec := httptest.NewRecorder()
	handler.ServeHTTP(
		detailRec,
		authenticatedRequest(http.MethodGet, "/admin/jobs/"+jobID.String()+"?notice=created", nil, sessions),
	)
	if detailRec.Code != http.StatusOK {
		t.Fatalf("detail page status = %d, want 200: %s", detailRec.Code, detailRec.Body.String())
	}
	for _, want := range []string{
		"nightly-report", "0 9 * * *", "America/Toronto", "env", "prod",
		"delivered", "scheduled", "Job created.", "2026-07-25 09:00:00 UTC",
	} {
		if !strings.Contains(detailRec.Body.String(), want) {
			t.Fatalf("detail page missing %q: %s", want, detailRec.Body.String())
		}
	}
}

func TestServiceErrorsMapToSafeStatuses(t *testing.T) {
	internalErr := errors.New("database failed with do-not-expose")
	tests := []struct {
		name       string
		err        error
		build      func(*fakeAdminSessionStore, uuid.UUID) *http.Request
		wantStatus int
		wantText   string
	}{
		{
			name: "missing job is not found",
			err:  domain.ErrJobNotFound,
			build: func(sessions *fakeAdminSessionStore, id uuid.UUID) *http.Request {
				return authenticatedRequest(http.MethodGet, "/admin/jobs/"+id.String(), nil, sessions)
			},
			wantStatus: http.StatusNotFound,
			wantText:   "does not exist",
		},
		{
			name: "disabled job action is a conflict",
			err:  domain.ErrJobDisabled,
			build: func(sessions *fakeAdminSessionStore, id uuid.UUID) *http.Request {
				return authenticatedRequest(
					http.MethodPost,
					"/admin/jobs/"+id.String()+"/trigger",
					url.Values{"csrf_token": {"csrf-token"}},
					sessions,
				)
			},
			wantStatus: http.StatusConflict,
			wantText:   "Resume this job",
		},
		{
			name: "repository failure is internal",
			err:  internalErr,
			build: func(sessions *fakeAdminSessionStore, id uuid.UUID) *http.Request {
				return authenticatedRequest(http.MethodGet, "/admin/executions/"+id.String(), nil, sessions)
			},
			wantStatus: http.StatusInternalServerError,
			wantText:   "Something went wrong",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sessions := &fakeAdminSessionStore{}
			svc := &fakeAdminService{hasKeys: true, err: tt.err}
			handler := newTestHandler(t, svc, sessions, nil)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, tt.build(sessions, uuid.New()))

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tt.wantText) {
				t.Fatalf("response missing safe text %q: %s", tt.wantText, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), internalErr.Error()) {
				t.Fatalf("internal service error leaked: %s", rec.Body.String())
			}
		})
	}
}

func TestPaginationPreservesFilters(t *testing.T) {
	sessions := &fakeAdminSessionStore{}
	jobID := uuid.New()
	jobs := make([]domain.JobWithSchedule, 26)
	executions := make([]domain.Execution, 26)
	for i := range jobs {
		jobs[i].Job = domain.Job{ID: uuid.New(), Namespace: "team", Name: "nightly"}
		executions[i] = domain.Execution{
			ID: uuid.New(), JobID: jobID, Namespace: "team",
			Status: domain.ExecutionStatusFailed, TriggerType: domain.TriggerTypeManual,
		}
	}
	svc := &fakeAdminService{
		hasKeys:    true,
		jobs:       jobs,
		job:        domain.Job{ID: jobID, Namespace: "team", Name: "nightly"},
		schedule:   domain.Schedule{CronExpression: "0 * * * *", Timezone: "UTC"},
		executions: executions,
	}
	handler := newTestHandler(t, svc, sessions, nil)

	jobsRec := httptest.NewRecorder()
	handler.ServeHTTP(
		jobsRec,
		authenticatedRequest(
			http.MethodGet,
			"/admin/jobs?name=nightly&enabled=false&page=2",
			nil,
			sessions,
		),
	)
	jobsBody := html.UnescapeString(jobsRec.Body.String())
	for _, want := range []string{
		"/admin/jobs?enabled=false&name=nightly&page=1",
		"/admin/jobs?enabled=false&name=nightly&page=3",
	} {
		if !strings.Contains(jobsBody, want) {
			t.Fatalf("jobs pagination missing %q: %s", want, jobsBody)
		}
	}
	if svc.listFilter.ListParams.Offset != 25 || svc.listFilter.ListParams.Limit != 26 ||
		svc.listFilter.Enabled == nil || *svc.listFilter.Enabled {
		t.Fatalf("jobs filter/pagination not forwarded: %#v", svc.listFilter)
	}

	detailRec := httptest.NewRecorder()
	handler.ServeHTTP(
		detailRec,
		authenticatedRequest(
			http.MethodGet,
			"/admin/jobs/"+jobID.String()+"?status=failed&trigger_type=manual&page=2",
			nil,
			sessions,
		),
	)
	detailBody := html.UnescapeString(detailRec.Body.String())
	for _, want := range []string{
		"/admin/jobs/" + jobID.String() + "?page=1&status=failed&trigger_type=manual",
		"/admin/jobs/" + jobID.String() + "?page=3&status=failed&trigger_type=manual",
	} {
		if !strings.Contains(detailBody, want) {
			t.Fatalf("detail pagination missing %q: %s", want, detailBody)
		}
	}
	if svc.executionFilter.ListParams.Offset != 25 || svc.executionFilter.ListParams.Limit != 26 ||
		svc.executionFilter.Status == nil || *svc.executionFilter.Status != domain.ExecutionStatusFailed ||
		svc.executionFilter.TriggerType == nil || *svc.executionFilter.TriggerType != "manual" {
		t.Fatalf("execution filter/pagination not forwarded: %#v", svc.executionFilter)
	}
}

func TestLoginStoreFailureReturnsSafe500(t *testing.T) {
	const sentinel = "session store failed with do-not-expose"
	sessions := &fakeAdminSessionStore{createErr: errors.New(sentinel)}
	keys := &fakeKeyLookup{key: domain.APIKey{ID: uuid.New(), Namespace: "team", Enabled: true}}
	handler := newTestHandler(t, &fakeAdminService{hasKeys: true}, sessions, keys)

	getRec := httptest.NewRecorder()
	handler.ServeHTTP(getRec, httptest.NewRequest(http.MethodGet, "/admin/login", nil))
	csrfCookie := getRec.Result().Cookies()[0]
	form := url.Values{
		"api_key":    {"ec_valid"},
		"csrf_token": {extractHiddenCSRF(t, getRec.Body.String())},
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(csrfCookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), sentinel) {
		t.Fatalf("session store failure leaked: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Something went wrong") {
		t.Fatalf("safe error page missing: %s", rec.Body.String())
	}
}

func TestBootstrapStillDisplaysOneTimeKeyWhenAutomaticLoginFails(t *testing.T) {
	const (
		generated = "ec_generated-once"
		sentinel  = "session store failed with do-not-expose"
	)
	key := domain.APIKey{ID: uuid.New(), Namespace: "team", Enabled: true}
	svc := &fakeAdminService{
		hasKeys: false,
		bootstrapResult: service.CreateAPIKeyResult{
			PlaintextToken: generated,
			Key:            key,
		},
	}
	sessions := &fakeAdminSessionStore{createErr: errors.New(sentinel)}
	var logs bytes.Buffer
	handler := newTestHandlerWithOptions(
		t,
		svc,
		sessions,
		&fakeKeyLookup{key: key},
		false,
		log.New(&logs, "", 0),
	)

	getRec := httptest.NewRecorder()
	handler.ServeHTTP(getRec, httptest.NewRequest(http.MethodGet, "/admin/setup", nil))
	csrfCookie := getRec.Result().Cookies()[0]
	form := url.Values{
		"csrf_token":      {extractHiddenCSRF(t, getRec.Body.String())},
		"bootstrap_token": {"install-secret"},
		"namespace":       {"team"},
		"label":           {"owner"},
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/setup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(csrfCookie)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), generated) {
		t.Fatalf("one-time API key was lost: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Sign in") {
		t.Fatalf("recoverable sign-in guidance missing: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), sentinel) {
		t.Fatalf("session failure leaked: %s", rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if !strings.Contains(logs.String(), "bootstrap_automatic_login_failed") {
		t.Fatalf("sanitized operational event missing from log: %s", logs.String())
	}
	if strings.Contains(logs.String(), sentinel) || strings.Contains(logs.String(), generated) {
		t.Fatalf("bootstrap key or session failure leaked to log: %s", logs.String())
	}
}

func TestAdminRejectsOversizedFormBodies(t *testing.T) {
	const oversizedValueSize = (1 << 20) + 1
	oversized := strings.Repeat("x", oversizedValueSize)
	for _, tt := range []struct {
		name string
		path string
		body url.Values
	}{
		{
			name: "setup",
			path: "/admin/setup",
			body: url.Values{"csrf_token": {"csrf"}, "bootstrap_token": {oversized}},
		},
		{
			name: "login",
			path: "/admin/login",
			body: url.Values{"csrf_token": {"csrf"}, "api_key": {oversized}},
		},
		{
			name: "job create",
			path: "/admin/jobs/new",
			body: url.Values{"csrf_token": {"csrf-token"}, "name": {oversized}},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			handler := newTestHandler(t, &fakeAdminService{hasKeys: true}, nil, nil)
			req := httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(tt.body.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("status = %d, want 413: %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(strings.ToLower(rec.Body.String()), "request body too large") {
				t.Fatalf("controlled 413 message missing: %s", rec.Body.String())
			}
			assertRequiredSecurityHeaders(t, rec.Header(), false)
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Fatalf("Cache-Control = %q, want no-store", got)
			}
		})
	}
}

func TestAdminRejectsOversizedMultipartFormBody(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormField("api_key")
	if err != nil {
		t.Fatalf("create multipart field: %v", err)
	}
	if _, err := io.Copy(part, strings.NewReader(strings.Repeat("x", (1<<20)+1))); err != nil {
		t.Fatalf("write multipart field: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	handler := newTestHandler(t, &fakeAdminService{hasKeys: true}, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/admin/login", bytes.NewReader(body.Bytes()))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.ContentLength = -1
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413: %s", rec.Code, rec.Body.String())
	}
}

func TestCreateJobRequiresCSRFAndRedirectsAfterSuccess(t *testing.T) {
	sessions := &fakeAdminSessionStore{}
	jobID := uuid.New()
	svc := &fakeAdminService{
		hasKeys:         true,
		createdJob:      domain.Job{ID: jobID, Namespace: "team"},
		createdSchedule: domain.Schedule{CronExpression: "0 9 * * *", Timezone: "UTC"},
	}
	handler := newTestHandler(t, svc, sessions, nil)
	form := url.Values{
		"csrf_token":      {"csrf-token"},
		"name":            {"daily"},
		"cron_expression": {"0 9 * * *"},
		"timezone":        {"UTC"},
		"webhook_url":     {"https://example.com/hook"},
		"timeout_seconds": {"30"},
	}
	req := authenticatedRequest(http.MethodPost, "/admin/jobs/new", form, sessions)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Location") != "/admin/jobs/"+jobID.String()+"?notice=created" {
		t.Fatalf("unexpected redirect: %q", rec.Header().Get("Location"))
	}
	if svc.createdInput.Name != "daily" {
		t.Fatalf("create input not forwarded: %#v", svc.createdInput)
	}

	badForm := form
	badForm.Set("csrf_token", "wrong")
	badReq := authenticatedRequest(http.MethodPost, "/admin/jobs/new", badForm, sessions)
	badRec := httptest.NewRecorder()
	handler.ServeHTTP(badRec, badReq)
	if badRec.Code != http.StatusForbidden {
		t.Fatalf("expected CSRF rejection, got %d", badRec.Code)
	}
}

func TestRenderedPagesContainNoScriptTags(t *testing.T) {
	handler := newTestHandler(t, nil, nil, nil)
	for _, path := range []string{"/admin/login", "/admin/setup"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if bytes.Contains(bytes.ToLower(rec.Body.Bytes()), []byte("<script")) {
			t.Fatalf("%s contains JavaScript", path)
		}
	}
}

func TestSuccessfulLogoutClearsCachedSiteData(t *testing.T) {
	sessions := &fakeAdminSessionStore{}
	handler := newTestHandler(t, nil, sessions, nil)
	req := authenticatedRequest(
		http.MethodPost,
		"/admin/logout",
		url.Values{"csrf_token": {"csrf-token"}},
		sessions,
	)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", rec.Code)
	}
	if got := rec.Header().Get("Clear-Site-Data"); got != `"cache"` {
		t.Fatalf("Clear-Site-Data = %q, want %q", got, `"cache"`)
	}
}

func extractHiddenCSRF(t *testing.T, body string) string {
	t.Helper()
	const prefix = `name="csrf_token" value="`
	start := strings.Index(body, prefix)
	if start < 0 {
		t.Fatalf("CSRF input missing: %s", body)
	}
	start += len(prefix)
	end := strings.Index(body[start:], `"`)
	if end < 0 {
		t.Fatalf("CSRF value malformed: %s", body)
	}
	return body[start : start+end]
}
