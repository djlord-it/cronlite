package webadmin

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"html"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/djlord-it/cronlite/internal/domain"
	"github.com/google/uuid"
)

func TestLiveSearchScriptAndScopedPolicy(t *testing.T) {
	handler := newTestHandler(t, nil, nil, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/admin/assets/jobs.js", nil))
	sum := sha256.Sum256(rec.Body.Bytes())
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "text/javascript; charset=utf-8" || "sha256-"+base64.StdEncoding.EncodeToString(sum[:]) != jobsScriptIntegrity {
		t.Fatal("live script or integrity hash mismatch")
	}
	assertRequiredSecurityHeaders(t, rec.Header(), false)
	for _, path := range []string{"/admin/login", "/admin/runs", "/admin/jobs/new", "/admin/assets/jobs.js"} {
		if adminPolicy(path) != adminCSP {
			t.Fatalf("live script policy escaped Jobs: %s", path)
		}
	}
	policy := adminPolicy("/admin/jobs")
	if !strings.Contains(policy, "script-src '"+jobsScriptIntegrity+"'") || !strings.Contains(policy, "connect-src 'self'") || strings.Contains(policy, "unsafe-") || strings.Contains(policy, "script-src 'self'") {
		t.Fatal("script must be hash pinned with no arbitrary script permission")
	}
}

func TestFleetNumberedNavigation(t *testing.T) {
	for _, tc := range []struct{ matched, page, total int }{{0, 1, 1}, {25, 1, 1}, {26, 2, 2}, {10000, 200, 400}, {10000, 400, 400}} {
		r := httptest.NewRequest("GET", "/admin/jobs?name=Invoice&enabled=false&tag=team%3Dpayments&view=board", nil)
		total, links := fleetPageLinks(r, tc.page, tc.matched)
		if total != tc.total || len(links) > 7 || links[0].Number != 1 || links[len(links)-1].Number != tc.total {
			t.Fatalf("wrong links %#v", links)
		}
		current := 0
		for _, link := range links {
			url, _ := url.Parse(link.URL)
			if url.Query().Get("name") != "Invoice" || url.Query().Get("tag") != "team=payments" || url.Query().Get("enabled") != "false" || url.Query().Get("view") != "board" {
				t.Fatal("filters lost")
			}
			if link.Current {
				current++
				if link.Number != tc.page {
					t.Fatal("wrong current page")
				}
			}
		}
		if current != 1 {
			t.Fatal("missing current page")
		}
	}
}

func TestFleetRejectsMalformedSearchBeforeQuerying(t *testing.T) {
	for _, query := range []string{"name=%ZZ", "name=%ff", "name=%00", "tag=team%3D%ff", "tag=team%3D%00", "unused=" + strings.Repeat("a", 4096)} {
		sessions := &fakeAdminSessionStore{}
		svc := &fakeAdminService{}
		handler := newTestHandler(t, svc, sessions, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, authenticatedRequest("GET", "/admin/jobs?"+query, nil, sessions))
		if rec.Code != 400 || svc.listFilter.Namespace != "" {
			t.Fatalf("malformed query accepted or reached DB: %q %d", query, rec.Code)
		}
	}
}

func TestControlQueryValidationAndSafeNavigation(t *testing.T) {
	for _, q := range []string{"page=0", "page=-1", "page=10001", "page=9223372036854775807", "page=no", "page=1&page=2", "enabled=enabled", "enabled=true&enabled=false", "view=garbage", "tag=env", "tag==prod", "tag=env=", "name=" + strings.Repeat("a", 201), "tag=" + strings.Repeat("a", 201)} {
		values, _ := url.ParseQuery(q)
		if _, _, _, err := fleetQuery(values); err == nil {
			t.Errorf("accepted fleet query %q", q)
		}
	}
	q := url.Values{"name": {" report "}, "tag": {"env=prod"}, "enabled": {"false"}, "view": {"board"}, "page": {"10000"}}
	p, f, v, err := fleetQuery(q)
	if err != nil || p != 10000 || f.Offset != 249975 || f.Name != "report" || *f.Enabled || f.Tags[0].Value != "prod" || v != "board" {
		t.Fatalf("wrong validated filter: %v %#v %q %v", p, f, v, err)
	}
	for _, q := range []string{"status=waiting", "trigger_type=api", "window=30d", "view=timeline", "status=failed&status=delivered", "page=0"} {
		values, _ := url.ParseQuery(q)
		if _, _, _, _, err := runsQuery(values, time.Now()); err == nil {
			t.Errorf("accepted runs query %q", q)
		}
	}
	for _, raw := range []string{"https://evil.example/", "//evil.example/admin/jobs", "/admin/jobs#fragment", "/admin/login", "/admin/jobs?page=0", "/admin/runs?window=bad", "%", "/admin/jobs?name=" + strings.Repeat("a", 2501)} {
		if got := safeReturn(raw); got != "/admin/jobs" {
			t.Errorf("unsafe return %q produced %q", raw, got)
		}
	}
	if got := safeReturn("/admin/jobs?name=report&view=board&page=2&notice=created&untrusted=https://evil.example"); got != "/admin/jobs?name=report&page=2&view=board" {
		t.Fatal(got)
	}
	if got := safeReturn("/admin/runs?status=failed&trigger_type=manual&window=7d&view=table&page=2"); !strings.Contains(got, "status=failed") {
		t.Fatal(got)
	}
	if deleteRedirect("") != "/admin/jobs?notice=deleted" || deleteRedirect("//evil.example") != "/admin/jobs" {
		t.Fatal("unsafe delete redirect")
	}
}
func TestControlFleetViewsCountsTimelineAndBoundaries(t *testing.T) {
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	rows := []domain.JobWithSchedule{
		{Job: domain.Job{ID: uuid.New(), Name: "Active job", Enabled: true}, Schedule: domain.Schedule{CronExpression: "*/5 * * * *", Timezone: "UTC"}},
		{Job: domain.Job{ID: uuid.New(), Name: "Paused job"}, Schedule: domain.Schedule{CronExpression: "* * * * *", Timezone: "UTC"}},
		{Job: domain.Job{ID: uuid.New(), Name: "Invalid schedule", Enabled: true}, Schedule: domain.Schedule{CronExpression: "bad", Timezone: "UTC"}},
		{Job: domain.Job{ID: uuid.New(), Name: "Impossible", Enabled: true}, Schedule: domain.Schedule{CronExpression: "0 0 31 2 *", Timezone: "UTC"}},
		{Job: domain.Job{ID: uuid.New(), Name: "Next year", Enabled: true}, Schedule: domain.Schedule{CronExpression: "0 0 1 1 *", Timezone: "UTC"}},
	}
	for _, view := range []string{"table", "board", "timeline"} {
		t.Run(view, func(t *testing.T) {
			sessions := &fakeAdminSessionStore{}
			svc := &fakeAdminService{fleet: &domain.Fleet{Jobs: rows, Total: 120, Active: 100, Paused: 20, Matched: 5}, views: []domain.SavedView{{Name: "Payments", Query: "tag=team%3Dpayments"}}}
			handler := newTestHandler(t, svc, sessions, nil)
			rec := httptest.NewRecorder()
			req := authenticatedRequest("GET", "/admin/jobs?view="+view+"&tag=team=payments", nil, sessions)
			handler.ServeHTTP(rec, req)
			if rec.Code != 200 {
				t.Fatalf("%d: %s", rec.Code, rec.Body.String())
			}
			body := html.UnescapeString(rec.Body.String())
			for _, text := range []string{"120", "100", "20", "Payments", "team=payments", "Job Control Center"} {
				if !strings.Contains(body, text) {
					t.Fatalf("missing %q", text)
				}
			}
			if view == "timeline" {
				if svc.listFilter.Limit != 501 || !strings.Contains(body, now.Add(5*time.Minute).Format("15:04:05")) || strings.Contains(body, ">Paused job</a>") {
					t.Fatal("timeline incorrectly bounded or calculated")
				}
			}
			if strings.Count(body, `<script src="/admin/assets/jobs.js" integrity="`+jobsScriptIntegrity+`" defer></script>`) != 1 {
				t.Fatal("unexpected live search script")
			}
			if strings.Contains(body, `class="board-column status-`) || strings.Contains(body, `<div class="status-`) {
				t.Fatal("status colors escaped the Jobs table")
			}
		})
	}
	sessions := &fakeAdminSessionStore{}
	svc := &fakeAdminService{fleet: &domain.Fleet{Matched: 26}}
	handler := newTestHandler(t, svc, sessions, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, authenticatedRequest("GET", "/admin/jobs?page=3&view=board&name=x", nil, sessions))
	if rec.Code != 303 || rec.Header().Get("Location") != "/admin/jobs?name=x&page=2&view=board" {
		t.Fatalf("boundary redirect %d %s", rec.Code, rec.Header().Get("Location"))
	}
	svc.fleet.Matched = 0
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, authenticatedRequest("GET", "/admin/jobs?page=2", nil, sessions))
	if rec.Code != 303 || !strings.Contains(rec.Header().Get("Location"), "page=1") {
		t.Fatal("empty boundary")
	}
	svc.controlErr = errors.New("database secret")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, authenticatedRequest("GET", "/admin/jobs", nil, sessions))
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "database secret") {
		t.Fatal("unsanitized error")
	}
}
func TestTimelineDSTAndLimits(t *testing.T) {
	now := time.Date(2026, 11, 1, 5, 45, 0, 0, time.UTC)
	h := &Handler{sessions: &sessionManager{now: func() time.Time { return now }}}
	row := domain.JobWithSchedule{Job: domain.Job{ID: uuid.New(), Enabled: true}, Schedule: domain.Schedule{CronExpression: "30 1 * * *", Timezone: "America/Toronto"}}
	data := pageData{Jobs: []domain.JobWithSchedule{row}}
	h.fillTimeline(&data)
	if len(data.Timeline) != 1 || !data.Timeline[0].Next.Equal(time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC)) {
		t.Fatalf("DST next run = %#v", data.Timeline)
	}
	data = pageData{Jobs: make([]domain.JobWithSchedule, 501)}
	for i := range data.Jobs {
		data.Jobs[i] = row
		data.Jobs[i].Job.ID = uuid.New()
	}
	h.fillTimeline(&data)
	if len(data.Timeline) != 50 || !data.TimelineLimited {
		t.Fatal("unbounded timeline")
	}
	// Equal times are ordered by stable job ID, independent of map iteration.
	for i := 1; i < len(data.Timeline); i++ {
		if data.Timeline[i-1].Job.ID.String() > data.Timeline[i].Job.ID.String() {
			t.Fatal("unstable timeline")
		}
	}
}
func TestRunsBoardUsesPersistedStatusAndEvidence(t *testing.T) {
	sessions := &fakeAdminSessionStore{}
	svc := &fakeAdminService{}
	for _, status := range []domain.ExecutionStatus{domain.ExecutionStatusEmitted, domain.ExecutionStatusInProgress, domain.ExecutionStatusDelivered, domain.ExecutionStatusFailed} {
		svc.runs.Runs = append(svc.runs.Runs, domain.RunEvidence{Execution: domain.Execution{ID: uuid.New(), Status: status, TriggerType: domain.TriggerTypeManual}, JobName: "Payments", Attempts: 2, LastStatusCode: 503, LastError: "upstream <script>"})
	}
	svc.runs.Total = 4
	handler := newTestHandler(t, svc, sessions, nil)
	for _, view := range []string{"board", "table"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, authenticatedRequest("GET", "/admin/runs?window=7d&view="+view+"&status=failed&trigger_type=manual", nil, sessions))
		body := rec.Body.String()
		if rec.Code != 200 || strings.Contains(body, "upstream <script>") || !strings.Contains(body, "4 matching executions") {
			t.Fatalf("runs %d %s", rec.Code, body)
		}
		if view == "table" && !strings.Contains(body, "Latest HTTP 503") {
			t.Fatal("table lost delivery evidence")
		}
		if view == "board" {
			if strings.Contains(body, "delivery attempts") || strings.Contains(body, "<dl>") || strings.Contains(body, "Execution details →") {
				t.Fatal("board cards still show execution details")
			}
			for _, run := range svc.runs.Runs {
				if !strings.Contains(html.UnescapeString(body), executionURL(run.Execution.ID, "/admin/runs?window=7d&view=board&status=failed&trigger_type=manual")) {
					t.Fatal("card title lost execution link or navigation state")
				}
			}
		}
		if svc.executionFilter.Until.Sub(*svc.executionFilter.Since) != 7*24*time.Hour {
			t.Fatal("unbounded runs")
		}
	}
	svc.runs.Total = 0
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, authenticatedRequest("GET", "/admin/runs?page=2", nil, sessions))
	if rec.Code != 303 {
		t.Fatal("runs boundary")
	}
	svc.controlErr = errors.New("secret")
	for _, path := range []string{"/admin/runs", "/admin/bulk", "/admin/bulk/" + uuid.NewString()} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, authenticatedRequest("GET", path, nil, sessions))
		if rec.Code != 500 || strings.Contains(rec.Body.String(), "secret") {
			t.Fatal("unsafe runs error")
		}
	}
}
func TestBulkPreviewConfirmationResultsAndViews(t *testing.T) {
	id, jobID := uuid.New(), uuid.New()
	sessions := &fakeAdminSessionStore{}
	svc := &fakeAdminService{batch: domain.BulkBatch{ID: id, Action: "pause", ExpiresAt: time.Now().Add(time.Minute), Items: []domain.BulkItem{{ID: jobID, Name: "Payroll", Enabled: true}}}}
	handler := newTestHandler(t, svc, sessions, nil)
	send := func(method, path string, form url.Values) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, authenticatedRequest(method, path, form, sessions))
		return rec
	}
	form := url.Values{"csrf_token": {"csrf-token"}, "action": {"pause"}, "job_id": {jobID.String()}, "return": {"/admin/jobs?name=Payroll&view=board&page=2"}}
	rec := send("POST", "/admin/bulk/preview", form)
	if rec.Code != 303 || svc.bulkAction != "pause" || len(svc.selectedIDs) != 1 || svc.confirmed {
		t.Fatal("preview mutated or not forwarded")
	}
	rec = send("GET", rec.Header().Get("Location"), nil)
	body := html.UnescapeString(rec.Body.String())
	if rec.Code != 200 || !strings.Contains(body, "no changes made") || !strings.Contains(body, "required") || !strings.Contains(body, "Payroll") {
		t.Fatalf("preview %d %s", rec.Code, body)
	}
	confirmPath := "/admin/bulk/" + id.String() + "/confirm"
	rec = send("POST", confirmPath, url.Values{"csrf_token": {"csrf-token"}})
	if rec.Code != 400 || svc.confirmed {
		t.Fatal("implicit confirmation accepted")
	}
	rec = send("POST", confirmPath, url.Values{"csrf_token": {"csrf-token"}, "confirm": {"yes"}})
	if rec.Code != 303 || !svc.confirmed {
		t.Fatal("confirmation not forwarded")
	}
	now := time.Now()
	svc.batch.ConfirmedAt = &now
	svc.batch.Items = []domain.BulkItem{{Result: "applied"}, {Result: "unchanged"}, {Result: "conflict"}, {Result: "unavailable"}, {Result: "failed"}}
	rec = send("GET", "/admin/bulk/"+id.String(), nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "1 applied · 1 unchanged · 3 not applied") || strings.Contains(rec.Body.String(), "Confirm pause</button>") {
		t.Fatal("incorrect persisted results")
	}
	svc.batches = []domain.BulkBatch{svc.batch}
	rec = send("GET", "/admin/bulk", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), id.String()) {
		t.Fatal("history missing")
	}
	rec = send("POST", "/admin/views", url.Values{"csrf_token": {"csrf-token"}, "view_name": {"Payroll"}, "query": {"name=payroll"}, "return": {"//evil.example"}})
	if rec.Code != 303 || rec.Header().Get("Location") != "/admin/jobs" || svc.savedName != "Payroll" {
		t.Fatal("saved view forwarding or unsafe redirect")
	}
	rec = send("POST", "/admin/views/"+id.String()+"/delete", url.Values{"csrf_token": {"csrf-token"}})
	if rec.Code != 303 || svc.actionID != id {
		t.Fatal("delete view")
	}
	for _, err := range []error{domain.ErrBulkNotFound, domain.ErrJobNotFound, domain.ErrAPIKeyNotFound, domain.ErrSavedViewLimit, domain.ErrInvalidControlInput} {
		svc.controlErr = err
		rec = send("POST", "/admin/bulk/preview", form)
		if rec.Code != 400 && rec.Code != 404 && rec.Code != 409 {
			t.Fatalf("wrong error code %d for %v", rec.Code, err)
		}
	}
}
func TestNewControlRoutesRequireAuthCSRFAndRejectMalformedInputs(t *testing.T) {
	sessions := &fakeAdminSessionStore{}
	svc := &fakeAdminService{}
	handler := newTestHandler(t, svc, sessions, nil)
	id := uuid.NewString()
	for _, path := range []string{"/admin/jobs", "/admin/runs", "/admin/bulk", "/admin/bulk/" + id} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != 303 {
			t.Errorf("unauthenticated %s: %d", path, rec.Code)
		}
	}
	for _, path := range []string{"/admin/views", "/admin/views/" + id + "/delete", "/admin/bulk/preview", "/admin/bulk/" + id + "/confirm"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, authenticatedRequest("POST", path, url.Values{}, sessions))
		if rec.Code != 403 {
			t.Errorf("CSRF %s: %d", path, rec.Code)
		}
	}
	for _, ids := range [][]string{nil, {"malformed"}, make([]string, 101)} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, authenticatedRequest("POST", "/admin/bulk/preview", url.Values{"csrf_token": {"csrf-token"}, "job_id": ids}, sessions))
		if rec.Code != 400 {
			t.Errorf("selection accepted: %d", rec.Code)
		}
	}
	for _, path := range []string{"/admin/jobs?enabled=unknown", "/admin/runs?status=unknown", "/admin/bulk/not-uuid", "/admin/views/not-uuid/delete", "/admin/bulk/not-uuid/confirm"} {
		method := "GET"
		form := url.Values(nil)
		if strings.HasSuffix(path, "delete") || strings.HasSuffix(path, "confirm") {
			method = "POST"
			form = url.Values{"csrf_token": {"csrf-token"}}
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, authenticatedRequest(method, path, form, sessions))
		if rec.Code != 400 && rec.Code != 404 {
			t.Errorf("invalid input %s accepted %d", path, rec.Code)
		}
	}
}

func TestJobDetailReportsUncalculableScheduleAndValidatesExecutionFilters(t *testing.T) {
	sessions := &fakeAdminSessionStore{}
	id := uuid.New()
	svc := &fakeAdminService{job: domain.Job{ID: id, Name: "Impossible", Enabled: true}, nextRunErr: domain.ErrInvalidCronExpression}
	handler := newTestHandler(t, svc, sessions, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, authenticatedRequest("GET", "/admin/jobs/"+id.String(), nil, sessions))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "no calculable next run") || strings.Contains(rec.Body.String(), "while paused") {
		t.Fatalf("schedule evidence: %d %s", rec.Code, rec.Body.String())
	}
	for _, q := range []string{"page=9223372036854775807", "status=waiting", "trigger_type=invalid"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, authenticatedRequest("GET", "/admin/jobs/"+id.String()+"?"+q, nil, sessions))
		if rec.Code != 400 {
			t.Fatalf("unvalidated detail query %q: %d", q, rec.Code)
		}
	}
}
