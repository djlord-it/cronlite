# CronLite Operator Guide

Operational contract: what CronLite guarantees, how it fails, and how to run it.

## Important Notes

**Use `DISPATCH_MODE=db` for multi-instance deployments.** Channel mode (the default) uses an in-memory event bus with no cross-instance coordination. Running multiple instances in channel mode causes duplicate webhook deliveries. If you're running a single instance, channel mode works fine.

**Make sure `RECONCILE_ENABLED=true` in production.** Without the reconciler, orphaned executions from crashes, buffer overflow, or stale claims have no automatic recovery path. The only alternative is direct SQL intervention.

## Deployment Decision Tree

### How many instances?

    ┌─ Are you running in production?
    │
    ├─ NO → 1 instance, DISPATCH_MODE=channel (default)
    │        Minimum: DATABASE_URL set
    │        Optional: RECONCILE_ENABLED=true (recommended even for dev)
    │
    └─ YES → Do you need high availability / zero-downtime deploys?
             │
             ├─ NO → 1 instance, DISPATCH_MODE=db
             │        Required: DATABASE_URL, RECONCILE_ENABLED=true, METRICS_ENABLED=true
             │        Apply all migrations (see [Migrations](#migrations))
             │        Benefit: crash recovery via claimed_at; ready to scale later
             │
             └─ YES → 2+ instances, DISPATCH_MODE=db
                       Required: DATABASE_URL (same on all), DISPATCH_MODE=db,
                                 LEADER_LOCK_KEY=728379 (same on all),
                                 RECONCILE_ENABLED=true, METRICS_ENABLED=true
                       Apply all migrations (see [Migrations](#migrations))
                       Tune: TCP keepalive on Postgres (see [Failover Timing](#failover-timing))
                       Tune: DISPATCHER_WORKERS=2-4

### What happens during failover?

Leader dies → Postgres detects dead connection (TCP keepalive) → Advisory lock released → Follower acquires lock (`LEADER_RETRY_INTERVAL`) → New leader starts scheduler + reconciler.

During the gap (3-25s depending on TCP keepalive tuning):
- NO new executions are scheduled
- Dispatch continues on all surviving instances (DB poll unaffected)
- API continues on all surviving instances
- `(job_id, scheduled_at)` unique constraint prevents double-scheduling

### What happens during rolling deploys?

1. Send SIGTERM to old instance
2. Old instance: scheduler stops → reconciler stops → dispatcher drains → HTTP drains → exit
3. New instance starts, attempts advisory lock
4. If old leader: lock released on exit, new instance acquires it
5. If old follower: no leadership change, new instance joins as follower

Max shutdown time: `DISPATCHER_DRAIN_TIMEOUT` + `HTTP_SHUTDOWN_TIMEOUT` (default 40s). Set deploy health check to wait at least 40s before marking unhealthy.

## Production Checklist

- [ ] All migrations applied in order (see [Migrations](#migrations))
- [ ] `CRONLITE_ENV=production` set so unsafe production configuration fails startup
- [ ] `DISPATCH_MODE=db` on all production instances
- [ ] `RECONCILE_ENABLED=true` on all instances
- [ ] `METRICS_ENABLED=true` on all instances
- [ ] `API_KEY` set for production startup validation, even if traffic uses namespace-scoped API keys
- [ ] `LEADER_LOCK_KEY` identical across all instances (default: 728379)
- [ ] `DISPATCHER_WORKERS` set to 2-4 for production workloads
- [ ] Postgres TCP keepalive tuned: `tcp_keepalives_idle=10`, `tcp_keepalives_interval=5`, `tcp_keepalives_count=3`
- [ ] Prometheus scraping `/metrics` endpoint on all instances
- [ ] Alerts configured: CronLiteNoLeader, CronLiteSplitBrain, CronLiteOrphanedExecutions, CronLiteBufferSaturation, CronLiteReconcilerDisabled, CronLiteCircuitBreakerActive
- [ ] Webhook handlers are idempotent (use `X-CronLite-Execution-ID` for dedup)
- [ ] Startup logs reviewed — no `WARNING [P0]` or `WARNING [P1]` lines present
- [ ] Health check configured at `/health?verbose=true` for load balancer
- [ ] Graceful shutdown timeout in orchestrator ≥ 45s (covers 40s CronLite drain)
- [ ] `DATABASE_URL` uses `sslmode=require` or stricter
- [ ] Circuit breaker enabled (`CIRCUIT_BREAKER_THRESHOLD=5`) with monitoring for `circuit_open` outcomes
- [ ] At least one API key provisioned (`cronlite create-key <namespace> <label>`) before exposing API traffic

## Configuration Reference

| Variable | Default | Description |
|----------|---------|-------------|
| `CRONLITE_ENV` | *(empty)* | Set to `production` to fail startup on unsafe production settings |
| `DATABASE_URL` | *required* | PostgreSQL connection string |
| `API_KEY` | *(empty)* | Legacy static Bearer token fallback (also used to protect `/metrics` when set) |
| `HTTP_ADDR` | `:8080` | Listen address |
| `TICK_INTERVAL` | `30s` | Scheduler polling interval |
| `DISPATCH_MODE` | `channel` | `channel` (in-memory) or `db` (Postgres polling) |
| `DISPATCHER_WORKERS` | `1` | Concurrent dispatch workers (DB mode) |
| `DB_POLL_INTERVAL` | `500ms` | Sleep between polls when idle (DB mode) |
| `RECONCILE_ENABLED` | `false` | Enable orphan recovery |
| `RECONCILE_INTERVAL` | `5m` | Orphan scan frequency |
| `RECONCILE_THRESHOLD` | `15m` | Age before execution is considered orphaned |
| `RECONCILE_REQUEUE_THRESHOLD` | `2m` | Age before in-progress execution is requeued (crash recovery) |
| `RECONCILE_BATCH_SIZE` | `100` | Max orphans per cycle |
| `METRICS_ENABLED` | `false` | Enable Prometheus `/metrics` |
| `CIRCUIT_BREAKER_THRESHOLD` | `5` | Consecutive failures to open circuit (0 = disabled) |
| `CIRCUIT_BREAKER_COOLDOWN` | `2m` | Cooldown before probe attempt |
| `DB_OP_TIMEOUT` | `5s` | Max time per DB operation |
| `DB_MAX_OPEN_CONNS` | `25` | Max open DB connections |
| `DB_MAX_IDLE_CONNS` | `5` | Max idle DB connections |
| `DB_CONN_MAX_LIFETIME` | `30m` | Connection max lifetime |
| `DB_CONN_MAX_IDLE_TIME` | `5m` | Connection idle timeout |
| `HTTP_SHUTDOWN_TIMEOUT` | `10s` | Graceful shutdown timeout for HTTP |
| `DISPATCHER_DRAIN_TIMEOUT` | `30s` | Drain timeout for buffered events |
| `LEADER_LOCK_KEY` | `728379` | Postgres advisory lock key (DB mode) |
| `LEADER_RETRY_INTERVAL` | `5s` | Follower lock acquisition retry (DB mode) |
| `LEADER_HEARTBEAT_INTERVAL` | `2s` | Leader connection health check (DB mode) |
| `MAX_FIRES_PER_TICK` | `1000` | Max executions a single job can emit per scheduler tick |
| `RATE_LIMIT` | `10` | Per-IP request rate limit (requests/sec) |
| `NAMESPACE_RATE_LIMIT` | `100` | Per-namespace request rate limit (requests/sec, after auth) |

### Required in Production

| Variable | Value | Why |
|----------|-------|-----|
| `CRONLITE_ENV` | `production` | Enables strict production validation before startup |
| `DISPATCH_MODE` | `db` | Avoids in-memory dispatch loss and supports multi-instance coordination |
| `RECONCILE_ENABLED` | `true` | Without this, orphaned executions are **permanently lost** |
| `METRICS_ENABLED` | `true` | Required for observability and alerting |
| `API_KEY` | non-empty | Ensures API authentication is configured at startup |

## Guarantees

**Scheduling**
- At-most-once execution creation per `(job_id, scheduled_at)` — enforced by DB unique constraint, even during split-brain
- Crash-safe scheduling — executions are persisted to Postgres before dispatch begins (DB mode)

**Delivery**
- Automatic retry with bounded backoff — up to 4 attempts (0s → 30s → 2m → 10m)
- Crash recovery — orphaned and in-progress executions are automatically detected and re-dispatched by the reconciler
- Per-URL circuit breaker — protects downstream services from retry storms; open circuit defers (not fails) executions for reconciler recovery
- Stable execution identity — the same `X-CronLite-Execution-ID` is preserved across retries and re-emits, enabling simple client-side deduplication
- Terminal state immutability — `delivered` and `failed` never change once set

**Operations**
- Bounded DB operations — all queries timeout after `DB_OP_TIMEOUT`
- Ordered shutdown — scheduler stops → reconciler stops → dispatcher drains → HTTP drains
- Automatic leader failover — follower promotes within seconds when the leader dies (DB mode)
- Zero-downtime rolling deploys — followers continue dispatching and serving API traffic during leadership transitions

**Your responsibilities:**
1. Use `DISPATCH_MODE=db` and `RECONCILE_ENABLED=true` in production for full crash recovery
2. Design idempotent webhook handlers (use `X-CronLite-Execution-ID` for dedup)
3. Monitor metrics (see [Monitoring](#monitoring))

## Dispatch Modes

| | Channel (default) | DB |
|---|---|---|
| **How** | In-memory EventBus (100-event buffer) | Postgres polling with `SKIP LOCKED` |
| **Crash resilience** | Buffer lost on crash | Rows survive crash |
| **Scaling** | Single process only | Multiple workers / instances |
| **DB load** | Lower | Higher (polling every `DB_POLL_INTERVAL`) |

DB mode requires all migrations to be applied. See [Migrations](#migrations).

## Migrations

Schema migrations live in `schema/` and are numbered sequentially. Apply them in order:

```bash
for f in schema/0*.sql; do psql cronlite < "$f"; done
```

Current migrations:

| File | Purpose |
|------|---------|
| `001_initial.sql` | Core tables: jobs, executions, delivery_attempts |
| `002_add_indexes.sql` | Performance indexes |
| `003_add_claimed_at.sql` | Adds `claimed_at` column for stale execution recovery (required for DB mode) |
| `004_agent_platform.sql` | API keys, namespaces, tags, execution acknowledgment |
| `005_drop_scopes.sql` | Removes unused `scopes` column from api_keys |
| `006_add_claimed_at_index.sql` | Partial index for reconciler crash recovery queries |

Current releases require all migrations through 006. Skipping migrations may cause auth failures, missing columns, or degraded reconciler performance.

## Auth Model

CronLite uses namespace-scoped API keys with SHA-256 hashed storage.

**Key concepts:**
- Each API key belongs to a **namespace** — all operations via that key are isolated to its namespace
- Keys are created with `cronlite create-key <namespace> <label>` — the plaintext token (`ec_<64-hex>`) is shown once
- Token format: `ec_` prefix + 64 hex characters (256-bit random)

**Authentication flow:**
1. Extract `Bearer <token>` from `Authorization` header
2. SHA-256 hash the token → look up in `api_keys` table
3. If found and enabled → inject key's namespace into request context, track `last_used_at`
4. If not found → fall back to legacy `API_KEY` env var (maps to namespace `default`)
5. If neither matches → 401

**Exempt paths:** `/health`, `/metrics`, `/mcp` (MCP has its own auth via `HTTPContextFunc`)

**Rate limiting:** Two-layer token bucket — per-IP (10 req/sec default, applied before auth) and per-namespace (100 req/sec default, applied after auth) on all endpoints except `/health`. Returns `429 Too Many Requests` when exceeded. Configure via `RATE_LIMIT` and `NAMESPACE_RATE_LIMIT`.

**`last_used_at` tracking:** Debounced in-memory with background flush every 60 seconds to minimize DB writes under high traffic.

**Legacy compatibility:** The `API_KEY` env var still works as a fallback and maps to the `default` namespace, but now emits rate-limited deprecation warnings in logs. Migrate to namespace-scoped API keys (`cronlite create-key`) for production use.

## Execution Lifecycle

```
emitted ──→ delivered  (terminal)
   │
   ├──→ failed  (terminal)
   │
   └──→ in_progress ──→ delivered  (DB mode only)
            │
            ├──→ failed
            │
            └──→ emitted  (stale requeue by reconciler)
```

Terminal states (`delivered`, `failed`) are immutable.

## Circuit Breaker

Per-URL, in-memory, resets on restart.

```
CLOSED ──(N consecutive failures)──→ OPEN ──(cooldown)──→ HALF_OPEN
                                       ↑                      │
                                       └──(probe fails)───────┘
                                              │
                                        (probe succeeds) → CLOSED
```

- Counts fully-failed **executions** (not individual HTTP attempts)
- Open circuit → executions are **deferred** (left in current state, no HTTP call). The reconciler will retry them once the circuit closes
- Half-open state allows up to 3 probe requests through to detect service recovery
- Each URL has an independent circuit
- Set `CIRCUIT_BREAKER_THRESHOLD=0` to disable

> **Forensics note:** When the circuit breaker is open, executions are skipped without any HTTP call — no `delivery_attempts` row is created, and the execution remains in its current state (`emitted` or `in_progress`). The `cronlite_dispatcher_delivery_outcomes_total{outcome="circuit_open"}` metric tracks these deferrals. Look for `"circuit open for ... skipping"` in logs.

## Failure Modes

| Failure | Behavior |
|---------|----------|
| **Postgres down at startup** | Refuses to start (exit 1) |
| **Postgres down at runtime** | Tick/dispatch fails after `DB_OP_TIMEOUT`, retried next cycle |
| **Redis down** | Analytics disabled, delivery unaffected |
| **Webhook timeout/5xx/429** | Retryable, up to 4 attempts |
| **Webhook 4xx (not 429)** | Non-retryable, marked `failed` immediately |
| **Circuit open** | Execution deferred (left in current state), no HTTP call; reconciler retries when circuit closes |
| **Buffer full (channel mode)** | Event dropped after 5s, execution orphaned |
| **Process crash** | Buffer lost; reconciler recovers DB-inserted orphans |

## Orphaned Executions

An execution is orphaned when `status = 'emitted'` but will never be delivered (buffer full, crash, shutdown).

**With reconciler enabled** (recommended): automatically detected and re-emitted every `RECONCILE_INTERVAL`.

**Without reconciler**: remains stuck in DB. Manual options: accept the loss, call webhook directly, or delete the record.

**Detection query:**
```sql
SELECT id, job_id, scheduled_at, created_at
FROM executions
WHERE status = 'emitted'
  AND created_at < NOW() - INTERVAL '10 minutes';
```

## Shutdown

On SIGINT/SIGTERM: scheduler stops → reconciler stops → dispatcher drains (`DISPATCHER_DRAIN_TIMEOUT`) → HTTP server stops (`HTTP_SHUTDOWN_TIMEOUT`) → exit 0.

**Max shutdown time:** `DISPATCHER_DRAIN_TIMEOUT` + `HTTP_SHUTDOWN_TIMEOUT` (default 40s).

Events in the in-memory buffer and incomplete retry sequences may be lost.

## Security Hardening

**SSRF protection:** Webhook URLs targeting private/reserved IP ranges are rejected at job creation time. Blocked ranges: RFC 1918 (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`), loopback (`127.0.0.0/8`), link-local (`169.254.0.0/16`), and IPv6 equivalents.

**Credential masking:** `cronlite config` masks `DATABASE_URL` passwords and `REDIS_ADDR` credentials in output. Safe for logging.

**SSL mode warning:** Startup emits a warning when `DATABASE_URL` uses `sslmode=disable`. Production deployments should use `sslmode=require` or stricter.

**Error sanitization:** Database error details (constraint names, query fragments) are never leaked in API responses. Internal errors return generic `500 Internal Server Error`.

**API key storage:** Tokens are SHA-256 hashed before storage. The plaintext token is returned exactly once at creation time and never persisted.

## MCP Transport

CronLite exposes an MCP (Model Context Protocol) interface for AI agent integration. Two deployment modes:

**Embedded (Streamable HTTP):** Mounted at `/mcp` on every instance. Uses the same auth flow as REST — Bearer token resolves to namespace. No additional configuration needed.

**Standalone (stdio):** The `cronlite-mcp` binary proxies MCP tool calls to the REST API over HTTP. Requires `CRONLITE_URL` and `CRONLITE_API_KEY` env vars.

**Operational notes:**
- MCP tools are namespace-scoped (same isolation as REST)
- The embedded server uses SSE for streaming — ensure your reverse proxy supports long-lived connections
- 10 tools available: `create-job`, `list-jobs`, `get-job`, `update-job`, `delete-job`, `pause-job`, `resume-job`, `trigger-job`, `next-run`, `resolve-schedule`
- `delete-job` is marked as destructive in the MCP tool metadata

## Horizontal Scaling (Multi-Instance HA)

Run multiple instances against the same Postgres for high availability. Requires `DISPATCH_MODE=db`.

```
┌──────────────┐   ┌──────────────┐   ┌──────────────┐
│  Instance 1  │   │  Instance 2  │   │  Instance 3  │
│  (leader)    │   │  (follower)  │   │  (follower)  │
│  scheduler ✓ │   │  scheduler ✗ │   │  scheduler ✗ │
│  reconciler ✓│   │  reconciler ✗│   │  reconciler ✗│
│  dispatcher ✓│   │  dispatcher ✓│   │  dispatcher ✓│
│  API ✓       │   │  API ✓       │   │  API ✓       │
└──────┬───────┘   └──────┬───────┘   └──────┬───────┘
       └──────────────────┼──────────────────┘
                   ┌──────┴──────┐
                   │  PostgreSQL  │
                   └─────────────┘
```

Leader election uses `pg_try_advisory_lock`. The leader runs scheduler + reconciler. All instances run dispatch workers and serve the API.

### Required Configuration (same on all instances)

```bash
DISPATCH_MODE=db
DATABASE_URL=postgres://...        # same DB
LEADER_LOCK_KEY=728379             # same lock key
RECONCILE_ENABLED=true
METRICS_ENABLED=true
```

### Production Multi-Instance Reference Configuration

```bash
# Required — same on ALL instances
DISPATCH_MODE=db
DATABASE_URL=postgres://user:pass@host:5432/cronlite?sslmode=require
LEADER_LOCK_KEY=728379
RECONCILE_ENABLED=true
METRICS_ENABLED=true

# Recommended
DISPATCHER_WORKERS=4
DB_POLL_INTERVAL=500ms
TICK_INTERVAL=30s
RECONCILE_INTERVAL=5m
RECONCILE_THRESHOLD=15m
LEADER_RETRY_INTERVAL=5s
LEADER_HEARTBEAT_INTERVAL=2s
CIRCUIT_BREAKER_THRESHOLD=5
CIRCUIT_BREAKER_COOLDOWN=2m
```

### Tuning

| Variable | Default | HA Recommendation | Trade-off |
|----------|---------|-------------------|-----------|
| `LEADER_RETRY_INTERVAL` | `5s` | `3s`–`5s` | Faster failover vs. lock contention |
| `LEADER_HEARTBEAT_INTERVAL` | `2s` | `1s`–`2s` | Faster death detection vs. DB pings |
| `DISPATCHER_WORKERS` | `1` | `2`–`4` | Throughput vs. DB connections |
| `TICK_INTERVAL` | `30s` | `10s`–`30s` | Scheduling gap vs. DB load |

### Failover

When the leader dies: Postgres releases the advisory lock (TCP keepalive, 0–5s) → follower acquires lock on next retry → new leader starts scheduler + reconciler.

**Worst-case failover: 3–10s.** During the gap, no new executions are scheduled but dispatch continues on all instances.

The `(job_id, scheduled_at)` unique constraint prevents double-scheduling even during brief split-brain.

### Failover Timing

Advisory lock release depends on Postgres detecting a dead connection. The detection speed depends on TCP keepalive settings on **both** the Postgres server and the OS running CronLite. Without tuning, Linux defaults (`tcp_keepalive_time=7200`) mean failover could take over 2 hours.

**Recommended Postgres settings** (in `postgresql.conf`):
- `tcp_keepalives_idle = 10` (seconds before first probe)
- `tcp_keepalives_interval = 5` (seconds between probes)
- `tcp_keepalives_count = 3` (failed probes before disconnect)

With these settings, worst-case failover is ~25 seconds (10 + 5×3).

The `LEADER_HEARTBEAT_INTERVAL` (default 2s) detects local connection failures quickly, but cannot detect remote connection death — that depends entirely on TCP keepalive.

### HA Test Harness

```bash
./scripts/ha_test.sh
```

Starts 3 instances, verifies single leader, kills leader, asserts failover, checks no double-scheduling. The harness sets `API_KEY=ha-test-key` and sends `Authorization: Bearer ha-test-key` on API calls. See [`docs/ha-test.md`](docs/ha-test.md).

## Monitoring

### Health Check

`GET /health` → `{"status":"ok"}` (200) or `{"status":"degraded"}` (503). Add `?verbose=true` for component details.

### Key Metrics

Enable with `METRICS_ENABLED=true`.

| Metric | Alert When | Meaning |
|--------|------------|---------|
| `cronlite_eventbus_buffer_saturation` | > 0.8 | Buffer filling, event loss imminent |
| `cronlite_orphaned_executions` | > 0 | Orphans detected |
| `cronlite_execution_latency_seconds` | p99 > 60s | Slow delivery |
| `cronlite_scheduler_tick_errors_total` | any increase | DB issues |
| `cronlite_dispatcher_delivery_outcomes_total{outcome="failed"}` | sustained increase | Webhooks failing |
| `cronlite_dispatcher_delivery_outcomes_total{outcome="circuit_open"}` | sustained increase | Circuit breaker active |
| `cronlite_leader_is_leader` | `sum() == 0` | No leader (HA mode) |
| `cronlite_leader_is_leader` | `sum() > 1` | Split brain (HA mode) |

### Recommended Alerts

```yaml
# Critical
- alert: CronLiteReconcilerDisabled
  expr: absent(cronlite_orphaned_executions) == 1
  for: 10m
  labels: { severity: critical }
  annotations:
    summary: "Reconciler appears disabled — no orphan metrics reported"

- alert: CronLiteSplitBrain
  expr: sum(cronlite_leader_is_leader) > 1
  for: 10s
  labels: { severity: critical }
  annotations:
    summary: "Multiple CronLite instances believe they are leader"

- alert: CronLiteNoLeader
  expr: sum(cronlite_leader_is_leader) == 0
  for: 30s
  labels: { severity: critical }
  annotations:
    summary: "No CronLite instance holds the leader lock"

- alert: CronLiteOrphanedExecutions
  expr: cronlite_orphaned_executions > 0
  for: 5m
  labels: { severity: critical }
  annotations:
    summary: "Orphaned executions detected"

# Warning
- alert: CronLiteBufferSaturation
  expr: cronlite_eventbus_buffer_saturation > 0.8
  for: 2m
  labels: { severity: warning }
  annotations:
    summary: "Event bus buffer above 80% capacity"

- alert: CronLiteCircuitBreakerActive
  expr: increase(cronlite_dispatcher_delivery_outcomes_total{outcome="circuit_open"}[5m]) > 0
  for: 1m
  labels: { severity: warning }
  annotations:
    summary: "Circuit breaker is open for one or more webhook URLs"
```

### Quick Diagnosis

```promql
# Losing executions?
increase(cronlite_eventbus_emit_errors_total[1h])
cronlite_orphaned_executions

# System saturated?
cronlite_eventbus_buffer_saturation

# Delivery latency?
histogram_quantile(0.99, rate(cronlite_execution_latency_seconds_bucket[5m]))

# Webhook success rate?
sum(rate(cronlite_dispatcher_delivery_outcomes_total{outcome="success"}[5m])) /
sum(rate(cronlite_dispatcher_delivery_outcomes_total[5m]))
```

### Log Signals

```
scheduler: emitted job=X                          # Execution created
dispatcher: job=X delivered attempt=N              # Webhook delivered
dispatcher: job=X failed                           # All retries exhausted
dispatcher: job=X circuit open for URL, skipping   # Circuit breaker active
reconciler: found N orphaned executions            # Orphans detected
leader: acquired advisory lock 728379              # Became leader
leader: lock 728379 held by another instance       # Follower
leader: released advisory lock 728379              # Lost leadership
```

## Capacity Limits

| Resource | Limit | Consequence |
|----------|-------|-------------|
| Event buffer (channel mode) | 100 events | Drops after 5s block → orphan |
| Fires per job per tick | 1000 (configurable via `MAX_FIRES_PER_TICK`) | Excess skipped until next tick |
| Webhook timeout | 1–60s (default 30) | Retried up to 4 attempts |
| Max retry duration | ~12 min | Then marked `failed` |
| Max shutdown time | 40s default | Buffer + HTTP drain |

### Safe Operating Ranges

| Metric | Safe | Warning | Investigate |
|--------|------|---------|-------------|
| Jobs per instance | < 500 | 500–1000 | > 1000 |
| Executions per minute | < 50 | 50–100 | > 100 |
| Buffer utilization | < 50% | 50–80% | > 80% |
| Webhook p99 latency | < 5s | 5–15s | > 15s |

## Deployment

### Systemd

```ini
[Unit]
Description=CronLite Scheduler
After=network.target postgresql.service

[Service]
Type=simple
User=cronlite
Environment=DATABASE_URL=postgres://localhost/cronlite
Environment=HTTP_ADDR=:8080
Environment=RECONCILE_ENABLED=true
Environment=METRICS_ENABLED=true
ExecStart=/usr/local/bin/cronlite serve
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
```

```bash
systemctl daemon-reload && systemctl enable --now cronlite
```
