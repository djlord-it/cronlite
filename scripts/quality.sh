#!/usr/bin/env bash
set -uo pipefail

GOBIN="$(go env GOPATH 2>/dev/null)/bin"
[[ -d "$GOBIN" ]] && export PATH="$GOBIN:$PATH"

ROOT="$(git -C "$(dirname "$0")" rev-parse --show-toplevel 2>/dev/null || dirname "$(dirname "$0")")"
cd "$ROOT"

if [ -t 1 ]; then
  B='\033[1m' D='\033[2m' R='\033[0m'
  RED='\033[31m' GREEN='\033[32m' YELLOW='\033[33m' CYAN='\033[36m'
else
  B='' D='' R='' RED='' GREEN='' YELLOW='' CYAN=''
fi

PASS="${GREEN}PASS${R}"
FAIL="${RED}FAIL${R}"
WARN="${YELLOW}WARN${R}"
INFO="${CYAN}INFO${R}"
FAILURES=0

section() { printf "\n${B}${CYAN}━━━ %s${R}\n" "$1"; }
check()   { printf "  %-50s " "$1"; }
pass()    { printf "[${PASS}] %s\n" "${1:-}"; }
fail()    { printf "[${FAIL}] %s\n" "${1:-}"; FAILURES=$((FAILURES + 1)); }
warn()    { printf "[${WARN}] %s\n" "${1:-}"; }
info()    { printf "[${INFO}] %s\n" "${1:-}"; }

need_tool() {
  if ! command -v "$1" &>/dev/null; then
    printf "  ${YELLOW}⚠ %s not installed — skipping (%s)${R}\n" "$1" "$2"
    return 1
  fi
}

section "SOLID Principles"

printf "\n  ${B}[S] Single Responsibility${R}\n"

check "Cyclomatic complexity ≤ 15"
if need_tool gocyclo "go install github.com/fzipp/gocyclo/cmd/gocyclo@latest"; then
  hot=$(gocyclo -over 15 ./... 2>/dev/null || true)
  if [ -z "$hot" ]; then
    pass "all functions ≤ 15"
  else
    count=$(echo "$hot" | wc -l | tr -d ' ')
    fail "$count functions exceed threshold"
    echo "$hot" | head -10 | sed 's/^/    /'
    [ "$count" -gt 10 ] && printf "    ${D}... and %d more${R}\n" $((count - 10))
  fi
fi

check "Cognitive complexity ≤ 20"
if need_tool gocognit "go install github.com/uudashr/gocognit/cmd/gocognit@latest"; then
  hot=$(gocognit -over 20 ./... 2>/dev/null || true)
  if [ -z "$hot" ]; then
    pass "all functions ≤ 20"
  else
    count=$(echo "$hot" | wc -l | tr -d ' ')
    fail "$count functions exceed threshold"
    echo "$hot" | head -10 | sed 's/^/    /'
  fi
fi

check "File length ≤ 500 LOC (non-test)"
long_files=$(find . -name '*.go' ! -name '*_test.go' ! -path './vendor/*' -exec awk \
  'END { if (NR > 500) printf "%6d  %s\n", NR, FILENAME }' {} \;)
if [ -z "$long_files" ]; then
  pass
else
  count=$(echo "$long_files" | wc -l | tr -d ' ')
  warn "$count files over 500 lines"
  echo "$long_files" | sort -rn | head -5 | sed 's/^/    /'
fi

check "Functions per file ≤ 20 (non-test)"
overloaded=""
while IFS= read -r f; do
  n=$(grep -cE '^func ' "$f" 2>/dev/null) || n=0
  if [ "$n" -gt 20 ]; then
    overloaded="${overloaded}$(printf '%4d  %s\n' "$n" "$f")"$'\n'
  fi
done < <(find . -name '*.go' ! -name '*_test.go' ! -path './vendor/*')
overloaded="${overloaded%$'\n'}"
if [ -z "$overloaded" ]; then
  pass
else
  count=$(echo "$overloaded" | wc -l | tr -d ' ')
  warn "$count files with >20 functions"
  echo "$overloaded" | sort -rn | head -5 | sed 's/^/    /'
fi

printf "\n  ${B}[O] Open/Closed${R}\n"

check "Type switches on concrete types"
type_switches=$(grep -rnE 'switch\s+\w+\.\(type\)' --include='*.go' . 2>/dev/null || true)
if [ -z "$type_switches" ]; then
  pass "none found"
else
  count=$(echo "$type_switches" | wc -l | tr -d ' ')
  warn "$count type switches (review for extensibility)"
  echo "$type_switches" | head -5 | sed 's/^/    /'
fi

printf "\n  ${B}[L] Liskov Substitution${R}\n"

check "go vet (type-safety checks)"
if go vet ./... 2>/dev/null; then
  pass
else
  fail "go vet reported issues"
fi

check "Tests pass (interface contracts hold)"
if go test ./... -count=1 -short -timeout 60s > /tmp/quality_test_out.txt 2>&1; then
  pass
else
  fail "some tests failing"
  tail -20 /tmp/quality_test_out.txt | sed 's/^/    /'
fi

printf "\n  ${B}[I] Interface Segregation${R}\n"

check "Interfaces ≤ 5 methods"
fat_ifaces=$(awk '
  /^type .* interface \{/ {
    name = $2; methods = 0; in_iface = 1; file = FILENAME; next
  }
  in_iface && /^\}/ {
    if (methods > 5) printf "%4d methods  %-30s  %s\n", methods, name, file
    in_iface = 0
  }
  in_iface && /^\t[A-Z]/ { methods++ }
' $(find . -name '*.go' ! -name '*_test.go' ! -path './vendor/*') 2>/dev/null || true)

if [ -z "$fat_ifaces" ]; then
  pass "all interfaces ≤ 5 methods"
else
  count=$(echo "$fat_ifaces" | wc -l | tr -d ' ')
  warn "$count interfaces have >5 methods"
  echo "$fat_ifaces" | sed 's/^/    /'
fi

printf "\n  ${B}[D] Dependency Inversion${R}\n"

check "Constructors accept interfaces"
concrete_deps=$(grep -rnE '^func New\w+\(.*\*\w+' --include='*.go' . 2>/dev/null \
  | grep -v '_test.go' | grep -v 'vendor/' || true)
if [ -z "$concrete_deps" ]; then
  pass "all constructors use abstractions"
else
  count=$(echo "$concrete_deps" | wc -l | tr -d ' ')
  info "$count constructors accept concrete pointers (review needed)"
  echo "$concrete_deps" | head -5 | sed 's/^/    /'
fi

section "ISO 25000 — Software Product Quality"

printf "\n  ${B}Functional Suitability${R}\n"

check "Test coverage (excl. generated code)"
cover_out=$(go test ./... -short -coverprofile=/tmp/quality_cover.out -timeout 60s 2>&1 || true)
if [ -f /tmp/quality_cover.out ]; then
  grep -v '\.gen\.go:' /tmp/quality_cover.out > /tmp/quality_cover_filtered.out || true
  total_cov=$(go tool cover -func=/tmp/quality_cover_filtered.out 2>/dev/null | tail -1 | awk '{print $NF}')
  raw_cov=$(go tool cover -func=/tmp/quality_cover.out 2>/dev/null | tail -1 | awk '{print $NF}')
  pct=$(echo "$total_cov" | tr -d '%')
  if awk "BEGIN{exit ($pct >= 60) ? 0 : 1}"; then
    pass "$total_cov (raw incl. generated: $raw_cov)"
  elif awk "BEGIN{exit ($pct >= 40) ? 0 : 1}"; then
    warn "$total_cov (raw incl. generated: $raw_cov, target: ≥60%)"
  else
    fail "$total_cov (raw incl. generated: $raw_cov, target: ≥60%)"
  fi
else
  warn "could not compute coverage"
fi

check "Test-to-code ratio"
test_lines=$(find . -name '*_test.go' ! -path './vendor/*' -exec cat {} + 2>/dev/null | wc -l | tr -d ' ')
code_lines=$(find . -name '*.go' ! -name '*_test.go' ! -path './vendor/*' -exec cat {} + 2>/dev/null | wc -l | tr -d ' ')
if [ "$code_lines" -gt 0 ]; then
  ratio=$(awk "BEGIN { printf \"%.2f\", $test_lines / $code_lines }")
  if awk "BEGIN{exit ($test_lines / $code_lines >= 0.5) ? 0 : 1}"; then
    pass "${ratio}:1 (${test_lines} test / ${code_lines} code)"
  else
    warn "${ratio}:1 (target: ≥0.5:1)"
  fi
fi

printf "\n  ${B}Reliability${R}\n"

check "Error handling (unchecked errors)"
if need_tool golangci-lint "go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest"; then
  errcheck_out=$(golangci-lint run --enable errcheck --disable-all --timeout 120s ./... 2>&1 || true)
  errcount=$(echo "$errcheck_out" | grep -c ':' 2>/dev/null) || errcount=0
  if [ "$errcount" -le 0 ]; then
    pass "no unchecked errors"
  else
    warn "$errcount unchecked error returns"
    echo "$errcheck_out" | head -5 | sed 's/^/    /'
  fi
fi

check "Panic usage outside of tests"
panics=$(grep -rnE '\bpanic\(' --include='*.go' . 2>/dev/null \
  | grep -v '_test.go' | grep -v 'vendor/' || true)
if [ -z "$panics" ]; then
  pass "no panics in production code"
else
  count=$(echo "$panics" | wc -l | tr -d ' ')
  warn "$count panic() calls (review needed)"
  echo "$panics" | head -5 | sed 's/^/    /'
fi

printf "\n  ${B}Maintainability${R}\n"

check "Package count and structure"
pkg_count=$(find . -name '*.go' ! -path './vendor/*' -exec dirname {} \; | sort -u | wc -l | tr -d ' ')
if [ "$pkg_count" -le 30 ]; then
  pass "$pkg_count packages"
else
  warn "$pkg_count packages (review for over-fragmentation)"
fi

check "Circular dependencies"
if need_tool golangci-lint ""; then
  circ=$(golangci-lint run --enable depguard --disable-all --timeout 120s ./... 2>&1 | grep -i 'cycle\|circular' || true)
  if [ -z "$circ" ]; then
    pass "no cycles detected"
  else
    fail "circular dependencies found"
    echo "$circ" | head -5 | sed 's/^/    /'
  fi
fi

check "TODO/FIXME/HACK markers"
markers=$(grep -rnEI 'TODO|FIXME|HACK|XXX' --include='*.go' . 2>/dev/null \
  | grep -v 'vendor/' || true)
if [ -z "$markers" ]; then
  pass "none"
else
  count=$(echo "$markers" | wc -l | tr -d ' ')
  info "$count markers found"
  echo "$markers" | head -5 | sed 's/^/    /'
fi

printf "\n  ${B}Security${R}\n"

check "Hardcoded secrets patterns"
secrets=$(grep -rnEI '(password|secret|apikey|api_key|token)\s*[:=]\s*"[^"]{8,}"' \
  --include='*.go' . 2>/dev/null | grep -v '_test.go' | grep -v 'vendor/' || true)
if [ -z "$secrets" ]; then
  pass "no hardcoded secrets detected"
else
  count=$(echo "$secrets" | wc -l | tr -d ' ')
  fail "$count potential hardcoded secrets"
  echo "$secrets" | head -5 | sed 's/^/    /'
fi

check "SQL injection risk (string concat in queries)"
sqli=$(grep -rnE 'fmt\.Sprintf.*SELECT|fmt\.Sprintf.*INSERT|fmt\.Sprintf.*UPDATE|fmt\.Sprintf.*DELETE' \
  --include='*.go' . 2>/dev/null | grep -v '_test.go' | grep -v 'vendor/' || true)
if [ -z "$sqli" ]; then
  pass "no string-concatenated SQL found"
else
  count=$(echo "$sqli" | wc -l | tr -d ' ')
  warn "$count potential SQL injection points"
  echo "$sqli" | head -5 | sed 's/^/    /'
fi

printf "\n  ${B}Performance Efficiency${R}\n"

check "Race condition safety"
if go build ./... 2>/dev/null; then
  race_out=$(go test -race -short -count=1 -timeout 120s ./... 2>&1 || true)
  races=$(echo "$race_out" | grep -c 'DATA RACE' 2>/dev/null) || races=0
  if [ "$races" -eq 0 ]; then
    pass "no races detected"
  else
    fail "$races data race(s)"
    echo "$race_out" | grep -A3 'DATA RACE' | head -15 | sed 's/^/    /'
  fi
else
  warn "build failed — skipping race detection"
fi

printf "\n  ${B}Portability${R}\n"

check "Go module tidiness"
if go mod tidy -diff > /tmp/quality_modtidy.txt 2>&1; then
  pass "go.mod is tidy"
else
  warn "go mod tidy would make changes"
fi

check "Build constraints / OS-specific files"
os_files=$(find . -name '*_linux.go' -o -name '*_darwin.go' -o -name '*_windows.go' \
  ! -path './vendor/*' 2>/dev/null || true)
if [ -z "$os_files" ]; then
  pass "no OS-specific files"
else
  count=$(echo "$os_files" | wc -l | tr -d ' ')
  info "$count platform-specific files"
fi

section "Summary"
if [ "$FAILURES" -eq 0 ]; then
  printf "\n  ${GREEN}${B}All automated checks passed.${R}\n\n"
else
  printf "\n  ${RED}${B}$FAILURES check(s) failed.${R}\n\n"
fi
exit "$FAILURES"
