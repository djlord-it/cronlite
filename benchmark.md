# CronLite Benchmark Results

## Scheduled webhooks you can trust

CronLite delivers reliably through normal traffic, retries, slow receivers, service restarts, leader changes, and database interruptions. This benchmark measures the full API-trigger-to-webhook lifecycle, including HMAC signature verification—not just synthetic requests per second.

| Result | Measured baseline |
| --- | --- |
| Steady-state callbacks delivered | 707 / 707 |
| Median end-to-end delivery | 72 ms |
| Delivery throughput | 25.6 deliveries/s |
| Leader failover | 1.4 s |
| PostgreSQL recovery | 2.2 s |
| Maximum retry timing error | Within 37 ms |
| Concurrent duplicate findings | 0 |
| Signed canary signature failures | 0 |

## Reliable delivery, end to end

The 707-callback suite covered cold and warm starts, sequential and concurrent workloads, control-plane operations, and slow receivers. The separate 20-execution signed canary verified every webhook HMAC. Across the measured suite, no signature-verification failure or concurrent duplicate finding was observed.

## Built to recover

Across three CronLite instances, leader failover completed in 1.4 seconds and PostgreSQL recovery in 2.2 seconds. A full dispatcher stop and restart recovered in-flight execution, while an active race produced no concurrent duplicate callbacks.

## Predictable retries

CronLite exercised 500, 503, 429, timeout, connection failure, non-retryable 400, and eventual-success scenarios using the unchanged production policy. The 30-second, 2-minute, and 10-minute backoffs stayed within 37 ms, with correct classifications throughout.

## Tested beyond the happy path

- Immediate and recurring schedules
- Sequential and concurrent delivery
- Slow receivers and timeouts
- HTTP failures and network failures
- Multi-instance recovery and leader failover
- PostgreSQL restore and duplicate-race coverage

## Test environment

Measured on macOS arm64 with 8 logical CPUs, 8 GiB memory, Docker 29.4.0, PostgreSQL 16.13, and three CronLite instances with two database dispatch workers each. These results are a measured, reproducible local regression baseline and may vary by environment.

## 2026-09-28 CPU, memory, and dispatch benchmark

These runs were captured on 2026-09-28 on macOS arm64, 8 logical CPUs and
8 GiB RAM, with Docker 29.4.0, PostgreSQL 16.13, and three CronLite instances
in DB dispatch mode (two poll workers per instance). They are local regression
measurements, not production capacity estimates. The harness reported base
commit `278d96d1`; the runs included the uncommitted changes for issues #109
and #102 that are included with this report.

The resource CSVs contain every timestamped Docker sample and its exact byte
count. Docker CPU percentage is relative to one CPU core, so values above 100%
are expected. The benchmark harness also writes full JSON, execution CSV, and
Markdown reports to the ignored `benchmark-output/` directory.

### Steady load

Run ID: `cd9be746-8986-48e4-b6b7-3167b6cce4d1`.

The `warm-sequential` run delivered all 500 measured executions plus one
warm-up in 71.801 seconds, with no correctness findings. It observed 6.964
measured executions/s, queue-wait p95 of 177.358 ms, and end-to-end delivery
p95 of 187.735 ms. Read-only diagnostics were enabled for the queue timings.

| Service | Samples | CPU avg / p95 / peak (%) | Memory avg / p95 / peak (MiB) |
|---|---:|---:|---:|
| CronLite 1 | 35 | 3.507 / 5.700 / 7.420 | 11.18 / 15.14 / 15.14 |
| CronLite 2 | 35 | 1.336 / 2.830 / 16.270 | 8.54 / 9.66 / 9.66 |
| CronLite 3 | 35 | 1.068 / 2.550 / 4.610 | 11.05 / 16.99 / 17.63 |
| PostgreSQL | 35 | 14.655 / 18.360 / 24.040 | 56.55 / 77.96 / 78.14 |

Raw data: [samples](benchmarks/2026-09-28/steady-resource-samples.csv),
[summary](benchmarks/2026-09-28/steady-resource-summary.csv).

```bash
go run ./tools/benchmark --start-compose --diagnostic \
  --scenario warm-sequential --sample-count 500 --timeout 90s \
  --fail-on-correctness --cleanup-environment --allow-disruptive \
  --output ./benchmark-output/issue-102-steady-final
```

### Saturation

Run ID: `e0736b41-5c9f-4e64-b9f8-cdb1757352d9`.

The `load` run delivered all 40,000 executions, 20,000 each at concurrency
100 and 250, in 61.473 seconds. It reported no correctness findings and
650.693 delivered executions/s, passing the 500/s local regression target.
End-to-end delivery p95 was 373.333 ms. Diagnostic queries were disabled so
the throughput gate measures application and database work without the
harness's extra per-execution SQL queries.

| Service | Samples | CPU avg / p95 / peak (%) | Memory avg / p95 / peak (MiB) |
|---|---:|---:|---:|
| CronLite 1 | 31 | 77.109 / 130.280 / 147.970 | 19.60 / 27.66 / 27.92 |
| CronLite 2 | 31 | 11.079 / 16.820 / 17.040 | 11.20 / 16.92 / 19.55 |
| CronLite 3 | 31 | 11.246 / 18.070 / 18.140 | 10.46 / 12.71 / 12.79 |
| PostgreSQL | 31 | 268.062 / 369.480 / 372.240 | 106.24 / 137.40 / 138.70 |

Raw data: [samples](benchmarks/2026-09-28/saturation-resource-samples.csv),
[summary](benchmarks/2026-09-28/saturation-resource-summary.csv).

```bash
go run ./tools/benchmark --start-compose \
  --scenario load --sample-count 20000 --concurrency 100,250 \
  --timeout 5m --min-load-throughput 500 --allow-disruptive \
  --fail-on-correctness --cleanup-environment \
  --output ./benchmark-output/issue-109-saturation-publish
```

The diagnostic pool is capped at four connections. A separate 10,000-execution
diagnostic run completed all deliveries at 418.627/s after that cap; the prior
uncapped run exhausted PostgreSQL's connections. Diagnostic mode changes the
load profile, so its throughput is not used for the regression gate. Docker
sampling averaged roughly two seconds between observations despite a one-second
requested interval. The raw timestamps show the actual spacing; runs on other
hardware should establish their own throughput target.
