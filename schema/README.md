# Database Schema

This directory contains the PostgreSQL schema for CronLite.

## Applying the Schema

```bash
psql <database_url> < schema/001_initial.sql
```

Or with environment variable:
```bash
psql $DATABASE_URL < schema/001_initial.sql
```

## Tables

### schedules

Stores cron expressions and timezones. Referenced by jobs.

### jobs

Stores job configuration including webhook URL, secret, timeout, and analytics settings. Each job references one schedule.

### executions

Records each time a job fires. The `(job_id, scheduled_at)` pair is unique to prevent duplicate executions on scheduler restart.

Status values:
- `emitted`: Execution created, webhook delivery in progress
- `delivered`: Webhook delivered successfully
- `failed`: All delivery attempts exhausted

### delivery_attempts

Records each webhook delivery attempt for an execution. Includes HTTP status code, error message, and timestamps.

## Constraints

- `executions.UNIQUE(job_id, scheduled_at)`: Enforces execution idempotency
- Foreign keys: jobs references schedules, executions references jobs, delivery_attempts references executions

## Notes

- No migration tooling is provided. Apply SQL files directly.
- Schema changes require manual migration scripts.
- UUIDs are used for all primary keys.
- Timestamps are stored as `TIMESTAMPTZ` (timezone-aware).
