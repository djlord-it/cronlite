# CronLite

**Schedule HTTP webhooks with cron expressions. No SDK, no queue, no complexity.**

[![Go](https://img.shields.io/badge/Go-1.25+-00ADD8?style=flat&logo=go)](https://go.dev)
[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)
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
go build -o cronlite ./cmd/cronlite
createdb cronlite
for f in schema/0*.sql; do psql cronlite < "$f"; done
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

## Configuration

All configuration is via environment variables. Run `./cronlite --help` for defaults. See the [Operator Guide](OPERATORS.md#configuration-reference) for the full reference and production recommendations.

## License

[Apache 2.0](LICENSE)
