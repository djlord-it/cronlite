#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LAUNCHER="$ROOT/scripts/dev.sh"

fail() {
  printf 'FAIL: %s\n' "$1" >&2
  exit 1
}

assert_contains() {
  local actual="$1"
  local expected="$2"
  [[ "$actual" == *"$expected"* ]] ||
    fail "expected output to contain: $expected"
}

assert_not_contains() {
  local actual="$1"
  local unexpected="$2"
  [[ "$actual" != *"$unexpected"* ]] ||
    fail "expected output not to contain: $unexpected"
}

assert_equals() {
  local actual="$1"
  local expected="$2"
  [[ "$actual" == "$expected" ]] ||
    fail "expected '$expected', got '$actual'"
}

[[ -f "$LAUNCHER" ]] || fail "scripts/dev.sh is missing"

test_root="$(mktemp -d)"
listener_pid=""
cleanup() {
  [[ -n "$listener_pid" ]] && kill "$listener_pid" 2>/dev/null
  rm -rf "$test_root"
}
trap cleanup EXIT

output=""
exit_status=0
calls=""
go_env=""
env_file=""
project_dir=""

# run_launcher CASE [ENV_FILE_CONTENT] [-- LAUNCHER_ARGS...]
run_launcher() {
  local case_name="$1"
  local env_content="${2:-}"
  shift 2 || shift $#
  [[ "${1:-}" == -- ]] && shift

  local case_dir="$test_root/$case_name"
  local fake_bin="$case_dir/bin"
  local calls_file="$case_dir/docker-calls"
  local go_env_file="$case_dir/go-env"
  project_dir="$case_dir/project"

  mkdir -p "$project_dir/scripts" "$project_dir/schema" "$fake_bin"
  cp "$LAUNCHER" "$project_dir/scripts/dev.sh"
  for migration in 007_admin_sessions 008_admin_session_absolute_expiry \
    009_emitted_claim_index 010_job_control_center 011_execution_ownership \
    012_api_key_tailnet_login; do
    cp "$ROOT/schema/$migration.sql" "$project_dir/schema/$migration.sql"
  done
  touch "$project_dir/docker-compose.yml"
  if [[ -n "$env_content" ]]; then
    printf '%s\n' "$env_content" > "$project_dir/.cronlite.local.env"
  fi

  cat > "$fake_bin/docker" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

printf '%s\n' "$*" >> "$FAKE_DOCKER_CALLS"

psql_prefix="compose exec -T -e PGOPTIONS=-c client_min_messages=warning postgres psql -q -v ON_ERROR_STOP=1 -U cronlite -d cronlite"

case "$*" in
  "compose version")
    [[ "${FAKE_COMPOSE_UNAVAILABLE:-0}" != 1 ]]
    ;;
  "compose down -v --remove-orphans")
    exit 0
    ;;
  "compose up -d postgres")
    printf 'host-port=%s\n' "${POSTGRES_HOST_PORT:-unset}" \
      >> "$FAKE_DOCKER_CALLS"
    exit 0
    ;;
  "compose ps -q postgres")
    printf 'container-id\n'
    ;;
  "compose ps -q cronlite")
    printf '%s\n' "${FAKE_COMPOSE_APP_ID:-}"
    ;;
  "compose port postgres 5432")
    printf '127.0.0.1:54329\n'
    ;;
  "inspect --format={{.State.Health.Status}} container-id")
    printf '%s\n' "${FAKE_HEALTH_STATUS:-healthy}"
    ;;
  "$psql_prefix -tAc "*"SELECT column_name FROM information_schema.columns"*)
    if [[ "${FAKE_ABSOLUTE_COLUMN_EXISTS:-0}" == 1 ]]; then
      printf 'absolute_expires_at\n'
    else
      printf '\n'
    fi
    ;;
  "$psql_prefix -tA")
    query="$(cat)"
    if [[ "$query" == *"FROM api_keys WHERE enabled AND token_hash = encode(sha256("* ]]; then
      printf 'key-check-input=%s\n' "$(printf '%s' "$query" | head -1)" \
        >> "$FAKE_DOCKER_CALLS"
      if [[ "${FAKE_KEY_VALID:-0}" == 1 ]]; then
        printf 't\n'
      else
        printf 'f\n'
      fi
    else
      printf 'unknown-query\n' >> "$FAKE_DOCKER_CALLS"
      exit 1
    fi
    ;;
  "$psql_prefix")
    migration_input="$(cat)"
    if [[ "$migration_input" == *"CREATE TABLE IF NOT EXISTS admin_sessions"* ]]; then
      printf 'migration-007-input\n' >> "$FAKE_DOCKER_CALLS"
      if [[ "${FAKE_MIGRATION_007_FAIL:-0}" == 1 ]]; then
        printf 'simulated migration 007 failure\n' >&2
        exit 1
      fi
    elif [[ "$migration_input" == *"absolute_expires_at"* ]]; then
      printf 'migration-008-input\n' >> "$FAKE_DOCKER_CALLS"
    elif [[ "$migration_input" == *"idx_executions_emitted_created_at"* ]]; then
      printf 'migration-009-input\n' >> "$FAKE_DOCKER_CALLS"
    elif [[ "$migration_input" == *"admin_saved_views"* ]]; then
      printf 'migration-010-input\n' >> "$FAKE_DOCKER_CALLS"
    elif [[ "$migration_input" == *"claim_generation"* ]]; then
      printf 'migration-011-input\n' >> "$FAKE_DOCKER_CALLS"
    elif [[ "$migration_input" == *"tailnet_login"* ]]; then
      printf 'migration-012-input\n' >> "$FAKE_DOCKER_CALLS"
    else
      printf 'unknown-migration-input\n' >> "$FAKE_DOCKER_CALLS"
    fi
    ;;
  *)
    printf 'unexpected docker call: %s\n' "$*" >&2
    exit 1
    ;;
esac
EOF

  cat > "$fake_bin/go" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

case "$*" in
  "run ./cmd/cronlite create-key default local-dev")
    printf 'create-key DATABASE_URL=%s\n' "${DATABASE_URL:-}" >> "$FAKE_DOCKER_CALLS"
    printf 'API Key created:\n  Namespace: default\n  Token:     ec_created\n'
    ;;
  "run ./cmd/cronlite serve")
    {
      printf 'args=%s\n' "$*"
      printf 'ADMIN_ENABLED=%s\n' "${ADMIN_ENABLED:-}"
      printf 'ADMIN_COOKIE_SECURE=%s\n' "${ADMIN_COOKIE_SECURE:-}"
      printf 'RECONCILE_ENABLED=%s\n' "${RECONCILE_ENABLED:-}"
      printf 'METRICS_ENABLED=%s\n' "${METRICS_ENABLED:-}"
      printf 'CRONLITE_API_KEY=%s\n' "${CRONLITE_API_KEY:-}"
      printf 'DATABASE_URL=%s\n' "${DATABASE_URL:-}"
    } > "$FAKE_GO_ENV"
    ;;
  *)
    printf 'unexpected go call: %s\n' "$*" >&2
    exit 1
    ;;
esac
EOF

  cat > "$fake_bin/sleep" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF

  chmod +x "$fake_bin/docker" "$fake_bin/go" "$fake_bin/sleep"
  touch "$calls_file"

  set +e
  output="$(
    cd "$project_dir"
    PATH="$fake_bin:/usr/bin:/bin" \
      FAKE_DOCKER_CALLS="$calls_file" \
      FAKE_GO_ENV="$go_env_file" \
      FAKE_ABSOLUTE_COLUMN_EXISTS="${FAKE_ABSOLUTE_COLUMN_EXISTS:-0}" \
      FAKE_MIGRATION_007_FAIL="${FAKE_MIGRATION_007_FAIL:-0}" \
      FAKE_COMPOSE_UNAVAILABLE="${FAKE_COMPOSE_UNAVAILABLE:-0}" \
      FAKE_COMPOSE_APP_ID="${FAKE_COMPOSE_APP_ID:-}" \
      FAKE_HEALTH_STATUS="${FAKE_HEALTH_STATUS:-healthy}" \
      FAKE_KEY_VALID="${FAKE_KEY_VALID:-0}" \
      DEV_HEALTH_ATTEMPTS="${DEV_HEALTH_ATTEMPTS:-30}" \
      HTTP_ADDR="${TEST_HTTP_ADDR:-:1}" \
      bash scripts/dev.sh "$@" 2>&1
  )"
  exit_status=$?
  set -e

  calls="$(cat "$calls_file")"
  if [[ -f "$go_env_file" ]]; then
    go_env="$(cat "$go_env_file")"
  else
    go_env=""
  fi
  if [[ -f "$project_dir/.cronlite.local.env" ]]; then
    env_file="$(cat "$project_dir/.cronlite.local.env")"
  else
    env_file=""
  fi
}

test_happy_path() {
  run_launcher happy

  assert_equals "$exit_status" 0
  assert_contains "$calls" "compose up -d postgres"
  assert_contains "$calls" "host-port=54329"
  assert_not_contains "$calls" "compose down"
  assert_contains "$calls" "PGOPTIONS=-c client_min_messages=warning"
  for migration in 007 008 009 010 011; do
    assert_contains "$calls" "migration-$migration-input"
  done
  assert_not_contains "$calls" "unknown"
  assert_contains "$calls" \
    "create-key DATABASE_URL=postgres://cronlite:cronlite@127.0.0.1:54329/cronlite?sslmode=disable"
  assert_contains "$env_file" "CRONLITE_API_KEY=ec_created"
  assert_contains "$env_file" "CRONLITE_URL=http://localhost:1"
  assert_equals "$(ls -l "$project_dir/.cronlite.local.env" | cut -c1-10)" "-rw-------"
  assert_contains "$go_env" "args=run ./cmd/cronlite serve"
  assert_contains "$go_env" "ADMIN_ENABLED=true"
  assert_contains "$go_env" "ADMIN_COOKIE_SECURE=false"
  assert_contains "$go_env" "RECONCILE_ENABLED=true"
  assert_contains "$go_env" "METRICS_ENABLED=true"
  assert_contains "$go_env" "CRONLITE_API_KEY=ec_created"
  assert_contains "$go_env" \
    "DATABASE_URL=postgres://cronlite:cronlite@127.0.0.1:54329/cronlite?sslmode=disable"
  assert_contains "$output" "http://localhost:1/admin"
  assert_contains "$output" "ec_created"
  assert_contains "$(cat "$ROOT/docker-compose.yml")" \
    '127.0.0.1:${POSTGRES_HOST_PORT:-54329}:5432'

  printf 'PASS: dev launcher happy path\n'
}

test_saved_key_is_reused() {
  local saved=$'OTHER_SETTING=1\nCRONLITE_API_KEY=ec_saved'
  FAKE_KEY_VALID=1 run_launcher saved-key "$saved"

  assert_equals "$exit_status" 0
  assert_contains "$calls" "key-check-input=\\set token 'ec_saved'"
  assert_not_contains "$calls" "-v token"
  assert_not_contains "$calls" "create-key"
  assert_equals "$env_file" "$saved"
  assert_contains "$go_env" "CRONLITE_API_KEY=ec_saved"

  printf 'PASS: a valid saved key is reused and the env file is untouched\n'
}

test_stale_key_is_replaced() {
  FAKE_KEY_VALID=0 run_launcher stale-key \
    $'OTHER_SETTING=1\nCRONLITE_URL=https://remote.example\nCRONLITE_API_KEY=ec_stale'

  assert_equals "$exit_status" 0
  assert_contains "$calls" "create-key"
  assert_contains "$env_file" "OTHER_SETTING=1"
  assert_contains "$env_file" \
    "# replaced by scripts/dev.sh: CRONLITE_URL=https://remote.example"
  assert_contains "$env_file" "# replaced by scripts/dev.sh: CRONLITE_API_KEY=ec_stale"
  assert_contains "$env_file" $'\nCRONLITE_API_KEY=ec_created'
  assert_contains "$go_env" "CRONLITE_API_KEY=ec_created"

  printf 'PASS: a stale key is replaced and previous values are kept as comments\n'
}

test_existing_absolute_column_skips_008() {
  FAKE_ABSOLUTE_COLUMN_EXISTS=1 run_launcher existing-column

  assert_equals "$exit_status" 0
  assert_contains "$calls" "migration-007-input"
  assert_not_contains "$calls" "migration-008-input"
  assert_contains "$calls" "migration-011-input"
  assert_contains "$calls" "migration-012-input"

  printf 'PASS: migration 008 is skipped when already applied\n'
}

test_migration_failure_stops_launch() {
  FAKE_MIGRATION_007_FAIL=1 run_launcher migration-007-failure

  [[ "$exit_status" -ne 0 ]] ||
    fail "failed migration 007 should stop the launcher"
  assert_not_contains "$calls" "create-key"
  assert_not_contains "$go_env" "args=run ./cmd/cronlite serve"

  printf 'PASS: migration failure stops launch\n'
}

test_reset() {
  run_launcher reset "" -- --reset

  assert_equals "$exit_status" 0
  assert_equals "$(printf '%s\n' "$calls" | grep -n 'compose down -v --remove-orphans' | cut -d: -f1)" \
    "$(($(printf '%s\n' "$calls" | grep -n 'compose up -d postgres' | cut -d: -f1) - 1))"

  printf 'PASS: --reset removes the volume before starting PostgreSQL\n'
}

test_bad_argument() {
  run_launcher bad-argument "" -- --nope

  assert_equals "$exit_status" 2
  assert_contains "$output" "Usage: ./scripts/dev.sh"
  assert_not_contains "$calls" "compose up"

  printf 'PASS: unknown arguments print usage\n'
}

test_port_in_use() {
  command -v python3 >/dev/null 2>&1 || {
    printf 'SKIP: port in use (python3 unavailable)\n'
    return
  }
  local port_file="$test_root/listener-port"
  python3 -c '
import socket, sys, time
s = socket.socket()
s.bind(("127.0.0.1", 0))
s.listen()
open(sys.argv[1], "w").write(str(s.getsockname()[1]))
time.sleep(60)
' "$port_file" &
  listener_pid=$!
  for _ in {1..50}; do
    [[ -s "$port_file" ]] && break
    /bin/sleep 0.1
  done
  local port
  port="$(cat "$port_file")"

  FAKE_COMPOSE_APP_ID=app-container TEST_HTTP_ADDR=":$port" \
    run_launcher port-in-use

  kill "$listener_pid" 2>/dev/null || true
  wait "$listener_pid" 2>/dev/null || true
  listener_pid=""

  assert_equals "$exit_status" 1
  assert_contains "$output" "port $port is already in use"
  assert_contains "$output" "docker compose stop cronlite"
  assert_not_contains "$calls" "compose up"

  printf 'PASS: an occupied HTTP port stops launch with a hint\n'
}

test_compose_unavailable() {
  FAKE_COMPOSE_UNAVAILABLE=1 run_launcher compose-unavailable

  [[ "$exit_status" -ne 0 ]] ||
    fail "missing Docker Compose should fail"
  assert_contains "$output" "Docker Compose v2 is required"
  assert_not_contains "$calls" "compose up -d postgres"

  printf 'PASS: missing Docker Compose is reported\n'
}

test_unhealthy() {
  FAKE_HEALTH_STATUS=unhealthy DEV_HEALTH_ATTEMPTS=1 run_launcher unhealthy

  [[ "$exit_status" -ne 0 ]] ||
    fail "unhealthy PostgreSQL should fail"
  assert_contains "$output" "PostgreSQL did not become healthy"

  inspect_count="$(
    printf '%s\n' "$calls" |
      awk '/^inspect / { count++ } END { print count + 0 }'
  )"
  assert_equals "$inspect_count" 1

  printf 'PASS: unhealthy PostgreSQL is reported\n'
}

CASES=(
  happy
  saved-key-reused
  stale-key-replaced
  existing-column
  migration-007-failure
  reset
  bad-argument
  port-in-use
  compose-unavailable
  unhealthy
)

run_case() {
  case "$1" in
    happy) test_happy_path ;;
    saved-key-reused) test_saved_key_is_reused ;;
    stale-key-replaced) test_stale_key_is_replaced ;;
    existing-column) test_existing_absolute_column_skips_008 ;;
    migration-007-failure) test_migration_failure_stops_launch ;;
    reset) test_reset ;;
    bad-argument) test_bad_argument ;;
    port-in-use) test_port_in_use ;;
    compose-unavailable) test_compose_unavailable ;;
    unhealthy) test_unhealthy ;;
    *) fail "unknown test case: $1" ;;
  esac
}

case "${1:---case}" in
  --all)
    for test_case in "${CASES[@]}"; do
      run_case "$test_case"
    done
    ;;
  --case)
    [[ $# -eq 2 ]] || fail "--case requires a case name"
    run_case "$2"
    ;;
  *)
    fail "usage: $0 [--all | --case CASE]"
    ;;
esac
