# CronLite Benchmark and Diagnostic Harness

This tool measures the observable lifecycle of CronLite executions. It records
API calls, scheduler timestamps, webhook callbacks, retry attempts, terminal
status polling, optional PostgreSQL state, Prometheus metrics, correctness
failures, and environment metadata.

It is test tooling. It does not change CronLite's API, schema, retry policy,
scheduler behavior, or production defaults.

## Quick start

With CronLite already running:

```bash
export CRONLITE_API_KEY="ec_..."

go run ./tools/benchmark \
  --base-url http://localhost:8080 \
  --api-key "$CRONLITE_API_KEY" \
  --scenario smoke \
  --output ./benchmark-output
```

The safe default is equivalent to `--scenario smoke --sample-count 10`. It
starts an embedded receiver, creates one benchmark-owned job, manually triggers
ten executions, waits for callbacks and terminal statuses, writes all three
outputs, and deletes the job.

Prefer the `CRONLITE_API_KEY` environment variable instead of `--api-key` in
shell history:

```bash
CRONLITE_API_KEY="ec_..." \
go run ./tools/benchmark --output ./benchmark-output
```

## Managed local environment

The harness can start an isolated three-instance database-dispatch environment:

```bash
go run ./tools/benchmark \
  --start-compose \
  --diagnostic \
  --scenario smoke \
  --sample-count 10 \
  --output ./benchmark-output
```

This uses `tools/benchmark/docker-compose.yml`, a unique
`cronlite-benchmark-*` Compose project, PostgreSQL 16, three CronLite instances,
database dispatch, two workers per instance, Prometheus metrics, and the
reconciler.

The environment is left running by default for inspection. To remove the
harness-owned containers and volume after the run, explicitly authorize it:

```bash
go run ./tools/benchmark \
  --start-compose \
  --diagnostic \
  --scenario smoke \
  --cleanup-environment \
  --allow-disruptive \
  --output ./benchmark-output
```

Cleanup validation rejects project names that do not begin with
`cronlite-benchmark-`. Process-control commands accept only the services defined
in the benchmark Compose file.

Managed ports are:

| Component | Host address |
|---|---|
| CronLite instance 1 | `127.0.0.1:18080` |
| CronLite instance 2 | `127.0.0.1:18081` |
| CronLite instance 3 | `127.0.0.1:18082` |
| PostgreSQL | `127.0.0.1:15432` |
| Embedded receiver | `0.0.0.0:19090` |

## Measurement modes

### Black-box mode

Black-box mode is the default. It uses only interfaces available to a normal
CronLite customer:

- REST API;
- health endpoints;
- execution inspection;
- webhook callbacks;
- Prometheus metrics when accessible.

It does not query CronLite's database.

### Diagnostic mode

Enable read-only PostgreSQL inspection with:

```bash
CRONLITE_BENCHMARK_DATABASE_URL="postgres://user:pass@localhost/cronlite?sslmode=disable" \
go run ./tools/benchmark \
  --diagnostic \
  --scenario smoke \
  --output ./benchmark-output
```

Every diagnostic query runs in a transaction initialized with:

```sql
SET TRANSACTION READ ONLY
```

Diagnostic mode observes execution creation, scheduling, firing, claiming,
attempt start/finish, status, queue depth, active executions, connection count,
and database size where permissions allow.

## Scenario reference

| Scenario | Purpose | Typical cost | Guard |
|---|---|---:|---|
| `smoke` | Create, trigger, receive, verify, inspect | seconds | none |
| `baseline` | CronLite health RTT, direct receiver RTT, optional DB RTT | seconds | none |
| `cold-warm` | First delivery versus a warmed delivery | seconds | none |
| `warm-sequential` | Warm-up followed by one-at-a-time manual triggers | seconds/minutes | none |
| `concurrent` | Configured concurrency levels and queue/tail behavior | minutes | bounded by flags |
| `control-plane` | Get/list/update/pause/resume/delete latency | seconds | none |
| `slow-receiver` | 100 ms, 500 ms, 2 s, and near-timeout responses | minutes | none |
| `retry` | 500/503/429/400, timeout, connection failure, eventual success | over 12 minutes | real production policy |
| `recurring` | Multiple real minute-resolution cron ticks | multiple minutes | none |
| `duplicate-race` | Stale requeue versus active delivery | minutes | `--allow-disruptive` |
| `crash-recovery` | Stop/restart a dispatcher during delivery | minutes | `--allow-disruptive` |
| `leader-failover` | Stop the leader and observe takeover | minutes | `--allow-disruptive` |
| `database-outage` | Pause/unpause harness-owned PostgreSQL | minutes | `--allow-disruptive` |
| `load` | Explicit high-load concurrent run | workload-dependent | `--allow-disruptive` |

Select more than one scenario with a comma:

```bash
go run ./tools/benchmark \
  --scenario baseline,smoke,warm-sequential,concurrent \
  --sample-count 100 \
  --concurrency 1,5,10,25,50 \
  --output ./benchmark-output
```

`--scenario all` expands to every scenario and therefore requires
`--allow-disruptive`. It includes real recurring ticks and production retry
backoff, so it is intentionally long-running.

### Retry profiles

`--retry-profile real-policy` uses CronLite's unchanged policy:

```text
attempt 1: immediate
attempt 2: 30 seconds
attempt 3: 2 minutes
attempt 4: 10 minutes
```

`--retry-profile fast-test` is accepted so automation can request the profile,
but the retry scenario is marked skipped. CronLite currently has no runtime or
build-tagged test-only retry injection. The harness does not modify production
retry defaults to shorten a benchmark.

### Recurring versus manual timing

CronLite recurring cron expressions have one-minute resolution. Recurring
results measure scheduler accuracy and drift across real ticks. Manual triggers
measure millisecond-level API, dispatch, callback, and persistence behavior.
The report never merges the two sample groups.

## Important flags

| Flag | Default | Description |
|---|---|---|
| `--base-url` | `http://127.0.0.1:8080` | CronLite customer-facing URL |
| `--api-key` | `CRONLITE_API_KEY` | Bearer key; environment use is preferred |
| `--receiver-addr` | `127.0.0.1:9090` | Embedded receiver listen address |
| `--receiver-public-url` | `http://127.0.0.1:9090` | URL reachable from CronLite |
| `--scenario` | `smoke` | Comma-separated scenarios or `all` |
| `--sample-count` | `10` | Measured samples per workload |
| `--concurrency` | `1,5,10,25,50` | Concurrent delivery levels |
| `--timeout` | `45s` | HTTP and per-execution polling timeout |
| `--poll-interval` | `100ms` | Terminal-status polling interval |
| `--diagnostic` | false | Enable read-only PostgreSQL observations |
| `--database-url` | environment | Diagnostic PostgreSQL URL |
| `--metrics-url` | `/metrics` on base port | Existing Prometheus endpoint |
| `--retry-profile` | `real-policy` | `real-policy` or `fast-test` |
| `--random-seed` | `1` | Reproducible data generation seed |
| `--keep-data` | false | Keep benchmark-created jobs |
| `--fail-on-correctness` | false | Exit 3 for critical correctness findings |
| `--allow-disruptive` | false | Authorize load/crash/outage scenarios |
| `--allow-non-local` | false | Authorize a non-loopback CronLite target |
| `--start-compose` | false | Start the isolated local stack |
| `--cleanup-environment` | false | Remove the owned stack and volume afterward |

Run `go run ./tools/benchmark --help` for the complete list.

## Correlation and receiver behavior

Every intended execution gets:

- benchmark run ID;
- scenario name;
- UUID benchmark correlation ID;
- job ID;
- execution ID;
- attempt/event ID;
- target URL;
- sample number and warm-up marker.

The receiver verifies `X-CronLite-Signature` using constant-time HMAC-SHA256,
bounds callback bodies to 64 KiB, and records UTC nanosecond timestamps. It
detects:

- repeated execution callbacks;
- repeated attempt IDs;
- simultaneous callbacks for one execution;
- callbacks after terminal status was observed;
- invalid signatures;
- callback payload changes.

Receiver observations are the authority for duplicate HTTP side effects. A
single final execution row does not erase evidence that a webhook was delivered
more than once.

## Timestamp provenance

Each measurement is labeled:

| Provenance | Meaning |
|---|---|
| `directly_observed` | Recorded by the benchmark client or receiver |
| `derived` | Calculated from two observations or a polling bound |
| `database_observed` | Read from CronLite's PostgreSQL state |
| `unavailable` | Unsupported, skipped, or missing with a reason |

Go's monotonic clock component measures durations inside the harness. UTC
RFC3339-nanosecond timestamps correlate different processes. Cross-process
durations therefore depend on clock synchronization.

## Formulas

The report uses milliseconds:

```text
api_trigger_latency_ms =
    trigger_response_received - trigger_request_start

scheduler_lag_ms =
    execution_created_at - intended_scheduled_at

queue_wait_ms =
    claimed_at - execution_created_at

claim_to_dispatch_ms =
    first_attempt_started_at - claimed_at

webhook_rtt_ms =
    attempt_finished_at - attempt_started_at

receiver_processing_ms =
    receiver_response_completed - receiver_arrival

retry_backoff_actual_ms =
    next_attempt_started_at - previous_attempt_finished_at

retry_backoff_error_ms =
    retry_backoff_actual_ms - configured_backoff_ms

end_to_end_delivery_ms (manual) =
    receiver_arrival - trigger_request_start

end_to_end_delivery_ms (recurring) =
    receiver_arrival - intended_scheduled_at

terminal_persistence_lag_ms =
    first_terminal_poll_observation - final_callback_response_completed
```

CronLite does not persist the exact terminal status update time.
`terminal_persistence_lag_ms` is therefore an upper bound whose uncertainty
includes `--poll-interval`.

## Statistics

Every timing distribution includes:

- sample count;
- minimum and maximum;
- arithmetic mean;
- median;
- sample standard deviation;
- p50, p90, p95, and p99 using nearest-rank percentiles.

The report warns when p90 has fewer than 10 samples, p95 fewer than 20, or p99
fewer than 100. It also reports throughput, success rate, retry rate, permanent
failure rate, duplicate rate, signature-failure rate, HTTP status distribution,
error classification, and queue/resource observations when available.

Warm-up samples remain in raw output but are excluded from steady-state
statistics.

## Output files

Every completed run writes atomically:

```text
benchmark-results.json
benchmark-results.csv
benchmark-report.md
```

### JSON

The JSON document has `schema_version: "1.0"` and retains full raw:

- environment and redacted configuration;
- API, callback, metric, database, and process-control observations;
- execution and attempt identifiers;
- timestamps and polling bounds;
- measurements with provenance;
- scenario failures and skips;
- correctness findings;
- limitations.

### CSV

The CSV has one row per delivery attempt, or one execution row when no attempt
is observable. Its stable columns are:

```text
run_id
scenario
correlation_id
job_id
execution_id
attempt_id
attempt
status_code
signature_valid
duplicate
warmup
trigger_type
final_status
error_class
scheduled_at
fired_at
execution_created_at
attempt_started_at
attempt_finished_at
receiver_arrived_at
api_trigger_latency_ms
scheduler_lag_ms
queue_wait_ms
claim_to_dispatch_ms
webhook_rtt_ms
receiver_processing_ms
terminal_persistence_lag_ms
end_to_end_delivery_ms
measurement_provenance
```

### Markdown

The report puts correctness failures before performance numbers and contains:

1. Environment and configuration
2. Correctness findings
3. Scenario summary
4. Baseline HTTP latency
5. API latency
6. Scheduler accuracy
7. Delivery latency
8. Retry timing
9. Throughput
10. Failure analysis
11. Duplicate-delivery findings
12. Resource usage
13. Limitations
14. Reproduction instructions
15. Raw-file locations

The run header also records commit SHA, exact redacted command, start/finish
times, and duration.

## Exit codes

| Code | Meaning |
|---:|---|
| 0 | Run completed; configured correctness threshold was not violated |
| 1 | Setup, scenario runtime, process-control, or output failure |
| 2 | Invalid or unsafe configuration |
| 3 | Critical correctness finding with `--fail-on-correctness` |

Outputs are still written after scenario failures whenever setup and output
storage remain available.

## Safety

- Non-loopback CronLite targets require `--allow-non-local`.
- Duplicate, load, crash, leader, and database-outage scenarios require
  `--allow-disruptive`.
- Environment deletion requires `--start-compose --allow-disruptive`.
- Compose deletion only targets `cronlite-benchmark-*` projects.
- Created jobs are deleted unless `--keep-data` is set.
- API keys, webhook secrets, authorization headers, PostgreSQL credentials, and
  receiver behavior tokens are redacted from output.
- Request/response bodies and Prometheus responses are bounded.
- Diagnostic SQL is parameterized and read-only.

Do not point aggressive workloads at a shared or production service without
explicit authorization and capacity planning.

## Current instrumentation gaps

The harness reports these gaps instead of changing CronLite:

1. Public execution responses omit delivery attempts even though the endpoint
   description says it returns them.
2. `claimed_at` exists in PostgreSQL but is not public.
3. Terminal status update time is not persisted.
4. Attempt start/finish times are stored but not returned by the public API.
5. Worker or instance identity is not attached to executions or attempts.
6. Webhook payloads have execution IDs but no customer-supplied correlation ID.
7. Retry backoff has no test-only runtime injection.
8. Exact final-response-to-terminal-persistence time is unavailable; API
   polling provides only a bound.
9. A database claim transaction commits before HTTP delivery, so stale requeue
   behavior must be verified from receiver side effects.

## General limitations

- Recurring cron resolution is one minute.
- Cross-process timings depend on synchronized clocks.
- Same-machine Docker introduces virtualization and contention noise.
- Local results are regression baselines, not universal capacity guarantees.
- Tail percentiles need adequate samples.
- Production network conditions can differ substantially.
- Diagnostic database visibility is not customer-visible behavior.
- A benchmark can reveal duplicates but cannot mathematically prove every race
  is absent.
- Missing Docker, PostgreSQL, permissions, or platform features produce
  explicit skipped scenarios.

## Example output

`tools/benchmark/example-output` contains a locally generated example. Read its
environment and scenario status before interpreting measurements. If Docker was
unavailable on the generating machine, the report marks the managed CronLite
scenario as skipped/synthetic rather than inventing production measurements.
