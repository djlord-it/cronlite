package webadmin

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/djlord-it/cronlite/internal/domain"
)

func TestSaveAppearanceSetsThemeCookie(t *testing.T) {
	tests := []struct {
		theme      string
		wantValue  string
		wantMaxAge int
	}{
		{"dark", "dark", themeCookieMaxAge},
		{"light", "light", themeCookieMaxAge},
		{"auto", "", -1},
	}
	for _, tt := range tests {
		t.Run(tt.theme, func(t *testing.T) {
			sessions := &fakeAdminSessionStore{}
			handler := newTestHandler(t, nil, sessions, nil)
			req := authenticatedRequest(http.MethodPost, "/admin/settings/appearance", url.Values{"csrf_token": {"csrf-token"}, "theme": {tt.theme}}, sessions)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/settings?notice=appearance" {
				t.Fatalf("response = %d %q, want 303 to settings", rec.Code, rec.Header().Get("Location"))
			}
			var cookie *http.Cookie
			for _, c := range rec.Result().Cookies() {
				if c.Name == themeCookieName {
					cookie = c
				}
			}
			if cookie == nil {
				t.Fatal("theme cookie not set")
			}
			if cookie.Value != tt.wantValue || cookie.MaxAge != tt.wantMaxAge || cookie.Path != "/admin" || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
				t.Fatalf("cookie = %+v", cookie)
			}
		})
	}
}

func TestSaveAppearanceRejectsBadInput(t *testing.T) {
	sessions := &fakeAdminSessionStore{}
	handler := newTestHandler(t, nil, sessions, nil)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, authenticatedRequest(http.MethodPost, "/admin/settings/appearance", url.Values{"csrf_token": {"csrf-token"}, "theme": {"neon"}}, sessions))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown theme status = %d, want 400", rec.Code)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, authenticatedRequest(http.MethodPost, "/admin/settings/appearance", url.Values{"csrf_token": {"wrong"}, "theme": {"dark"}}, sessions))
	if rec.Code != http.StatusForbidden || len(rec.Result().Cookies()) != 0 && rec.Result().Cookies()[0].Name == themeCookieName {
		t.Fatalf("bad CSRF status = %d, want 403 without a theme cookie", rec.Code)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/admin/settings/appearance", strings.NewReader("theme=dark")))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/login" {
		t.Fatalf("unauthenticated response = %d %q, want login redirect", rec.Code, rec.Header().Get("Location"))
	}
}

func TestPagesRenderSavedTheme(t *testing.T) {
	for _, tt := range []struct {
		cookie, want, pressed string
	}{
		{"dark", `<html lang="en" data-theme="dark">`, `value="dark" aria-pressed="true"`},
		{"light", `<html lang="en" data-theme="light">`, `value="light" aria-pressed="true"`},
		{"<script>", `<html lang="en">`, `value="auto" aria-pressed="true"`},
		{"", `<html lang="en">`, `value="auto" aria-pressed="true"`},
	} {
		t.Run(tt.cookie, func(t *testing.T) {
			sessions := &fakeAdminSessionStore{}
			handler := newTestHandler(t, nil, sessions, nil)
			req := authenticatedRequest(http.MethodGet, "/admin/settings", nil, sessions)
			if tt.cookie != "" {
				req.AddCookie(&http.Cookie{Name: themeCookieName, Value: url.QueryEscape(tt.cookie)})
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			body := rec.Body.String()
			if rec.Code != http.StatusOK || !strings.Contains(body, tt.want) || !strings.Contains(body, tt.pressed) {
				t.Fatalf("status %d; want %q and %q in settings page", rec.Code, tt.want, tt.pressed)
			}
		})
	}

	// Public pages follow the saved theme too.
	handler := newTestHandler(t, &fakeAdminService{hasKeys: true}, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/admin/login", nil)
	req.AddCookie(&http.Cookie{Name: themeCookieName, Value: "dark"})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `data-theme="dark"`) {
		t.Fatal("login page ignores the saved theme")
	}
}

func TestSettingsShowsAppearanceNotice(t *testing.T) {
	sessions := &fakeAdminSessionStore{}
	handler := newTestHandler(t, nil, sessions, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, authenticatedRequest(http.MethodGet, "/admin/settings?notice=appearance", nil, sessions))
	if !strings.Contains(rec.Body.String(), "Appearance saved.") {
		t.Fatal("settings page missing the appearance notice")
	}
}

func TestNextRunText(t *testing.T) {
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	next := nextRun(func() time.Time { return now })
	row := func(expr string, enabled bool) domain.JobWithSchedule {
		return domain.JobWithSchedule{Job: domain.Job{Enabled: enabled}, Schedule: domain.Schedule{CronExpression: expr, Timezone: "UTC"}}
	}
	tests := []struct {
		row       domain.JobWithSchedule
		text      string
		wantExact bool
	}{
		{row("*/15 * * * *", true), "in 15 min", true},
		{row("0 15 * * *", true), "in 3 h", true},
		{row("0 9 1 1 *", true), "Jan 1 09:00 UTC", true},
		{row("*/15 * * * *", false), "Paused", false},
		{row("not cron", true), "—", false},
	}
	for _, tt := range tests {
		got := next(tt.row)
		if got.Text != tt.text || (got.Exact != "") != tt.wantExact {
			t.Errorf("nextRun(%q, enabled=%v) = %+v, want %q", tt.row.Schedule.CronExpression, tt.row.Job.Enabled, got, tt.text)
		}
	}
}

func TestJobsTableRowsRevealDetailsAndActions(t *testing.T) {
	sessions := &fakeAdminSessionStore{}
	active := jobRow("active-job", "https://api.example.com/hook")
	paused := jobRow("paused-job", "https://other.example.com/hook")
	paused.Job.Enabled = false
	svc := &fakeAdminService{hasKeys: true, jobs: []domain.JobWithSchedule{active, paused}}
	handler := newTestHandler(t, svc, sessions, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, authenticatedRequest(http.MethodGet, "/admin/jobs", nil, sessions))
	body := rec.Body.String()

	if got := strings.Count(body, `<details name="job-row">`); got != 2 {
		t.Fatalf("expandable rows = %d, want 2", got)
	}
	for _, want := range []string{
		`action="/admin/jobs/` + active.Job.ID.String() + `/trigger"`,
		`action="/admin/jobs/` + active.Job.ID.String() + `/pause"`,
		`action="/admin/jobs/` + paused.Job.ID.String() + `/resume"`,
		"https://api.example.com/hook",
		"in 5 min",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("jobs page missing %q", want)
		}
	}
	if strings.Contains(body, `action="/admin/jobs/`+paused.Job.ID.String()+`/trigger"`) {
		t.Error("paused job must not offer Run now")
	}
	if strings.Count(body, `name="csrf_token" value="csrf-token"`) < 4 {
		t.Error("row actions must carry the CSRF token")
	}
}

func TestOnboardingMarksJobStep(t *testing.T) {
	for _, tt := range []struct {
		name  string
		total int
		want  string
	}{
		{"no jobs", 0, `href="/admin/jobs/new"`},
		{"has jobs", 3, "This namespace has 3 jobs."},
	} {
		t.Run(tt.name, func(t *testing.T) {
			sessions := &fakeAdminSessionStore{}
			svc := &fakeAdminService{hasKeys: true, fleet: &domain.Fleet{Total: tt.total}}
			handler := newTestHandler(t, svc, sessions, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, authenticatedRequest(http.MethodGet, "/admin/onboarding", nil, sessions))
			if !strings.Contains(rec.Body.String(), tt.want) {
				t.Fatalf("onboarding missing %q", tt.want)
			}
			if svc.listFilter.Namespace != "team" || svc.listFilter.Limit != 1 {
				t.Fatalf("fleet filter = %+v, want namespace team and limit 1", svc.listFilter)
			}
		})
	}
}

// The CSP has no style-src 'unsafe-inline', so inline style attributes would be
// dropped by the browser.
func TestTemplatesHaveNoInlineStyles(t *testing.T) {
	inline := regexp.MustCompile(`(?i)<[^>]+\sstyle\s*=`)
	err := fs.WalkDir(embeddedFiles, "templates", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(embeddedFiles, path)
		if err != nil {
			return err
		}
		if inline.Match(data) {
			t.Errorf("%s has an inline style attribute", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestStylesheetURLCarriesContentHash(t *testing.T) {
	handler := newTestHandler(t, &fakeAdminService{hasKeys: true}, nil, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/login", nil))
	link := regexp.MustCompile(`href="/admin/assets/admin\.css\?v=([0-9a-f]{12})"`).FindStringSubmatch(rec.Body.String())
	if link == nil {
		t.Fatal("stylesheet link has no content hash")
	}
	css, err := fs.ReadFile(embeddedFiles, "assets/admin.css")
	if err != nil {
		t.Fatal(err)
	}
	if sum := sha256.Sum256(css); link[1] != hex.EncodeToString(sum[:6]) {
		t.Fatalf("stylesheet version %s does not match the embedded CSS", link[1])
	}

	// The versioned URL still serves the stylesheet.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/assets/admin.css?v="+link[1], nil))
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/css") {
		t.Fatalf("versioned stylesheet = %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}
