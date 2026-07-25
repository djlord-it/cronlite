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

export DATABASE_URL="${DATABASE_URL:-postgres://cronlite:cronlite@localhost:5432/cronlite?sslmode=disable}"

docker compose up -d postgres

container_id="$(docker compose ps -q postgres)"
[[ -n "$container_id" ]] || {
  printf 'error: PostgreSQL container was not created\n' >&2
  exit 1
}

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

table="$(
  docker compose exec -T postgres psql -U cronlite -d cronlite -tAc \
    "SELECT to_regclass('public.admin_sessions')" |
    tr -d '[:space:]'
)"
if [[ "$table" != admin_sessions ]]; then
  docker compose exec -T postgres psql -v ON_ERROR_STOP=1 -U cronlite -d cronlite \
    < schema/007_admin_sessions.sql
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
