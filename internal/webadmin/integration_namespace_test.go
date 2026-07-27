//go:build integration

package webadmin

import (
	"database/sql"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/djlord-it/cronlite/internal/domain"
	"github.com/google/uuid"
)

func integrationWebhookURL(t *testing.T) string {
	t.Helper()
	webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(webhook.Close)

	parsed, err := url.Parse(webhook.URL)
	if err != nil {
		t.Fatalf("parse local webhook URL: %v", err)
	}
	_, port, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		t.Fatalf("read local webhook port: %v", err)
	}
	// nip.io resolves to loopback, so this remains a local httptest endpoint,
	// while exercising the same hostname-based URL validation as production.
	parsed.Host = net.JoinHostPort("127.0.0.1.nip.io", port)
	return parsed.String()
}

func createIntegrationJob(
	t *testing.T,
	client *http.Client,
	serverURL string,
	name string,
	webhookURL string,
) uuid.UUID {
	t.Helper()
	response, _ := integrationPostForm(t, client, serverURL+"/admin/jobs/new", url.Values{
		"csrf_token":      {authenticatedCSRF(t, client, serverURL)},
		"name":            {name},
		"cron_expression": {"*/5 * * * *"},
		"timezone":        {"UTC"},
		"webhook_url":     {webhookURL},
		"timeout_seconds": {"10"},
		"tags":            {"env=integration"},
	})
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("create job status = %d, want 303", response.StatusCode)
	}
	location := response.Header.Get("Location")
	idText := strings.TrimPrefix(strings.SplitN(location, "?", 2)[0], "/admin/jobs/")
	id, err := uuid.Parse(idText)
	if err != nil {
		t.Fatalf("create job returned invalid UUID location %q", location)
	}
	return id
}

func postIntegrationJobAction(
	t *testing.T,
	client *http.Client,
	serverURL string,
	jobID uuid.UUID,
	action string,
) *http.Response {
	t.Helper()
	response, _ := integrationPostForm(
		t,
		client,
		serverURL+"/admin/jobs/"+jobID.String()+"/"+action,
		url.Values{"csrf_token": {authenticatedCSRF(t, client, serverURL)}},
	)
	return response
}

func latestExecutionID(t *testing.T, db *sql.DB, jobID uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	integrationScanRow(t, db, `
SELECT id FROM executions WHERE job_id = $1 ORDER BY created_at DESC LIMIT 1
`, []any{jobID}, &id)
	return id
}

func insertIntegrationExecution(
	t *testing.T,
	rt *integrationRuntime,
	execution domain.Execution,
) {
	t.Helper()
	ctx, cancel := integrationContext(t)
	defer cancel()
	if err := rt.store.InsertExecution(ctx, execution); err != nil {
		t.Fatalf("insert integration execution: %v", err)
	}
}

func insertIntegrationAttempt(
	t *testing.T,
	rt *integrationRuntime,
	attempt domain.DeliveryAttempt,
) {
	t.Helper()
	ctx, cancel := integrationContext(t)
	defer cancel()
	if err := rt.store.InsertDeliveryAttempt(ctx, attempt); err != nil {
		t.Fatalf("insert integration delivery attempt: %v", err)
	}
}

func TestIntegrationNamespaceIsolationAndJobCRUD(t *testing.T) {
	rt := newIntegrationRuntime(t)
	keyA := mustBootstrap(t, rt, "namespace-a")
	keyB := mustCreateKey(t, rt, "namespace-b")
	server := newIntegrationServer(t, rt)
	t.Cleanup(server.Close)
	clientA := newIntegrationClient(t)
	clientB := newIntegrationClient(t)
	loginIntegrationClient(t, clientA, server.URL, keyA.PlaintextToken)
	loginIntegrationClient(t, clientB, server.URL, keyB.PlaintextToken)

	if got := countRows(t, rt.db, `SELECT COUNT(*) FROM admin_sessions`); got != 2 {
		t.Fatalf("namespace session count = %d, want 2", got)
	}

	webhookURL := integrationWebhookURL(t)
	jobA := createIntegrationJob(t, clientA, server.URL, "namespace-a-job", webhookURL)
	jobB := createIntegrationJob(t, clientB, server.URL, "namespace-b-job", webhookURL)

	response, body := integrationGet(t, clientA, server.URL+"/admin/jobs")
	if response.StatusCode != http.StatusOK ||
		!strings.Contains(body, "namespace-a-job") ||
		strings.Contains(body, "namespace-b-job") {
		t.Fatal("namespace A job list was not isolated")
	}
	response, body = integrationGet(t, clientB, server.URL+"/admin/jobs")
	if response.StatusCode != http.StatusOK ||
		!strings.Contains(body, "namespace-b-job") ||
		strings.Contains(body, "namespace-a-job") {
		t.Fatal("namespace B job list was not isolated")
	}

	namespaceAContext, namespaceACancel := integrationContext(t)
	namespaceAContext = domain.NamespaceToContext(namespaceAContext, keyA.Key.Namespace)
	serviceJobsA, err := rt.service.ListJobsWithSchedules(namespaceAContext, domain.JobFilter{
		Namespace:  keyB.Key.Namespace,
		ListParams: domain.ListParams{Limit: 100},
	})
	namespaceACancel()
	if err != nil {
		t.Fatalf("list namespace A jobs with mismatched filter namespace: %v", err)
	}
	if len(serviceJobsA) != 1 ||
		serviceJobsA[0].Job.ID != jobA ||
		serviceJobsA[0].Job.ID == jobB {
		t.Fatal("service did not overwrite the caller-supplied job namespace")
	}

	response, body = integrationGet(t, clientA, server.URL+"/admin/jobs/"+jobA.String())
	if response.StatusCode != http.StatusOK || !strings.Contains(body, "namespace-a-job") {
		t.Fatal("namespace A could not view its job detail")
	}
	response, _ = integrationGet(t, clientB, server.URL+"/admin/jobs/"+jobA.String())
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("namespace B viewing A job status = %d, want 404", response.StatusCode)
	}

	response, _ = integrationPostForm(
		t,
		clientB,
		server.URL+"/admin/jobs/"+jobA.String()+"/edit",
		url.Values{
			"csrf_token":      {authenticatedCSRF(t, clientB, server.URL)},
			"name":            {"cross-namespace-update"},
			"cron_expression": {"0 * * * *"},
			"timezone":        {"UTC"},
			"webhook_url":     {webhookURL},
			"timeout_seconds": {"10"},
		},
	)
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("namespace B updating A job status = %d, want 404", response.StatusCode)
	}

	response, _ = integrationPostForm(
		t,
		clientA,
		server.URL+"/admin/jobs/"+jobA.String()+"/edit",
		url.Values{
			"csrf_token":      {authenticatedCSRF(t, clientA, server.URL)},
			"name":            {"namespace-a-renamed"},
			"cron_expression": {"0 * * * *"},
			"timezone":        {"UTC"},
			"webhook_url":     {webhookURL},
			"timeout_seconds": {"15"},
			"tags":            {"env=integration\nowner=a"},
		},
	)
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("namespace A update status = %d, want 303", response.StatusCode)
	}
	response, body = integrationGet(t, clientA, server.URL+"/admin/jobs/"+jobA.String())
	if response.StatusCode != http.StatusOK ||
		!strings.Contains(body, "namespace-a-renamed") ||
		!strings.Contains(body, "owner=a") {
		t.Fatal("namespace A update was not visible in job detail")
	}

	response = postIntegrationJobAction(t, clientA, server.URL, jobA, "pause")
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("pause status = %d, want 303", response.StatusCode)
	}
	response, body = integrationGet(t, clientA, server.URL+"/admin/jobs/"+jobA.String())
	if response.StatusCode != http.StatusOK || !strings.Contains(body, "Paused") {
		t.Fatal("paused job detail did not show paused state")
	}
	response = postIntegrationJobAction(t, clientA, server.URL, jobA, "resume")
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("resume status = %d, want 303", response.StatusCode)
	}
	response = postIntegrationJobAction(t, clientA, server.URL, jobA, "trigger")
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("trigger status = %d, want 303", response.StatusCode)
	}
	response = postIntegrationJobAction(t, clientB, server.URL, jobB, "trigger")
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("namespace B trigger status = %d, want 303", response.StatusCode)
	}

	executionA := latestExecutionID(t, rt.db, jobA)
	executionB := latestExecutionID(t, rt.db, jobB)
	now := time.Now().UTC()
	nonmatchingStatusExecutionA := domain.Execution{
		ID:          uuid.New(),
		JobID:       jobA,
		Namespace:   keyA.Key.Namespace,
		TriggerType: domain.TriggerTypeManual,
		ScheduledAt: now.Add(-time.Hour),
		FiredAt:     now.Add(-time.Hour),
		Status:      domain.ExecutionStatusDelivered,
		CreatedAt:   now.Add(-time.Hour),
	}
	insertIntegrationExecution(t, rt, nonmatchingStatusExecutionA)
	nonmatchingTriggerExecutionA := domain.Execution{
		ID:          uuid.New(),
		JobID:       jobA,
		Namespace:   keyA.Key.Namespace,
		TriggerType: domain.TriggerTypeScheduled,
		ScheduledAt: now.Add(-2 * time.Hour),
		FiredAt:     now.Add(-2 * time.Hour),
		Status:      domain.ExecutionStatusEmitted,
		CreatedAt:   now.Add(-2 * time.Hour),
	}
	insertIntegrationExecution(t, rt, nonmatchingTriggerExecutionA)

	attemptAID := uuid.New()
	insertIntegrationAttempt(t, rt, domain.DeliveryAttempt{
		ID:          attemptAID,
		ExecutionID: executionA,
		Attempt:     1,
		StatusCode:  http.StatusServiceUnavailable,
		Error:       "namespace-a-attempt",
		StartedAt:   now,
		FinishedAt:  now.Add(time.Second),
	})
	attemptBID := uuid.New()
	insertIntegrationAttempt(t, rt, domain.DeliveryAttempt{
		ID:          attemptBID,
		ExecutionID: executionB,
		Attempt:     1,
		StatusCode:  http.StatusBadGateway,
		Error:       "namespace-b-attempt",
		StartedAt:   now,
		FinishedAt:  now.Add(time.Second),
	})

	emittedStatus := domain.ExecutionStatusEmitted
	manualTrigger := string(domain.TriggerTypeManual)
	namespaceAContext, namespaceACancel = integrationContext(t)
	namespaceAContext = domain.NamespaceToContext(namespaceAContext, keyA.Key.Namespace)
	serviceExecutionsA, err := rt.service.ListExecutions(namespaceAContext, domain.ExecutionFilter{
		Namespace:   keyB.Key.Namespace,
		Status:      &emittedStatus,
		TriggerType: &manualTrigger,
		ListParams:  domain.ListParams{Limit: 100},
	})
	namespaceACancel()
	if err != nil {
		t.Fatalf("list namespace A executions with mismatched filter namespace: %v", err)
	}
	if len(serviceExecutionsA) != 1 ||
		serviceExecutionsA[0].ID != executionA ||
		serviceExecutionsA[0].ID == executionB ||
		serviceExecutionsA[0].ID == nonmatchingStatusExecutionA.ID ||
		serviceExecutionsA[0].ID == nonmatchingTriggerExecutionA.ID {
		t.Fatal("service did not enforce context namespace and execution filters")
	}

	response, body = integrationGet(
		t,
		clientA,
		server.URL+"/admin/jobs/"+jobA.String()+"?status=emitted&trigger_type=manual",
	)
	if response.StatusCode != http.StatusOK ||
		!strings.Contains(body, executionA.String()) ||
		strings.Contains(body, nonmatchingStatusExecutionA.ID.String()) ||
		strings.Contains(body, nonmatchingTriggerExecutionA.ID.String()) ||
		strings.Contains(body, executionB.String()) {
		t.Fatal("namespace A filtered execution list was not isolated")
	}
	response, body = integrationGet(t, clientA, server.URL+"/admin/executions/"+executionA.String())
	if response.StatusCode != http.StatusOK ||
		!strings.Contains(body, "namespace-a-attempt") ||
		strings.Contains(body, "namespace-b-attempt") {
		t.Fatal("namespace A delivery attempt detail was not isolated")
	}
	response, _ = integrationGet(t, clientB, server.URL+"/admin/executions/"+executionA.String())
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("namespace B viewing A execution status = %d, want 404", response.StatusCode)
	}
	response, _ = integrationGet(t, clientA, server.URL+"/admin/executions/"+executionB.String())
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("namespace A viewing B execution status = %d, want 404", response.StatusCode)
	}

	response = postIntegrationJobAction(t, clientB, server.URL, jobA, "pause")
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("namespace B pausing A job status = %d, want 404", response.StatusCode)
	}

	response, _ = integrationPostForm(
		t,
		clientB,
		server.URL+"/admin/jobs/"+jobA.String()+"/delete",
		url.Values{"csrf_token": {authenticatedCSRF(t, clientB, server.URL)}},
	)
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("namespace B deleting A job status = %d, want 404", response.StatusCode)
	}

	var scheduleA uuid.UUID
	integrationScanRow(
		t,
		rt.db,
		`SELECT schedule_id FROM jobs WHERE id = $1`,
		[]any{jobA},
		&scheduleA,
	)
	if got := countRows(t, rt.db, `SELECT COUNT(*) FROM jobs WHERE id = $1`, jobA); got != 1 {
		t.Fatalf("namespace B delete changed A job count to %d, want 1", got)
	}
	if got := countRows(t, rt.db, `SELECT COUNT(*) FROM schedules WHERE id = $1`, scheduleA); got != 1 {
		t.Fatalf("namespace B delete changed A schedule count to %d, want 1", got)
	}
	if got := countRows(t, rt.db, `SELECT COUNT(*) FROM tags WHERE job_id = $1`, jobA); got != 2 {
		t.Fatalf("namespace B delete changed A tag count to %d, want 2", got)
	}
	if got := countRows(t, rt.db, `SELECT COUNT(*) FROM executions WHERE job_id = $1`, jobA); got != 3 {
		t.Fatalf("namespace B delete changed A execution count to %d, want 3", got)
	}
	if got := countRows(t, rt.db, `
SELECT COUNT(*) FROM delivery_attempts WHERE id = $1 AND execution_id = $2
`, attemptAID, executionA); got != 1 {
		t.Fatalf("namespace B delete changed A delivery attempt count to %d, want 1", got)
	}
	namespaceAContext, namespaceACancel = integrationContext(t)
	namespaceAContext = domain.NamespaceToContext(namespaceAContext, keyA.Key.Namespace)
	jobAfterDeniedDelete, scheduleAfterDeniedDelete, tagsAfterDeniedDelete, executionsAfterDeniedDelete, err :=
		rt.service.GetJob(namespaceAContext, jobA)
	namespaceACancel()
	if err != nil {
		t.Fatalf("get namespace A job after denied delete: %v", err)
	}
	if jobAfterDeniedDelete.ID != jobA ||
		scheduleAfterDeniedDelete.ID != scheduleA ||
		len(tagsAfterDeniedDelete) != 2 ||
		len(executionsAfterDeniedDelete) != 3 {
		t.Fatal("namespace A aggregate changed after namespace B delete attempt")
	}

	response, _ = integrationPostForm(
		t,
		clientA,
		server.URL+"/admin/jobs/"+jobA.String()+"/delete",
		url.Values{"csrf_token": {authenticatedCSRF(t, clientA, server.URL)}},
	)
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("namespace A delete status = %d, want 303", response.StatusCode)
	}
	if got := countRows(t, rt.db, `SELECT COUNT(*) FROM jobs WHERE id = $1`, jobA); got != 0 {
		t.Fatalf("deleted namespace A jobs remaining = %d, want 0", got)
	}
	if got := countRows(t, rt.db, `SELECT COUNT(*) FROM schedules WHERE id = $1`, scheduleA); got != 0 {
		t.Fatalf("deleted namespace A schedules remaining = %d, want 0", got)
	}
	if got := countRows(t, rt.db, `SELECT COUNT(*) FROM tags WHERE job_id = $1`, jobA); got != 0 {
		t.Fatalf("deleted namespace A tags remaining = %d, want 0", got)
	}
	if got := countRows(t, rt.db, `SELECT COUNT(*) FROM executions WHERE job_id = $1`, jobA); got != 0 {
		t.Fatalf("deleted namespace A executions remaining = %d, want 0", got)
	}
	if got := countRows(t, rt.db, `SELECT COUNT(*) FROM delivery_attempts WHERE id = $1`, attemptAID); got != 0 {
		t.Fatalf("deleted namespace A delivery attempts remaining = %d, want 0", got)
	}
	if got := countRows(t, rt.db, `SELECT COUNT(*) FROM delivery_attempts WHERE id = $1`, attemptBID); got != 1 {
		t.Fatalf("namespace A deletion changed namespace B delivery attempt count to %d, want 1", got)
	}
	response, _ = integrationGet(t, clientA, server.URL+"/admin/jobs/"+jobA.String())
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("deleted namespace A job status = %d, want 404", response.StatusCode)
	}
	response, body = integrationGet(t, clientB, server.URL+"/admin/jobs/"+jobB.String())
	if response.StatusCode != http.StatusOK || !strings.Contains(body, "namespace-b-job") {
		t.Fatal("namespace A deletion affected namespace B job")
	}

	t.Log("ADMIN_INTEGRATION_OK")
}
