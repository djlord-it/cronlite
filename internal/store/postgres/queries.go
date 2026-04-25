package postgres

// ── Jobs ──────────────────────────────────────────────────────────────────────

const queryGetEnabledJobs = `
SELECT
    j.id, j.namespace, j.name, j.enabled, j.schedule_id,
    j.delivery_type, j.webhook_url, j.secret, j.timeout_ms,
    j.analytics_enabled, j.analytics_retention_seconds,
    j.created_at, j.updated_at,
    s.id, s.cron_expression, s.timezone, s.created_at, s.updated_at
FROM jobs j
JOIN schedules s ON j.schedule_id = s.id
WHERE j.enabled = true
ORDER BY j.id
LIMIT $1 OFFSET $2
`

const queryInsertJob = `
INSERT INTO jobs (id, namespace, name, enabled, schedule_id, delivery_type, webhook_url, secret, timeout_ms, analytics_enabled, analytics_retention_seconds, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
`

const queryGetJobByID = `
SELECT
    id, namespace, name, enabled, schedule_id,
    delivery_type, webhook_url, secret, timeout_ms,
    analytics_enabled, analytics_retention_seconds,
    created_at, updated_at
FROM jobs
WHERE id = $1
`

const queryGetJobWithSchedule = `
SELECT
    j.id, j.namespace, j.name, j.enabled, j.schedule_id,
    j.delivery_type, j.webhook_url, j.secret, j.timeout_ms,
    j.analytics_enabled, j.analytics_retention_seconds,
    j.created_at, j.updated_at,
    s.id, s.cron_expression, s.timezone, s.created_at, s.updated_at
FROM jobs j
JOIN schedules s ON j.schedule_id = s.id
WHERE j.id = $1
`

const queryGetJobWithScheduleScoped = `
SELECT
    j.id, j.namespace, j.name, j.enabled, j.schedule_id,
    j.delivery_type, j.webhook_url, j.secret, j.timeout_ms,
    j.analytics_enabled, j.analytics_retention_seconds,
    j.created_at, j.updated_at,
    s.id, s.cron_expression, s.timezone, s.created_at, s.updated_at
FROM jobs j
JOIN schedules s ON j.schedule_id = s.id
WHERE j.id = $1 AND j.namespace = $2
`

const queryUpdateJob = `
UPDATE jobs SET
    name = $1,
    enabled = $2,
    delivery_type = $3,
    webhook_url = $4,
    secret = $5,
    timeout_ms = $6,
    analytics_enabled = $7,
    analytics_retention_seconds = $8,
    updated_at = $9
WHERE id = $10 AND namespace = $11
`

const queryDeleteJob = `
WITH deleted_attempts AS (
    DELETE FROM delivery_attempts
    WHERE execution_id IN (SELECT id FROM executions WHERE job_id = $1)
),
deleted_executions AS (
    DELETE FROM executions WHERE job_id = $1
),
deleted_schedules AS (
    DELETE FROM schedules WHERE id IN (SELECT schedule_id FROM jobs WHERE id = $1 AND namespace = $2)
)
DELETE FROM jobs WHERE id = $1 AND namespace = $2
RETURNING id`

// ── Schedules ─────────────────────────────────────────────────────────────────

const queryInsertSchedule = `
INSERT INTO schedules (id, cron_expression, timezone, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5)
`

const queryGetSchedule = `
SELECT id, cron_expression, timezone, created_at, updated_at
FROM schedules
WHERE id = $1
`

const queryUpdateSchedule = `
UPDATE schedules SET
    cron_expression = $1,
    timezone = $2,
    updated_at = $3
WHERE id = $4
`

// ── Executions ────────────────────────────────────────────────────────────────

const queryInsertExecution = `
INSERT INTO executions (id, job_id, namespace, trigger_type, scheduled_at, fired_at, status, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
`

const queryGetExecution = `
SELECT id, job_id, namespace, trigger_type, scheduled_at, fired_at, status, acknowledged_at, created_at
FROM executions
WHERE id = $1
`

const queryGetExecutionScoped = `
SELECT id, job_id, namespace, trigger_type, scheduled_at, fired_at, status, acknowledged_at, created_at
FROM executions
WHERE id = $1 AND namespace = $2
`

const queryGetRecentExecutions = `
SELECT id, job_id, namespace, trigger_type, scheduled_at, fired_at, status, acknowledged_at, created_at
FROM executions
WHERE job_id = $1
ORDER BY created_at DESC
LIMIT $2
`

const queryAckExecution = `
UPDATE executions
SET acknowledged_at = NOW()
WHERE id = $1
  AND namespace = $2
  AND acknowledged_at IS NULL
  AND status IN ('delivered', 'failed')
`

const queryGetExecutionStatus = `
SELECT status FROM executions WHERE id = $1
`

const queryUpdateExecutionStatus = `
UPDATE executions
SET status = $1
WHERE id = $2
  AND status NOT IN ('delivered', 'failed')
`

const queryGetOrphanedExecutions = `
SELECT id, job_id, namespace, trigger_type, scheduled_at, fired_at, status, acknowledged_at, created_at
FROM executions
WHERE status = 'emitted'
  AND created_at < $1
ORDER BY created_at ASC
LIMIT $2
`

const queryDequeueExecution = `
SELECT id, job_id, namespace, trigger_type, scheduled_at, fired_at, status, acknowledged_at, created_at
FROM executions
WHERE status = 'emitted'
ORDER BY created_at ASC
FOR UPDATE SKIP LOCKED
LIMIT 1
`

const queryClaimExecution = `
UPDATE executions
SET status = 'in_progress', claimed_at = NOW()
WHERE id = $1
`

const queryRequeueStaleExecutions = `
WITH stale AS (
    SELECT id FROM executions
    WHERE status = 'in_progress'
      AND claimed_at < $1
    ORDER BY claimed_at ASC
    LIMIT $2
    FOR UPDATE SKIP LOCKED
)
UPDATE executions
SET status = 'emitted', claimed_at = NULL
FROM stale
WHERE executions.id = stale.id
`

// ── Delivery Attempts ─────────────────────────────────────────────────────────

const queryInsertDeliveryAttempt = `
INSERT INTO delivery_attempts (id, execution_id, attempt, status_code, error, started_at, finished_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
`

const queryGetDeliveryAttempts = `
SELECT id, execution_id, attempt, status_code, error, started_at, finished_at
FROM delivery_attempts
WHERE execution_id = $1
ORDER BY attempt
`

// ── Tags ──────────────────────────────────────────────────────────────────────

const queryUpsertTag = `
INSERT INTO tags (job_id, key, value)
VALUES ($1, $2, $3)
ON CONFLICT (job_id, key) DO UPDATE SET value = EXCLUDED.value
`

const queryGetTags = `
SELECT key, value
FROM tags
WHERE job_id = $1
ORDER BY key
`

const queryDeleteTags = `
DELETE FROM tags WHERE job_id = $1
`

// ── API Keys ──────────────────────────────────────────────────────────────────

const queryInsertAPIKey = `
INSERT INTO api_keys (id, namespace, token_hash, label, enabled, created_at)
VALUES ($1, $2, $3, $4, $5, $6)
`

const queryGetKeyByTokenHash = `
SELECT id, namespace, token_hash, label, enabled, created_at, last_used_at
FROM api_keys
WHERE token_hash = $1 AND enabled = true
`

const queryListKeys = `
SELECT id, namespace, token_hash, label, enabled, created_at, last_used_at
FROM api_keys
WHERE namespace = $1
ORDER BY created_at DESC
LIMIT $2 OFFSET $3
`

const queryDeleteKey = `
DELETE FROM api_keys WHERE id = $1 AND namespace = $2
`

const queryUpdateLastUsedAt = `
UPDATE api_keys SET last_used_at = NOW()
WHERE id = ANY($1)
`
