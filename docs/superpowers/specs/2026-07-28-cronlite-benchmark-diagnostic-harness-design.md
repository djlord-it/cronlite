# CronLite Benchmark and Diagnostic Harness Design

## Objective

Build a reproducible Go benchmark under `tools/benchmark` that measures the
complete observable lifecycle of CronLite executions. The harness must preserve
raw observations, distinguish customer-visible measurements from local
diagnostics, detect correctness failures before reporting performance, and avoid
changing CronLite production behavior.

The safe default is a small local smoke run. High-load, long-running,
multi-instance, destructive, or non-local runs require explicit flags.

## Language Choice

The harness will use Go because CronLite is already a Go module, the repository
requires Go 1.25, and the module already includes PostgreSQL and Prometheus
dependencies. Go also provides:

- monotonic duration measurement through `time.Time`;
- nanosecond UTC wall-clock observations for cross-process correlation;
- a race-detector-supported concurrency model for the webhook receiver;
- one-command execution without adding a Python environment or dependency lock;
- direct reuse of repository build, formatting, vet, test, and CI conventions.

Python would simplify ad hoc analysis, but it would add a second runtime and
dependency surface. Extending the existing HA shell scripts would reuse process
control, but shell is not suitable for typed lifecycle records, concurrent
callback correlation, signature validation, or statistical testing.

## Operating Modes

### Black-box mode

Black-box mode uses only customer-visible interfaces:

- REST API;
- `/health` and verbose health checks;
- the local webhook receiver;
- execution inspection endpoints;
- Prometheus metrics when enabled.

It never queries PostgreSQL. This mode is the default and remains usable against
a remote CronLite deployment when the user explicitly allows a non-local target.

### Diagnostic mode

Diagnostic mode adds read-only PostgreSQL queries and existing Prometheus
metrics. It does not add routes, modify schemas, or change production
configuration. Queries inspect:

- execution `scheduled_at`, `fired_at`, `created_at`, `claimed_at`, and status;
- delivery-attempt IDs, sequence numbers, status codes, errors, start times, and
  finish times;
- queue depth, active execution count, oldest queue-item age, connection count,
  and database size when permissions permit.

Database access uses a read-only transaction and rejects mutation statements.
Unavailable permissions or fields degrade the affected measurement instead of
blocking the run.

## Measurement Provenance

Every measurement and timestamp carries one of four provenance labels:

- `directly_observed`: recorded by the benchmark client or webhook receiver;
- `database_observed`: read from CronLite's PostgreSQL state;
- `derived`: calculated from observations, including polling bounds;
- `unavailable`: unsupported by current instrumentation or skipped capability.

Raw timestamps are stored separately from derived durations. Missing data is
represented explicitly with a reason; it is never silently converted to zero.

## Timestamp Sources

The harness records UTC wall-clock timestamps using `time.Now().UTC()` and
serializes them with RFC3339 nanosecond precision. Durations between events in
the same process use Go's monotonic clock component. Cross-process durations use
UTC wall clocks and are labeled with the clock-synchronization limitation.

For a polled status transition, the harness stores both the last non-terminal
observation and first terminal observation. The reported terminal persistence
time is a derived upper bound and includes the polling resolution. It is not
presented as an exact database update timestamp.

## Correlation Model

Each run has a UUID run ID. Each intended execution has a separate UUID
benchmark correlation ID and records:

- scenario name and sample index;
- job ID and execution ID;
- callback execution ID and attempt ID headers;
- scheduled time and target URL;
- API request interval;
- callback request interval;
- final status polling interval;
- database execution and delivery-attempt observations;
- externally supplied worker or instance identity when observable.

CronLite does not accept an execution-level external correlation field. The
harness therefore creates the correlation ID before triggering, captures the
execution ID from the trigger response, and joins callbacks by execution ID.
Callbacks that race ahead of the API response remain in the receiver's
append-only observation store and are joined after the response arrives.

Benchmark run and scenario identifiers are also placed in job tags. They are
not assumed to appear in callback payloads.

## Webhook Receiver

The receiver is embedded in the benchmark process for normal runs. Its listening
address and the public URL supplied to CronLite are separate so a Dockerized
CronLite instance can use `host.docker.internal`.

The receiver:

- bounds request bodies and retained error details;
- records arrival, response start, and response completion observations;
- verifies `X-CronLite-Signature` with constant-time HMAC-SHA256 comparison;
- records execution and event/attempt headers;
- parses and snapshots the webhook payload;
- supports concurrency safely;
- tracks active deliveries per execution;
- detects duplicate execution IDs, duplicate attempt IDs, overlapping delivery,
  post-terminal callback observations, invalid signatures, and payload changes;
- offers deterministic behavior plans for immediate success, delayed response,
  fixed status, status sequences, timeout/blocking, and connection failure
  targets.

Receiver behavior is selected by a run-scoped opaque path token so scenario
configuration is not inferred from untrusted callback data.

## API and Metrics Collection

The API client uses one bounded `http.Client`, attaches bearer authentication,
and captures request start, first response completion, status, bounded error
body, and decoded identifiers. It supports:

- health checks;
- create, get, list, update, pause, resume, delete, and trigger operations;
- execution polling to terminal status;
- cleanup of benchmark-created jobs.

The Prometheus collector snapshots existing CronLite metrics before and after
each scenario. Counter deltas and gauge observations are preserved as raw
measurements. The harness does not infer individual execution timings from
aggregate histogram buckets.

## Scenarios

Every scenario implements a common interface and returns a status of passed,
failed, or skipped with a structured reason.

### Safe scenarios

- `smoke`: 10 manual executions by default, full signature and final-status
  verification.
- `baseline`: HTTP RTT to CronLite health, direct receiver RTT, immediate-204
  receiver processing overhead, and optional PostgreSQL query latency.
- `cold-warm`: first health/API/delivery observations separated from warmed
  observations.
- `warm-sequential`: warm-up followed by one-at-a-time manual triggers.
- `concurrent`: bounded trigger batches at configured concurrency levels.
- `control-plane`: get/list/update/pause/resume/delete latency on harness-owned
  jobs.
- `slow-receiver`: 100 ms, 500 ms, 2 second, and configured near-timeout
  behavior.
- `retry`: 500, 503, 429, non-retryable 400, timeout, connection failure, and
  eventual-success behavior.
- `recurring`: minute-resolution cron ticks observed separately from manual
  dispatch.

The retry scenario supports `real-policy`. `fast-test` is reported unavailable
because the repository has no runtime or build-tagged retry injection. The
harness will not change the dispatcher defaults.

### Guarded scenarios

- `duplicate-race`: database dispatch with multiple workers/instances and a
  callback held beyond the configured requeue threshold.
- `crash-recovery`: stop a dispatcher during delivery, restart it, and measure
  callback/status recovery.
- `leader-failover`: stop the observed scheduler leader and measure readiness,
  leadership, and recurring-delivery recovery.
- `database-outage`: pause and restore the harness-owned PostgreSQL service.
- `load`: user-selected high sample counts or concurrency.

These require `--allow-disruptive`. They run only against localhost or a
harness-owned Compose project unless `--allow-non-local` is also provided.
Process-control scenarios are skipped with an explicit reason when Docker
Compose or required service metadata is unavailable.

## Local Environment Control

`tools/benchmark/docker-compose.yml` defines an isolated PostgreSQL service and
one or more CronLite instances using database dispatch. It uses a unique Compose
project name and exposes a test-only legacy API key. Linux host access adds the
`host-gateway` mapping; macOS uses Docker's standard
`host.docker.internal` mapping.

The harness can connect to an already running environment or start its own.
Cleanup defaults to deleting benchmark-created jobs. Environment teardown and
volume removal require an explicit cleanup option and target only the unique
project created by the harness.

Docker control is implemented behind an interface so command generation,
capability checks, and safety validation are unit tested without disrupting
services.

## Lifecycle Measurements

The normalized record includes, where available:

- `api_create_latency_ms`;
- `api_trigger_latency_ms`;
- `api_get_execution_latency_ms`;
- `api_list_executions_latency_ms`;
- control-plane mutation latencies;
- `scheduler_lag_ms = execution_created_or_fired - scheduled_at`;
- `queue_wait_ms = claimed_at - created_at`;
- `claim_to_dispatch_ms = attempt_started_at - claimed_at`;
- `webhook_rtt_ms = attempt_finished_at - attempt_started_at`;
- `receiver_processing_ms = response_completed - receiver_arrival`;
- `terminal_persistence_lag_ms`;
- `end_to_end_delivery_ms`;
- `retry_backoff_actual_ms`;
- `retry_backoff_error_ms = actual - configured`;
- time to final success or permanent failure.

For manual triggers, end-to-end time uses trigger request start. For recurring
executions, it uses intended `scheduled_at`. The two result groups are never
merged.

## Duplicate and Correctness Detection

Receiver observations are authoritative for side-effect duplication. A
duplicate is reported even if PostgreSQL ultimately contains one terminal row.
The report distinguishes:

- repeated execution callbacks;
- repeated attempt IDs;
- overlapping callbacks for one execution;
- multiple worker identities when headers or control metadata expose them;
- callbacks after the harness first observed a terminal state;
- signature failures;
- payload mutations;
- API/database final-status disagreements;
- missing callbacks and unexpected retry classifications.

Correctness failures appear before latency summaries and can make the process
exit non-zero when `--fail-on-correctness` is enabled.

## Statistics and Reporting

For each metric and scenario the harness reports:

- count, success count, failure count, and duplicate count;
- minimum, maximum, arithmetic mean, median, and sample standard deviation;
- p50, p90, p95, and p99 using a documented nearest-rank percentile rule;
- an instability warning when the sample size is too small for a percentile;
- execution and callback throughput;
- success, retry, permanent-failure, duplicate, and signature-failure rates;
- HTTP status and error-classification distributions;
- scheduling-lag and oldest-queue-item observations.

Outputs are written atomically:

- `benchmark-results.json`: versioned run metadata plus full execution, attempt,
  callback, API, database, metric, resource, error, and skip observations;
- `benchmark-results.csv`: one row per execution/attempt combination with a
  documented stable schema;
- `benchmark-report.md`: environment, command, duration, scenarios, baselines,
  API, scheduler, delivery, retries, throughput, correctness failures,
  duplicate findings, resources, limitations, reproduction instructions, and
  raw file locations.

Secrets, authorization headers, database passwords, webhook secrets, and
behavior tokens are redacted before serialization.

## Resource Measurements

Collection is best effort:

- Docker CPU and memory samples for CronLite and PostgreSQL;
- PostgreSQL connection count and database size;
- worker count from known configuration;
- queue depth, active/in-progress count, and oldest queue age from diagnostic
  queries;
- in-flight executions, open circuits, goroutine count, and leader state only
  when existing metrics expose them.

Unavailable resource collectors produce structured skip records and never block
the benchmark.

## CLI and Safety

The default command performs a local black-box smoke benchmark:

```bash
go run ./tools/benchmark \
  --base-url http://localhost:8080 \
  --api-key "$CRONLITE_API_KEY" \
  --output ./benchmark-output
```

Flags cover base URL, API key, receiver listen/public addresses, scenarios,
sample count, concurrency levels, timeout, dispatch mode, diagnostic database
URL, retry profile, output directory, seed, cleanup behavior, Compose startup,
correctness thresholds, and disruptive/non-local authorization.

Validation rejects:

- aggressive scenarios without explicit authorization;
- non-loopback targets without `--allow-non-local`;
- receiver public URLs that cannot be parsed;
- unbounded or nonsensical sample/concurrency values;
- diagnostic mode without a database URL;
- destructive Compose operations without a harness-owned project identity.

## File Boundaries

The implementation is split by responsibility:

- `main.go`: command entrypoint and exit status;
- `config.go`: flags, defaults, validation, and redacted configuration;
- `model.go`: versioned raw observation and provenance types;
- `api_client.go`: public CronLite REST observations;
- `receiver.go`: HMAC-verifying controlled webhook receiver;
- `correlation.go`: execution/attempt joins and correctness findings;
- `diagnostic.go`: read-only PostgreSQL inspection;
- `prometheus.go`: existing metric snapshots and deltas;
- `environment.go`: environment metadata and guarded Compose control;
- `scenarios.go`: scenario registry and shared execution contracts;
- `scenario_delivery.go`: baseline, smoke, sequential, concurrent, slow, retry,
  recurring, and cold/warm workloads;
- `scenario_resilience.go`: duplicate race, crash, leader, and database recovery;
- `statistics.go`: distributions, rates, throughput, and warnings;
- `export.go`: atomic JSON and CSV output;
- `report.go`: correctness-first Markdown report;
- matching `_test.go` files for all behavior and calculations;
- `README.md`: setup, CLI reference, schemas, formulas, safety, and limitations;
- `docker-compose.yml`: isolated optional benchmark environment;
- `example-output/`: locally generated example report and raw files.

No production package, API schema, migration, or runtime default is changed.

## Verification

Each implementation increment follows test-driven development and receives a
focused Conventional Commit. Before completion, verification includes:

```bash
gofmt -w tools/benchmark
go test ./tools/benchmark -count=1
go test -race ./tools/benchmark -count=1
go test ./... -count=1
go test -race ./... -count=1
go vet ./...
./scripts/quality.sh
```

The harness will also run the safe local scenarios supported by the current
machine and generate example output. Docker-dependent scenarios that cannot run
are recorded as skipped rather than represented as successful.

## Known Instrumentation Gaps

Repository inspection found these limitations:

1. The public execution response omits delivery attempts even though the
   endpoint summary says it includes them.
2. `claimed_at` exists in PostgreSQL but is absent from public execution models
   and most repository scans.
3. Execution rows do not persist a terminal status update timestamp.
4. Delivery attempts persist start and finish times, but the public API discards
   them.
5. Worker/instance identity is not stored with an execution or attempt.
6. The webhook payload has execution ID but no customer-supplied correlation
   field.
7. Retry backoff is fixed in the dispatcher and has no current test-only runtime
   injection.
8. Exact final-response-to-terminal-persistence latency is therefore
   unavailable; polling can only provide bounds.
9. Cross-process durations depend on synchronized clocks.
10. In database dispatch mode the claim transaction commits before HTTP
    delivery, so the reconciler does not retain a row lock throughout the
    delivery. The duplicate-race scenario must verify real HTTP side effects.

These gaps are reported by the harness. They are not fixed as part of this test
tooling task.
