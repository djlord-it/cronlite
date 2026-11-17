#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

fail() {
  printf 'FAIL: %s\n' "$1" >&2
  exit 1
}

assert_contains() {
  local file="$1"
  local expected="$2"
  grep -Fq -- "$expected" "$file" ||
    fail "$file must contain: $expected"
}

assert_not_contains() {
  local file="$1"
  local unexpected="$2"
  if grep -Fq -- "$unexpected" "$file"; then
    fail "$file must not contain: $unexpected"
  fi
}

assert_contains Dockerfile \
  'FROM golang:1.27.4-alpine3.23@sha256:f70850ef6ce341e8b6427f677049145d63c0b7ce002d4d7e3d464828b1b96dcd AS builder'
assert_contains Dockerfile \
  'FROM alpine:3.23@sha256:469c9ea410b9215eaee7976ba6c0639039b444be8e5d638f7abbada189971eb2'
assert_contains Dockerfile 'git=2.52.1-r0'
assert_contains Dockerfile 'gcc=15.2.0-r2'
assert_contains Dockerfile 'musl-dev=1.2.5-r24'
assert_contains Dockerfile 'ca-certificates=20261020-r0'
assert_contains Dockerfile 'tzdata=2026f-r0'
assert_not_contains Dockerfile 'apk upgrade'

assert_contains .dockerignore '**/.venv/'
assert_contains .dockerignore 'playground/'
assert_contains .dockerignore '.cronlite.local.env'
assert_contains .gitignore '.cronlite.local.env'

assert_contains docker-compose.yml \
  'image: postgres:16-alpine@sha256:4e6e670bb069649261c9c18031f0aded7bb249a5b6664ddec29c013a89310d50'

printf 'PASS: container images and Alpine packages are reproducibly pinned\n'
