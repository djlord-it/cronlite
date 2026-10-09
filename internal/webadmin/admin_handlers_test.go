package webadmin

import (
	"errors"
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

func TestWorkspaceRoutesRequireAuthenticationAndCSRF(t *testing.T) {
	id := uuid.New().String()
	for _, path := range []string{"/admin/keys", "/admin/keys/" + id + "/delete", "/admin/onboarding", "/admin/settings", "/admin/schedule", "/admin/executions"} {
		t.Run("GET "+path, func(t *testing.T) {
			h := newTestHandler(t, nil, nil, nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
			if rec.Code != 303 || rec.Header().Get("Location") != "/admin/login" {
				t.Fatalf("unauthenticated access: %d", rec.Code)
			}
		})
	}
	for _, path := range []string{"/admin/keys", "/admin/keys/" + id + "/delete", "/admin/schedule", "/admin/executions/" + id + "/ack"} {
		t.Run("POST "+path, func(t *testing.T) {
			sessions := &fakeAdminSessionStore{}
			svc := &fakeAdminService{}
			h := newTestHandler(t, svc, sessions, nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, authenticatedRequest("POST", path, url.Values{"csrf_token": {"wrong"}}, sessions))
			if rec.Code != 403 {
				t.Fatalf("missing CSRF protection: %d", rec.Code)
			}
			if svc.bootstrapNS != "" || svc.actionID != uuid.Nil {
				t.Fatal("rejected request reached service")
			}
		})
	}
}

func TestKeysListPaginationAndSecretIsolation(t *testing.T) {
	sessions := &fakeAdminSessionStore{}
	keys := make([]domain.APIKey, 26)
	for i := range keys {
		keys[i] = domain.APIKey{ID: uuid.New(), Namespace: "team", Label: "worker", TokenHash: "do-not-render", Enabled: true}
	}
	svc := &fakeAdminService{apiKeys: keys}
	h := newTestHandler(t, svc, sessions, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authenticatedRequest("GET", "/admin/keys?page=2", nil, sessions))
	if rec.Code != 200 || svc.bootstrapNS != "team" || svc.keyListParams.Offset != 25 {
		t.Fatal("key list is not namespace scoped or paginated")
	}
	if n := strings.Count(rec.Body.String(), "Revoke key"); n != 25 {
		t.Fatalf("rows: %d", n)
	}
	if strings.Contains(rec.Body.String(), "do-not-render") || !strings.Contains(rec.Body.String(), "page=3") {
		t.Fatal("hash leaked or pagination missing")
	}
}

func TestKeyCreationValidationAndFailure(t *testing.T) {
	for _, tc := range []struct {
		label string
		err   error
		want  int
	}{{"", nil, 422}, {strings.Repeat("a", 129), nil, 422}, {"worker", errors.New("database failed"), 500}, {" worker ", nil, 200}} {
		t.Run(tc.label, func(t *testing.T) {
			sessions := &fakeAdminSessionStore{}
			svc := &fakeAdminService{err: tc.err, bootstrapResult: service.CreateAPIKeyResult{PlaintextToken: "ec_once", Key: domain.APIKey{Label: "worker"}}}
			h := newTestHandler(t, svc, sessions, nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, authenticatedRequest("POST", "/admin/keys", url.Values{"csrf_token": {"csrf-token"}, "label": {tc.label}}, sessions))
			if rec.Code != tc.want {
				t.Fatalf("got %d want %d", rec.Code, tc.want)
			}
			if tc.want == 200 && (svc.bootstrapNS != "team" || svc.bootstrapLabel != "worker" || !strings.Contains(rec.Body.String(), "ec_once")) {
				t.Fatal("creation lost scope, label or one-time key")
			}
			if rec.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("key response cacheable")
			}
		})
	}
}

func TestKeyRevocationConfirmationAndGuard(t *testing.T) {
	key := domain.APIKey{ID: uuid.New(), Label: "worker"}
	for _, tc := range []struct {
		path   string
		method string
		keys   []domain.APIKey
		err    error
		want   int
	}{{"/admin/keys/" + key.ID.String() + "/delete", "GET", []domain.APIKey{key}, nil, 200}, {"/admin/keys/" + key.ID.String() + "/delete", "GET", nil, nil, 404}, {"/admin/keys/invalid/delete", "GET", nil, nil, 404}, {"/admin/keys/" + key.ID.String() + "/delete", "GET", nil, errors.New("db"), 500}, {"/admin/keys/" + key.ID.String() + "/delete", "POST", nil, nil, 303}, {"/admin/keys/" + key.ID.String() + "/delete", "POST", nil, domain.ErrAPIKeyNotFound, 404}, {"/admin/keys/invalid/delete", "POST", nil, nil, 404}} {
		t.Run(tc.method+tc.path+http.StatusText(tc.want), func(t *testing.T) {
			sessions := &fakeAdminSessionStore{}
			svc := &fakeAdminService{apiKeys: tc.keys, err: tc.err}
			h := newTestHandler(t, svc, sessions, nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, authenticatedRequest(tc.method, tc.path, url.Values{"csrf_token": {"csrf-token"}}, sessions))
			if rec.Code != tc.want {
				t.Fatalf("got %d want %d", rec.Code, tc.want)
			}
		})
	}
	sessions := &fakeAdminSessionStore{}
	svc := &fakeAdminService{}
	h := newTestHandler(t, svc, sessions, nil)
	req := authenticatedRequest("POST", "/admin/keys/"+key.ID.String()+"/delete", url.Values{"csrf_token": {"csrf-token"}}, sessions)
	sessions.key.ID = key.ID
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 409 || svc.actionID != uuid.Nil {
		t.Fatal("current key could be revoked")
	}
}

func TestWorkspacePagesAndScheduleValidation(t *testing.T) {
	for _, path := range []string{"/admin/onboarding", "/admin/settings", "/admin/schedule"} {
		sessions := &fakeAdminSessionStore{}
		h := newTestHandler(t, nil, sessions, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, authenticatedRequest("GET", path, nil, sessions))
		if rec.Code != 200 {
			t.Fatalf("%s: %d", path, rec.Code)
		}
		if path == "/admin/onboarding" && !strings.Contains(rec.Body.String(), "Skip onboarding") {
			t.Fatal("onboarding cannot be skipped")
		}
	}
	for _, tc := range []struct {
		err  error
		want int
	}{{nil, 200}, {domain.ErrScheduleParseFailure, 422}, {domain.ErrInvalidTimezone, 422}, {errors.New("db"), 500}} {
		sessions := &fakeAdminSessionStore{}
		svc := &fakeAdminService{err: tc.err, resolved: service.ResolveResult{CronExpression: "*/15 * * * *", Timezone: "UTC", NextRuns: []time.Time{time.Now()}}}
		h := newTestHandler(t, svc, sessions, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, authenticatedRequest("POST", "/admin/schedule", url.Values{"csrf_token": {"csrf-token"}, "description": {"every 15 minutes"}, "timezone": {"UTC"}}, sessions))
		if rec.Code != tc.want {
			t.Fatalf("schedule: %d want %d", rec.Code, tc.want)
		}
	}
}

func TestExecutionFiltersPaginationAndAcknowledgement(t *testing.T) {
	for _, query := range []string{"?status=delivered&trigger_type=manual&page=2", "?status=pending", "?status=failed", "?status=emitted", "?status=in_progress&trigger_type=scheduled"} {
		sessions := &fakeAdminSessionStore{}
		svc := &fakeAdminService{executions: make([]domain.Execution, 26)}
		h := newTestHandler(t, svc, sessions, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, authenticatedRequest("GET", "/admin/executions"+query, nil, sessions))
		if rec.Code != 200 {
			t.Fatalf("execution list %d", rec.Code)
		}
		if strings.Contains(query, "page=2") && (svc.executionFilter.Offset != 25 || svc.executionFilter.Status == nil || *svc.executionFilter.Status != domain.ExecutionStatusDelivered || svc.executionFilter.TriggerType == nil || *svc.executionFilter.TriggerType != "manual") {
			t.Fatal("filters lost")
		}
	}
	for _, path := range []string{"/admin/executions", "/admin/executions?status=pending"} {
		sessions := &fakeAdminSessionStore{}
		svc := &fakeAdminService{err: errors.New("db")}
		h := newTestHandler(t, svc, sessions, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, authenticatedRequest("GET", path, nil, sessions))
		if rec.Code != 500 {
			t.Fatal("list error ignored")
		}
	}
	for _, tc := range []struct {
		id   string
		err  error
		want int
	}{{uuid.New().String(), nil, 303}, {uuid.New().String(), domain.ErrExecutionNotFound, 404}, {"invalid", nil, 404}} {
		sessions := &fakeAdminSessionStore{}
		svc := &fakeAdminService{err: tc.err}
		h := newTestHandler(t, svc, sessions, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, authenticatedRequest("POST", "/admin/executions/"+tc.id+"/ack", url.Values{"csrf_token": {"csrf-token"}}, sessions))
		if rec.Code != tc.want {
			t.Fatalf("ack: %d", rec.Code)
		}
		if tc.want == 303 && svc.bootstrapNS != "team" {
			t.Fatal("ack not scoped")
		}
	}
}

func TestBundledAdminAssets(t *testing.T) {
	for path, contentType := range map[string]string{"/admin/assets/jetbrains-mono.woff2": "font/woff2", "/admin/assets/favicon.svg": "image/svg+xml"} {
		h := newTestHandler(t, nil, nil, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != 200 || rec.Header().Get("Content-Type") != contentType || rec.Body.Len() == 0 {
			t.Fatalf("bundled asset unavailable: %s", path)
		}
	}
}
