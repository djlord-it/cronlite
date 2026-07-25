#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SMOKE="$ROOT/scripts/admin_smoke_test.sh"
COMPOSE="$ROOT/docker-compose.admin-ci.yml"
SCHEMA_README="$ROOT/schema/README.md"

fail() {
  printf 'FAIL: %s\n' "$1" >&2
  exit 1
}

assert_secret_absent() {
  local output="$1"
  local secret="$2"
  [[ "$output" != *"$secret"* ]] ||
    fail "smoke test output exposed a supplied secret"
}

run_expect_failure() {
  local output_file="$1"
  shift

  set +e
  "$@" >"$output_file" 2>&1
  exit_status=$?
  set -e

  [[ "$exit_status" -ne 0 ]] || fail "expected command to fail"
}

[[ -x "$SMOKE" ]] || fail "scripts/admin_smoke_test.sh must exist and be executable"
grep -Fq 'postgres:16-alpine@sha256:4e6e670bb069649261c9c18031f0aded7bb249a5b6664ddec29c013a89310d50' "$COMPOSE" ||
  fail "admin CI PostgreSQL image must use the tested digest"
grep -Eq '^set -e$' "$SCHEMA_README" ||
  fail "schema migration example must stop on the first psql error"
grep -Fq 'docker compose -p <unique-project>' "$SCHEMA_README" ||
  fail "schema README must require a unique Compose project name"

test_root="$(mktemp -d "${TMPDIR:-/tmp}/cronlite-admin-smoke-test.XXXXXX")"
trap 'rm -rf -- "$test_root"' EXIT

missing_env_output="$test_root/missing-env-output"
run_expect_failure "$missing_env_output" env -i PATH="$PATH" bash "$SMOKE"

bootstrap_sentinel="bootstrap secret&=+/%must-not-leak"
no_server_output="$test_root/no-server-output"
run_expect_failure "$no_server_output" env \
  ADMIN_BASE_URL="http://127.0.0.1:1" \
  ADMIN_BOOTSTRAP_TOKEN="$bootstrap_sentinel" \
  bash "$SMOKE"
assert_secret_absent "$(cat "$no_server_output")" "$bootstrap_sentinel"

fake_bin="$test_root/bin"
malicious_home="$test_root/home"
mkdir -p "$fake_bin"
mkdir -p "$malicious_home"
request_log="$test_root/requests"
request_state="$test_root/state"
cookie_path_file="$test_root/cookie-path"
cat >"$malicious_home/.curlrc" <<'EOF'
--trace-ascii -
EOF

cat >"$fake_bin/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

if [[ "${1:-}" != "--disable" ]]; then
  if [[ -f "${HOME:-}/.curlrc" ]]; then
    printf 'malicious curl trace: %s\n' "$FAKE_BOOTSTRAP_TOKEN" >&2
  fi
  printf 'curl --disable must be the first argument\n' >&2
  exit 2
fi
shift

for argument in "$@"; do
  case "$argument" in
    --data-urlencode|--data-urlencode=*)
      printf 'sensitive form data must not use argv\n' >&2
      exit 2
      ;;
  esac
  for sensitive_value in \
    "$FAKE_BOOTSTRAP_TOKEN" \
    "public-csrf" \
    "auth-csrf" \
    "ci-smoke" \
    "ci-owner"; do
    if [[ "$argument" == *"$sensitive_value"* ]]; then
      printf 'sensitive form value appeared in curl argv\n' >&2
      exit 2
    fi
  done
done

method=GET
output_file=""
write_status=false
cookie_input=""
cookie_output=""
header_output=""
content_type=""
read_stdin=false
url=""

while [[ "$#" -gt 0 ]]; do
  case "$1" in
    --request)
      method="$2"
      shift 2
      ;;
    --output)
      output_file="$2"
      shift 2
      ;;
    --write-out)
      write_status=true
      shift 2
      ;;
    --cookie)
      cookie_input="$2"
      shift 2
      ;;
    --cookie-jar)
      cookie_output="$2"
      shift 2
      ;;
    --dump-header)
      header_output="$2"
      shift 2
      ;;
    --header)
      content_type="$2"
      shift 2
      ;;
    --data-binary)
      [[ "$2" == "@-" ]] || {
        printf 'form body must be read from stdin\n' >&2
        exit 2
      }
      read_stdin=true
      shift 2
      ;;
    --connect-timeout|--max-time)
      shift 2
      ;;
    --silent|--show-error|--fail)
      shift
      ;;
    http://*)
      url="$1"
      shift
      ;;
    *)
      printf 'unexpected curl argument\n' >&2
      exit 2
      ;;
  esac
done

[[ -n "$cookie_input" && "$cookie_input" == "$cookie_output" ]] || {
  printf 'a consistent nonempty cookie jar is required\n' >&2
  exit 2
}
if [[ -f "$FAKE_COOKIE_PATH_FILE" ]]; then
  [[ "$(cat "$FAKE_COOKIE_PATH_FILE")" == "$cookie_input" ]] || {
    printf 'cookie jar changed between requests\n' >&2
    exit 2
  }
else
  printf '%s\n' "$cookie_input" >"$FAKE_COOKIE_PATH_FILE"
fi

request_body=""
if [[ "$read_stdin" == true ]]; then
  request_body="$(cat)"
fi

printf '%s %s\n' "$method" "$url" >>"$FAKE_REQUEST_LOG"

body=""
status=200
step=0
if [[ -f "$FAKE_REQUEST_STATE" ]]; then
  step="$(cat "$FAKE_REQUEST_STATE")"
fi

case "$step $method $url" in
  "0 GET http://admin.test/admin/setup")
    [[ "$read_stdin" == false && ! -s "$cookie_input" ]] || {
      printf 'initial setup request had unexpected state\n' >&2
      exit 2
    }
    printf 'public-cookie\n' >"$cookie_output"
    body='<input value="public-csrf" type="hidden" name="csrf_token">'
    ;;
  "1 POST http://admin.test/admin/setup")
    [[ "$(cat "$cookie_input")" == "public-cookie" ]] || {
      printf 'setup POST is missing the public CSRF cookie\n' >&2
      exit 2
    }
    [[ "$content_type" == "Content-Type: application/x-www-form-urlencoded" ]] || {
      printf 'setup POST has the wrong content type\n' >&2
      exit 2
    }
    expected_setup_body='csrf_token=public-csrf&bootstrap_token=bootstrap%20secret%26%3D%2B%2F%25must-not-leak&namespace=ci-smoke&label=ci-owner'
    [[ "$request_body" == "$expected_setup_body" ]] || {
      printf 'setup POST form body is incorrect\n' >&2
      exit 2
    }
    printf 'authenticated-cookie\n' >"$cookie_output"
    body='<pre class="secret">ec_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa</pre>'
    ;;
  "2 GET http://admin.test/admin/jobs")
    [[ "$(cat "$cookie_input")" == "authenticated-cookie" ]] || {
      printf 'authenticated jobs request is missing its session cookie\n' >&2
      exit 2
    }
    body='<form><input name="csrf_token" value="auth-csrf" type="hidden"></form>'
    ;;
  "3 POST http://admin.test/admin/logout")
    [[ "$(cat "$cookie_input")" == "authenticated-cookie" ]] || {
      printf 'logout is missing its session cookie\n' >&2
      exit 2
    }
    [[ "$content_type" == "Content-Type: application/x-www-form-urlencoded" ]] || {
      printf 'logout POST has the wrong content type\n' >&2
      exit 2
    }
    [[ "$request_body" == "csrf_token=auth-csrf" ]] || {
      printf 'logout POST form body is incorrect\n' >&2
      exit 2
    }
    [[ -n "$header_output" ]] || {
      printf 'logout response headers were not captured\n' >&2
      exit 2
    }
    printf 'logged-out-cookie\n' >"$cookie_output"
    printf 'HTTP/1.1 303 See Other\r\nLocation: /admin/login\r\n\r\n' >"$header_output"
    status=303
    ;;
  "4 GET http://admin.test/admin/jobs")
    [[ "$(cat "$cookie_input")" == "logged-out-cookie" ]] || {
      printf 'logged-out jobs request has the wrong cookie state\n' >&2
      exit 2
    }
    [[ -n "$header_output" ]] || {
      printf 'logged-out response headers were not captured\n' >&2
      exit 2
    }
    printf 'HTTP/1.1 303 See Other\r\nlocation: /admin/login\r\n\r\n' >"$header_output"
    status=303
    ;;
  *)
    printf 'unexpected request lifecycle step\n' >&2
    exit 2
    ;;
esac
printf '%s\n' "$((step + 1))" >"$FAKE_REQUEST_STATE"

if [[ -n "$output_file" && "$output_file" != /dev/null ]]; then
  printf '%s\n' "$body" >"$output_file"
elif [[ -z "$output_file" ]]; then
  printf '%s\n' "$body"
fi
if [[ "$write_status" == true ]]; then
  printf '%s' "$status"
fi
EOF
chmod +x "$fake_bin/curl"

success_output="$test_root/success-output"
api_key_sentinel="ec_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
set +e
env \
  PATH="$fake_bin:/usr/bin:/bin" \
  FAKE_REQUEST_LOG="$request_log" \
  FAKE_REQUEST_STATE="$request_state" \
  FAKE_COOKIE_PATH_FILE="$cookie_path_file" \
  FAKE_BOOTSTRAP_TOKEN="$bootstrap_sentinel" \
  HOME="$malicious_home" \
  ADMIN_BASE_URL="http://admin.test/" \
  ADMIN_BOOTSTRAP_TOKEN="$bootstrap_sentinel" \
  bash "$SMOKE" >"$success_output" 2>&1
success_status=$?
set -e
[[ "$success_status" -eq 0 ]] ||
  fail "smoke test failed the modeled admin lifecycle"

success_text="$(cat "$success_output")"
assert_secret_absent "$success_text" "$bootstrap_sentinel"
[[ "$success_text" == "ADMIN_SMOKE_OK" ]] ||
  fail "successful smoke test must print only ADMIN_SMOKE_OK"
assert_secret_absent "$success_text" "$api_key_sentinel"

expected_requests="$test_root/expected-requests"
cat >"$expected_requests" <<'EOF'
GET http://admin.test/admin/setup
POST http://admin.test/admin/setup
GET http://admin.test/admin/jobs
POST http://admin.test/admin/logout
GET http://admin.test/admin/jobs
EOF
cmp -s "$expected_requests" "$request_log" ||
  fail "smoke test did not execute the expected admin lifecycle"

printf 'PASS: admin smoke test contract\n'
