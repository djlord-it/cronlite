#!/usr/bin/env bash
set -uo pipefail

# ─── HA Test Harness for EasyCron ───────────────────────────────────────
# Validates: single-leader election, failover, rolling overlap,
#            no double-scheduling during transitions.
# ────────────────────────────────────────────────────────────────────────

COMPOSE="docker compose -f docker-compose.ha-test.yml -p easycron-ha-test"
INSTANCES=("easycron_1" "easycron_2" "easycron_3")
PORTS=("8081" "8082" "8083")
WEBHOOK_URL="http://localhost:9090"
API_BASE="http://localhost:8081"  # any instance can accept API calls
AUTH_HEADER="Authorization: Bearer ha-test-key"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

PASSED=0
FAILED=0

pass() { echo -e "${GREEN}  ✓ $1${NC}"; PASSED=$((PASSED + 1)); }
fail() { echo -e "${RED}  ✗ $1${NC}"; FAILED=$((FAILED + 1)); }
info() { echo -e "${YELLOW}  → $1${NC}"; }

cleanup() {
    echo ""
    info "Cleaning up containers..."
    $COMPOSE down -v --remove-orphans 2>/dev/null || true
}
trap cleanup EXIT

# ── Helpers ──────────────────────────────────────────────────────────────

webhook_count() {
    local raw
    raw=$(curl -sf "${WEBHOOK_URL}/stats" 2>/dev/null) || { echo "0"; return; }
    echo "$raw" | python3 -c "import sys,json; print(json.load(sys.stdin)['count'])" 2>/dev/null || echo "0"
}

# Detect leader via Prometheus metric easycron_leader_is_leader == 1.
# Falls back to log grep if metric is unavailable.
detect_leader_by_metrics() {
    local leaders=()
    for i in 0 1 2; do
        local raw val
        raw=$(curl -sf "http://localhost:${PORTS[$i]}/metrics" 2>/dev/null) || continue
        val=$(echo "$raw" | grep '^easycron_leader_is_leader ' | awk '{print $2}') || continue
        if [[ "$val" == "1" ]]; then
            leaders+=("${INSTANCES[$i]}")
        fi
    done
    echo "${leaders[*]:-}"
}

detect_leader_by_logs() {
    local leaders=()
    for inst in "${INSTANCES[@]}"; do
        local logs
        logs=$($COMPOSE logs "$inst" 2>/dev/null) || continue
        if echo "$logs" | grep -q "leader: acquired advisory lock"; then
            local acq rel
            acq=$(echo "$logs" | grep -c "leader: acquired advisory lock") || acq=0
            rel=$(echo "$logs" | grep -c "leader: released advisory lock") || rel=0
            if (( acq > rel )); then
                leaders+=("$inst")
            fi
        fi
    done
    echo "${leaders[*]:-}"
}

detect_leader() {
    local leaders
    leaders=$(detect_leader_by_metrics)
    if [[ -z "$leaders" ]]; then
        info "Metrics unavailable, falling back to log detection"
        leaders=$(detect_leader_by_logs)
    fi
    echo "$leaders"
}

wait_for_leader() {
    local timeout=${1:-30}
    local elapsed=0
    while (( elapsed < timeout )); do
        local leaders
        leaders=$(detect_leader)
        local lcount
        lcount=$(echo "$leaders" | wc -w | tr -d ' ')
        if (( lcount == 1 )); then
            echo "$leaders"
            return 0
        fi
        sleep 2
        elapsed=$((elapsed + 2))
    done
    echo ""
    return 1
}

wait_for_healthy() {
    local timeout=${1:-60}
    local elapsed=0
    while (( elapsed < timeout )); do
        local all_ok=true
        for port in "${PORTS[@]}"; do
            if ! curl -sf "http://localhost:${port}/health" >/dev/null 2>&1; then
                all_ok=false
                break
            fi
        done
        if $all_ok; then
            return 0
        fi
        sleep 2
        elapsed=$((elapsed + 2))
    done
    return 1
}

# ── Step 0: Build and start ─────────────────────────────────────────────

echo "═══════════════════════════════════════════════════════════════"
echo " EasyCron HA Test Harness"
echo "═══════════════════════════════════════════════════════════════"
echo ""
echo "Step 0: Starting infrastructure..."

$COMPOSE down -v --remove-orphans 2>/dev/null || true
$COMPOSE up -d --build || { fail "docker compose up failed"; exit 1; }

# ── Step 1: Wait for healthy ────────────────────────────────────────────

echo ""
echo "Step 1: Waiting for all instances to be healthy..."

if wait_for_healthy 90; then
    pass "All 3 EasyCron instances are healthy"
else
    fail "Not all instances became healthy within 90s"
    echo "Aborting."
    exit 1
fi

# Also verify webhook-receiver
if curl -sf "${WEBHOOK_URL}/health" >/dev/null 2>&1; then
    pass "Webhook receiver is healthy"
else
    fail "Webhook receiver is not healthy"
    exit 1
fi

# ── Step 2: Create a frequently-firing job ──────────────────────────────

echo ""
echo "Step 2: Creating test job (*/1 * * * *, fires every minute)..."

JOB_RESPONSE=$(curl -sf -X POST "${API_BASE}/jobs" \
    -H "Content-Type: application/json" \
    -H "${AUTH_HEADER}" \
    -d '{
        "name": "ha-test-webhook",
        "cron_expression": "*/1 * * * *",
        "timezone": "UTC",
        "webhook_url": "http://webhook-receiver:8080/hook",
        "webhook_secret": "test-secret"
    }') || JOB_RESPONSE=""

JOB_ID=""
if [[ -n "$JOB_RESPONSE" ]]; then
    JOB_ID=$(echo "$JOB_RESPONSE" | python3 -c "import sys,json; print(json.load(sys.stdin)['id'])" 2>/dev/null) || JOB_ID=""
fi

if [[ -n "$JOB_ID" ]]; then
    pass "Job created: ${JOB_ID}"
else
    fail "Failed to create job. Response: ${JOB_RESPONSE}"
    exit 1
fi

# ── Step 3: Assert single leader ───────────────────────────────────────

echo ""
echo "Step 3: Asserting exactly one leader..."

sleep 5  # Let leader election settle

LEADER=""
LEADER=$(wait_for_leader 30) || true
LEADER_COUNT=$(echo "$LEADER" | wc -w | tr -d ' ')

if (( LEADER_COUNT == 1 )); then
    pass "Exactly 1 leader detected: ${LEADER}"
else
    fail "Expected 1 leader, found ${LEADER_COUNT}: [${LEADER}]"
fi

# ── Step 4: Verify webhook deliveries ──────────────────────────────────

echo ""
echo "Step 4: Waiting for webhook deliveries (up to 90s)..."

COUNT_BEFORE=$(webhook_count)
info "Initial webhook count: ${COUNT_BEFORE}"

# Wait up to 90s for count to increase
WAIT=0
while (( WAIT < 90 )); do
    sleep 10
    WAIT=$((WAIT + 10))
    CURRENT=$(webhook_count)
    info "Webhook count after ${WAIT}s: ${CURRENT}"
    if (( CURRENT > COUNT_BEFORE )); then
        break
    fi
done

COUNT_AFTER=$(webhook_count)
if (( COUNT_AFTER > COUNT_BEFORE )); then
    pass "Webhook deliveries increasing: ${COUNT_BEFORE} → ${COUNT_AFTER}"
else
    fail "No webhook deliveries after 90s (stuck at ${COUNT_BEFORE})"
fi

# Record delivery rate before failover for spike detection
RATE_BEFORE=$COUNT_AFTER
RATE_BEFORE_TIME=$SECONDS

# ── Step 5: Kill the leader ────────────────────────────────────────────

echo ""
echo "Step 5: Killing leader container (${LEADER})..."

docker kill "$LEADER" || info "Warning: docker kill returned non-zero"
info "Leader ${LEADER} killed"

# ── Step 6: Wait for failover ──────────────────────────────────────────

echo ""
echo "Step 6: Waiting for failover (up to 15s = LEADER_RETRY_INTERVAL*3 + tolerance)..."

sleep 3  # Give Postgres time to detect dead connection

NEW_LEADER=""
NEW_LEADER=$(wait_for_leader 15) || true
NEW_LEADER_COUNT=$(echo "$NEW_LEADER" | wc -w | tr -d ' ')

if (( NEW_LEADER_COUNT == 1 )) && [[ "$NEW_LEADER" != "$LEADER" ]]; then
    pass "Failover succeeded: new leader is ${NEW_LEADER}"
elif (( NEW_LEADER_COUNT == 1 )); then
    fail "Leader didn't change (still ${NEW_LEADER}). The old leader may have restarted."
else
    fail "Expected 1 new leader after failover, found ${NEW_LEADER_COUNT}: [${NEW_LEADER}]"
fi

# ── Step 7: Verify continued deliveries ────────────────────────────────

echo ""
echo "Step 7: Verifying webhook deliveries continue after failover..."

COUNT_POST_FAILOVER=$(webhook_count)
info "Count right after failover: ${COUNT_POST_FAILOVER}"

# Wait for more deliveries
sleep 70  # Just over one minute to catch the next tick
COUNT_AFTER_FAILOVER=$(webhook_count)
info "Count after waiting: ${COUNT_AFTER_FAILOVER}"

if (( COUNT_AFTER_FAILOVER > COUNT_POST_FAILOVER )); then
    pass "Deliveries continued after failover: ${COUNT_POST_FAILOVER} → ${COUNT_AFTER_FAILOVER}"
else
    fail "Deliveries stalled after failover (stuck at ${COUNT_POST_FAILOVER})"
fi

# ── Double-scheduling spike check ──────────────────────────────────────

# Expected: ~1 delivery per minute. A spike would be >2 deliveries in a
# single minute window, indicating double-scheduling.
ELAPSED_SINCE_RATE_START=$(( SECONDS - RATE_BEFORE_TIME ))
TOTAL_DELIVERIES_DURING_TEST=$(( COUNT_AFTER_FAILOVER - RATE_BEFORE ))
MINUTES_ELAPSED=$(( ELAPSED_SINCE_RATE_START / 60 ))
if (( MINUTES_ELAPSED < 1 )); then MINUTES_ELAPSED=1; fi
RATE=$(( TOTAL_DELIVERIES_DURING_TEST / MINUTES_ELAPSED ))

info "Delivery rate during failover period: ~${RATE} per minute (${TOTAL_DELIVERIES_DURING_TEST} deliveries in ~${MINUTES_ELAPSED} min)"

# With 1 job firing */1, we expect ≤2 per minute (1 expected + tolerance for tick overlap).
# >3 would indicate double-scheduling.
if (( RATE <= 3 )); then
    pass "No delivery spike during failover (rate: ${RATE}/min, threshold: ≤3)"
else
    fail "Possible double-scheduling spike during failover (rate: ${RATE}/min, expected ≤3)"
fi

# ── Step 8: Rolling overlap ────────────────────────────────────────────

echo ""
echo "Step 8: Rolling overlap test (restart a follower while leader is up)..."

# Find a follower (not the current leader, not the killed one)
FOLLOWER=""
for inst in "${INSTANCES[@]}"; do
    if [[ "$inst" != "$NEW_LEADER" && "$inst" != "$LEADER" ]]; then
        FOLLOWER="$inst"
        break
    fi
done

if [[ -z "$FOLLOWER" ]]; then
    info "No available follower to test rolling restart, skipping"
else
    info "Restarting follower ${FOLLOWER}..."
    COUNT_BEFORE_ROLLING=$(webhook_count)
    ROLLING_START=$SECONDS

    docker restart "$FOLLOWER" || info "Warning: docker restart returned non-zero"

    sleep 5  # Let it rejoin

    # Verify still exactly one leader
    ROLLING_LEADER=$(detect_leader)
    ROLLING_LEADER_COUNT=$(echo "$ROLLING_LEADER" | wc -w | tr -d ' ')

    if (( ROLLING_LEADER_COUNT == 1 )) && [[ "$ROLLING_LEADER" == "$NEW_LEADER" ]]; then
        pass "Leader unchanged during follower restart: ${ROLLING_LEADER}"
    else
        fail "Leader changed or split during follower restart: [${ROLLING_LEADER}]"
    fi

    # Wait another minute and check for delivery spike
    sleep 65
    COUNT_AFTER_ROLLING=$(webhook_count)
    ROLLING_ELAPSED=$(( SECONDS - ROLLING_START ))
    ROLLING_DELIVERIES=$(( COUNT_AFTER_ROLLING - COUNT_BEFORE_ROLLING ))
    ROLLING_MINUTES=$(( ROLLING_ELAPSED / 60 ))
    if (( ROLLING_MINUTES < 1 )); then ROLLING_MINUTES=1; fi
    ROLLING_RATE=$(( ROLLING_DELIVERIES / ROLLING_MINUTES ))

    info "Rolling overlap delivery rate: ~${ROLLING_RATE}/min (${ROLLING_DELIVERIES} in ~${ROLLING_MINUTES} min)"

    if (( ROLLING_RATE <= 3 )); then
        pass "No delivery spike during rolling overlap (rate: ${ROLLING_RATE}/min, threshold: ≤3)"
    else
        fail "Possible double-scheduling during rolling overlap (rate: ${ROLLING_RATE}/min, expected ≤3)"
    fi
fi

# ── Step 9: Summary ────────────────────────────────────────────────────

echo ""
echo "═══════════════════════════════════════════════════════════════"
TOTAL=$((PASSED + FAILED))
if (( FAILED == 0 )); then
    echo -e "${GREEN} PASS: All ${TOTAL} assertions passed${NC}"
else
    echo -e "${RED} FAIL: ${FAILED}/${TOTAL} assertions failed${NC}"
fi
echo "═══════════════════════════════════════════════════════════════"

exit $FAILED
