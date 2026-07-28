# Customer Benchmark Page Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Create a concise customer-facing `benchmark.md` that sells CronLite through verified reliability, latency, retry, and recovery metrics.

**Architecture:** The deliverable is one standalone Markdown page at the repository root. It will draw only from the committed full-suite results, lead with a scannable scorecard, translate technical scenarios into customer benefits, and close with a short reproducibility note and setup links.

**Tech Stack:** Markdown, existing CronLite benchmark reports, Git.

---

### Task 1: Write the customer benchmark page

**Files:**
- Create: `benchmark.md`
- Reference: `tools/benchmark/FULL_SUITE_RESULTS.md`
- Reference: `README.md`

- [ ] **Step 1: Verify every selected customer metric against the committed results**

Run:

```bash
rg -n "707/707|72\\.405|25\\.625|1\\.398|2\\.195|36\\.486|signature" \
  tools/benchmark/FULL_SUITE_RESULTS.md
```

Expected: the source document shows 707/707 callbacks, 72.405 ms median
delivery, 25.625/s throughput, 1.398-second failover, 2.195-second database
recovery, a 36.486 ms maximum retry timing error, and no signature failures.

- [ ] **Step 2: Create the page with the approved proof-first copy**

Create `benchmark.md` with this content:

```markdown
# CronLite Benchmark Results

## Scheduled webhooks you can trust

CronLite delivers scheduled webhooks reliably through normal traffic, retries,
slow receivers, service restarts, leader changes, and database interruptions.
Our benchmark measures the complete execution lifecycle—from API trigger to the
signed webhook reaching its receiver—not just synthetic requests per second.

## Results at a glance

| What we measured | Verified result |
|---|---:|
| Steady-state webhook callbacks | **707 / 707 delivered** |
| Median end-to-end delivery | **72 ms** |
| Concurrent delivery throughput | **25.6 deliveries/second** |
| Leader failover | **1.4 seconds** |
| Database recovery | **2.2 seconds** |
| Production retry timing accuracy | **Within 37 ms** |
| Concurrent duplicate findings | **0** |
| Signature-verification failures | **0** |

## Reliable delivery, end to end

CronLite completed every execution in the 707-callback steady-state suite. The
test covered cold and warm starts, sequential jobs, concurrent delivery,
control-plane operations, and slow webhook receivers.

Every delivered webhook was HMAC-signed and verified by the benchmark
receiver. No concurrent duplicate delivery or signature failure was observed.

## Built to recover

The resilience suite ran three CronLite instances against PostgreSQL and
deliberately interrupted the systems that keep schedules moving.

- Scheduler leadership moved to a healthy instance in **1.4 seconds**.
- CronLite recovered after a controlled database interruption in **2.2
  seconds**.
- A full dispatcher stop and restart recovered the in-flight execution.
- Active-delivery race testing completed without concurrent duplicate
  callbacks.

CronLite is designed so webhook scheduling does not depend on one process
remaining alive.

## Predictable retries

The benchmark exercised `500`, `503`, `429`, timeout, connection failure,
non-retryable `400`, and eventual-success responses using CronLite's unchanged
production retry policy.

The configured 30-second, 2-minute, and 10-minute backoffs were reproduced to
within **37 ms**. Retryable failures continued through the expected attempts,
non-retryable responses stopped immediately, and eventual-success deliveries
completed as designed.

## Tested beyond the happy path

CronLite was exercised with:

- immediate and recurring schedules;
- sequential and concurrent webhook delivery;
- slow and timing-out receivers;
- retryable and permanent HTTP failures;
- network connection failures;
- multi-instance dispatcher recovery;
- scheduler leader failover;
- database interruption and restoration;
- duplicate-delivery race detection.

Correctness was evaluated before performance. The receiver verified signatures,
execution IDs, attempt IDs, payload consistency, callback timing, and concurrent
delivery behavior.

## Test environment

These results were produced by a reproducible local benchmark on macOS arm64
with 8 logical CPUs, 8 GiB memory, Docker 29.4.0, PostgreSQL 16.13, and three
CronLite instances using two database-dispatch workers each. Results are a
measured regression baseline; performance will vary with hardware, network,
database, and workload.

Ready to schedule reliable webhooks? Start with the [CronLite quick
start](README.md#quick-start). Engineers can inspect the [benchmark methodology
and reproducible scenarios](tools/benchmark/README.md).
```

- [ ] **Step 3: Compare the rendered copy against the editorial spec**

Run:

```bash
sed -n '1,260p' benchmark.md
```

Expected: a short proof-first page with one metric scorecard, four concise
benefit sections, one environment note, and links to the existing setup and
methodology documents. It must not include internal defect history or the
saturation-limit analysis.

### Task 2: Verify and commit the page

**Files:**
- Verify: `benchmark.md`

- [ ] **Step 1: Check claims, placeholders, formatting, and links**

Run:

```bash
rg -n "TBD|TODO|FIXME|145|saturation|missing_callback|exactly-once" benchmark.md
```

Expected: no matches.

Run:

```bash
test -f README.md
test -f tools/benchmark/README.md
git diff --check
```

Expected: all commands exit 0 with no formatting errors.

- [ ] **Step 2: Confirm only the intended page is uncommitted**

Run:

```bash
git status --short --untracked-files=all
```

Expected: only `benchmark.md` is listed.

- [ ] **Step 3: Commit without pushing**

Run:

```bash
git add -f benchmark.md
git commit -m "docs: publish customer benchmark results"
```

Expected: one commit containing only `benchmark.md`. Do not run `git push`.

