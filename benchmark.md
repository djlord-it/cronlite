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
