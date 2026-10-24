#!/usr/bin/env bash
# Local development launcher: PostgreSQL, migrations, an API key, the server,
# and the admin UI in one command. Re-running it is safe and reuses the key
# saved in .cronlite.local.env.
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: ./scripts/dev.sh [--reset]

Starts PostgreSQL (Docker Compose), applies migrations, ensures an API key,
then runs CronLite with the admin UI enabled.

  --reset   Delete the local database volume first (all local data is lost)

Environment overrides: POSTGRES_HOST_PORT (default 54329), HTTP_ADDR (default :8080).
EOF
}

reset=false
for arg in "$@"; do
  case "$arg" in
    --reset) reset=true ;;
    -h | --help)
      usage
      exit 0
      ;;
    *)
      usage >&2
      exit 2
      ;;
  esac
done

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

ENV_FILE=.cronlite.local.env

error() {
  printf 'error: %s\n' "$1" >&2
  exit 1
}

need() {
  command -v "$1" >/dev/null 2>&1 || error "$1 is required"
}

need docker
need go
docker compose version >/dev/null 2>&1 || error "Docker Compose v2 is required"

if [[ -f "$ENV_FILE" ]]; then
  set -a
  # shellcheck disable=SC1090
  source "$ENV_FILE"
  set +a
fi

export POSTGRES_HOST_PORT="${POSTGRES_HOST_PORT:-54329}"
http_addr="${HTTP_ADDR:-:8080}"
http_port="${http_addr##*:}"

if (exec 3<>"/dev/tcp/127.0.0.1/$http_port") 2>/dev/null; then
  hint="stop whatever is listening there or set HTTP_ADDR"
  if [[ -n "$(docker compose ps -q cronlite 2>/dev/null)" ]]; then
    hint="the Compose app container is running; stop it with: docker compose stop cronlite"
  fi
  error "port $http_port is already in use ($hint)"
fi

if [[ "$reset" == true ]]; then
  docker compose down -v --remove-orphans
fi

docker compose up -d postgres

container_id="$(docker compose ps -q postgres)"
[[ -n "$container_id" ]] || error "PostgreSQL container was not created"

published_address="$(docker compose port postgres 5432)"
postgres_port="${published_address##*:}"
[[ "$postgres_port" =~ ^[0-9]+$ ]] ||
  error "could not determine the PostgreSQL host port"
export DATABASE_URL="postgres://cronlite:cronlite@127.0.0.1:${postgres_port}/cronlite?sslmode=disable"

healthy=false
health_attempts="${DEV_HEALTH_ATTEMPTS:-30}"
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

[[ "$healthy" == true ]] ||
  error "PostgreSQL did not become healthy after $health_attempts checks"

# psql inside the container; NOTICEs from idempotent migrations are suppressed.
db_psql() {
  docker compose exec -T -e PGOPTIONS='-c client_min_messages=warning' \
    postgres psql -q -v ON_ERROR_STOP=1 -U cronlite -d cronlite "$@"
}

# A fresh volume already ran every migration via docker-entrypoint-initdb.d.
# These are the idempotent ones an older local database may still be missing.
db_psql < schema/007_admin_sessions.sql

absolute_expiry_column="$(
  db_psql -tAc \
    "SELECT column_name FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'admin_sessions' AND column_name = 'absolute_expires_at'" |
    tr -d '[:space:]'
)"
if [[ "$absolute_expiry_column" != absolute_expires_at ]]; then
  db_psql < schema/008_admin_session_absolute_expiry.sql
fi

db_psql < schema/009_emitted_claim_index.sql
db_psql < schema/010_job_control_center.sql

# Ownership cutover requires all old CronLite writers to be stopped; the port
# check above guarantees no local server is running.
db_psql < schema/011_execution_ownership.sql

# Reuse the saved key while it is still enabled in this database; otherwise
# (first run, --reset, or a revoked key) create one and save it.
key_valid=false
if [[ -n "${CRONLITE_API_KEY:-}" ]]; then
  # The token travels on stdin so it never appears in process arguments.
  key_check="$(
    printf '\\set token %s\n%s\n' "'${CRONLITE_API_KEY//\'/}'" \
      "SELECT EXISTS (SELECT 1 FROM api_keys WHERE enabled AND token_hash = encode(sha256(convert_to(:'token', 'UTF8')), 'hex'));" |
      db_psql -tA |
      tr -d '[:space:]'
  )"
  [[ "$key_check" == t ]] && key_valid=true
fi

if [[ "$key_valid" != true ]]; then
  key_output="$(go run ./cmd/cronlite create-key default local-dev)"
  CRONLITE_API_KEY="$(
    printf '%s\n' "$key_output" | awk '$1 == "Token:" { print $2 }'
  )"
  [[ -n "$CRONLITE_API_KEY" ]] || error "could not parse the created API key"

  tmp_env="$(umask 077 && mktemp "$ENV_FILE.XXXXXX")"
  # Previous values are commented out, not deleted, in case they pointed at
  # another CronLite instance.
  if [[ -f "$ENV_FILE" ]]; then
    sed -E 's/^((export )?CRONLITE_(URL|API_KEY)=)/# replaced by scripts\/dev.sh: \1/' \
      "$ENV_FILE" > "$tmp_env"
  fi
  printf 'CRONLITE_URL=http://localhost:%s\nCRONLITE_API_KEY=%s\n' \
    "$http_port" "$CRONLITE_API_KEY" >> "$tmp_env"
  mv "$tmp_env" "$ENV_FILE"
fi
export CRONLITE_API_KEY

export ADMIN_ENABLED=true
export ADMIN_COOKIE_SECURE=false
export TICK_INTERVAL="${TICK_INTERVAL:-5s}"
export RECONCILE_ENABLED="${RECONCILE_ENABLED:-true}"
export METRICS_ENABLED="${METRICS_ENABLED:-true}"

cat <<EOF

  CronLite dev environment
  ------------------------
  API        http://localhost:${http_port}
  Admin UI   http://localhost:${http_port}/admin
  API key    ${CRONLITE_API_KEY}  (saved to ${ENV_FILE})
  Postgres   postgres://cronlite:cronlite@127.0.0.1:${postgres_port}/cronlite

  Try it:    source ${ENV_FILE}
             curl -H "Authorization: Bearer \$CRONLITE_API_KEY" http://localhost:${http_port}/jobs

  Ctrl+C stops CronLite; PostgreSQL keeps running (docker compose stop postgres).

EOF

exec go run ./cmd/cronlite serve
