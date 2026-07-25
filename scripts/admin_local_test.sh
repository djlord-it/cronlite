#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LAUNCHER="$ROOT/scripts/admin-local.sh"

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

[[ -f "$LAUNCHER" ]] || fail "scripts/admin-local.sh is missing"

test_root="$(mktemp -d)"
trap 'rm -rf "$test_root"' EXIT

output=""
exit_status=0
calls=""
go_env=""
env_checksum_before=""
env_checksum_after=""

run_launcher() {
  local case_name="$1"
  local case_dir="$test_root/$case_name"
  local project_dir="$case_dir/project"
  local fake_bin="$case_dir/bin"
  local calls_file="$case_dir/docker-calls"
  local go_env_file="$case_dir/go-env"

  mkdir -p "$project_dir/scripts" "$project_dir/schema" "$fake_bin"
  cp "$LAUNCHER" "$project_dir/scripts/admin-local.sh"
  cp "$ROOT/schema/007_admin_sessions.sql" \
    "$project_dir/schema/007_admin_sessions.sql"
  touch "$project_dir/docker-compose.yml"
  printf 'HTTP_ADDR=:8080\n' > "$project_dir/.cronlite.local.env"
  env_checksum_before="$(cksum "$project_dir/.cronlite.local.env")"

  cat > "$fake_bin/docker" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

printf '%s\n' "$*" >> "$FAKE_DOCKER_CALLS"

case "$*" in
  "compose version")
    [[ "${FAKE_COMPOSE_UNAVAILABLE:-0}" != 1 ]]
    ;;
  "compose up -d postgres")
    printf 'host-port=%s\n' "${POSTGRES_HOST_PORT:-unset}" \
      >> "$FAKE_DOCKER_CALLS"
    exit 0
    ;;
  "compose ps -q postgres")
    printf 'container-id\n'
    ;;
  "compose port postgres 5432")
    printf '0.0.0.0:49152\n'
    ;;
  "inspect --format={{.State.Health.Status}} container-id")
    printf '%s\n' "${FAKE_HEALTH_STATUS:-healthy}"
    ;;
  *"SELECT to_regclass"*)
    if [[ "${FAKE_TABLE_EXISTS:-0}" == 1 ]]; then
      printf 'admin_sessions\n'
    else
      printf '\n'
    fi
    ;;
  "compose exec -T postgres psql -v ON_ERROR_STOP=1 -U cronlite -d cronlite")
    cat >/dev/null
    printf 'migration-input\n' >> "$FAKE_DOCKER_CALLS"
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

{
  printf 'args=%s\n' "$*"
  printf 'ADMIN_ENABLED=%s\n' "${ADMIN_ENABLED:-}"
  printf 'ADMIN_COOKIE_SECURE=%s\n' "${ADMIN_COOKIE_SECURE:-}"
  printf 'ADMIN_BOOTSTRAP_TOKEN=%s\n' "${ADMIN_BOOTSTRAP_TOKEN:-}"
  printf 'DATABASE_URL=%s\n' "${DATABASE_URL:-}"
} > "$FAKE_GO_ENV"
EOF

  cat > "$fake_bin/openssl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

[[ "$*" == "rand -hex 32" ]] || exit 1
printf 'test-bootstrap-token\n'
EOF

  cat > "$fake_bin/sleep" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF

  chmod +x \
    "$fake_bin/docker" \
    "$fake_bin/go" \
    "$fake_bin/openssl" \
    "$fake_bin/sleep"

  set +e
  output="$(
    cd "$project_dir"
    PATH="$fake_bin:/usr/bin:/bin" \
      FAKE_DOCKER_CALLS="$calls_file" \
      FAKE_GO_ENV="$go_env_file" \
      FAKE_TABLE_EXISTS="${FAKE_TABLE_EXISTS:-0}" \
      FAKE_COMPOSE_UNAVAILABLE="${FAKE_COMPOSE_UNAVAILABLE:-0}" \
      FAKE_HEALTH_STATUS="${FAKE_HEALTH_STATUS:-healthy}" \
      ADMIN_LOCAL_HEALTH_ATTEMPTS="${ADMIN_LOCAL_HEALTH_ATTEMPTS:-30}" \
      bash scripts/admin-local.sh 2>&1
  )"
  exit_status=$?
  set -e

  calls="$(cat "$calls_file")"
  if [[ -f "$go_env_file" ]]; then
    go_env="$(cat "$go_env_file")"
  else
    go_env=""
  fi
  env_checksum_after="$(cksum "$project_dir/.cronlite.local.env")"
  assert_equals "$env_checksum_after" "$env_checksum_before"
}

test_happy_path() {
  run_launcher happy

  assert_equals "$exit_status" 0
  assert_contains "$calls" "compose up -d postgres"
  assert_contains "$calls" "host-port=0"
  assert_contains "$calls" "compose port postgres 5432"
  assert_contains "$calls" \
    "inspect --format={{.State.Health.Status}} container-id"
  assert_contains "$calls" "compose exec -T postgres psql"
  assert_contains "$calls" "migration-input"
  assert_contains "$go_env" "args=run ./cmd/cronlite serve"
  assert_contains "$go_env" "ADMIN_ENABLED=true"
  assert_contains "$go_env" "ADMIN_COOKIE_SECURE=false"
  assert_contains "$go_env" \
    "ADMIN_BOOTSTRAP_TOKEN=test-bootstrap-token"
  assert_contains "$go_env" \
    "DATABASE_URL=postgres://cronlite:cronlite@127.0.0.1:49152/cronlite?sslmode=disable"
  assert_contains "$output" "http://localhost:8080/admin"
  assert_contains "$output" "test-bootstrap-token"
  assert_contains "$(cat "$ROOT/docker-compose.yml")" \
    '${POSTGRES_HOST_PORT:-5432}:5432'

  printf 'PASS: admin local launcher happy path\n'
}

test_table_exists() {
  FAKE_TABLE_EXISTS=1 run_launcher table-exists

  assert_equals "$exit_status" 0
  assert_not_contains "$calls" "migration-input"

  printf 'PASS: existing migration is skipped\n'
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
  FAKE_HEALTH_STATUS=unhealthy \
    ADMIN_LOCAL_HEALTH_ATTEMPTS=1 \
    run_launcher unhealthy

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

run_case() {
  case "$1" in
    happy)
      test_happy_path
      ;;
    table-exists)
      test_table_exists
      ;;
    compose-unavailable)
      test_compose_unavailable
      ;;
    unhealthy)
      test_unhealthy
      ;;
    *)
      fail "unknown test case: $1"
      ;;
  esac
}

case "${1:---case}" in
  --all)
    run_case happy
    run_case table-exists
    run_case compose-unavailable
    run_case unhealthy
    ;;
  --case)
    [[ $# -eq 2 ]] || fail "--case requires a case name"
    run_case "$2"
    ;;
  *)
    fail "usage: $0 [--all | --case CASE]"
    ;;
esac
