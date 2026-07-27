#!/usr/bin/env bash
set -euo pipefail

fail() {
  printf 'admin smoke test: %s\n' "$1" >&2
  exit 1
}

[[ -n "${ADMIN_BASE_URL:-}" ]] ||
  fail "ADMIN_BASE_URL is required"
[[ -n "${ADMIN_BOOTSTRAP_TOKEN:-}" ]] ||
  fail "ADMIN_BOOTSTRAP_TOKEN is required"

command -v curl >/dev/null 2>&1 ||
  fail "curl is required"

base_url="${ADMIN_BASE_URL%/}"
cookie_jar="$(mktemp "${TMPDIR:-/tmp}/cronlite-admin-smoke.cookies.XXXXXX")"
logout_headers="$(mktemp "${TMPDIR:-/tmp}/cronlite-admin-smoke.logout-headers.XXXXXX")"
logged_out_headers="$(mktemp "${TMPDIR:-/tmp}/cronlite-admin-smoke.logged-out-headers.XXXXXX")"
setup_page=""
setup_csrf=""
setup_body=""
setup_complete_page=""
api_key=""
jobs_page=""
authenticated_csrf=""
logout_body=""
logout_status=""
logout_location=""
logged_out_status=""
logged_out_location=""

cleanup() {
  rm -f -- "$cookie_jar" "$logout_headers" "$logged_out_headers"
  unset setup_page setup_csrf setup_body setup_complete_page api_key jobs_page
  unset authenticated_csrf logout_body logout_status logout_location
  unset logged_out_status logged_out_location
  unset ADMIN_BOOTSTRAP_TOKEN
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

form_urlencode() {
  local value="$1"
  local index=0
  local character=""
  local LC_ALL=C

  while [[ "$index" -lt "${#value}" ]]; do
    character="${value:$index:1}"
    case "$character" in
      [a-zA-Z0-9.~_-])
        printf '%s' "$character"
        ;;
      *)
        printf '%%%02X' "'$character"
        ;;
    esac
    index=$((index + 1))
  done
}

extract_hidden_csrf() {
  local page="$1"
  local input=""

  input="$(
    printf '%s\n' "$page" |
      grep -Eom1 '<input[^>]*(type="hidden"[^>]*name="csrf_token"|name="csrf_token"[^>]*type="hidden")[^>]*>' ||
      true
  )"
  [[ -n "$input" ]] || return 1

  printf '%s\n' "$input" |
    sed -nE 's/.*value="([^"]+)".*/\1/p' |
    head -n 1
}

extract_location() {
  local headers_file="$1"

  awk '{
    line = $0
    sub(/\r$/, "", line)
    colon = index(line, ":")
    if (colon > 0 && tolower(substr(line, 1, colon - 1)) == "location") {
      value = substr(line, colon + 1)
      sub(/^[[:space:]]+/, "", value)
      sub(/[[:space:]]+$/, "", value)
      print value
      exit
    }
  }' "$headers_file"
}

setup_page="$(
  curl --disable --silent --show-error --fail \
    --connect-timeout 5 --max-time 30 \
    --cookie "$cookie_jar" --cookie-jar "$cookie_jar" \
    "$base_url/admin/setup"
)" || fail "GET /admin/setup failed"

setup_csrf="$(extract_hidden_csrf "$setup_page" || true)"
[[ -n "$setup_csrf" ]] ||
  fail "GET /admin/setup did not contain a public CSRF token"

setup_body="csrf_token=$(form_urlencode "$setup_csrf")"
setup_body="${setup_body}&bootstrap_token=$(form_urlencode "$ADMIN_BOOTSTRAP_TOKEN")"
setup_body="${setup_body}&namespace=$(form_urlencode "ci-smoke")"
setup_body="${setup_body}&label=$(form_urlencode "ci-owner")"
setup_complete_page="$(
  printf '%s' "$setup_body" |
    curl --disable --silent --show-error --fail \
      --connect-timeout 5 --max-time 30 \
      --request POST \
      --cookie "$cookie_jar" --cookie-jar "$cookie_jar" \
      --header "Content-Type: application/x-www-form-urlencoded" \
      --data-binary @- \
      "$base_url/admin/setup"
)" || fail "POST /admin/setup failed"
unset setup_body

api_key="$(
  printf '%s\n' "$setup_complete_page" |
    grep -Eom1 'ec_[0-9a-f]{64}' ||
    true
)"
[[ -n "$api_key" ]] ||
  fail "POST /admin/setup did not return a one-time API key"

jobs_page="$(
  curl --disable --silent --show-error --fail \
    --connect-timeout 5 --max-time 30 \
    --cookie "$cookie_jar" --cookie-jar "$cookie_jar" \
    "$base_url/admin/jobs"
)" || fail "authenticated GET /admin/jobs failed"

authenticated_csrf="$(extract_hidden_csrf "$jobs_page" || true)"
[[ -n "$authenticated_csrf" ]] ||
  fail "authenticated GET /admin/jobs did not contain a CSRF token"

logout_body="csrf_token=$(form_urlencode "$authenticated_csrf")"
logout_status="$(
  printf '%s' "$logout_body" |
    curl --disable --silent --show-error \
      --connect-timeout 5 --max-time 30 \
      --request POST \
      --output /dev/null --write-out '%{http_code}' \
      --dump-header "$logout_headers" \
      --cookie "$cookie_jar" --cookie-jar "$cookie_jar" \
      --header "Content-Type: application/x-www-form-urlencoded" \
      --data-binary @- \
      "$base_url/admin/logout"
)" || fail "POST /admin/logout failed"
unset logout_body
[[ "$logout_status" == "303" ]] ||
  fail "POST /admin/logout returned an unexpected HTTP status"
logout_location="$(extract_location "$logout_headers")"
[[ "$logout_location" == "/admin/login" ]] ||
  fail "POST /admin/logout returned an unexpected redirect location"

logged_out_status="$(
  curl --disable --silent --show-error \
    --connect-timeout 5 --max-time 30 \
    --output /dev/null --write-out '%{http_code}' \
    --dump-header "$logged_out_headers" \
    --cookie "$cookie_jar" --cookie-jar "$cookie_jar" \
    "$base_url/admin/jobs"
)" || fail "logged-out GET /admin/jobs failed"
[[ "$logged_out_status" == "303" ]] ||
  fail "logged-out GET /admin/jobs did not redirect"
logged_out_location="$(extract_location "$logged_out_headers")"
[[ "$logged_out_location" == "/admin/login" ]] ||
  fail "logged-out GET /admin/jobs returned an unexpected redirect location"

unset setup_page setup_csrf setup_complete_page api_key jobs_page authenticated_csrf
unset logout_status logout_location logged_out_status logged_out_location
unset ADMIN_BOOTSTRAP_TOKEN
printf 'ADMIN_SMOKE_OK\n'
