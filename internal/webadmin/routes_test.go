package webadmin

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/djlord-it/cronlite/internal/domain"
	"github.com/google/uuid"
)

func TestProtectedRouteRedirectsToLogin(t *testing.T) {
	handler := newTestHandler(t, nil, nil, nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/jobs", nil))

	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/login" {
		t.Fatalf("expected login redirect, got status=%d location=%q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestAdminRootAndUnknownPath(t *testing.T) {
	handler := newTestHandler(t, nil, nil, nil)
	for _, path := range []string{"/admin", "/admin/"} {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))

			if rec.Code != http.StatusTemporaryRedirect || rec.Header().Get("Location") != "/admin/jobs" {
				t.Fatalf("root response = %d %q, want 307 to /admin/jobs", rec.Code, rec.Header().Get("Location"))
			}
		})
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/unknown", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown path status = %d, want 404", rec.Code)
	}
}

func TestInvalidUUIDReturnsNotFound(t *testing.T) {
	tests := []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/admin/jobs/not-a-uuid"},
		{method: http.MethodGet, path: "/admin/jobs/not-a-uuid/edit"},
		{method: http.MethodPost, path: "/admin/jobs/not-a-uuid/edit"},
		{method: http.MethodGet, path: "/admin/jobs/not-a-uuid/delete"},
		{method: http.MethodPost, path: "/admin/jobs/not-a-uuid/delete"},
		{method: http.MethodPost, path: "/admin/jobs/not-a-uuid/pause"},
		{method: http.MethodPost, path: "/admin/jobs/not-a-uuid/resume"},
		{method: http.MethodPost, path: "/admin/jobs/not-a-uuid/trigger"},
		{method: http.MethodGet, path: "/admin/executions/not-a-uuid"},
	}

	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			sessions := &fakeAdminSessionStore{}
			svc := &fakeAdminService{hasKeys: true}
			handler := newTestHandler(t, svc, sessions, nil)
			var form url.Values
			if tt.method == http.MethodPost {
				form = url.Values{"csrf_token": {"csrf-token"}}
			}
			req := authenticatedRequest(tt.method, tt.path, form, sessions)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body.String())
			}
			if svc.actionID != uuid.Nil {
				t.Fatalf("invalid UUID reached service action: %s", svc.actionID)
			}
		})
	}
}

func FuzzPathUUID(f *testing.F) {
	validID := uuid.MustParse("123e4567-e89b-12d3-a456-426614174000")
	for _, seed := range []string{
		"",
		validID.String(),
		"not-a-uuid",
		"123e4567-e89b-12d3-a456-42661417400",
		"../../jobs",
		"環境",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		req := httptest.NewRequest(http.MethodGet, "/admin/jobs/"+url.PathEscape(raw), nil)
		req.SetPathValue("id", raw)
		rec := httptest.NewRecorder()

		got, ok := pathUUID(rec, req)
		want, err := uuid.Parse(raw)
		if err != nil {
			if ok {
				t.Fatalf("pathUUID(%q) succeeded with %s", raw, got)
			}
			if got != uuid.Nil {
				t.Fatalf("pathUUID(%q) = %s, want uuid.Nil", raw, got)
			}
			if rec.Code != http.StatusNotFound {
				t.Fatalf("pathUUID(%q) status = %d, want 404", raw, rec.Code)
			}
			return
		}

		if !ok {
			t.Fatalf("pathUUID(%q) rejected valid UUID %s", raw, want)
		}
		if got != want {
			t.Fatalf("pathUUID(%q) = %s, want %s", raw, got, want)
		}
	})
}

func TestEditPageNeverRendersStoredWebhookSecret(t *testing.T) {
	sessions := &fakeAdminSessionStore{}
	jobID := uuid.New()
	svc := &fakeAdminService{
		hasKeys: true,
		job: domain.Job{
			ID: jobID, Namespace: "team", Name: "private-job", Enabled: true,
			Delivery: domain.DeliveryConfig{
				WebhookURL: "https://example.com/hook",
				Secret:     "stored-super-secret",
				Timeout:    30 * time.Second,
			},
		},
		schedule: domain.Schedule{CronExpression: "0 * * * *", Timezone: "UTC"},
		tags:     []domain.Tag{{Key: "env", Value: "prod"}},
	}
	handler := newTestHandler(t, svc, sessions, nil)
	req := authenticatedRequest(http.MethodGet, "/admin/jobs/"+jobID.String()+"/edit", nil, sessions)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "stored-super-secret") {
		t.Fatal("stored webhook secret leaked into edit page")
	}
	for _, want := range []string{"private-job", "0 * * * *", "env=prod"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("expected edit page to contain %q", want)
		}
	}
}

func TestEditPageCanExplicitlyClearWebhookSecret(t *testing.T) {
	sessions := &fakeAdminSessionStore{}
	jobID := uuid.New()
	svc := &fakeAdminService{
		hasKeys: true,
		job:     domain.Job{ID: jobID, Namespace: "team", Name: "job"},
		schedule: domain.Schedule{
			CronExpression: "0 * * * *",
			Timezone:       "UTC",
		},
	}
	handler := newTestHandler(t, svc, sessions, nil)
	form := url.Values{
		"csrf_token":           {"csrf-token"},
		"name":                 {"job"},
		"cron_expression":      {"0 * * * *"},
		"timezone":             {"UTC"},
		"webhook_url":          {"https://example.com/hook"},
		"timeout_seconds":      {"30"},
		"clear_webhook_secret": {"true"},
	}
	req := authenticatedRequest(http.MethodPost, "/admin/jobs/"+jobID.String()+"/edit", form, sessions)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d: %s", rec.Code, rec.Body.String())
	}
	if svc.updatedInput.Secret == nil || *svc.updatedInput.Secret != "" {
		t.Fatalf("expected explicit secret clear, got %#v", svc.updatedInput.Secret)
	}
}

func TestJobActionsUseScopedService(t *testing.T) {
	jobID := uuid.New()
	for _, action := range []string{"pause", "resume", "trigger"} {
		t.Run(action, func(t *testing.T) {
			sessions := &fakeAdminSessionStore{}
			svc := &fakeAdminService{hasKeys: true}
			handler := newTestHandler(t, svc, sessions, nil)
			form := url.Values{"csrf_token": {"csrf-token"}}
			req := authenticatedRequest(http.MethodPost, "/admin/jobs/"+jobID.String()+"/"+action, form, sessions)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusSeeOther {
				t.Fatalf("expected 303, got %d: %s", rec.Code, rec.Body.String())
			}
			if svc.actionID != jobID {
				t.Fatalf("service received %s, want %s", svc.actionID, jobID)
			}
		})
	}
}

func TestDeleteUsesConfirmationThenPost(t *testing.T) {
	sessions := &fakeAdminSessionStore{}
	jobID := uuid.New()
	svc := &fakeAdminService{
		hasKeys: true,
		job:     domain.Job{ID: jobID, Namespace: "team", Name: "delete-me"},
		schedule: domain.Schedule{
			CronExpression: "0 * * * *",
			Timezone:       "UTC",
		},
	}
	handler := newTestHandler(t, svc, sessions, nil)

	getReq := authenticatedRequest(http.MethodGet, "/admin/jobs/"+jobID.String()+"/delete", nil, sessions)
	getRec := httptest.NewRecorder()
	handler.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK || !strings.Contains(getRec.Body.String(), "Delete delete-me?") {
		t.Fatalf("confirmation missing: status=%d body=%s", getRec.Code, getRec.Body.String())
	}

	form := url.Values{"csrf_token": {"csrf-token"}}
	postReq := authenticatedRequest(http.MethodPost, "/admin/jobs/"+jobID.String()+"/delete", form, sessions)
	postRec := httptest.NewRecorder()
	handler.ServeHTTP(postRec, postReq)
	if postRec.Code != http.StatusSeeOther || svc.actionID != jobID {
		t.Fatalf("delete failed: status=%d id=%s", postRec.Code, svc.actionID)
	}
}

func TestExecutionPageRendersDeliveryAttempts(t *testing.T) {
	sessions := &fakeAdminSessionStore{}
	executionID := uuid.New()
	jobID := uuid.New()
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	svc := &fakeAdminService{
		hasKeys: true,
		executions: []domain.Execution{{
			ID: executionID, JobID: jobID, Namespace: "team",
			Status: domain.ExecutionStatusFailed, TriggerType: domain.TriggerTypeManual,
			ScheduledAt: now, FiredAt: now, CreatedAt: now,
		}},
		attempts: []domain.DeliveryAttempt{{
			ID: uuid.New(), ExecutionID: executionID, Attempt: 1,
			StatusCode: 503, Error: "upstream unavailable", StartedAt: now, FinishedAt: now.Add(time.Second),
		}},
	}
	handler := newTestHandler(t, svc, sessions, nil)
	req := authenticatedRequest(http.MethodGet, "/admin/executions/"+executionID.String(), nil, sessions)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	for _, want := range []string{"failed", "manual", "503", "upstream unavailable"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("expected execution page to contain %q", want)
		}
	}
}

func TestLogoutDeletesServerSession(t *testing.T) {
	sessions := &fakeAdminSessionStore{}
	handler := newTestHandler(t, nil, sessions, nil)
	form := url.Values{"csrf_token": {"csrf-token"}}
	req := authenticatedRequest(http.MethodPost, "/admin/logout", form, sessions)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/login" {
		t.Fatalf("unexpected logout response: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if sessions.deletedHash == "" {
		t.Fatal("server-side session was not deleted")
	}
}

func TestBootstrapRejectsWrongInstallationToken(t *testing.T) {
	svc := &fakeAdminService{hasKeys: false}
	handler := newTestHandler(t, svc, nil, nil)

	getRec := httptest.NewRecorder()
	handler.ServeHTTP(getRec, httptest.NewRequest(http.MethodGet, "/admin/setup", nil))
	csrfCookie := getRec.Result().Cookies()[0]
	csrf := extractHiddenCSRF(t, getRec.Body.String())

	form := url.Values{
		"csrf_token":      {csrf},
		"bootstrap_token": {"wrong"},
		"namespace":       {"team"},
		"label":           {"owner"},
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/setup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(csrfCookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized || svc.bootstrapNS != "" {
		t.Fatalf("wrong token reached bootstrap: status=%d namespace=%q", rec.Code, svc.bootstrapNS)
	}
}

func TestBootstrapIsUniformlyUnavailableAfterFirstKeyExists(t *testing.T) {
	for _, token := range []string{"install-secret", "wrong"} {
		t.Run(token, func(t *testing.T) {
			svc := &fakeAdminService{hasKeys: true}
			handler := newTestHandler(t, svc, nil, nil)
			form := url.Values{
				"csrf_token":      {"public-csrf"},
				"bootstrap_token": {token},
				"namespace":       {"team"},
				"label":           {"owner"},
			}
			req := httptest.NewRequest(http.MethodPost, "/admin/setup", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.AddCookie(&http.Cookie{
				Name: publicCSRFCookieName, Value: "public-csrf", Path: "/admin",
			})
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusSeeOther {
				t.Fatalf("status = %d, want 303: %s", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Location"); got != "/admin/login" {
				t.Fatalf("Location = %q, want /admin/login", got)
			}
			if svc.bootstrapNS != "" || svc.bootstrapLabel != "" {
				t.Fatalf("unavailable bootstrap reached service: namespace=%q label=%q", svc.bootstrapNS, svc.bootstrapLabel)
			}
		})
	}
}
