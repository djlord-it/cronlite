# Customer Benchmark Page Design

## Purpose

Create a simple, customer-facing `benchmark.md` that positions CronLite as a
reliable, measurable, failure-tested webhook scheduler. The page should sell
the product through verified outcomes rather than implementation detail.

## Audience and tone

The primary audience is a prospective customer evaluating whether CronLite can
reliably schedule and deliver production webhooks.

The writing will be:

- confident and straightforward;
- readable without deep infrastructure knowledge;
- specific about measured results;
- concise enough to scan in a few minutes;
- grounded in the existing benchmark data.

The page will not reproduce the heavy diagnostic documentation, internal defect
history, or saturation-limit analysis. It will include a short environment note
so the claims remain credible and properly scoped.

## Structure

1. A headline stating that CronLite delivers scheduled webhooks reliably
   through retries, crashes, and failover.
2. A one-paragraph summary explaining that the benchmark measures the complete
   execution lifecycle rather than synthetic request throughput.
3. A compact metric table using the strongest verified customer outcomes:
   707/707 steady-state callbacks, 72 ms median delivery, 25.6 deliveries per
   second in the local concurrent profile, 1.4-second leader failover,
   2.2-second database recovery, and retry timing within 37 ms.
4. Three short benefit sections:
   - reliable delivery;
   - resilience under infrastructure failure;
   - secure and predictable webhook behavior.
5. A brief description of the scenarios exercised: normal delivery, slow
   receivers, retries, recurring schedules, concurrency, crash recovery,
   failover, and database interruption.
6. A transparent one-paragraph test-environment note.
7. A concise call to action linking to the main setup documentation.

## Claim boundaries

Every numeric claim must come from `tools/benchmark/FULL_SUITE_RESULTS.md` or
the generated reports it identifies. Results will be rounded for readability
without making them more favorable:

- `72.405 ms` becomes `72 ms`;
- `25.625/s` becomes `25.6 deliveries/s`;
- `1.398s` becomes `1.4 seconds`;
- `2.195s` becomes `2.2 seconds`;
- maximum retry timing error of `36.486 ms` becomes `within 37 ms`.

The page will not claim exactly-once semantics, unlimited capacity, universal
production performance, or results from hardware other than the recorded local
environment.

## Verification

Before commit:

- compare every number against the full-suite results;
- scan for internal debugging language and unnecessary jargon;
- check Markdown formatting and links;
- run `git diff --check`;
- confirm the worktree contains only the intended documentation change.

