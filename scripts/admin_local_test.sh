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

[[ -f "$LAUNCHER" ]] || fail "scripts/admin-local.sh is missing"

test_dir="$(mktemp -d)"
trap 'rm -rf "$test_dir"' EXIT

project_dir="$test_dir/project"
fake_bin="$test_dir/bin"
mkdir -p "$project_dir/scripts" "$project_dir/schema" "$fake_bin"
cp "$LAUNCHER" "$project_dir/scripts/admin-local.sh"
cp "$ROOT/schema/007_admin_sessions.sql" "$project_dir/schema/007_admin_sessions.sql"
touch "$project_dir/docker-compose.yml"

calls_file="$test_dir/docker-calls"
go_env_file="$test_dir/go-env"

cat > "$fake_bin/docker" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

printf '%s\n' "$*" >> "$FAKE_DOCKER_CALLS"

case "$*" in
  "compose version")
    exit 0
    ;;
  "compose up -d postgres")
    exit 0
    ;;
  "compose ps -q postgres")
    printf 'container-id\n'
    ;;
  "inspect --format={{.State.Health.Status}} container-id")
    printf 'healthy\n'
    ;;
  *"SELECT to_regclass"*)
    printf '\n'
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

chmod +x "$fake_bin/docker" "$fake_bin/go" "$fake_bin/openssl"

output="$(
  cd "$project_dir"
  PATH="$fake_bin:/usr/bin:/bin" \
    FAKE_DOCKER_CALLS="$calls_file" \
    FAKE_GO_ENV="$go_env_file" \
    bash scripts/admin-local.sh
)"

calls="$(cat "$calls_file")"
go_env="$(cat "$go_env_file")"

assert_contains "$calls" "compose up -d postgres"
assert_contains "$calls" "inspect --format={{.State.Health.Status}} container-id"
assert_contains "$calls" "compose exec -T postgres psql"
assert_contains "$calls" "migration-input"
assert_contains "$go_env" "args=run ./cmd/cronlite serve"
assert_contains "$go_env" "ADMIN_ENABLED=true"
assert_contains "$go_env" "ADMIN_COOKIE_SECURE=false"
assert_contains "$go_env" "ADMIN_BOOTSTRAP_TOKEN=test-bootstrap-token"
assert_contains "$go_env" \
  "DATABASE_URL=postgres://cronlite:cronlite@localhost:5432/cronlite?sslmode=disable"
assert_contains "$output" "http://localhost:8080/admin"
assert_contains "$output" "test-bootstrap-token"

printf 'PASS: admin local launcher happy path\n'
