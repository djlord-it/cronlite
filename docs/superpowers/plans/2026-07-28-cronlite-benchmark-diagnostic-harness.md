# CronLite Benchmark and Diagnostic Harness Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a safe, reproducible Go harness that correlates CronLite API, webhook, metrics, and read-only PostgreSQL observations into raw JSON/CSV and a correctness-first Markdown report.

**Architecture:** A single `main` package under `tools/benchmark` keeps the command directly runnable while focused files isolate configuration, observations, receiving, API/diagnostic collection, scenarios, correlation, statistics, export, reporting, and environment control. Black-box operation is the default; database and Docker capabilities are optional adapters that return explicit skipped/unavailable observations.

**Tech Stack:** Go 1.25, standard library HTTP/CSV/JSON/crypto packages, `github.com/google/uuid`, `database/sql` with the repository's existing `github.com/lib/pq`, Prometheus text parsing, Docker Compose CLI for guarded local process control.

---

## File Map

- `tools/benchmark/main.go`: CLI entrypoint, lifecycle wiring, and exit codes.
- `tools/benchmark/config.go`: flags, defaults, scenario parsing, redaction, and safety validation.
- `tools/benchmark/model.go`: versioned raw result, provenance, observation, scenario, execution, attempt, and finding types.
- `tools/benchmark/statistics.go`: descriptive statistics, rates, and percentile warnings.
- `tools/benchmark/receiver.go`: bounded HMAC-verifying receiver with deterministic behavior plans.
- `tools/benchmark/api_client.go`: instrumented customer-facing REST client.
- `tools/benchmark/diagnostic.go`: read-only PostgreSQL observations and resource queries.
- `tools/benchmark/prometheus.go`: metric text snapshots and deltas.
- `tools/benchmark/correlation.go`: lifecycle joins, formulas, duplicates, and correctness findings.
- `tools/benchmark/scenarios.go`: scenario registry, runner contracts, and common setup/cleanup.
- `tools/benchmark/scenario_delivery.go`: baselines, smoke, sequential, concurrent, control-plane, slow, retry, recurring, and cold/warm scenarios.
- `tools/benchmark/scenario_resilience.go`: guarded duplicate and recovery scenarios.
- `tools/benchmark/environment.go`: metadata collection and guarded harness-owned Compose control.
- `tools/benchmark/export.go`: atomic JSON and stable CSV output.
- `tools/benchmark/report.go`: correctness-first Markdown generation.
- `tools/benchmark/*_test.go`: unit, race, HTTP integration, SQL mock, safety, and golden-output tests.
- `tools/benchmark/docker-compose.yml`: optional isolated local PostgreSQL and multi-instance CronLite environment.
- `tools/benchmark/README.md`: command, schema, formulas, provenance, safety, and limitations.
- `tools/benchmark/example-output/*`: locally generated example output.
- `.gitignore`: allow the benchmark README/report while continuing to ignore ad hoc benchmark outputs.

### Task 1: Versioned observation model and CLI safety

**Files:**
- Create: `tools/benchmark/model.go`
- Create: `tools/benchmark/config.go`
- Test: `tools/benchmark/config_test.go`

- [ ] **Step 1: Write failing tests for safe defaults, explicit high-risk authorization, non-local protection, and redaction**

```go
func TestDefaultConfigIsSafeSmoke(t *testing.T) {
	cfg := defaultConfig()
	if !reflect.DeepEqual([]string{"smoke"}, cfg.Scenarios) {
		t.Fatalf("scenarios = %v", cfg.Scenarios)
	}
	if cfg.SampleCount != 10 || cfg.AllowDisruptive || cfg.AllowNonLocal {
		t.Fatalf("unsafe defaults: %+v", cfg)
	}
}

func TestValidateRejectsDisruptiveScenarioWithoutAuthorization(t *testing.T) {
	cfg := defaultConfig()
	cfg.Scenarios = []string{"duplicate-race"}
	if err := cfg.Validate(); !errors.Is(err, ErrDisruptiveNotAllowed) {
		t.Fatalf("expected disruptive error, got %v", err)
	}
}

func TestValidateRejectsRemoteTargetWithoutAuthorization(t *testing.T) {
	cfg := defaultConfig()
	cfg.BaseURL = "https://cron.example.com"
	if err := cfg.Validate(); !errors.Is(err, ErrNonLocalNotAllowed) {
		t.Fatalf("expected non-local error, got %v", err)
	}
}

func TestRedactedConfigDoesNotExposeSecrets(t *testing.T) {
	cfg := defaultConfig()
	cfg.APIKey = "secret-api-key"
	cfg.WebhookSecret = "secret-webhook-key"
	cfg.DatabaseURL = "postgres://user:password@localhost/db"
	raw, err := json.Marshal(cfg.Redacted())
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"secret-api-key", "secret-webhook-key", "password"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatalf("serialized config exposed %q: %s", secret, raw)
		}
	}
}
```

- [ ] **Step 2: Run the tests and verify they fail because the model and configuration do not exist**

Run: `go test ./tools/benchmark -run 'Test(DefaultConfig|Validate|Redacted)' -count=1`

Expected: FAIL with undefined `defaultConfig`, `ErrDisruptiveNotAllowed`, and observation types.

- [ ] **Step 3: Implement the versioned model and safe configuration**

```go
type Provenance string

const (
	ProvenanceDirect      Provenance = "directly_observed"
	ProvenanceDerived     Provenance = "derived"
	ProvenanceDatabase    Provenance = "database_observed"
	ProvenanceUnavailable Provenance = "unavailable"
)

type Measurement struct {
	Name       string     `json:"name"`
	ValueMS    *float64   `json:"value_ms,omitempty"`
	Provenance Provenance `json:"provenance"`
	Reason     string     `json:"reason,omitempty"`
}

type Config struct {
	BaseURL          string
	APIKey           string
	ReceiverAddr     string
	ReceiverPublicURL string
	Scenarios        []string
	SampleCount      int
	Concurrency      []int
	Timeout          time.Duration
	DatabaseURL      string
	OutputDir        string
	RandomSeed       int64
	WebhookSecret    string
	RetryProfile     string
	AllowDisruptive  bool
	AllowNonLocal    bool
	FailOnCorrectness bool
	KeepData         bool
	StartCompose     bool
	ComposeProject   string
}

var (
	ErrDisruptiveNotAllowed = errors.New("disruptive scenario requires --allow-disruptive")
	ErrNonLocalNotAllowed   = errors.New("non-local target requires --allow-non-local")
)
```

`Validate` parses URLs, accepts only known scenarios, bounds samples and
concurrency, requires diagnostic URLs where needed, treats loopback hostnames and
addresses as local, and recognizes all guarded scenario names.

- [ ] **Step 4: Run focused tests, formatting, and vet**

Run: `gofmt -w tools/benchmark/model.go tools/benchmark/config.go tools/benchmark/config_test.go`

Run: `go test ./tools/benchmark -run 'Test(DefaultConfig|Validate|Redacted)' -count=1`

Expected: PASS.

Run: `go vet ./tools/benchmark`

Expected: no output and exit 0.

- [ ] **Step 5: Commit the model and safety boundary**

```bash
git add tools/benchmark/model.go tools/benchmark/config.go tools/benchmark/config_test.go
git commit -m "feat: define benchmark model and safety gates"
```

### Task 2: Statistical calculations

**Files:**
- Create: `tools/benchmark/statistics.go`
- Test: `tools/benchmark/statistics_test.go`

- [ ] **Step 1: Write failing tests for empty, singleton, and representative distributions**

```go
func TestSummarizeRepresentativeDistribution(t *testing.T) {
	got := summarize([]float64{1, 2, 3, 4, 100})
	want := Stats{
		Count: 5, Min: 1, Max: 100, Mean: 22, Median: 3,
		StdDev: math.Sqrt(1902.5), P50: 3, P90: 100, P95: 100, P99: 100,
	}
	assertStatsClose(t, want, got)
	if len(got.Warnings) == 0 {
		t.Fatal("expected unstable high-percentile warning")
	}
}

func TestSummarizeEmptyIsUnavailable(t *testing.T) {
	got := summarize(nil)
	if got.Count != 0 || !got.Unavailable {
		t.Fatalf("unexpected empty summary: %+v", got)
	}
}
```

- [ ] **Step 2: Run tests and verify the missing implementation failure**

Run: `go test ./tools/benchmark -run TestSummarize -count=1`

Expected: FAIL with undefined `summarize` and `Stats`.

- [ ] **Step 3: Implement sorted-copy nearest-rank percentiles and sample standard deviation**

```go
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(math.Ceil(p*float64(len(sorted)))) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= len(sorted) {
		rank = len(sorted) - 1
	}
	return sorted[rank]
}
```

`summarize` must not mutate its input, uses `n-1` sample variance when `n > 1`,
and warns when `n < 100` for p99, `n < 20` for p95, or `n < 10` for p90.

- [ ] **Step 4: Run focused and race tests**

Run: `gofmt -w tools/benchmark/statistics.go tools/benchmark/statistics_test.go`

Run: `go test -race ./tools/benchmark -run TestSummarize -count=1`

Expected: PASS with no race report.

- [ ] **Step 5: Commit statistics**

```bash
git add tools/benchmark/statistics.go tools/benchmark/statistics_test.go
git commit -m "feat: calculate benchmark latency statistics"
```

### Task 3: HMAC-verifying controlled webhook receiver

**Files:**
- Create: `tools/benchmark/receiver.go`
- Test: `tools/benchmark/receiver_test.go`

- [ ] **Step 1: Write failing HTTP tests for valid signatures, invalid signatures, status sequences, bounded bodies, and overlapping duplicates**

```go
func TestReceiverVerifiesSignatureAndRecordsAttempt(t *testing.T) {
	store := newCallbackStore()
	server := httptest.NewServer(newReceiverHandler(store, "secret", receiverLimits()))
	t.Cleanup(server.Close)
	body := []byte(`{"execution_id":"exec-1","job_id":"job-1","scheduled_at":"2026-07-28T12:00:00Z","fired_at":"2026-07-28T12:00:01Z"}`)
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/hook/success", bytes.NewReader(body))
	req.Header.Set("X-CronLite-Execution-ID", "exec-1")
	req.Header.Set("X-CronLite-Event-ID", "attempt-1")
	req.Header.Set("X-CronLite-Signature", sign("secret", body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	got := store.snapshot()
	if len(got) != 1 || !got[0].SignatureValid || got[0].AttemptID != "attempt-1" {
		t.Fatalf("unexpected callback: %+v", got)
	}
}

func TestReceiverDetectsOverlappingExecutionCallbacks(t *testing.T) {
	store := newCallbackStore()
	plan := BehaviorPlan{Statuses: []int{204}, Delay: 100 * time.Millisecond}
	token := store.registerPlan(plan)
	server := httptest.NewServer(newReceiverHandler(store, "secret", receiverLimits()))
	t.Cleanup(server.Close)
	send := func(attempt string) { sendSignedCallback(t, server.URL+"/hook/"+token, "exec-1", attempt) }
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); send("attempt-1") }()
	go func() { defer wg.Done(); send("attempt-2") }()
	wg.Wait()
	if !store.summary("exec-1").ConcurrentDuplicate {
		t.Fatal("expected concurrent duplicate")
	}
}
```

- [ ] **Step 2: Verify receiver tests fail for missing types**

Run: `go test ./tools/benchmark -run TestReceiver -count=1`

Expected: FAIL with undefined receiver functions and types.

- [ ] **Step 3: Implement the receiver and callback store**

The handler must:

```go
func verifySignature(secret string, body []byte, signature string) bool {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(signature))
}
```

Use `http.MaxBytesReader`, an append-only mutex-protected observation slice,
active counts keyed by execution ID, payload hashes, per-plan atomic attempt
indices, response delays bounded by configuration, and a separate `/baseline`
immediate-204 endpoint.

- [ ] **Step 4: Run focused receiver tests with the race detector**

Run: `gofmt -w tools/benchmark/receiver.go tools/benchmark/receiver_test.go`

Run: `go test -race ./tools/benchmark -run TestReceiver -count=1`

Expected: PASS with no race report.

- [ ] **Step 5: Commit the receiver**

```bash
git add tools/benchmark/receiver.go tools/benchmark/receiver_test.go
git commit -m "feat: add instrumented webhook receiver"
```

### Task 4: Instrumented CronLite API client

**Files:**
- Create: `tools/benchmark/api_client.go`
- Test: `tools/benchmark/api_client_test.go`

- [ ] **Step 1: Write failing tests for auth, bounded errors, trigger IDs, execution polling bounds, and cleanup**

```go
func TestAPIClientTriggerCapturesExecutionAndTiming(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer key" {
			t.Fatalf("authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":"11111111-1111-1111-1111-111111111111","job_id":"22222222-2222-2222-2222-222222222222","scheduled_at":"2026-07-28T12:00:00Z","fired_at":"2026-07-28T12:00:00Z","status":"emitted","trigger_type":"manual","created_at":"2026-07-28T12:00:00Z"}`)
	}))
	t.Cleanup(server.Close)
	client := newAPIClient(server.URL, "key", time.Second)
	exec, obs, err := client.trigger(context.Background(), "22222222-2222-2222-2222-222222222222")
	if err != nil {
		t.Fatal(err)
	}
	if exec.ID == "" || obs.Duration <= 0 || obs.StatusCode != http.StatusCreated {
		t.Fatalf("execution=%+v observation=%+v", exec, obs)
	}
}

func TestPollTerminalPreservesObservationBounds(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		status := "emitted"
		if requests.Add(1) == 2 {
			status = "delivered"
		}
		fmt.Fprintf(w, `{"id":"11111111-1111-1111-1111-111111111111","job_id":"22222222-2222-2222-2222-222222222222","scheduled_at":"2026-07-28T12:00:00Z","fired_at":"2026-07-28T12:00:00Z","status":%q,"trigger_type":"manual","created_at":"2026-07-28T12:00:00Z"}`, status)
	}))
	t.Cleanup(server.Close)
	client := newAPIClient(server.URL, "key", time.Second)
	got, err := client.pollTerminal(context.Background(), "11111111-1111-1111-1111-111111111111", time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if got.PollCount != 2 || got.LastNonTerminalAt == nil || got.FirstTerminalAt == nil {
		t.Fatalf("poll result = %+v", got)
	}
	if got.LastNonTerminalAt.After(*got.FirstTerminalAt) {
		t.Fatalf("invalid bounds: %+v", got)
	}
}
```

- [ ] **Step 2: Verify tests fail**

Run: `go test ./tools/benchmark -run 'TestAPIClient|TestPollTerminal' -count=1`

Expected: FAIL with undefined API client symbols.

- [ ] **Step 3: Implement REST methods and observation capture**

Use typed local wire structs matching `api/openapi.yaml`, a single request helper
that bounds error bodies to 64 KiB, and methods for health, create/get/list,
patch, pause, resume, delete, trigger, and terminal polling. Record start/end
wall times plus monotonic durations.

- [ ] **Step 4: Run focused tests and vet**

Run: `gofmt -w tools/benchmark/api_client.go tools/benchmark/api_client_test.go`

Run: `go test ./tools/benchmark -run 'TestAPIClient|TestPollTerminal' -count=1`

Expected: PASS.

Run: `go vet ./tools/benchmark`

Expected: exit 0.

- [ ] **Step 5: Commit the API client**

```bash
git add tools/benchmark/api_client.go tools/benchmark/api_client_test.go
git commit -m "feat: instrument CronLite API requests"
```

### Task 5: Diagnostic PostgreSQL and Prometheus collectors

**Files:**
- Create: `tools/benchmark/diagnostic.go`
- Create: `tools/benchmark/prometheus.go`
- Test: `tools/benchmark/diagnostic_test.go`
- Test: `tools/benchmark/prometheus_test.go`

- [ ] **Step 1: Write failing tests for query mapping, read-only setup, metric parsing, and deltas**

```go
func TestDiagnosticExecutionIncludesClaimAndAttempts(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	mock.ExpectBegin()
	mock.ExpectExec("SET TRANSACTION READ ONLY").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT.+claimed_at").
		WithArgs("exec-1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "job_id", "scheduled_at", "fired_at", "created_at", "claimed_at", "status"}).
			AddRow("exec-1", "job-1", time.Now(), time.Now(), time.Now(), time.Now(), "delivered"))
	mock.ExpectQuery("SELECT.+delivery_attempts").
		WithArgs("exec-1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "attempt", "status_code", "error", "started_at", "finished_at"}))
	mock.ExpectCommit()
	got, err := newDiagnosticCollector(db).execution(context.Background(), "exec-1")
	if err != nil || got.ExecutionID != "exec-1" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

func TestPrometheusDelta(t *testing.T) {
	before := parsePrometheus([]byte("cronlite_dispatcher_delivery_outcomes_total{outcome=\"success\"} 2\n"))
	after := parsePrometheus([]byte("cronlite_dispatcher_delivery_outcomes_total{outcome=\"success\"} 5\n"))
	if got := metricDelta(before, after)["cronlite_dispatcher_delivery_outcomes_total{outcome=\"success\"}"]; got != 3 {
		t.Fatalf("delta = %v", got)
	}
}
```

- [ ] **Step 2: Verify focused tests fail**

Run: `go test ./tools/benchmark -run 'TestDiagnostic|TestPrometheus' -count=1`

Expected: FAIL with missing collectors.

- [ ] **Step 3: Implement read-only queries and bounded Prometheus text parsing**

The diagnostic transaction begins with `SET TRANSACTION READ ONLY`; queries use
parameters and select only known columns. Resource queries return individual
unavailable observations on permission errors. Prometheus parsing accepts metric
sample lines, preserves label sets, rejects non-finite values, and limits input
size.

- [ ] **Step 4: Run collector tests**

Run: `gofmt -w tools/benchmark/diagnostic.go tools/benchmark/prometheus.go tools/benchmark/diagnostic_test.go tools/benchmark/prometheus_test.go`

Run: `go test ./tools/benchmark -run 'TestDiagnostic|TestPrometheus' -count=1`

Expected: PASS.

- [ ] **Step 5: Commit diagnostic collectors**

```bash
git add tools/benchmark/diagnostic.go tools/benchmark/prometheus.go tools/benchmark/diagnostic_test.go tools/benchmark/prometheus_test.go
git commit -m "feat: collect benchmark diagnostics"
```

### Task 6: Correlation, lifecycle formulas, and correctness findings

**Files:**
- Create: `tools/benchmark/correlation.go`
- Test: `tools/benchmark/correlation_test.go`

- [ ] **Step 1: Write failing tests for formulas, provenance, duplicate side effects, payload mutation, and polling bounds**

```go
func TestCorrelateComputesDiagnosticLifecycle(t *testing.T) {
	record := fixtureExecutionRecord()
	record.Diagnostic = &DiagnosticExecution{
		CreatedAt: at(100), ClaimedAt: ptrTime(at(120)),
		Attempts: []DiagnosticAttempt{{StartedAt: at(125), FinishedAt: at(150)}},
	}
	got := correlate(record)
	assertMeasurement(t, got, "queue_wait_ms", 20, ProvenanceDatabase)
	assertMeasurement(t, got, "claim_to_dispatch_ms", 5, ProvenanceDerived)
	assertMeasurement(t, got, "webhook_rtt_ms", 25, ProvenanceDatabase)
}

func TestCorrelateReportsConcurrentDuplicateBeforeLatency(t *testing.T) {
	record := fixtureExecutionRecord()
	record.Callbacks = []CallbackObservation{
		{ExecutionID: "exec-1", AttemptID: "a1", ConcurrentForExecution: 1},
		{ExecutionID: "exec-1", AttemptID: "a2", ConcurrentForExecution: 2},
	}
	got := correlate(record)
	if !hasCritical(got.Findings, "concurrent_duplicate_delivery") {
		t.Fatalf("findings: %+v", got.Findings)
	}
}
```

- [ ] **Step 2: Verify tests fail**

Run: `go test ./tools/benchmark -run TestCorrelate -count=1`

Expected: FAIL with undefined correlation functions.

- [ ] **Step 3: Implement joins and formulas**

Correlate by execution ID and attempt ID, never by arrival order alone. Emit
unavailable measurements for missing exact terminal timestamps and worker IDs.
Compute retry backoff from consecutive diagnostic attempt boundaries, using
`[]time.Duration{0, 30*time.Second, 2*time.Minute, 10*time.Minute}` only as an
expected policy description—not as runtime behavior.

- [ ] **Step 4: Run correlation tests with race detector**

Run: `gofmt -w tools/benchmark/correlation.go tools/benchmark/correlation_test.go`

Run: `go test -race ./tools/benchmark -run TestCorrelate -count=1`

Expected: PASS.

- [ ] **Step 5: Commit correlation**

```bash
git add tools/benchmark/correlation.go tools/benchmark/correlation_test.go
git commit -m "feat: correlate execution lifecycle observations"
```

### Task 7: Scenario registry and delivery scenarios

**Files:**
- Create: `tools/benchmark/scenarios.go`
- Create: `tools/benchmark/scenario_delivery.go`
- Test: `tools/benchmark/scenarios_test.go`

- [ ] **Step 1: Write failing tests for registry coverage, safe smoke flow, deterministic concurrency, skips, and cleanup**

```go
func TestScenarioRegistryContainsRequiredScenarios(t *testing.T) {
	registry := scenarioRegistry()
	for _, name := range []string{
		"smoke", "baseline", "cold-warm", "warm-sequential", "concurrent",
		"control-plane", "recurring", "slow-receiver", "retry",
		"duplicate-race", "crash-recovery", "leader-failover", "database-outage",
	} {
		if _, ok := registry[name]; !ok {
			t.Errorf("missing scenario %q", name)
		}
	}
}

func TestFastRetryProfileIsExplicitlySkipped(t *testing.T) {
	env := fakeScenarioEnvironment{Config: Config{RetryProfile: "fast-test"}}
	result := runRetry(context.Background(), &env)
	if result.Status != ScenarioSkipped || !strings.Contains(result.Reason, "no test-only retry injection") {
		t.Fatalf("result=%+v", result)
	}
}
```

- [ ] **Step 2: Verify scenario tests fail**

Run: `go test ./tools/benchmark -run 'TestScenario|TestFastRetry' -count=1`

Expected: FAIL with undefined scenario registry and runners.

- [ ] **Step 3: Implement the common runner and delivery workloads**

Each scenario receives interfaces for API, receiver, diagnostics, metrics,
clock, and cleanup. Warm-up records are marked and excluded from measured
statistics. Concurrent batches use a semaphore and deterministic input order.
Recurring results require multiple real minute ticks. Retry subcases each use an
isolated job/behavior plan. Scenario errors are accumulated without hiding
completed raw observations.

- [ ] **Step 4: Run scenario tests**

Run: `gofmt -w tools/benchmark/scenarios.go tools/benchmark/scenario_delivery.go tools/benchmark/scenarios_test.go`

Run: `go test -race ./tools/benchmark -run 'TestScenario|TestFastRetry' -count=1`

Expected: PASS.

- [ ] **Step 5: Commit delivery scenarios**

```bash
git add tools/benchmark/scenarios.go tools/benchmark/scenario_delivery.go tools/benchmark/scenarios_test.go
git commit -m "feat: add benchmark delivery scenarios"
```

### Task 8: Guarded Compose environment and resilience scenarios

**Files:**
- Create: `tools/benchmark/environment.go`
- Create: `tools/benchmark/scenario_resilience.go`
- Create: `tools/benchmark/docker-compose.yml`
- Test: `tools/benchmark/environment_test.go`
- Test: `tools/benchmark/scenario_resilience_test.go`

- [ ] **Step 1: Write failing tests for ownership validation, command targeting, missing capabilities, and duplicate-race evidence**

```go
func TestComposeControllerRejectsUnownedProject(t *testing.T) {
	controller := composeController{Project: "production", OwnedPrefix: "cronlite-benchmark-"}
	if err := controller.validateDestructiveTarget(); !errors.Is(err, ErrUnownedComposeProject) {
		t.Fatalf("expected ownership error, got %v", err)
	}
}

func TestCrashScenarioSkipsWithoutProcessControl(t *testing.T) {
	env := fakeResilienceEnvironment{Capabilities: Capabilities{DockerCompose: false}}
	got := runCrashRecovery(context.Background(), &env)
	if got.Status != ScenarioSkipped {
		t.Fatalf("result=%+v", got)
	}
}

func TestDuplicateRaceUsesReceiverEvidence(t *testing.T) {
	env := duplicateFixtureEnvironment()
	got := runDuplicateRace(context.Background(), &env)
	if !hasCritical(got.Findings, "concurrent_duplicate_delivery") {
		t.Fatalf("findings=%+v", got.Findings)
	}
}
```

- [ ] **Step 2: Verify resilience tests fail**

Run: `go test ./tools/benchmark -run 'TestCompose|TestCrashScenario|TestDuplicateRace' -count=1`

Expected: FAIL with missing controller and scenario implementations.

- [ ] **Step 3: Implement guarded process control and resilience orchestration**

Use `exec.CommandContext` with argument slices, never a shell. Restrict stop,
start, pause, unpause, down, and volume removal to project names beginning with
`cronlite-benchmark-`. Detect the leader through existing metrics. Record each
control action interval and readiness transition. The Compose file uses
database dispatch, multiple worker-capable instances, metrics, reconciler,
mounted migrations, and host-gateway receiver access.

- [ ] **Step 4: Validate Compose configuration and run tests**

Run: `gofmt -w tools/benchmark/environment.go tools/benchmark/scenario_resilience.go tools/benchmark/environment_test.go tools/benchmark/scenario_resilience_test.go`

Run: `go test -race ./tools/benchmark -run 'TestCompose|TestCrashScenario|TestDuplicateRace' -count=1`

Expected: PASS.

Run when Docker Compose is available:
`docker compose -f tools/benchmark/docker-compose.yml config --quiet`

Expected: exit 0. If Docker is unavailable, record the command as skipped for
local validation while retaining the unit tests.

- [ ] **Step 5: Commit environment and resilience scenarios**

```bash
git add tools/benchmark/environment.go tools/benchmark/scenario_resilience.go tools/benchmark/docker-compose.yml tools/benchmark/environment_test.go tools/benchmark/scenario_resilience_test.go
git commit -m "feat: add guarded resilience benchmarks"
```

### Task 9: Atomic JSON/CSV export and correctness-first report

**Files:**
- Create: `tools/benchmark/export.go`
- Create: `tools/benchmark/report.go`
- Test: `tools/benchmark/export_test.go`
- Test: `tools/benchmark/report_test.go`
- Test fixture: `tools/benchmark/testdata/report.golden.md`

- [ ] **Step 1: Write failing tests for stable schemas, atomic replacement, redaction, and report ordering**

```go
func TestCSVHasOneRowPerAttemptAndStableHeader(t *testing.T) {
	var out bytes.Buffer
	if err := writeCSV(&out, fixtureRunResult()); err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(bytes.NewReader(out.Bytes())).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	wantHeader := []string{"run_id", "scenario", "correlation_id", "job_id", "execution_id", "attempt_id", "attempt", "status_code", "signature_valid", "duplicate", "measurement_name", "measurement_ms", "provenance", "error_class"}
	if !reflect.DeepEqual(wantHeader, rows[0]) {
		t.Fatalf("header mismatch: want=%v got=%v", wantHeader, rows[0])
	}
}

func TestReportPutsCriticalFindingsBeforePerformance(t *testing.T) {
	report := renderReport(fixtureRunResult())
	critical := strings.Index(report, "CRITICAL:")
	performance := strings.Index(report, "## API Latency")
	if critical < 0 || performance < 0 || critical > performance {
		t.Fatalf("incorrect report ordering:\n%s", report)
	}
}
```

- [ ] **Step 2: Verify export/report tests fail**

Run: `go test ./tools/benchmark -run 'TestCSV|TestReport' -count=1`

Expected: FAIL with missing writers.

- [ ] **Step 3: Implement atomic exporters and Markdown report**

Write temporary files in the destination directory, `Sync`, close, and rename.
JSON uses an explicit schema version. CSV documents and emits the stable header.
The report contains all 17 required sections, formula notes, skipped scenarios,
small-sample warnings, raw paths, and prominent signature/duplicate/final-status
failures before timing tables.

- [ ] **Step 4: Run golden and race tests**

Run: `gofmt -w tools/benchmark/export.go tools/benchmark/report.go tools/benchmark/export_test.go tools/benchmark/report_test.go`

Run: `go test -race ./tools/benchmark -run 'TestCSV|TestReport' -count=1`

Expected: PASS.

- [ ] **Step 5: Commit output generation**

```bash
git add tools/benchmark/export.go tools/benchmark/report.go tools/benchmark/export_test.go tools/benchmark/report_test.go tools/benchmark/testdata/report.golden.md
git commit -m "feat: export benchmark results and report"
```

### Task 10: CLI integration and one-command execution

**Files:**
- Create: `tools/benchmark/main.go`
- Test: `tools/benchmark/main_test.go`

- [ ] **Step 1: Write failing tests for help, default smoke wiring, output paths, cleanup, and correctness exit status**

```go
func TestRunWritesAllRequiredOutputs(t *testing.T) {
	dir := t.TempDir()
	deps := fixtureDependencies()
	code := run(context.Background(), []string{"--output", dir}, deps)
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	for _, name := range []string{"benchmark-results.json", "benchmark-results.csv", "benchmark-report.md"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestRunFailsOnCorrectnessWhenConfigured(t *testing.T) {
	deps := fixtureDependenciesWithCriticalFinding()
	code := run(context.Background(), []string{"--fail-on-correctness"}, deps)
	if code == 0 {
		t.Fatal("expected non-zero correctness exit")
	}
}
```

- [ ] **Step 2: Verify CLI tests fail**

Run: `go test ./tools/benchmark -run TestRun -count=1`

Expected: FAIL with undefined `run`.

- [ ] **Step 3: Implement CLI lifecycle**

Parse flags without reading secrets from positional arguments, start the
receiver before creating jobs, collect environment/commit/command metadata,
snapshot metrics/resources, execute requested scenarios, perform scoped cleanup,
export results even after scenario failures, print output paths, and return:

- `0` for completed runs without configured correctness violations;
- `1` for runtime/setup/output failures;
- `2` for invalid configuration;
- `3` for configured correctness-threshold violations.

- [ ] **Step 4: Run CLI tests and build**

Run: `gofmt -w tools/benchmark/main.go tools/benchmark/main_test.go`

Run: `go test -race ./tools/benchmark -run TestRun -count=1`

Expected: PASS.

Run: `go build ./tools/benchmark`

Expected: exit 0.

- [ ] **Step 5: Commit the command**

```bash
git add tools/benchmark/main.go tools/benchmark/main_test.go
git commit -m "feat: wire benchmark command"
```

### Task 11: Documentation and generated local example

**Files:**
- Modify: `.gitignore`
- Create: `tools/benchmark/README.md`
- Create: `tools/benchmark/example-output/benchmark-results.json`
- Create: `tools/benchmark/example-output/benchmark-results.csv`
- Create: `tools/benchmark/example-output/benchmark-report.md`

- [ ] **Step 1: Document the command, output schemas, formulas, scenario costs, safety, and limitations**

The README must include:

```bash
go run ./tools/benchmark \
  --base-url http://localhost:8080 \
  --api-key "$CRONLITE_API_KEY" \
  --scenario smoke \
  --output ./benchmark-output
```

It also documents managed Compose, diagnostic mode, retry duration, recurring
minute resolution, explicit disruptive flags, CSV columns, measurement
provenance, formulas, cleanup behavior, redaction, and all known instrumentation
gaps.

- [ ] **Step 2: Add narrow ignore exceptions**

```gitignore
!tools/benchmark/README.md
!tools/benchmark/example-output/
!tools/benchmark/example-output/benchmark-report.md
```

Keep arbitrary `benchmark-output` directories ignored or untracked.

- [ ] **Step 3: Run the safe local smoke scenario**

Run:

```bash
go run ./tools/benchmark \
  --start-compose \
  --scenario smoke \
  --sample-count 10 \
  --output tools/benchmark/example-output
```

Expected: three output files. If Docker is unavailable, run the CLI against an
`httptest`-backed fixture generator and mark the report environment/scenarios as
synthetic/skipped rather than inventing local CronLite measurements.

- [ ] **Step 4: Inspect generated artifacts for secrets and required sections**

Run:

```bash
rg -n 'CRONLITE_API_KEY|postgres://[^ ]+:[^@ ]+@|webhook_secret' tools/benchmark/example-output
```

Expected: no secret values.

Run:

```bash
rg -n '^## (Environment and Configuration|Correctness Findings|Scenario Summary|Baseline HTTP Latency|API Latency|Scheduler Accuracy|Delivery Latency|Retry Timing|Throughput|Failure Analysis|Duplicate-Delivery Findings|Resource Usage|Limitations|Reproduction Instructions|Raw File Locations)' tools/benchmark/example-output/benchmark-report.md
```

Expected: every required section is present.

- [ ] **Step 5: Commit documentation and example output**

```bash
git add .gitignore tools/benchmark/README.md tools/benchmark/example-output
git commit -m "docs: document benchmark harness"
```

### Task 12: Full repository verification

**Files:**
- Modify only files required to correct failures caused by the benchmark harness.

- [ ] **Step 1: Verify formatting and diff hygiene**

Run: `gofmt -w tools/benchmark`

Run: `git diff --check`

Expected: no output.

- [ ] **Step 2: Run benchmark unit and race suites**

Run: `go test ./tools/benchmark -count=1`

Expected: PASS.

Run: `go test -race ./tools/benchmark -count=1`

Expected: PASS with no race report.

- [ ] **Step 3: Run all repository tests and vet**

Run: `go test ./... -count=1`

Expected: PASS.

Run: `go test -race ./... -count=1`

Expected: PASS with no race report.

Run: `go vet ./...`

Expected: exit 0.

- [ ] **Step 4: Run the repository quality/style checker**

Run: `./scripts/quality.sh`

Expected: exit 0. Existing warnings may remain, but no new benchmark-caused
failure is accepted.

- [ ] **Step 5: Review scope and commit verification-only corrections**

Run: `git status --short`

Run: `git diff --stat main...HEAD`

Expected: only benchmark tooling, its documentation/example, the design/plan,
and narrow ignore exceptions.

If verification required corrections:

```bash
git add tools/benchmark .gitignore
git commit -m "test: finalize benchmark harness verification"
```

Do not create an empty commit when no corrections were required. Do not push.
