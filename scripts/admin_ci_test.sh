#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

go test ./internal/ciworkflow -count=1

# Set ADMIN_COVERAGE_FILE to retain the atomic profile at an explicit path
# (for example, for CI artifact upload). The default temporary file is removed on EXIT.
cleanup_coverage=0
coverage_file="${ADMIN_COVERAGE_FILE:-}"
if [[ -z "$coverage_file" ]]; then
  coverage_file="$(mktemp "${TMPDIR:-/tmp}/cronlite-admin-coverage.XXXXXX")"
  cleanup_coverage=1
fi
cleanup() {
  if [[ "$cleanup_coverage" -eq 1 ]]; then
    rm -f -- "$coverage_file"
  fi
}
trap cleanup EXIT

bash -n scripts/admin-local.sh scripts/admin_local_test.sh
bash scripts/admin_local_test.sh --all
go test -race ./internal/webadmin ./cmd/cronlite
go test ./internal/webadmin -coverprofile="$coverage_file" -covermode=atomic

coverage="$(
  go tool cover -func="$coverage_file" |
    awk '/^total:/ {gsub("%", "", $3); print $3}'
)"
if [[ -z "$coverage" ]]; then
  echo "admin coverage gate: unable to extract total coverage" >&2
  exit 1
fi
awk -v coverage="$coverage" 'BEGIN {
  if ((coverage + 0) < 80) {
    printf "admin coverage gate: %.1f%% is below required 80.0%%\n", coverage > "/dev/stderr"
    exit 1
  }
  printf "admin coverage gate: %.1f%% (minimum 80.0%%)\n", coverage
}'

go test ./internal/webadmin -run=^$ -fuzz=FuzzParseTags -fuzztime=5s
go test ./internal/webadmin -run=^$ -fuzz=FuzzPositivePage -fuzztime=5s
