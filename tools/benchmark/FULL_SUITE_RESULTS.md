# Local Full-Suite Results

This is a regression baseline from 2026-07-28, not a production-capacity
claim. It ran on macOS arm64 with 8 logical CPUs, 8 GiB memory, Docker 29.4.0,
PostgreSQL 16.13, and three CronLite instances with two DB-dispatch workers
each.

The raw JSON, CSV, and Markdown files are under the ignored local
`benchmark-output/` directory. Each report contains its exact command, commit,
configuration, timestamps, environment, raw-file paths, and limitations.

## Coverage and outcomes

| Profile | Workload | Outcome |
|---|---|---|
| Final canary | 20 signed end-to-end executions plus HTTP baselines | 20/20 delivered; no duplicates or signature failures |
| Steady state | cold/warm, 101 sequential, 600 concurrent, control plane, four slow receivers | 707/707 callbacks; no duplicates or permanent failures |
| Saturation sweep | 1,000 executions at concurrency 100, 250, 500, and 1,000 | no process crash, restart, OOM, duplicate, or signature failure |
| Entry-point cliff | 1,000 simultaneous trigger requests | 855 accepted and all 855 delivered; 145 POST requests reset/EOF before acceptance |
| Recurring | three actual minute-boundary ticks | 3/3 delivered; scheduler lag 917–921 ms |
| Production retry | 500, 503, 429, 400, timeout, closed port, and eventual success | attempt classification and final states matched policy |
| Resilience | active-delivery race, all-node stop/restart, leader loss, PostgreSQL pause | all four scenarios passed on the current branch |

## Measured baselines

The 707-execution steady-state run reported:

- health RTT p50 0.205 ms, p95 0.339 ms, p99 0.681 ms;
- direct receiver RTT p50 0.108 ms, p95 0.136 ms, p99 0.184 ms;
- end-to-end delivery p50 72.405 ms, p95 1,899.100 ms, p99 2,002.794 ms;
- concurrent execution and callback throughput 25.625/s.

The saturation sweep reported aggregate p99 trigger latency 3,059.650 ms,
queue wait 28,259.136 ms, and end-to-end delivery 29,021.021 ms. Concurrency
100 and 250 accepted and delivered all 1,000 requests. At concurrency 500,
994 requests were accepted. The dedicated concurrency-1,000 rerun recovered 14
transient GET failures and proved that all accepted executions reached terminal
delivery, while exposing the 145-request POST acceptance cliff.

## Retry timing

The real-policy run lasted 14m30s and observed:

| Attempt | Configured backoff | Actual range | Error range |
|---:|---:|---:|---:|
| 2 | 30s | 30,002.519–30,035.023 ms | 2.519–35.023 ms |
| 3 | 2m | 120,008.531–120,036.486 ms | 8.531–36.486 ms |
| 4 | 10m | 600,014.538–600,024.890 ms | 14.538–24.890 ms |

The permanent 500, timeout, and closed-port cases made four attempts. A 400
stopped after one attempt. The 500 and 503 eventual-success cases delivered on
attempt two. The 429 case delivered on attempt three after one transient
network failure. No retry used a duplicate attempt ID and no active retry was
requeued.

That run exposed the original two-minute stale requeue threshold as unsafe:
the reconciler started duplicate retry sequences while another worker was
still in production backoff. The default is now 19 minutes, derived from the
full 16m30s attempt-and-backoff window plus safety margin. Normal
configurations reject shorter explicit values.

## Recovery timing

The final current-branch resilience run completed in 28.736 seconds:

- duplicate-race: 7.044s, one callback, no overlap;
- all-node crash/recovery: 15.401s, at-least-once redelivery observed, no
  concurrent duplicate;
- leader failover: 1.398s, leadership moved from instance 1 to instance 3;
- PostgreSQL outage: 2.195s, including the bounded two-second degraded-health
  probe and readiness recovery.

## Local artifact directories

- `benchmark-output/final-canary-passed`
- `benchmark-output/steady-stress-safe`
- `benchmark-output/almost-kill-load-fixed`
- `benchmark-output/almost-kill-poll-retry`
- `benchmark-output/retry-real-policy-final`
- `benchmark-output/final-recurring-current`
- `benchmark-output/final-resilience-current`

The retry report was generated immediately before the callback-accounting fix
and therefore contains a stale `missing_callback` finding for the intentionally
failed closed-port execution. Its raw attempts and timings are valid; current
correlation tests require callbacks only for terminal `delivered` executions.
