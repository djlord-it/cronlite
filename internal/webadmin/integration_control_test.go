//go:build integration

package webadmin

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/djlord-it/cronlite/internal/domain"
	"github.com/google/uuid"
)

func controlJob(t *testing.T, rt *integrationRuntime, ns domain.Namespace, name string, enabled bool) domain.Job {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	schedule := domain.Schedule{ID: uuid.New(), CronExpression: "*/5 * * * *", Timezone: "UTC", CreatedAt: now, UpdatedAt: now}
	job := domain.Job{ID: uuid.New(), Namespace: ns, Name: name, Enabled: enabled, ScheduleID: schedule.ID, Delivery: domain.DeliveryConfig{Type: domain.DeliveryTypeWebhook, WebhookURL: "https://example.com/hook", Secret: "must-not-render-control-secret", Timeout: time.Second}, CreatedAt: now, UpdatedAt: now}
	ctx, cancel := integrationContext(t)
	defer cancel()
	if err := rt.store.CreateJobAggregate(ctx, job, schedule, []domain.Tag{{Key: "team", Value: "payments"}}); err != nil {
		t.Fatal(err)
	}
	return job
}
func controlContext(ns domain.Namespace) context.Context {
	return domain.NamespaceToContext(context.Background(), ns)
}

func TestIntegrationFleetSearchThousandsAccuracyAndLatency(t *testing.T) {
	rt := newIntegrationRuntime(t)
	key := mustBootstrap(t, rt, "search-large")
	ctx := controlContext(key.Key.Namespace)
	integrationExec(t, rt.db, `INSERT INTO schedules(id,cron_expression,timezone) VALUES(md5('large-schedule')::uuid,'*/5 * * * *','UTC')`)
	integrationExec(t, rt.db, `INSERT INTO jobs(id,namespace,name,enabled,schedule_id,delivery_type,webhook_url,secret,timeout_ms,created_at) SELECT md5('large-'||i)::uuid,CASE WHEN i<=10000 THEN 'search-large' ELSE 'search-foreign' END,'Invoice reconciliation '||lpad(((i-1)%10000+1)::text,5,'0'),i%4<>0,md5('large-schedule')::uuid,'webhook','https://example.com/hook','private',1000,'2026-01-01T00:00:00Z' FROM generate_series(1,20000) i`)
	integrationExec(t, rt.db, `INSERT INTO tags(job_id,key,value) SELECT id,'team',CASE WHEN enabled THEN 'payments' ELSE 'reports' END FROM jobs`)
	integrationExec(t, rt.db, `UPDATE jobs SET name='Équipe literal %_\ job' WHERE id=md5('large-1')::uuid`)
	integrationExec(t, rt.db, `ANALYZE jobs; ANALYZE tags; ANALYZE schedules`)
	paused := false
	cases := []struct {
		name          string
		filter        domain.JobFilter
		matched, rows int
	}{
		{"first", domain.JobFilter{}, 10000, 25},
		{"last", domain.JobFilter{ListParams: domain.ListParams{Limit: 25, Offset: 9975}}, 10000, 25},
		{"boundary", domain.JobFilter{ListParams: domain.ListParams{Limit: 25, Offset: 10000}}, 10000, 0},
		{"case-insensitive substring", domain.JobFilter{Name: "CONCILIATION"}, 9999, 25},
		{"name state and tag", domain.JobFilter{Name: "invoice", Enabled: &paused, Tags: []domain.Tag{{Key: "team", Value: "reports"}}}, 2500, 25},
		{"incompatible state/tag", domain.JobFilter{Enabled: &paused, Tags: []domain.Tag{{Key: "team", Value: "payments"}}}, 0, 0},
		{"literal wildcards", domain.JobFilter{Name: `%_\`}, 1, 1},
		{"unicode", domain.JobFilter{Name: "éQUIPE"}, 1, 1},
		{"injection", domain.JobFilter{Name: `x' OR TRUE --`}, 0, 0},
	}
	var timings []time.Duration
	for i := 0; i < 4; i++ {
		for _, tc := range cases {
			f := tc.filter
			if f.Limit == 0 {
				f.Limit = 25
			}
			f.Namespace = "search-foreign" // Service must use the authenticated namespace.
			start := time.Now()
			fleet, err := rt.service.Fleet(ctx, f)
			timings = append(timings, time.Since(start))
			if err != nil || fleet.Total != 10000 || fleet.Active != 7500 || fleet.Paused != 2500 || fleet.Matched != tc.matched || len(fleet.Jobs) != tc.rows {
				t.Fatalf("%s: counts/rows inaccurate: %+v %v", tc.name, fleet, err)
			}
			for _, row := range fleet.Jobs {
				if row.Job.Namespace != key.Key.Namespace {
					t.Fatal("foreign namespace leak")
				}
			}
		}
	}
	sort.Slice(timings, func(i, j int) bool { return timings[i] < timings[j] })
	t.Logf("10,000 jobs plus 10,000 foreign jobs; %d complete Fleet calls: p50=%s p95=%s max=%s", len(timings), timings[len(timings)/2], timings[len(timings)*95/100], timings[len(timings)-1])
	// Inspect the actual aggregate plan used by the live substring/count query.
	rows, err := rt.db.QueryContext(ctx, `EXPLAIN (ANALYZE, BUFFERS) SELECT count(*),count(*) FILTER(WHERE j.enabled),count(*) FILTER(WHERE NOT j.enabled),count(*) FILTER(WHERE j.namespace=$1 AND j.name ILIKE $2) FROM jobs j WHERE j.namespace=$1`, string(key.Key.Namespace), "%reconciliation%")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var line string
		if err = rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		t.Log(line)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	server := newIntegrationServer(t, rt)
	defer server.Close()
	client := newIntegrationClient(t)
	loginIntegrationClient(t, client, server.URL, key.PlaintextToken)
	var httpTimes []time.Duration
	for i := 0; i < 20; i++ {
		start := time.Now()
		response, body := integrationGet(t, client, server.URL+"/admin/jobs?name=reconciliation&enabled=false&tag=team%3Dreports")
		httpTimes = append(httpTimes, time.Since(start))
		if response.StatusCode != 200 || !strings.Contains(body, "2500 matching") || strings.Count(body, `name="job_id"`) != 25 || !strings.Contains(body, "Page 1 of 100") {
			t.Fatal("large-fleet HTTP search inaccurate")
		}
	}
	sort.Slice(httpTimes, func(i, j int) bool { return httpTimes[i] < httpTimes[j] })
	t.Logf("20 authenticated HTML search requests: p50=%s p95=%s max=%s", httpTimes[len(httpTimes)/2], httpTimes[len(httpTimes)*95/100], httpTimes[len(httpTimes)-1])
}
func TestIntegrationControlFleetAndRunsIsolation(t *testing.T) {
	rt := newIntegrationRuntime(t)
	keyA := mustBootstrap(t, rt, "fleet-a")
	keyB := mustCreateKey(t, rt, "fleet-b")
	ctxA := controlContext(keyA.Key.Namespace)
	// 500 tied creation timestamps exercise deterministic ordering and every page.
	integrationExec(t, rt.db, `INSERT INTO schedules(id,cron_expression,timezone) SELECT md5('fleet-schedule-'||i)::uuid,'*/5 * * * *','UTC' FROM generate_series(1,500) i`)
	integrationExec(t, rt.db, `INSERT INTO jobs(id,namespace,name,enabled,schedule_id,delivery_type,webhook_url,secret,timeout_ms,created_at) SELECT md5('fleet-job-'||i)::uuid,'fleet-a','payments-'||lpad(i::text,3,'0'),i%4<>0,md5('fleet-schedule-'||i)::uuid,'webhook','https://example.com/hook','must-not-render-control-secret',1000,'2026-01-01T00:00:00Z' FROM generate_series(1,500) i`)
	integrationExec(t, rt.db, `INSERT INTO tags(job_id,key,value) SELECT id,'team','payments' FROM jobs WHERE namespace='fleet-a'`)
	jobB := controlJob(t, rt, keyB.Key.Namespace, "secret-other-namespace", true)
	seen := map[uuid.UUID]bool{}
	for page := 0; page < 20; page++ {
		fleet, err := rt.service.Fleet(ctxA, domain.JobFilter{Namespace: "fleet-b", ListParams: domain.ListParams{Limit: 25, Offset: page * 25}})
		if err != nil {
			t.Fatal(err)
		}
		if fleet.Total != 500 || fleet.Active != 375 || fleet.Paused != 125 || fleet.Matched != 500 || len(fleet.Jobs) != 25 {
			t.Fatalf("wrong fleet counts: %#v", fleet)
		}
		for _, row := range fleet.Jobs {
			if seen[row.Job.ID] || row.Job.Namespace != "fleet-a" {
				t.Fatal("unstable or leaking pagination")
			}
			seen[row.Job.ID] = true
		}
	}
	if len(seen) != 500 {
		t.Fatal("jobs lost across pages")
	}
	enabled := false
	fleet, err := rt.service.Fleet(ctxA, domain.JobFilter{Enabled: &enabled, Tags: []domain.Tag{{Key: "team", Value: "payments"}}, Name: "payments-", ListParams: domain.ListParams{Limit: 25, Offset: 125}})
	if err != nil || fleet.Matched != 125 || len(fleet.Jobs) != 0 {
		t.Fatal("empty boundary counts", err)
	}
	for _, name := range []string{"%", "_", "x' OR TRUE --", "missing"} {
		fleet, err = rt.service.Fleet(ctxA, domain.JobFilter{Name: name})
		if err != nil || fleet.Matched != 0 {
			t.Fatalf("nonliteral/unsafe name %q: %#v %v", name, fleet, err)
		}
	}
	var jobA uuid.UUID
	integrationScanRow(t, rt.db, `SELECT id FROM jobs WHERE namespace='fleet-a' LIMIT 1`, nil, &jobA)
	now := time.Now().UTC().Truncate(time.Microsecond)
	for _, status := range []domain.ExecutionStatus{domain.ExecutionStatusEmitted, domain.ExecutionStatusInProgress, domain.ExecutionStatusDelivered, domain.ExecutionStatusFailed} {
		execution := domain.Execution{ID: uuid.New(), JobID: jobA, Namespace: keyA.Key.Namespace, TriggerType: domain.TriggerTypeScheduled, ScheduledAt: now, FiredAt: now, Status: status, CreatedAt: now}
		insertIntegrationExecution(t, rt, execution)
		if status == domain.ExecutionStatusFailed {
			for i := 1; i <= 2; i++ {
				insertIntegrationAttempt(t, rt, domain.DeliveryAttempt{ID: uuid.New(), ExecutionID: execution.ID, Attempt: i, StatusCode: 500 + i, Error: "evidence-error", StartedAt: now, FinishedAt: now.Add(time.Duration(i) * time.Second)})
			}
		}
		now = now.Add(-time.Minute)
	}
	insertIntegrationExecution(t, rt, domain.Execution{ID: uuid.New(), JobID: jobB.ID, Namespace: keyB.Key.Namespace, TriggerType: domain.TriggerTypeManual, ScheduledAt: now, FiredAt: now, Status: domain.ExecutionStatusFailed, CreatedAt: now})
	until := time.Now()
	since := until.Add(-24 * time.Hour)
	runs, err := rt.service.Runs(ctxA, domain.ExecutionFilter{Namespace: keyB.Key.Namespace, Since: &since, Until: &until})
	if err != nil || runs.Total != 4 || len(runs.Runs) != 4 {
		t.Fatalf("run isolation: %#v %v", runs, err)
	}
	for _, run := range runs.Runs {
		if run.Execution.Namespace != keyA.Key.Namespace {
			t.Fatal("leaked run")
		}
		if run.Execution.Status == domain.ExecutionStatusFailed && (run.Attempts != 2 || run.LastStatusCode != 502 || run.LastError != "evidence-error") {
			t.Fatalf("incorrect evidence: %#v", run)
		}
	}
	server := newIntegrationServer(t, rt)
	defer server.Close()
	client := newIntegrationClient(t)
	loginIntegrationClient(t, client, server.URL, keyA.PlaintextToken)
	for _, path := range []string{"/admin/jobs", "/admin/jobs?view=board", "/admin/jobs?view=timeline", "/admin/runs", "/admin/runs?view=table"} {
		response, body := integrationGet(t, client, server.URL+path)
		if response.StatusCode != 200 || strings.Contains(body, "secret-other-namespace") || strings.Contains(body, "must-not-render-control-secret") {
			t.Fatalf("UI isolation %s: %d %s", path, response.StatusCode, body)
		}
	}
	response, _ := integrationGet(t, client, server.URL+"/admin/jobs?page=10000&view=board&tag=team%3Dpayments")
	if response.StatusCode != 303 || !strings.Contains(response.Header.Get("Location"), "page=20") {
		t.Fatal("last-page clamp")
	}
	response, _ = integrationGet(t, client, server.URL+"/admin/jobs?name=absent&page=2")
	if response.StatusCode != 303 || !strings.Contains(response.Header.Get("Location"), "page=1") {
		t.Fatal("empty result clamp")
	}
}
func TestIntegrationBulkOwnershipExpiryPartialFailureAndReplay(t *testing.T) {
	rt := newIntegrationRuntime(t)
	keyA := mustBootstrap(t, rt, "bulk-a")
	keyA2 := mustCreateKey(t, rt, "bulk-a")
	keyB := mustCreateKey(t, rt, "bulk-b")
	ctxA := controlContext(keyA.Key.Namespace)
	ctxB := controlContext(keyB.Key.Namespace)
	applied := controlJob(t, rt, keyA.Key.Namespace, "applied", true)
	stale := controlJob(t, rt, keyA.Key.Namespace, "stale", true)
	deleted := controlJob(t, rt, keyA.Key.Namespace, "deleted", true)
	unchanged := controlJob(t, rt, keyA.Key.Namespace, "unchanged", false)
	failed := controlJob(t, rt, keyA.Key.Namespace, "db-failure", true)
	foreign := controlJob(t, rt, keyB.Key.Namespace, "foreign-private", true)
	if _, err := rt.service.PreviewBulk(ctxA, keyA.Key.ID, "pause", []uuid.UUID{applied.ID, foreign.ID}); !errors.Is(err, domain.ErrJobNotFound) {
		t.Fatal("foreign job preview", err)
	}
	if got := countRows(t, rt.db, `SELECT count(*) FROM admin_bulk_batches`); got != 0 {
		t.Fatal("invalid selection persisted")
	}
	if _, err := rt.service.PreviewBulk(ctxA, keyB.Key.ID, "pause", []uuid.UUID{applied.ID}); !errors.Is(err, domain.ErrAPIKeyNotFound) {
		t.Fatal("foreign actor accepted")
	}
	batch, err := rt.service.PreviewBulk(ctxA, keyA.Key.ID, "pause", []uuid.UUID{applied.ID, stale.ID, deleted.ID, unchanged.ID, failed.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, rt.db, `SELECT count(*) FROM jobs WHERE namespace='bulk-a' AND enabled`); got != 4 {
		t.Fatal("preview changed jobs")
	}
	for _, actor := range []uuid.UUID{keyA2.Key.ID, keyB.Key.ID} {
		if _, err := rt.service.GetBulk(ctxA, actor, batch.ID); !errors.Is(err, domain.ErrBulkNotFound) {
			t.Fatal("foreign actor read batch", err)
		}
		if _, err := rt.service.ConfirmBulk(ctxA, actor, batch.ID); err == nil {
			t.Fatal("foreign actor confirmed batch")
		}
	}
	if _, err := rt.service.GetBulk(ctxB, keyB.Key.ID, batch.ID); !errors.Is(err, domain.ErrBulkNotFound) {
		t.Fatal("foreign namespace batch")
	}
	integrationExec(t, rt.db, `UPDATE jobs SET name='edited',updated_at=clock_timestamp() WHERE id=$1`, stale.ID)
	if err := rt.service.DeleteJob(ctxA, deleted.ID); err != nil {
		t.Fatal(err)
	}
	integrationExec(t, rt.db, `CREATE FUNCTION control_reject_update() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF OLD.name='db-failure' THEN RAISE EXCEPTION 'injected private db error'; END IF; RETURN NEW; END $$`)
	integrationExec(t, rt.db, `CREATE TRIGGER control_reject_update BEFORE UPDATE ON jobs FOR EACH ROW EXECUTE FUNCTION control_reject_update()`)
	t.Cleanup(func() {
		integrationExec(t, rt.db, `DROP TRIGGER IF EXISTS control_reject_update ON jobs`)
		integrationExec(t, rt.db, `DROP FUNCTION IF EXISTS control_reject_update()`)
	})
	result, err := rt.service.ConfirmBulk(ctxA, keyA.Key.ID, batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := map[uuid.UUID]string{applied.ID: "applied", stale.ID: "conflict", deleted.ID: "unavailable", unchanged.ID: "unchanged", failed.ID: "failed"}
	for _, item := range result.Items {
		if item.Result != want[item.ID] {
			t.Fatalf("%s: got %s want %s", item.Name, item.Result, want[item.ID])
		}
	}
	if result.ConfirmedAt == nil {
		t.Fatal("missing confirmation audit")
	}
	job, schedule, _, _, err := rt.service.GetJob(ctxA, applied.ID)
	if err != nil || job.Enabled || job.Name != "applied" {
		t.Fatal("successful change not committed")
	}
	// A stale configuration write must preserve bulk fleet state.
	applied.Name = "configuration edit"
	applied.UpdatedAt = time.Now()
	if err := rt.store.UpdateJobAggregate(ctxA, applied, schedule, nil); err != nil {
		t.Fatal(err)
	}
	job, _, _, _, err = rt.service.GetJob(ctxA, applied.ID)
	if err != nil || job.Enabled || job.Name != "configuration edit" {
		t.Fatal("config edit reverted bulk state")
	}
	replay, err := rt.service.ConfirmBulk(ctxA, keyA.Key.ID, batch.ID)
	if err != nil || !replay.ConfirmedAt.Equal(*result.ConfirmedAt) {
		t.Fatal("confirmation replay changed audit", err)
	}
	if got := countRows(t, rt.db, `SELECT count(*) FROM jobs WHERE id IN ($1,$2) AND enabled`, stale.ID, failed.ID); got != 2 {
		t.Fatal("failed/conflicting jobs changed")
	}
	fresh := controlJob(t, rt, keyA.Key.Namespace, "fresh", true)
	expired, err := rt.service.PreviewBulk(ctxA, keyA.Key.ID, "pause", []uuid.UUID{fresh.ID})
	if err != nil {
		t.Fatal(err)
	}
	integrationExec(t, rt.db, `UPDATE admin_bulk_batches SET expires_at=now()-interval '1 second' WHERE id=$1`, expired.ID)
	if _, err := rt.service.ConfirmBulk(ctxA, keyA.Key.ID, expired.ID); !errors.Is(err, domain.ErrBulkNotFound) {
		t.Fatal("expired preview confirmed", err)
	}
	// HTTP confirms need an explicit checkbox and CSRF token; server recreation
	// keeps ownership/results because no preview state lives in process memory.
	server := newIntegrationServer(t, rt)
	defer server.Close()
	client := newIntegrationClient(t)
	loginIntegrationClient(t, client, server.URL, keyA.PlaintextToken)
	csrf := authenticatedCSRF(t, client, server.URL)
	response, body := integrationGet(t, client, server.URL+"/admin/bulk/"+batch.ID.String())
	if response.StatusCode != 200 || !strings.Contains(body, "1 applied · 1 unchanged · 3 not applied") || strings.Contains(body, "injected private db error") {
		t.Fatal("persisted result UI")
	}
	for _, form := range []url.Values{{"csrf_token": {csrf}}, {"confirm": {"yes"}}} {
		response, _ := integrationPostForm(t, client, server.URL+"/admin/bulk/"+batch.ID.String()+"/confirm", form)
		if response.StatusCode != 400 && response.StatusCode != 403 {
			t.Fatal("implicit confirmation or invalid CSRF accepted")
		}
	}
	response, body = integrationGet(t, client, server.URL+"/admin/bulk")
	if response.StatusCode != 200 || !strings.Contains(body, batch.ID.String()) {
		t.Fatal("audit missing")
	}
}
func TestIntegrationBulkConcurrentConfirmation(t *testing.T) {
	rt := newIntegrationRuntime(t)
	key := mustBootstrap(t, rt, "concurrent")
	ctx := controlContext(key.Key.Namespace)
	jobs := []domain.Job{controlJob(t, rt, key.Key.Namespace, "one", true), controlJob(t, rt, key.Key.Namespace, "two", true)}
	ids := []uuid.UUID{jobs[1].ID, jobs[0].ID}
	batch, err := rt.service.PreviewBulk(ctx, key.Key.ID, "pause", ids)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan domain.BulkBatch, 8)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, e := rt.service.ConfirmBulk(ctx, key.Key.ID, batch.ID)
			results <- result
			errs <- e
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var first *time.Time
	for result := range results {
		if result.ConfirmedAt == nil {
			t.Fatal("unconfirmed")
		}
		if first == nil {
			first = result.ConfirmedAt
		}
		if !first.Equal(*result.ConfirmedAt) {
			t.Fatal("multiple confirmations")
		}
		for _, item := range result.Items {
			if item.Result != "applied" {
				t.Fatal("concurrent replay lost recorded result")
			}
		}
	}
	// Two overlapping previews are deadlock-free and detect the first mutation.
	for _, job := range jobs {
		if _, err := rt.service.ResumeJob(ctx, job.ID); err != nil {
			t.Fatal(err)
		}
	}
	b1, err := rt.service.PreviewBulk(ctx, key.Key.ID, "pause", ids)
	if err != nil {
		t.Fatal(err)
	}
	b2, err := rt.service.PreviewBulk(ctx, key.Key.ID, "pause", []uuid.UUID{ids[1], ids[0]})
	if err != nil {
		t.Fatal(err)
	}
	results = make(chan domain.BulkBatch, 2)
	errs = make(chan error, 2)
	for _, b := range []domain.BulkBatch{b1, b2} {
		wg.Add(1)
		go func(id uuid.UUID) {
			defer wg.Done()
			result, e := rt.service.ConfirmBulk(ctx, key.Key.ID, id)
			results <- result
			errs <- e
		}(b.ID)
	}
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	applied, conflicts := 0, 0
	for b := range results {
		for _, item := range b.Items {
			if item.Result == "applied" {
				applied++
			}
			if item.Result == "conflict" {
				conflicts++
			}
		}
	}
	if applied != 2 || conflicts != 2 {
		t.Fatalf("overlap applied=%d conflict=%d", applied, conflicts)
	}
}
func TestIntegrationSavedViewsOwnershipQuotaAndRevocation(t *testing.T) {
	rt := newIntegrationRuntime(t)
	a := mustBootstrap(t, rt, "saved-a")
	a2 := mustCreateKey(t, rt, "saved-a")
	b := mustCreateKey(t, rt, "saved-b")
	ctxA := controlContext(a.Key.Namespace)
	ctxB := controlContext(b.Key.Namespace)
	if err := rt.service.SaveView(ctxA, a.Key.ID, "Payments", "tag=team%3Dpayments&view=board"); err != nil {
		t.Fatal(err)
	}
	views, err := rt.service.ListSavedViews(ctxA, a.Key.ID)
	if err != nil || len(views) != 1 {
		t.Fatal("saved view missing", err)
	}
	id := views[0].ID
	for _, actor := range []uuid.UUID{a2.Key.ID, b.Key.ID} {
		v, e := rt.service.ListSavedViews(ctxA, actor)
		if e != nil || len(v) != 0 {
			t.Fatal("view leaked")
		}
		if err := rt.service.DeleteView(ctxA, actor, id); !errors.Is(err, domain.ErrBulkNotFound) {
			t.Fatal("foreign actor deletion")
		}
	}
	if err := rt.service.SaveView(ctxA, b.Key.ID, "foreign", ""); !errors.Is(err, domain.ErrAPIKeyNotFound) {
		t.Fatal("foreign actor write")
	}
	v, e := rt.service.ListSavedViews(ctxB, a.Key.ID)
	if e != nil || len(v) != 0 {
		t.Fatal("namespace view leak")
	}
	if err := rt.service.SaveView(ctxA, a.Key.ID, "Payments", "enabled=false"); err != nil {
		t.Fatal(err)
	}
	views, _ = rt.service.ListSavedViews(ctxA, a.Key.ID)
	if len(views) != 1 || views[0].Query != "enabled=false" {
		t.Fatal("named view update")
	}
	// Quota cannot be exceeded by concurrent requests on different connections.
	var wg sync.WaitGroup
	errs := make(chan error, 25)
	for i := 0; i < 25; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- rt.service.SaveView(ctxA, a.Key.ID, fmt.Sprintf("View %02d", i), "")
		}(i)
	}
	wg.Wait()
	close(errs)
	rejected := 0
	for err := range errs {
		if errors.Is(err, domain.ErrSavedViewLimit) {
			rejected++
		} else if err != nil {
			t.Fatal(err)
		}
	}
	views, _ = rt.service.ListSavedViews(ctxA, a.Key.ID)
	if len(views) != 20 || rejected != 6 {
		t.Fatalf("quota len=%d rejected=%d", len(views), rejected)
	}
	if err := rt.service.DeleteView(ctxA, a.Key.ID, id); err != nil {
		t.Fatal(err)
	}
	if err := rt.service.SaveView(ctxA, a.Key.ID, "Replacement", ""); err != nil {
		t.Fatal(err)
	}
	job := controlJob(t, rt, a.Key.Namespace, "job", true)
	batch, err := rt.service.PreviewBulk(ctxA, a.Key.ID, "pause", []uuid.UUID{job.ID})
	if err != nil {
		t.Fatal(err)
	}
	integrationExec(t, rt.db, `UPDATE api_keys SET enabled=false WHERE id=$1`, a.Key.ID)
	if _, err := rt.service.ConfirmBulk(ctxA, a.Key.ID, batch.ID); !errors.Is(err, domain.ErrAPIKeyNotFound) {
		t.Fatal("revoked key confirmed")
	}
	if err := rt.service.SaveView(ctxA, a.Key.ID, "revoked", ""); !errors.Is(err, domain.ErrAPIKeyNotFound) {
		t.Fatal("revoked key saved")
	}
	views, _ = rt.service.ListSavedViews(ctxA, a.Key.ID)
	if len(views) != 0 {
		t.Fatal("revoked key views")
	}
	integrationExec(t, rt.db, `DELETE FROM api_keys WHERE id=$1`, a.Key.ID)
	if got := countRows(t, rt.db, `SELECT count(*) FROM admin_saved_views WHERE api_key_id=$1`, a.Key.ID); got != 0 {
		t.Fatal("views not cascaded")
	}
}
