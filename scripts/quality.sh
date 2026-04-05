#!/usr/bin/env bash
# quality.sh — unified local quality check script.
# Runs lint, vet, test with race detector + coverage, security scan, and build.
# Usage: ./scripts/quality.sh

set -euo pipefail

RED='\033[0;31m'
GREEN='\033[0;32m'
NC='\033[0m'

pass() { echo -e "${GREEN}PASS${NC}: $1"; }
fail() { echo -e "${RED}FAIL${NC}: $1"; exit 1; }

echo "=== Lint ==="
if command -v golangci-lint &>/dev/null; then
    golangci-lint run ./... && pass "lint" || fail "lint"
else
    echo "SKIP: golangci-lint not installed (install: https://golangci-lint.run/welcome/install/)"
fi

echo ""
echo "=== Vet ==="
go vet ./... && pass "vet" || fail "vet"

echo ""
echo "=== Test (race + coverage) ==="
go test -race -coverprofile=coverage.out -covermode=atomic ./... && pass "tests" || fail "tests"

echo ""
echo "=== Coverage ==="
COVERAGE=$(go tool cover -func=coverage.out | grep '^total:' | awk '{print $3}' | tr -d '%')
echo "Total coverage: ${COVERAGE}%"
THRESHOLD=70
if [ "$(echo "$COVERAGE < $THRESHOLD" | bc -l)" -eq 1 ]; then
    fail "coverage ${COVERAGE}% is below ${THRESHOLD}% threshold"
else
    pass "coverage ${COVERAGE}% >= ${THRESHOLD}%"
fi

echo ""
echo "=== Security ==="
if command -v govulncheck &>/dev/null; then
    govulncheck ./... && pass "govulncheck" || fail "govulncheck"
else
    echo "SKIP: govulncheck not installed (install: go install golang.org/x/vuln/cmd/govulncheck@latest)"
fi

echo ""
echo "=== Build ==="
go build -o /dev/null ./cmd/easycron && pass "build" || fail "build"

echo ""
echo "=== All checks passed ==="
