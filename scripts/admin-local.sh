#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

need() {
  command -v "$1" >/dev/null 2>&1 || {
    printf 'error: %s is required\n' "$1" >&2
    exit 1
  }
}

need docker
need go
docker compose version >/dev/null 2>&1 || {
  printf 'error: Docker Compose v2 is required\n' >&2
  exit 1
}

if [[ -f .cronlite.local.env ]]; then
  set -a
  # shellcheck disable=SC1091
  source .cronlite.local.env
  set +a
fi

export POSTGRES_HOST_PORT=0
docker compose up -d postgres

container_id="$(docker compose ps -q postgres)"
[[ -n "$container_id" ]] || {
  printf 'error: PostgreSQL container was not created\n' >&2
  exit 1
}

published_address="$(docker compose port postgres 5432)"
postgres_port="${published_address##*:}"
[[ "$postgres_port" =~ ^[0-9]+$ ]] || {
  printf 'error: could not determine the PostgreSQL host port\n' >&2
  exit 1
}
export DATABASE_URL="postgres://cronlite:cronlite@127.0.0.1:${postgres_port}/cronlite?sslmode=disable"

healthy=false
health_attempts="${ADMIN_LOCAL_HEALTH_ATTEMPTS:-30}"
for ((attempt = 1; attempt <= health_attempts; attempt++)); do
  status="$(
    docker inspect --format='{{.State.Health.Status}}' "$container_id" 2>/dev/null ||
      true
  )"
  if [[ "$status" == healthy ]]; then
    healthy=true
    break
  fi
  sleep 1
done

[[ "$healthy" == true ]] || {
  printf 'error: PostgreSQL did not become healthy after %s checks\n' \
    "$health_attempts" >&2
  exit 1
}

docker compose exec -T postgres psql -v ON_ERROR_STOP=1 -U cronlite -d cronlite \
  < schema/007_admin_sessions.sql

absolute_expiry_column="$(
  docker compose exec -T postgres psql -U cronlite -d cronlite -tAc \
    "SELECT column_name FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'admin_sessions' AND column_name = 'absolute_expires_at'" |
    tr -d '[:space:]'
)"
if [[ "$absolute_expiry_column" != absolute_expires_at ]]; then
  docker compose exec -T postgres psql -v ON_ERROR_STOP=1 -U cronlite -d cronlite \
    < schema/008_admin_session_absolute_expiry.sql
fi

if command -v openssl >/dev/null 2>&1; then
  bootstrap_token="$(openssl rand -hex 32)"
else
  bootstrap_token="$(
    od -An -N32 -tx1 /dev/urandom |
      tr -d '[:space:]'
  )"
fi

export ADMIN_ENABLED=true
export ADMIN_COOKIE_SECURE=false
export ADMIN_BOOTSTRAP_TOKEN="$bootstrap_token"

printf '\nCronLite admin: http://localhost:8080/admin\n'
printf 'Bootstrap token: %s\n' "$bootstrap_token"
printf 'The token is temporary and is not written to disk.\n\n'

exec go run ./cmd/cronlite serve
