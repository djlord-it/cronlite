# Database Schema

This directory contains the PostgreSQL schema for CronLite.

## Applying the Schema

Apply every numbered SQL file in order. For example:

```bash
set -e
for migration in schema/[0-9][0-9][0-9]_*.sql; do
  psql "$DATABASE_URL" -v ON_ERROR_STOP=1 < "$migration"
done
```

`docker-compose.yml` and `docker-compose.admin-ci.yml` mount migrations
`001` through `011` into PostgreSQL's initialization directory in the same order.
The admin CI file intentionally does not set a Compose project name. Call it with
`docker compose -p <unique-project> -f docker-compose.admin-ci.yml ...` so each
local or CI run gets isolated containers, networks, and volumes.

## Tables

### schedules

Stores cron expressions and timezones. Referenced by jobs.

### jobs

Stores job configuration including webhook URL, secret, timeout, and analytics settings. Each job references one schedule.

### executions

Records each time a job fires. The `(job_id, scheduled_at)` pair is unique to prevent duplicate executions on scheduler restart.

Status values:
- `emitted`: Execution created and waiting to be claimed
- `in_progress`: Claimed for webhook delivery
- `delivered`: Webhook delivered successfully
- `failed`: All delivery attempts exhausted

### delivery_attempts

Records each webhook delivery attempt for an execution. Includes HTTP status code, error message, and timestamps.

### admin_sessions

Stores opaque, revocable browser sessions for the optional `/admin` UI. Sessions have idle and absolute expiry timestamps, and rows are deleted automatically when their API key is revoked.

## Constraints

- `executions.UNIQUE(job_id, scheduled_at)`: Enforces execution idempotency
- Foreign keys: jobs references schedules, executions references jobs, delivery_attempts references executions

## Notes

- No migration tooling is provided. Apply SQL files directly.
- Schema changes require manual migration scripts.
- UUIDs are used for all primary keys.
- Timestamps are stored as `TIMESTAMPTZ` (timezone-aware).

Migration 010 adds `admin_saved_views` (API-key-owned, deleted with the key) and
`admin_bulk_batches` (preview snapshots and durable per-job audit results).
Bulk audit records retain the actor UUID after key deletion. Confirmed records
have no automatic retention policy; operators may archive them according to
their installation requirements. Unconfirmed expired previews are cleaned up
when their owner creates another preview. The migration is transactional and
idempotent. Apply it before enabling the updated Admin UI on an existing database.

## Execution ownership migration (011)

Stop and drain every old CronLite writer before applying migration 011 and
starting updated instances. Also wait for their PostgreSQL statements/transactions
to finish: a killed process can leave a backend running or waiting for a lock.
Terminate abandoned old-writer sessions through normal database administration
if necessary. Mixed old/new writers cannot provide fencing:
old binaries still issue unconditional status writes. Apply migrations in order;
011 is additive, transactional and idempotent. Index creation can block writes
on large tables, so use a maintenance window. Both dispatch modes now require it.

Legacy executions/attempts remain generation 0. Terminal rows are unchanged.
When reconciliation is enabled, its first scans requeue legacy in-progress rows
with null leases in bounded batches (including null claimed_at), assigning a
nonzero recovery generation. Channel notifications for recovered rows are
eligible immediately. With recovery disabled, run the same bounded legacy
transition manually before starting DB poll workers. Channel mode must temporarily
enable reconciliation to emit notifications; SQL alone does not wake its consumer:

```sql
WITH legacy AS (
  SELECT id FROM executions
  WHERE status = 'in_progress' AND claim_generation = 0 AND lease_until IS NULL
  ORDER BY created_at, id LIMIT 100 FOR UPDATE SKIP LOCKED
)
UPDATE executions AS e
SET status = 'emitted', claimed_at = NULL, claim_generation = 1
FROM legacy WHERE e.id = legacy.id;
```

Repeat until no rows change. Do not reset terminal rows or generations. Fresh
claims increment generation, and renewal/completion require the matching current
generation and unexpired PostgreSQL lease. `claimed_at` remains the initial claim
time; `lease_until` tracks expiry. Attempt history stores the observing generation
without changing public REST/MCP response fields.

The default lease is two minutes, renewed every thirty seconds; the existing
RECONCILE_REQUEUE_THRESHOLD setting controls its duration in both modes. Database
time determines expiry. Recovery requires reconciliation and remains conditional
on database availability, scan cadence and queue progress. Receiver deduplication
by execution ID is still needed for at-least-once delivery.
