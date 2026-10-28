# CronLite Operator Guide

Operational contract: what CronLite guarantees, how it fails, and how to run it.

## Important Notes

**Use `DISPATCH_MODE=db` for multi-instance deployments.** Channel mode (the default) uses local in-memory notifications and single-instance scheduler wiring. Persistent claims coordinate delivery admission, but channel mode does not provide supported multi-instance scheduling or leader failover. If you're running a single instance, channel mode works fine.

**Make sure `RECONCILE_ENABLED=true` in production.** Without the reconciler, orphaned executions from crashes, buffer overflow, or stale claims have no automatic recovery path. DB mode can recover with deliberate SQL intervention; channel mode also needs a notification or temporarily enabled reconciliation.

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
             │        Benefit: crash recovery via fenced renewable leases; ready to scale later
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
2. Old instance: MCP streams close → HTTP drains → scheduler stops → reconciler stops → dispatcher drains → exit
3. New instance starts, attempts advisory lock
4. If old leader: its dedicated lock session closes after scheduler/reconciler stop, allowing a follower to acquire it
5. If old follower: no leadership change, new instance joins as follower

HTTP and channel-dispatch drain budgets total `DISPATCHER_DRAIN_TIMEOUT` + `HTTP_SHUTDOWN_TIMEOUT` (default 40s). Allow additional time for producer and database cancellation; this is not a hard process-exit deadline.

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
| `DISPATCHER_WORKERS` | `1` | DB poll workers; each allows 4 concurrent deliveries (DB mode) |
| `DB_POLL_INTERVAL` | `500ms` | Sleep between polls when idle (DB mode) |
| `RECONCILE_ENABLED` | `false` | Enable orphan recovery |
| `RECONCILE_INTERVAL` | `5m` | Orphan scan frequency |
| `RECONCILE_THRESHOLD` | `19m` | Age before execution is considered orphaned |
| `RECONCILE_REQUEUE_THRESHOLD` | `2m` | Renewable execution lease in both modes; production minimum 30s |
| `RECONCILE_BATCH_SIZE` | `100` | Max orphans per cycle |
| `METRICS_ENABLED` | `false` | Enable Prometheus `/metrics` |
| `CIRCUIT_BREAKER_THRESHOLD` | `5` | Consecutive failures to open circuit (0 = disabled) |
| `CIRCUIT_BREAKER_COOLDOWN` | `2m` | Cooldown before probe attempt |
| `DB_OP_TIMEOUT` | `5s` | Positive context deadline per store operation; not a hard transport deadline |
| `DB_MAX_OPEN_CONNS` | `25` | Max open DB connections; DB mode needs at least two because leadership reserves one |
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
| `ADMIN_ENABLED` | `false` | Enable the server-rendered admin UI at `/admin` |
| `ADMIN_BOOTSTRAP_TOKEN` | *(empty)* | Secret required by `/admin/setup` when no API key exists; never printed by `cronlite config` |
| `ADMIN_SESSION_TTL` | `30m` | Idle timeout for PostgreSQL-backed admin sessions; activity refreshes it after half the timeout |
| `ADMIN_SESSION_ABSOLUTE_TTL` | `12h` | Maximum session lifetime; requires sign-in again even if the session remains active |
| `ADMIN_COOKIE_SECURE` | production-aware | Secure cookie flag; defaults to `true` in production and `false` otherwise |
| `MIGRATE_ON_START` | `false` | Apply pending schema migrations before `serve` starts (see [Migrations](#migrations)) |
| `MIGRATE_LOCK_TIMEOUT` | `2m` | How long migrations wait for the migration lock held by another instance |
| `WEBHOOK_ALLOWED_PRIVATE_SUFFIXES` | *(empty)* | Comma-separated hostnames or `.suffix` domains whose private-range (RFC 1918 / unique-local) addresses webhooks may reach; see [Security Hardening](#security-hardening) |
| `WEBHOOK_ALLOW_TAILNET` | `false` | Deliver webhooks to hosts on this node's tailnet over an embedded Tailscale node; needs a `-tags tailscale` build. See [Tailnet delivery](#tailnet-delivery) |
| `WEBHOOK_TAILNET_HOSTS` | *(empty)* | Which tailnet hosts each namespace may call, e.g. `billing=invoices;ops=*`. Required with `WEBHOOK_ALLOW_TAILNET=true`, rejected otherwise. Hosts are single names without the tailnet suffix; `*` allows all hosts or all namespaces. A namespace not listed can reach no tailnet host. See [Tailnet delivery](#tailnet-delivery) |
| `TS_LISTEN` | `false` | Also serve the API, admin UI and MCP endpoint at `https://<TS_HOSTNAME>.<tailnet>.ts.net`; needs a `-tags tailscale` build. See [Tailnet access](#tailnet-access) |
| `TS_DISABLE_PUBLIC_LISTENER` | `false` | Skip the `HTTP_ADDR` listener so CronLite is reachable only over the tailnet; requires `TS_LISTEN=true` |
| `TS_AUTHKEY` | *(empty)* | Tailscale auth key for the embedded node. Never logged or printed by `cronlite config`. Optional once `TS_STATE_DIR` holds a logged-in node |
| `TS_HOSTNAME` | `cronlite` | Node name on the tailnet (a single DNS label) |
| `TS_STATE_DIR` | *(empty)* | Directory for the node's identity and keys (and its HTTPS certificate); required with `WEBHOOK_ALLOW_TAILNET` or `TS_LISTEN`. Use a persistent volume |

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
- Database operation contexts — store queries use `DB_OP_TIMEOUT`; startup probes and a failed PostgreSQL cancellation exchange can still block
- Ordered shutdown — MCP streams close → HTTP drains → scheduler stops → reconciler stops → dispatcher drains
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

Both modes require all migrations to be applied. See [Migrations](#migrations).

## Migrations

Schema migrations live in `schema/` and are numbered sequentially. CronLite ships a runner that applies them in order:

```bash
cronlite migrate           # apply every pending migration
cronlite migrate --status  # show applied / pending migrations
```

Set `MIGRATE_ON_START=true` to run the same logic before `cronlite serve` starts (off by default; `MIGRATE_LOCK_TIMEOUT` bounds the wait for the migration lock, default `2m`). Progress is recorded in the `cronlite_schema_migrations` table. A database that already has the schema is baselined rather than re-migrated.

**Existing installs and 011:** migration 011 is never applied automatically to an existing install, because it requires every old writer to be stopped first. `cronlite migrate` and `MIGRATE_ON_START` exit with a "manual migration required" error listing every pending migration up to and including 011; apply the pending migrations named in the error, in order, then rerun, and the next run records them and continues. On a fresh empty database 011 is applied automatically. See [schema/README.md](schema/README.md) for the full procedure and runner details, and [Upgrading](README.md#upgrading) for release notes.

Manual application with `psql` still works if you prefer to manage migrations yourself:

```bash
set -euo pipefail
for f in schema/0*.sql; do
  psql -v ON_ERROR_STOP=1 cronlite < "$f"
done
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
| `007_admin_sessions.sql` | Revocable browser sessions for the optional admin UI |
| `008_admin_session_absolute_expiry.sql` | Absolute lifetime cap for admin sessions |
| `009_emitted_claim_index.sql` | Partial queue-claim index for DB dispatch |
| `010_job_control_center.sql` | Admin saved views and bulk audit |
| `011_execution_ownership.sql` | Generation fencing and renewable leases in both modes |

Migration 009 builds an index and drops the previous broad status index. Apply
it during a maintenance window on large existing databases because a regular
index build can briefly block writes. Fresh Compose databases apply it during
initialization.

Current releases require all migrations through 011. Skipping migrations may cause auth failures, missing columns, or degraded DB dispatch and reconciler performance.

## Admin UI Operations

The admin UI is disabled unless `ADMIN_ENABLED=true`. It is embedded in the existing `cronlite` binary and serves English HTML/CSS at `/admin`, with permanent dark mode and a bundled JetBrains Mono font. Existing namespace data appears immediately after sign-in; getting-started guidance is optional.

The UI supports the complete namespace administration API: job configuration and actions, key creation/listing/revocation, execution history and acknowledgements, and schedule resolution. API key secrets appear once and are not listed afterward. Key revocation requires confirmation, and the UI blocks revoking the current sign-in key to avoid accidental lockout; create a replacement and sign in with it first. Runtime Settings shows a safe subset of effective server configuration. Change deployment settings through the server environment and restart; no database credentials, bootstrap tokens, or legacy API secrets appear in Settings.

For a fresh database:

1. Stop/drain old writers and apply migrations through `011_execution_ownership.sql`.
2. Set a long random `ADMIN_BOOTSTRAP_TOKEN`.
3. Start CronLite and open `/admin/setup`.
4. Enter the installation token, namespace, and first key label.
5. Save the API key shown once, then remove `ADMIN_BOOTSTRAP_TOKEN` from the runtime environment.

The bootstrap token is an installation secret. Keep it out of application logs, support bundles, and CI artifacts. The bootstrap transaction takes a PostgreSQL advisory lock and refuses creation while any API key exists, so a retained token is dormant during normal operation. If operators delete every API key, setup becomes available again and the configured token regains authority. Remove `ADMIN_BOOTSTRAP_TOKEN` immediately after activation. Before any intentional bootstrap reuse, generate and configure a new token, restart CronLite, complete setup, and remove it again.

Regular sign-in hashes the supplied API key, creates an opaque session tied to that key, and deletes the session token already presented by that browser. Sessions are stored in PostgreSQL and remain valid across CronLite restarts. The 30-minute idle expiry refreshes on activity once half the idle window remains, capped by the 12-hour absolute expiry. Either expiry requires sign-in again. Disabling a key makes its sessions unusable immediately; deleting it cascades to its session rows.

Session and public-CSRF cookies are host-only, `HttpOnly`, `SameSite=Strict`, and scoped to `/admin`. `ADMIN_COOKIE_SECURE=false` supports local HTTP only. In production, terminate TLS before CronLite, use HTTPS externally, leave `ADMIN_COOKIE_SECURE=true`, and verify the resulting one-year HSTS header is appropriate for the host.

All state-changing requests require CSRF tokens and pass through Go's `CrossOriginProtection`; login and setup also use the normal per-IP rate limit. The admin UI sends a restrictive CSP and `no-store` on HTML/authentication responses. Logout deletes the server-side session, expires its cookie, and clears browser cache data.

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

CronLite keys its per-IP limiter from Go's `RemoteAddr` and does not trust `X-Forwarded-For`. When serving through a TLS reverse proxy, enforce client-IP rate limits at the trusted proxy or ingress; treat CronLite's limiter, which normally sees the proxy address, as defense-in-depth.

**`last_used_at` tracking:** Debounced in-memory with background flush every 60 seconds to minimize DB writes under high traffic.

**Legacy compatibility:** The `API_KEY` env var still works as a fallback and maps to the `default` namespace, but now emits rate-limited deprecation warnings in logs. Migrate to namespace-scoped API keys (`cronlite create-key`) for production use.

## Execution Lifecycle

```
emitted ──→ in_progress ──→ delivered  (both modes)
            │
            ├──→ failed
            │
            └──→ emitted  (stale requeue by reconciler)
```

Terminal states (`delivered`, `failed`) are immutable. Completion is conditional
on the current claim generation and an unexpired lease.

The default two-minute lease renews every thirty seconds across HTTP and retry
backoffs. PostgreSQL decides expiry after acquiring the row lock; application
clock skew cannot reclaim a valid lease. With the default five-minute scan,
recovery typically waits the remaining lease plus a scan (up to roughly seven
minutes under healthy, uncongested conditions). A live worker losing confirmation
stops further attempts. Old in-flight remote effects can still overlap recovery.

Before upgrade, stop/drain all old writers, apply migration 011, then start the
new instances. Old unfenced writers invalidate this guarantee during mixed-version
operation. Legacy null-lease rows are recovered in bounded batches; see
[schema migration instructions](schema/README.md#execution-ownership-migration-011).
Persistence stages have five-second contexts and a separate cancellation watchdog.
A database transport that ignores cancellation can still retain a bounded delivery
slot and block shutdown. An audit-write deadline after HTTP success abandons
completion conservatively; ordinary immediate audit errors remain best effort.
Redis analytics deadlines do not revoke ownership or prevent delivery.

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
| **Postgres rejects startup connection** | Refuses to start (exit 1); a silent connection can stall startup |
| **Postgres down at runtime** | Store operations use `DB_OP_TIMEOUT`, then retry/recovery applies; driver cancellation can still stall on a silent connection |
| **HTTP listener unavailable** | Refuses to start (exit 1); unexpected serve errors stop the runtime |
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

On SIGINT/SIGTERM: MCP streams close → HTTP server drains (`HTTP_SHUTDOWN_TIMEOUT`) → scheduler stops → reconciler stops → dispatcher drains (`DISPATCHER_DRAIN_TIMEOUT`) → exit 0. HTTP ingress drains first because REST and MCP manual triggers also produce events. If HTTP draining times out, request contexts are cancelled and connections are closed; an interrupted request may have persisted an execution and relies on recovery.

**Drain budgets:** `DISPATCHER_DRAIN_TIMEOUT` + `HTTP_SHUTDOWN_TIMEOUT` (default 40s), plus producer/database cancellation. These budgets do not establish a hard process-exit deadline.

Events in the in-memory buffer and incomplete retry sequences may be lost.

## Security Hardening

**SSRF protection:** Webhook URLs targeting private/reserved IP ranges are rejected at job creation time, and every webhook connection is checked again at connect time. The guard resolves the hostname itself, checks every resolved address, and dials a checked address directly, so DNS rebinding and redirects to internal hosts are covered.
- Always blocked (cannot be allowlisted): loopback, link-local (including cloud metadata `169.254.169.254`), unspecified/`0.0.0.0/8`, carrier-grade NAT `100.64.0.0/10`, multicast, IPv6 zones, and the IPv4-mapped/NAT64 forms of these.
- Blocked unless allowlisted: RFC 1918 (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`) and IPv6 unique-local `fc00::/7`.
- `WEBHOOK_ALLOWED_PRIVATE_SUFFIXES` is a comma-separated list of hostnames. An entry starting with `.` (e.g. `.railway.internal`) matches that domain and its subdomains; any other entry (e.g. `webhook-receiver`) matches that exact hostname. Matching is case-insensitive. IP literals are rejected as entries and a literal IP in a URL is never allowlisted. An allowlisted name only unlocks the private ranges above, never the always-blocked ones.
- **The allowlist is installation-wide.** Any API key holder in any namespace can deliver to allowlisted private hosts (with the Railway template default `.railway.internal`, that is every sibling service in the Railway project). Keep it as narrow as practical, e.g. exact service names instead of `.railway.internal`.
- `host.docker.internal` only helps where it resolves to a private range (Linux `host-gateway`, e.g. 172.17.0.1). Docker Desktop resolves it to 0.250.250.254, inside the always-blocked `0.0.0.0/8`, so allowlisting it does not work there: run the receiver as a Compose service (or a relay on the Compose network) and allowlist that service name.
- A blocked delivery fails immediately: it is not retried and does not count toward the circuit breaker.
- **Breaking change:** jobs that call private destinations (Docker/Compose service names, `*.railway.internal`, LAN hosts) now fail until their hostnames are allowlisted. See [Upgrading](README.md#upgrading).
- Tailnet hosts are a separate, opt-in path with its own checks: see [Tailnet delivery](#tailnet-delivery).

**Credential masking:** `cronlite config` masks `DATABASE_URL` passwords and `REDIS_ADDR` credentials in output. Safe for logging.

**Admin secret handling:** `cronlite config` omits `ADMIN_BOOTSTRAP_TOKEN`, and admin startup logs report only idle/absolute session TTLs and the Secure-cookie setting. Do not put bootstrap tokens or one-time API keys in logs, build output, diagnostics, or retained artifacts.

**SSL mode warning:** Startup emits a warning when `DATABASE_URL` uses `sslmode=disable`. Production deployments should use `sslmode=require` or stricter.

**Error sanitization:** Database error details (constraint names, query fragments) are never leaked in API responses. Internal errors return generic `500 Internal Server Error`.

**API key storage:** Tokens are SHA-256 hashed before storage. The plaintext token is returned exactly once at creation time and never persisted.

**Admin transport:** Production admin traffic must use HTTPS with `ADMIN_COOKIE_SECURE=true`. In that mode CronLite adds `Strict-Transport-Security: max-age=31536000`; local HTTP intentionally uses insecure cookies and does not emit HSTS.

## Tailscale

CronLite can join a Tailscale tailnet with an embedded node (`tsnet`): it can deliver webhooks to tailnet services, serve its API and admin UI on the tailnet, or both. For a step-by-step setup (tailnet ACLs, auth key, `docker run`, troubleshooting), see the [Tailscale guide](TAILSCALE.md). This section is the reference.

### Tailnet delivery

With `WEBHOOK_ALLOW_TAILNET=true`, CronLite runs an embedded Tailscale node (`tsnet`, no Tailscale app or sidecar) and can deliver webhooks to services on your tailnet that have no public address, e.g. `https://my-service.<tailnet>.ts.net/hook`.

- **Build:** the Tailscale code is only compiled into binaries built with `-tags tailscale` (`docker build --build-arg TAGS=tailscale .`). It adds about 19 MB to the binary. A default build refuses `WEBHOOK_ALLOW_TAILNET=true` at startup and in `cronlite validate`.
- **Startup:** the node starts when `WEBHOOK_ALLOW_TAILNET` or `TS_LISTEN` is set. `serve` waits up to 60s for it to connect and exits if it does not. The tailnet must have MagicDNS enabled. The node shuts down after the dispatcher has drained.
- **Which URLs use the tailnet:** only hostnames that are subdomains of this node's own tailnet domain (the MagicDNS suffix, e.g. `example-tailnet.ts.net`). Matching works on whole labels, so `evil-ts.net`, `ts.net.evil.com` and other tailnets' `*.ts.net` hosts (for example Funnel URLs) take the normal public path as before.
- **Checks on the tailnet path:** the name is resolved through MagicDNS. Every answer must be a Tailscale node address (`100.64.0.0/10` or `fd7a:115c:a1e0::/48`), excluding `100.100.100.100`, the 4via6 subnet-router range and this node's own addresses. A checked address is then dialled directly. Anything else is blocked and not retried.
- **Unchanged everywhere else:** IP literals, including `100.x` tailnet addresses, are never routed to the tailnet and stay blocked. Every redirect hop is routed and checked again on its own, in both directions.
- **Scope:** each namespace reaches only the hosts listed for it in `WEBHOOK_TAILNET_HOSTS`, e.g. `billing=invoices;ops=*`. Job creation fails with 422 `webhook_host_not_allowed` for any other host, and delivery is blocked with `tailnet host not allowed for namespace` and not retried. Also use Tailscale ACLs: tag the node (e.g. `tag:cronlite`) and grant it only the destinations and ports it should call.
- **Logs:** `TS_AUTHKEY` is never logged. tsnet's verbose backend logs are discarded; its user-facing messages (such as a login URL when no auth key is set) appear with a `tailnet:` prefix. tsnet uploads diagnostic logs to Tailscale by default; set `TS_NO_LOGS_NO_SUPPORT=true` to turn that off.

### Tailnet access

With `TS_LISTEN=true`, the same embedded node also serves everything the HTTP listener serves (REST API, `/admin`, `/mcp`, `/health`, `/metrics`) at `https://<TS_HOSTNAME>.<tailnet>.ts.net`, with no public port. It can be used with or without `WEBHOOK_ALLOW_TAILNET`.

- **HTTPS:** the tailnet must have MagicDNS and HTTPS certificates enabled (Tailscale admin console → DNS); otherwise `serve` exits at startup with a message saying so. Plain `http://` on the tailnet only redirects to the HTTPS name.
- **Certificate:** `serve` requests the node's Let's Encrypt certificate in the background as soon as the node is up and logs `tailnet: https certificate ready`. Issuance can take 30 seconds or more; HTTPS requests that arrive before it finishes fail the TLS handshake. The certificate is stored in `TS_STATE_DIR`, so later restarts with the same state are immediate, and Tailscale renews it automatically as it nears expiry. Certificates are publicly logged (Certificate Transparency), so the node's full `*.ts.net` name becomes public, though it stays unreachable from outside the tailnet.
- **Public DNS and outbound HTTPS:** issuing the certificate needs the host's normal DNS to resolve `acme-v02.api.letsencrypt.org` and outbound HTTPS to it. Containers and hosts without Tailscale installed have this already. If you run CronLite on a machine that also runs the Tailscale client, make sure that client has not taken over the system DNS without an upstream resolver for public names (`tailscale set --accept-dns=false`, or add global nameservers in the admin console). Otherwise the log shows `tailnet: https certificate not ready: ... server misbehaving`.
- **Auth is unchanged:** tailnet requests go through the same API key, admin session and rate-limit middleware as public ones. Being on the tailnet grants nothing by itself. Limit who can reach the node with Tailscale ACLs.
- **Public listener:** `HTTP_ADDR` keeps listening unless `TS_DISABLE_PUBLIC_LISTENER=true`. With it disabled, platform health checks that probe `HTTP_ADDR`/`PORT` (Railway, a Docker or Kubernetes probe on the container port) fail, so remove or replace them.
- **Shutdown:** the tailnet listener drains together with the HTTP listener, first in the shutdown order. The node stops last, after the dispatcher.
- **Status:** `/health?verbose=true` includes `"tailnet": "up"` or `"down"` whenever the node is enabled. It is informational and never turns the response into a 503, so a Tailscale outage does not take a healthy instance out of a load balancer. Alert on the `cronlite_tailnet_up` gauge (1 = connected) instead.
- **Multiple instances:** each instance runs its own node. A second node with the same `TS_HOSTNAME` is renamed by Tailscale (`cronlite-1`, …), so a single tailnet URL reaches one instance; it is not load-balanced.

## MCP Transport

CronLite exposes an MCP (Model Context Protocol) interface for AI agent integration. Two deployment modes:

**Embedded (Streamable HTTP):** Mounted at `/mcp` on every instance. Uses the same auth flow as REST — Bearer token resolves to namespace. No additional configuration needed.

**Standalone (stdio):** The `cronlite-mcp` binary proxies MCP tool calls to the REST API over HTTP. Requires `CRONLITE_URL` and `CRONLITE_API_KEY` env vars.

**Operational notes:**
- MCP tools are namespace-scoped (same isolation as REST)
- The embedded server uses SSE for streaming — ensure your reverse proxy supports long-lived connections
- Abandoned legacy sessions expire after 10 minutes of inactivity (the SDK sweeps every 5 minutes). Active SSE streams send a heartbeat every 30 seconds; application shutdown cancels requests and closes sessions.
- REST webhook timeouts are validated at 1–60 seconds. Embedded MCP retains its existing support for longer representable timeouts; renewable ownership is independent of those timeouts and the retry window.
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

Starts 3 instances, verifies single leader, kills leader, asserts failover, checks no double-scheduling. The harness sets `API_KEY=ha-test-key` and sends `Authorization: Bearer ha-test-key` on API calls. See [`scripts/ha_test.sh`](scripts/ha_test.sh).

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
| HTTP/backoff budget | 16m30s at 60s attempts | Four attempts; DB/analytics work adds time |
| Max shutdown time | 40s default | Buffer + HTTP drain |

### Safe Operating Ranges

| Metric | Safe | Warning | Investigate |
|--------|------|---------|-------------|
| Jobs per instance | < 500 | 500–1000 | > 1000 |
| Executions per minute | < 50 | 50–100 | > 100 |
| Buffer utilization | < 50% | 50–80% | > 80% |
| Webhook p99 latency | < 5s | 5–15s | > 15s |

## Admin CI Gates

The blocking [admin workflow](.github/workflows/admin-ci.yml) runs on pull requests and pushes to `main`, avoiding duplicate runs for feature branches. It has four independent jobs:

| Job | Gate |
|-----|------|
| `admin-unit-security` | Workflow contract, race-enabled admin/CLI tests, fuzz smoke tests, and admin coverage of at least 80% |
| `admin-postgres-integration` | Tagged integration tests must run and pass under the race detector, with empty or skipped suites rejected against a fresh dedicated database migrated through 011 |
| `admin-assets-launcher` | Launcher/smoke/container shell contracts and Actionlint; template/asset/security-header tests run in the unit suite |
| `admin-smoke` | Linux/Darwin amd64/arm64 CGO-disabled builds and the isolated Docker admin lifecycle, including workspace views and bundled CSS/font |

Run the main local gate with `./scripts/admin_ci_test.sh`. The PostgreSQL gate requires Bash, Go, Docker with Compose v2, and OpenSSL. This procedure starts only a one-off PostgreSQL service with an ephemeral host port in a unique disposable Compose project. Its fresh volume causes migrations 001–011 to run in lexical order with `ON_ERROR_STOP`, and its EXIT trap removes only that project and volume:

```bash
set -euo pipefail

integration_project="cronlite-admin-integration-${UID}-$$"
export ADMIN_BOOTSTRAP_TOKEN="$(openssl rand -hex 32)"
cleanup_admin_integration() {
  docker compose --project-name "$integration_project" \
    --file docker-compose.admin-ci.yml down -v --remove-orphans
  unset ADMIN_BOOTSTRAP_TOKEN
}
trap cleanup_admin_integration EXIT

db_container="$(
  docker compose --project-name "$integration_project" \
    --file docker-compose.admin-ci.yml run --detach --no-deps \
    --publish 127.0.0.1::5432 postgres
)"
for attempt in {1..30}; do
  health="$(docker inspect --format '{{.State.Health.Status}}' "$db_container")"
  [[ "$health" == healthy ]] && break
  if [[ "$health" == unhealthy ]]; then
    docker logs "$db_container"
    exit 1
  fi
  sleep 1
done
[[ "$health" == healthy ]]

published_address="$(docker port "$db_container" 5432/tcp)"
published_port="${published_address##*:}"
[[ "$published_port" =~ ^[0-9]+$ ]]

ADMIN_TEST_DATABASE_URL="postgresql://cronlite:cronlite@127.0.0.1:${published_port}/cronlite?sslmode=disable" \
  go test -tags=integration -race ./internal/webadmin \
    -run '^TestIntegration' -count=1 -v
```

When their diagnostic collection steps run, integration and smoke failures upload sanitized diagnostics retained for seven days. Failures before a job reaches diagnostic collection may not produce an artifact. Redaction is defense in depth: bootstrap tokens, plaintext API keys, database URLs, and database passwords must never be written intentionally to logs or artifacts. The coverage profile is retained for seven days only when profile generation was reached; early contract, setup, or test failures may leave no coverage file to upload.

## Deploying on Railway

The Railway template deploys CronLite from a pinned `ghcr.io/djlord-it/cronlite` image next to Railway's managed Postgres, with a public domain and `/health` as the healthcheck. Railway's built-in cron is minimal (5-minute minimum, UTC only, timing can drift by minutes, overlapping runs are skipped); CronLite adds per-minute schedules, timezones, retries, signed webhooks and an API/MCP surface.

### Variables

| Variable | Value |
|----------|-------|
| `DATABASE_URL` | `${{Postgres.DATABASE_URL}}` |
| `MIGRATE_ON_START` | `true` |
| `DISPATCH_MODE` | `db` |
| `RECONCILE_ENABLED` | `true` |
| `ADMIN_ENABLED` | `true` |
| `ADMIN_COOKIE_SECURE` | `true` |
| `ADMIN_BOOTSTRAP_TOKEN` | generated secret |
| `WEBHOOK_ALLOWED_PRIVATE_SUFFIXES` | `.railway.internal` |

**Allowlist scope:** `WEBHOOK_ALLOWED_PRIVATE_SUFFIXES` is installation-wide: any API key holder in any namespace can deliver to allowlisted private hosts. With the template default `.railway.internal` that is every sibling service in the Railway project, so narrow it to exact service names where practical.

`PORT` is picked up automatically as the listen port unless `HTTP_ADDR` is set.

**`CRONLITE_ENV` is intentionally unset.** `CRONLITE_ENV=production` enforces `METRICS_ENABLED=true` and a non-empty legacy `API_KEY`, so the container would refuse to start with the template's defaults. The production-relevant settings (`db` dispatch, reconciliation, secure cookies) are set explicitly instead.

**Metrics** are off by default. Set `METRICS_ENABLED=true` to expose `/metrics`; when enabled, `/metrics` requires any valid Bearer API key unless `METRICS_PUBLIC=true`.

### First run

1. Open `https://<domain>/admin/setup`.
2. Paste the `ADMIN_BOOTSTRAP_TOKEN` value from the service's Railway Variables tab.
3. Create the first API key and store it; it is shown once.
4. Delete `ADMIN_BOOTSTRAP_TOKEN` and redeploy.

### Connecting clients

- **MCP:** `https://<domain>/mcp` with header `Authorization: Bearer <key>`. See [MCP Transport](#mcp-transport).
- **Sibling services:** webhook to other services in the same Railway project via `http://<service>.railway.internal:<port>/path`. These resolve to private addresses and are permitted by `WEBHOOK_ALLOWED_PRIVATE_SUFFIXES=.railway.internal`; other private hosts must be added to that list.

### Scaling and upgrading

- **Scaling:** increase the service's replica count. `DISPATCH_MODE=db` with leader election handles multiple instances; see [Horizontal Scaling](#horizontal-scaling-multi-instance-ha).
- **Upgrading:** bump the image tag. `MIGRATE_ON_START=true` applies pending migrations on boot under a lock, so replicas starting together are safe. Migration 011 is not auto-applied to databases created before it; see [Migrations](#migrations) and [Upgrading](README.md#upgrading).

### Marketplace description

> CronLite: self-hosted cron-as-a-service. Schedule HTTP webhooks with cron expressions, timezones and signed payloads, with automatic retries, an admin UI and an MCP endpoint for AI agents. Deploys with Postgres; open `/admin/setup` after the first deploy to create your API key.

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
