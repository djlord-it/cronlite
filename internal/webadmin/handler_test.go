package webadmin

import (
	"bytes"
	"context"
	"io"
	"log"
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
		Service:        svc,
		Sessions:       sessions,
		Keys:           keys,
		BootstrapToken: "install-secret",
		SessionTTL:     12 * time.Hour,
		CookieSecure:   false,
		Now:            func() time.Time { return time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC) },
		Logger:         log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	return handler
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
	req.AddCookie(newSessionCookie(rawSession, time.Now().Add(12*time.Hour), false))
	sessions.session = domain.AdminSession{
		TokenHash: service.HashToken(rawSession),
		CSRFToken: "csrf-token",
		ExpiresAt: time.Date(2026, 7, 25, 0, 0, 0, 0, time.UTC),
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
