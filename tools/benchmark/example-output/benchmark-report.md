# CronLite Benchmark Report

- Run ID: `example-docker-unavailable`
- Schema version: `1.0`
- Started: `2026-07-28T04:38:55.800288Z`
- Finished: `2026-07-28T04:38:55.944438Z`
- Duration: `144ms`

## Environment and Configuration

| Field | Value |
|---|---|
| Commit SHA | 1fe908c7443d1a63bd3bf204a0073d436b482236 |
| Operating system | darwin |
| Architecture | arm64 |
| CPU count | 8 |
| Memory bytes | 8589934592 |
| Go version | go1.25.8 |
| Docker version |  |
| PostgreSQL version |  |
| Dispatch mode | db |
| CronLite instances | 3 |
| Random seed | 1 |
| Retry profile | real-policy |
| Sample count | 10 |
| Diagnostic mode | true |

## Correctness Findings

No correctness failures were observed in the executed scenarios.

## Scenario Summary

| Scenario | Status | Executions | Callbacks | Failures | Duplicates | Duration |
|---|---:|---:|---:|---:|---:|---:|
| smoke | skipped | 0 | 0 | 0 | 0 | 144ms |

smoke: managed local smoke skipped: Docker daemon was unavailable

## Baseline HTTP Latency

No measurements were available.

## API Latency

No measurements were available.

## Scheduler Accuracy

No measurements were available.

## Delivery Latency

No measurements were available.

## Retry Timing

No measurements were available.

## Throughput

| Scenario | Execution throughput/s | Callback throughput/s | Success rate | Retry rate |
|---|---:|---:|---:|---:|
| smoke | 0.000 | 0.000 | 0.00% | 0.00% |

## Failure Analysis

- Permanent failures: 0
- Signature-verification failures: 0
- HTTP status distribution: ``
- Error classification: ``

## Duplicate-Delivery Findings

- Executions evaluated: 0
- Executions with duplicate callback evidence: 0 (0.0000%)
- Concurrent duplicate findings: 0

## Resource Usage

Resource collection was unavailable or not enabled for this run.

## Limitations

- CronLite recurring cron resolution is one minute; manual and recurring measurements are separate.
- Cross-process wall-clock measurements depend on clock synchronization.
- Same-machine Docker results include operating-system, virtualization, and resource-contention noise.
- Local results are regression baselines, not universal production capacity guarantees.
- High percentiles are unstable at small sample counts.
- Production network conditions may differ substantially.
- Diagnostic database inspection is internal test visibility, not the customer experience.
- A benchmark can reveal duplicates but cannot prove the absence of every race.
- Terminal status update time and worker identity are not persisted by current instrumentation.
- The public execution API does not expose delivery attempts or claimed_at.
- Fast retry injection is unavailable; real-policy retries use production backoff durations.
- Unavailable Docker, PostgreSQL, permission, and platform capabilities are reported as skipped.

## Reproduction Instructions

```bash
go run ./tools/benchmark --start-compose --diagnostic --scenario smoke --sample-count 10 --output tools/benchmark/example-output
```

## Raw File Locations

- JSON: `/Users/jesseelorddushime/Documents/Projects/cronlite/tools/benchmark/example-output/benchmark-results.json`
- CSV: `/Users/jesseelorddushime/Documents/Projects/cronlite/tools/benchmark/example-output/benchmark-results.csv`
- Report: `/Users/jesseelorddushime/Documents/Projects/cronlite/tools/benchmark/example-output/benchmark-report.md`
