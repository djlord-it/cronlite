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
  'FROM golang:1.25-alpine3.23@sha256:cc985ef6f9c3bf9ece7488129c9abe0a150388ccdfa428d886fc709dca0b230a AS builder'
assert_contains Dockerfile \
  'FROM alpine:3.23@sha256:fd791d74b68913cbb027c6546007b3f0d3bc45125f797758156952bc2d6daf40'
assert_contains Dockerfile 'git=2.52.0-r0'
assert_contains Dockerfile 'gcc=15.2.0-r2'
assert_contains Dockerfile 'musl-dev=1.2.5-r23'
assert_contains Dockerfile 'ca-certificates=20260611-r0'
assert_contains Dockerfile 'tzdata=2026c-r0'
assert_not_contains Dockerfile 'apk upgrade'

assert_contains .dockerignore '**/.venv/'
assert_contains .dockerignore 'playground/'

assert_contains docker-compose.yml \
  'image: postgres:16-alpine@sha256:4e6e670bb069649261c9c18031f0aded7bb249a5b6664ddec29c013a89310d50'

printf 'PASS: container images and Alpine packages are reproducibly pinned\n'
