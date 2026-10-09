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
`001` through `010` into PostgreSQL's initialization directory in the same order.
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
