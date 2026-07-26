# CronLite

**Schedule HTTP webhooks with cron expressions. No SDK, no queue, no complexity.**

[![Go](https://img.shields.io/badge/Go-1.25+-00ADD8?style=flat&logo=go)](https://go.dev)
[![License](https://img.shields.io/badge/License-AGPL%203.0-blue.svg)](LICENSE)
[![Ask DeepWiki](https://deepwiki.com/badge.svg)](https://deepwiki.com/djlord-it/cronlite)

CronLite is a self-hosted cron-as-a-service with namespace-scoped API keys and MCP support. POST a job with a cron expression and a webhook URL — CronLite fires HTTP callbacks on schedule with HMAC-signed payloads, automatic retries, and Prometheus metrics.

## Quick Start

```bash
git clone https://github.com/djlord-it/cronlite.git
cd cronlite
docker compose up -d
```

Bootstrap an API key:

```bash
docker compose exec cronlite cronlite create-key default local-dev
```

Copy the printed token and export it:

```bash
export CRONLITE_API_KEY="ec_..."
```

Create a job:

```bash
curl -X POST http://localhost:8080/jobs \
  -H "Authorization: Bearer ${CRONLITE_API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "test-job",
    "cron_expression": "* * * * *",
    "timezone": "UTC",
    "webhook_url": "https://httpbin.org/post",
    "webhook_secret": "my-secret"
  }'
```

Check executions:

```bash
curl -H "Authorization: Bearer ${CRONLITE_API_KEY}" \
  http://localhost:8080/jobs/{job_id}/executions
```

<details>
<summary>Manual setup (without Docker)</summary>

```bash
set -euo pipefail
go build -o cronlite ./cmd/cronlite
createdb cronlite
for f in schema/0*.sql; do
  psql -v ON_ERROR_STOP=1 cronlite < "$f"
done
export DATABASE_URL="postgres://localhost/cronlite?sslmode=disable"
./cronlite create-key default local-dev
./cronlite serve
```
</details>

## Architecture

```mermaid
flowchart LR
    App[Your App] -->|POST /jobs| API[REST API]
    API --> DB[(PostgreSQL)]
    DB --> S[Scheduler]
    S -->|insert executions| DB
    DB -->|SKIP LOCKED| D[Dispatcher]
    D -->|POST webhook| App
    R[Reconciler] -.->|recover orphans| DB
```

1. Register jobs via the REST API (any instance)
2. Instances compete for a Postgres advisory lock — exactly one becomes **leader**
3. The leader's **Scheduler** inserts executions into Postgres on each tick
4. **Dispatcher workers** on all instances poll Postgres with `SKIP LOCKED` to claim and deliver webhooks
5. The leader's **Reconciler** recovers stalled executions
6. If the leader dies, a follower takes over within seconds

> **Single-instance mode:** Set `DISPATCH_MODE=channel` (default) for an in-memory Event Bus instead of DB polling. Simpler, but no horizontal scaling.

## API

All API routes except `/health` require `Authorization: Bearer <token>`. Each API key is scoped to a **namespace** — operations only see and modify resources within the caller's namespace.

The full OpenAPI 3.0 spec is at [`api/openapi.yaml`](api/openapi.yaml) and can be used for client SDK generation via `oapi-codegen` or other tools.

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/health` | Health check (`?verbose=true` for components) |
| `POST` | `/jobs` | Create a job |
| `GET` | `/jobs` | List jobs (`?limit=&offset=&enabled=&name=&tag=key:value`) |
| `GET` | `/jobs/{id}` | Get job details + recent executions |
| `PATCH` | `/jobs/{id}` | Update job fields |
| `DELETE` | `/jobs/{id}` | Delete a job |
| `POST` | `/jobs/{id}/pause` | Pause a job |
| `POST` | `/jobs/{id}/resume` | Resume a job |
| `POST` | `/jobs/{id}/trigger` | Trigger immediate execution |
| `GET` | `/jobs/{id}/next-run` | Get next run + upcoming run times |
| `GET` | `/jobs/{id}/executions` | List executions (`status`, `trigger_type`, `since`, `until`) |
| `GET` | `/executions/{id}` | Get execution detail |
| `GET` | `/executions/pending-ack` | List unacknowledged completed executions |
| `POST` | `/executions/{id}/ack` | Acknowledge execution |
| `POST` | `/schedules/resolve` | Resolve natural-language schedule to cron |
| `POST` | `/api-keys` | Create API key (token returned once) |
| `GET` | `/api-keys` | List API keys |
| `DELETE` | `/api-keys/{id}` | Revoke API key |

### Webhook Delivery

Each fired job sends a POST with HMAC-signed payload:

```
X-CronLite-Event-ID: <attempt-uuid>
X-CronLite-Execution-ID: <execution-uuid>
X-CronLite-Signature: <hmac-sha256-hex>
```

**Retries:** 4 attempts with backoff (immediate → 30s → 2m → 10m). Retryable: 5xx, 429, network errors. Non-retryable: 4xx.

**Circuit breaker:** Per-URL circuit breaker protects downstream services from retry storms. See the [Operator Guide](OPERATORS.md#circuit-breaker) for full behavior and tuning.

Use `X-CronLite-Execution-ID` for idempotency in your handler.

<details>
<summary>Signature verification (Go)</summary>

```go
func verifySignature(secret string, body []byte, signature string) bool {
    mac := hmac.New(sha256.New, []byte(secret))
    mac.Write(body)
    expected := hex.EncodeToString(mac.Sum(nil))
    return hmac.Equal([]byte(expected), []byte(signature))
}
```
</details>

## Horizontal Scaling

Run multiple instances against the same Postgres for HA. Requires `DISPATCH_MODE=db`.

- **Leader election** via Postgres advisory lock — one instance runs scheduler + reconciler
- **All instances** dispatch webhooks and serve the API
- **Automatic failover** within seconds if the leader dies

> See the [Operator Guide](OPERATORS.md#horizontal-scaling-multi-instance-ha) for configuration, tuning, failover timing, and alerting rules.

## Lightweight Admin UI

CronLite includes an optional server-rendered admin UI at `/admin`. It uses Go templates and embedded CSS only—no JavaScript, frontend framework, Node runtime, CDN, or separate asset files.

For local development, Docker and Go are the only prerequisites:

```bash
./scripts/admin-local.sh
```

The launcher starts PostgreSQL, applies missing admin migrations through 008, generates a temporary bootstrap token, and prints the admin URL. It does not modify `.cronlite.local.env`; PostgreSQL remains running after CronLite exits.

For a manual launch, apply every numbered migration in order through 008, set an installation token, and enable the UI:

```bash
set -e
for migration in schema/[0-9][0-9][0-9]_*.sql; do
  psql "$DATABASE_URL" -v ON_ERROR_STOP=1 < "$migration"
done
export ADMIN_ENABLED=true
export ADMIN_BOOTSTRAP_TOKEN="replace-with-a-long-random-token"
./cronlite serve
```

Open `http://localhost:8080/admin`. On a fresh installation, `/admin/setup` accepts the installation token and creates the first namespace-scoped API key. The key is displayed once. Setup is unavailable whenever any API key exists, and users sign in with an existing API key. If operators delete all API keys through an external administrative process, setup becomes available again and still requires `ADMIN_BOOTSTRAP_TOKEN`. Remove the token from the runtime environment immediately after activation; set a new secret and restart CronLite if bootstrap is intentionally needed again.

Admin sessions are opaque, revocable PostgreSQL records, so they survive process restarts. Activity extends the 30-minute idle timeout (`ADMIN_SESSION_TTL`) when half of it remains, but never beyond the 12-hour absolute lifetime (`ADMIN_SESSION_ABSOLUTE_TTL`); either expiry requires sign-in again. A successful login replaces the session presented by that browser. Revoking an API key immediately invalidates its sessions, and deleting the key removes them through the database foreign-key cascade.

Session and public-CSRF cookies are host-only, `HttpOnly`, `SameSite=Strict`, and scoped to `/admin`. `Secure` is disabled for local HTTP and enabled by default when `CRONLITE_ENV=production`; production must serve the admin UI over HTTPS so those cookies work. Secure-cookie mode also emits HSTS. The admin handler uses per-form CSRF tokens, Go's cross-origin request protection, a restrictive Content Security Policy, `no-store` on HTML and authentication responses, and server-side session deletion plus cookie clearing on logout.

## Admin CI

The blocking admin workflow is [`.github/workflows/admin-ci.yml`](.github/workflows/admin-ci.yml). Its four jobs cover:

- `admin-unit-security`: race-enabled unit/security tests, fuzz smoke tests, and an 80% admin coverage gate.
- `admin-postgres-integration`: tagged integration tests against a fresh dedicated PostgreSQL database with every migration applied.
- `admin-assets-launcher`: shell contracts, template/assets/header tests, and Actionlint.
- `admin-smoke`: four CGO-disabled cross-builds and an isolated Docker image lifecycle smoke test.

Run the primary gate locally:

```bash
./scripts/admin_ci_test.sh
```

The database suite requires Bash, Go, Docker with Compose v2, and OpenSSL. The following starts only a one-off PostgreSQL service with an ephemeral host port in a uniquely named Compose project. Its fresh volume causes the pinned PostgreSQL entrypoint to apply the mounted migrations 001–008 in lexical order with `ON_ERROR_STOP`; the EXIT trap removes only this disposable project and volume, leaving any normal `cronlite` database untouched.

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

Validate the workflow and reproduce the build/smoke checks with:

```bash
go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.7 .github/workflows/*.yml

CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /tmp/cronlite-linux-amd64 ./cmd/cronlite
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o /tmp/cronlite-linux-arm64 ./cmd/cronlite
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -o /tmp/cronlite-darwin-amd64 ./cmd/cronlite
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -o /tmp/cronlite-darwin-arm64 ./cmd/cronlite

export ADMIN_BOOTSTRAP_TOKEN="$(openssl rand -hex 32)"
export COMPOSE_PROJECT_NAME="cronlite-admin-smoke-$UID-$$"
cleanup_admin_smoke() {
  docker compose -f docker-compose.admin-ci.yml down -v --remove-orphans
  unset ADMIN_BOOTSTRAP_TOKEN COMPOSE_PROJECT_NAME
}
trap cleanup_admin_smoke EXIT

docker build --tag cronlite:admin-ci .
docker compose -f docker-compose.admin-ci.yml up --detach --wait --no-build
published_address="$(docker compose -f docker-compose.admin-ci.yml port cronlite 8080)"
ADMIN_BASE_URL="http://127.0.0.1:${published_address##*:}" \
  ./scripts/admin_smoke_test.sh
```

## CLI

| Command | Description |
|---------|-------------|
| `cronlite serve` | Start server |
| `cronlite validate` | Validate config (exit 0/2) |
| `cronlite config` | Print effective config (secrets masked) |
| `cronlite version` | Print version |
| `cronlite create-key <namespace> <label>` | Create namespace API key and print plaintext token once |

## MCP (Model Context Protocol)

CronLite exposes an MCP interface so AI agents can manage cron jobs programmatically. Two deployment options:

### Embedded Server (Streamable HTTP)

Every CronLite instance serves MCP at `/mcp`. No extra binary needed — just point your MCP client at the running server.

### Standalone Stdio Proxy

For MCP clients that require stdio transport (e.g., Claude Desktop):

```bash
CRONLITE_URL=http://localhost:8080 \
CRONLITE_API_KEY=$CRONLITE_API_KEY \
go run ./cmd/cronlite-mcp
```

<details>
<summary>Claude Desktop configuration</summary>

```json
{
  "mcpServers": {
    "cronlite": {
      "command": "/path/to/cronlite-mcp",
      "env": {
        "CRONLITE_URL": "http://localhost:8080",
        "CRONLITE_API_KEY": "ec_..."
      }
    }
  }
}
```
</details>

### Available Tools

| Tool | Description |
|------|-------------|
| `create-job` | Create a job (name, cron, timezone, webhook URL, optional tags/secret) |
| `list-jobs` | List jobs (filter by name, enabled status) |
| `get-job` | Get job details with schedule and recent executions |
| `update-job` | Update job fields |
| `delete-job` | Delete a job |
| `pause-job` | Pause scheduled execution |
| `resume-job` | Resume a paused job |
| `trigger-job` | Trigger immediate manual execution |
| `next-run` | Get next scheduled run times |
| `resolve-schedule` | Convert natural language (e.g., "every weekday at 9am") to cron |

All tools are namespace-scoped via the API key used for authentication.

## Security

- **Namespace isolation**: API keys are scoped to namespaces — each key can only access its own jobs and executions
- **SSRF protection**: Webhook URLs targeting private/reserved IP ranges (RFC 1918, loopback, link-local) are rejected at creation time
- **Rate limiting**: Two-layer rate limiting — per-IP (default 10 req/sec, before auth) and per-namespace (default 100 req/sec, after auth) on all endpoints except `/health`
- **Credential safety**: `DATABASE_URL` and `REDIS_ADDR` credentials are masked in `cronlite config` output; startup warns when `sslmode=disable`
- **Error sanitization**: Database error details are never exposed in API responses

The built-in per-IP limiter keys clients from Go's `RemoteAddr` and deliberately does not trust `X-Forwarded-For`. Behind a TLS reverse proxy, configure client-IP rate limiting at the trusted proxy or ingress; the CronLite limiter then remains defense-in-depth and normally sees the proxy address.

## Configuration

All configuration is via environment variables. Run `./cronlite --help` for defaults. See the [Operator Guide](OPERATORS.md#configuration-reference) for the full reference and production recommendations.

## License

[AGPL-3.0](LICENSE)
