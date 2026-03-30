#!/usr/bin/env bash
set -uo pipefail

# ─── HA Soak Test for EasyCron ──────────────────────────────────────────
# Extended-duration test that validates horizontal scaling correctness
# over time. Samples deliveries periodically and asserts monotonic
# increase with no double-scheduling spikes.
#
# Reuses the same docker-compose.ha-test.yml as ha_test.sh.
# ────────────────────────────────────────────────────────────────────────

# ── Configuration (override via env) ────────────────────────────────────

HA_DURATION="${HA_DURATION:-20m}"
HA_MAX_FAILOVER_SECONDS="${HA_MAX_FAILOVER_SECONDS:-10}"
HA_SPIKE_THRESHOLD="${HA_SPIKE_THRESHOLD:-3}"

# Parse duration to seconds
parse_duration() {
    local val="$1"
    if [[ "$val" =~ ^([0-9]+)m$ ]]; then
        echo $(( ${BASH_REMATCH[1]} * 60 ))
    elif [[ "$val" =~ ^([0-9]+)s$ ]]; then
        echo "${BASH_REMATCH[1]}"
    elif [[ "$val" =~ ^([0-9]+)$ ]]; then
        echo "$val"
    else
        echo "Error: cannot parse duration '${val}' (use Ns or Nm)" >&2
        exit 2
    fi
}

DURATION_SECS=$(parse_duration "$HA_DURATION")
SAMPLE_INTERVAL=60  # seconds between delivery samples

# ── Constants ───────────────────────────────────────────────────────────

COMPOSE="docker compose -f docker-compose.ha-test.yml -p easycron-ha-test"
INSTANCES=("easycron_1" "easycron_2" "easycron_3")
PORTS=("8081" "8082" "8083")
WEBHOOK_URL="http://localhost:9090"
API_BASE="http://localhost:8081"
AUTH_HEADER="Authorization: Bearer ha-test-key"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

PASSED=0
FAILED=0

pass() { echo -e "${GREEN}  ✓ $1${NC}"; PASSED=$((PASSED + 1)); }
fail() { echo -e "${RED}  ✗ $1${NC}"; FAILED=$((FAILED + 1)); dump_diagnostics; }
info() { echo -e "${YELLOW}  → $1${NC}"; }
ts()   { echo -e "${CYAN}  [$(date +%H:%M:%S)]${NC} $1"; }

# ── Diagnostics (printed on any failure) ────────────────────────────────

dump_diagnostics() {
    echo ""
    echo -e "${RED}─── Diagnostics ───${NC}"

    echo "Leader detection:"
    detect_leader_by_metrics
    echo ""

    echo "Webhook receiver stats:"
    curl -sf "${WEBHOOK_URL}/stats" 2>/dev/null | python3 -m json.tool 2>/dev/null || echo "  (unavailable)"
    echo ""

    echo "Container status:"
    $COMPOSE ps 2>/dev/null || true
    echo ""

    for inst in "${INSTANCES[@]}"; do
        echo "--- Last 50 lines: ${inst} ---"
        $COMPOSE logs --tail=50 "$inst" 2>/dev/null || echo "  (unavailable)"
        echo ""
    done
    echo -e "${RED}─── End Diagnostics ───${NC}"
    echo ""
}

# ── Cleanup ─────────────────────────────────────────────────────────────

cleanup() {
    echo ""
    info "Cleaning up containers..."
    $COMPOSE down -v --remove-orphans 2>/dev/null || true
}
trap cleanup EXIT

# ── Helpers (same as ha_test.sh) ────────────────────────────────────────

webhook_count() {
    local raw
    raw=$(curl -sf "${WEBHOOK_URL}/stats" 2>/dev/null) || { echo "0"; return; }
    echo "$raw" | python3 -c "import sys,json; print(json.load(sys.stdin)['count'])" 2>/dev/null || echo "0"
}

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
        if $all_ok; then return 0; fi
        sleep 2
        elapsed=$((elapsed + 2))
    done
    return 1
}

assert_single_leader() {
    local label="$1"
    local leader
    leader=$(detect_leader)
    local lcount
    lcount=$(echo "$leader" | wc -w | tr -d ' ')
    if (( lcount == 1 )); then
        pass "${label}: single leader (${leader})"
        echo "$leader"
        return 0
    else
        fail "${label}: expected 1 leader, found ${lcount}: [${leader}]"
        echo ""
        return 1
    fi
}

# ── Banner ──────────────────────────────────────────────────────────────

echo "═══════════════════════════════════════════════════════════════"
echo " EasyCron HA Soak Test"
echo "═══════════════════════════════════════════════════════════════"
echo ""
info "Duration:            ${HA_DURATION} (${DURATION_SECS}s)"
info "Sample interval:     ${SAMPLE_INTERVAL}s"
info "Max failover time:   ${HA_MAX_FAILOVER_SECONDS}s"
info "Spike threshold:     ${HA_SPIKE_THRESHOLD} deliveries/min"
echo ""

# ── Phase 1: Startup ───────────────────────────────────────────────────

echo "Phase 1: Starting infrastructure..."

$COMPOSE down -v --remove-orphans 2>/dev/null || true
$COMPOSE up -d --build || { fail "docker compose up failed"; exit 1; }

if wait_for_healthy 90; then
    pass "All instances healthy"
else
    fail "Instances not healthy within 90s"
    exit 1
fi

if curl -sf "${WEBHOOK_URL}/health" >/dev/null 2>&1; then
    pass "Webhook receiver healthy"
else
    fail "Webhook receiver not healthy"
    exit 1
fi

# Reset webhook receiver counter
curl -sf "${WEBHOOK_URL}/reset" >/dev/null 2>&1 || true

# ── Phase 2: Create job and verify baseline ─────────────────────────────

echo ""
echo "Phase 2: Creating job and verifying baseline..."

JOB_RESPONSE=$(curl -sf -X POST "${API_BASE}/jobs" \
    -H "Content-Type: application/json" \
    -H "${AUTH_HEADER}" \
    -d '{
        "name": "soak-test-webhook",
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
    fail "Failed to create job"
    exit 1
fi

sleep 5
LEADER=""
LEADER=$(assert_single_leader "Baseline") || true

# Wait for first delivery
info "Waiting for first delivery (up to 90s)..."
WAIT=0
while (( WAIT < 90 )); do
    sleep 10
    WAIT=$((WAIT + 10))
    CURRENT=$(webhook_count)
    if (( CURRENT > 0 )); then
        pass "First delivery received after ${WAIT}s"
        break
    fi
done
if (( $(webhook_count) == 0 )); then
    fail "No deliveries after 90s"
    exit 1
fi

# ── Phase 3: Soak window with periodic sampling ────────────────────────

echo ""
echo "Phase 3: Soak window (${HA_DURATION})..."
echo ""

SOAK_START=$SECONDS
PREV_COUNT=$(webhook_count)
PREV_SAMPLE_TIME=$SECONDS
SAMPLE_NUM=0

# Schedule disruption events at specific times within the soak window
# Event 1: Kill leader at ~25% through the soak
# Event 2: Restart follower at ~60% through the soak
KILL_AT=$(( DURATION_SECS / 4 ))
RESTART_AT=$(( DURATION_SECS * 3 / 5 ))
KILL_DONE=false
RESTART_DONE=false
CURRENT_LEADER="$LEADER"

while true; do
    ELAPSED=$(( SECONDS - SOAK_START ))
    REMAINING=$(( DURATION_SECS - ELAPSED ))

    if (( REMAINING <= 0 )); then
        break
    fi

    # Sleep for sample interval or remaining time, whichever is shorter
    SLEEP_FOR=$SAMPLE_INTERVAL
    if (( SLEEP_FOR > REMAINING )); then
        SLEEP_FOR=$REMAINING
    fi
    sleep "$SLEEP_FOR"

    ELAPSED=$(( SECONDS - SOAK_START ))
    SAMPLE_NUM=$((SAMPLE_NUM + 1))
    NOW_COUNT=$(webhook_count)
    SAMPLE_ELAPSED=$(( SECONDS - PREV_SAMPLE_TIME ))

    # ── Assertion: monotonic delivery count ──
    if (( NOW_COUNT < PREV_COUNT )); then
        fail "Sample #${SAMPLE_NUM}: delivery count decreased (${PREV_COUNT} → ${NOW_COUNT})"
    fi

    # ── Assertion: no delivery spike ──
    SAMPLE_DELTA=$(( NOW_COUNT - PREV_COUNT ))
    SAMPLE_MINUTES=$(( SAMPLE_ELAPSED / 60 ))
    if (( SAMPLE_MINUTES < 1 )); then SAMPLE_MINUTES=1; fi
    SAMPLE_RATE=$(( SAMPLE_DELTA / SAMPLE_MINUTES ))

    if (( SAMPLE_RATE > HA_SPIKE_THRESHOLD )); then
        fail "Sample #${SAMPLE_NUM}: delivery spike (${SAMPLE_RATE}/min > threshold ${HA_SPIKE_THRESHOLD})"
    fi

    MINS_ELAPSED=$(( ELAPSED / 60 ))
    MINS_REMAIN=$(( REMAINING / 60 ))
    ts "Sample #${SAMPLE_NUM}: count=${NOW_COUNT} (+${SAMPLE_DELTA}), rate=${SAMPLE_RATE}/min, elapsed=${MINS_ELAPSED}m, remaining=~${MINS_REMAIN}m"

    PREV_COUNT=$NOW_COUNT
    PREV_SAMPLE_TIME=$SECONDS

    # ── Event 1: Kill leader ──
    if [[ "$KILL_DONE" == "false" ]] && (( ELAPSED >= KILL_AT )); then
        echo ""
        echo "  ── Event: Kill leader (${CURRENT_LEADER}) at ${MINS_ELAPSED}m ──"

        docker kill "$CURRENT_LEADER" || info "Warning: docker kill returned non-zero"
        info "Leader ${CURRENT_LEADER} killed"

        sleep 3
        NEW_LEADER=""
        NEW_LEADER=$(wait_for_leader "$HA_MAX_FAILOVER_SECONDS") || true
        NEW_LEADER_COUNT=$(echo "$NEW_LEADER" | wc -w | tr -d ' ')

        if (( NEW_LEADER_COUNT == 1 )) && [[ "$NEW_LEADER" != "$CURRENT_LEADER" ]]; then
            pass "Failover: ${CURRENT_LEADER} → ${NEW_LEADER}"
            CURRENT_LEADER="$NEW_LEADER"
        else
            fail "Failover failed: expected new leader, got [${NEW_LEADER}]"
        fi

        # Verify deliveries continue after failover
        POST_KILL_COUNT=$(webhook_count)
        sleep 70
        POST_KILL_COUNT_2=$(webhook_count)
        if (( POST_KILL_COUNT_2 > POST_KILL_COUNT )); then
            pass "Deliveries continued after failover: ${POST_KILL_COUNT} → ${POST_KILL_COUNT_2}"
        else
            fail "Deliveries stalled after failover (stuck at ${POST_KILL_COUNT})"
        fi

        PREV_COUNT=$(webhook_count)
        PREV_SAMPLE_TIME=$SECONDS
        KILL_DONE=true
        echo ""
    fi

    # ── Event 2: Restart follower ──
    if [[ "$RESTART_DONE" == "false" ]] && [[ "$KILL_DONE" == "true" ]] && (( ELAPSED >= RESTART_AT )); then
        echo ""
        echo "  ── Event: Restart follower at ${MINS_ELAPSED}m ──"

        FOLLOWER=""
        for inst in "${INSTANCES[@]}"; do
            if [[ "$inst" != "$CURRENT_LEADER" ]]; then
                # Check if container is running
                if docker inspect --format='{{.State.Running}}' "$inst" 2>/dev/null | grep -q true; then
                    FOLLOWER="$inst"
                    break
                fi
            fi
        done

        if [[ -z "$FOLLOWER" ]]; then
            info "No running follower found, skipping restart event"
        else
            info "Restarting follower ${FOLLOWER}..."
            docker restart "$FOLLOWER" || info "Warning: docker restart returned non-zero"
            sleep 5

            ROLLING_LEADER=""
            ROLLING_LEADER=$(detect_leader)
            ROLLING_COUNT=$(echo "$ROLLING_LEADER" | wc -w | tr -d ' ')

            if (( ROLLING_COUNT == 1 )) && [[ "$ROLLING_LEADER" == "$CURRENT_LEADER" ]]; then
                pass "Leader stable during follower restart: ${ROLLING_LEADER}"
            else
                fail "Leader changed during follower restart: [${ROLLING_LEADER}]"
            fi

            PREV_COUNT=$(webhook_count)
            PREV_SAMPLE_TIME=$SECONDS
        fi

        RESTART_DONE=true
        echo ""
    fi
done

# ── Phase 4: Final assertions ──────────────────────────────────────────

echo ""
echo "Phase 4: Final assertions..."

FINAL_COUNT=$(webhook_count)
TOTAL_ELAPSED=$(( SECONDS - SOAK_START ))
TOTAL_MINUTES=$(( TOTAL_ELAPSED / 60 ))
if (( TOTAL_MINUTES < 1 )); then TOTAL_MINUTES=1; fi
OVERALL_RATE=$(( FINAL_COUNT / TOTAL_MINUTES ))

info "Total deliveries: ${FINAL_COUNT} over ~${TOTAL_MINUTES} minutes (${OVERALL_RATE}/min avg)"

# Should have received roughly 1 delivery per minute
if (( FINAL_COUNT >= TOTAL_MINUTES / 2 )); then
    pass "Delivery count reasonable: ${FINAL_COUNT} deliveries in ${TOTAL_MINUTES} min"
else
    fail "Too few deliveries: ${FINAL_COUNT} in ${TOTAL_MINUTES} min (expected ≥$((TOTAL_MINUTES / 2)))"
fi

# Final leader check
assert_single_leader "Final" >/dev/null || true

if [[ "$KILL_DONE" == "true" ]]; then
    pass "Leader kill event completed"
else
    fail "Leader kill event never triggered (soak too short?)"
fi

if [[ "$RESTART_DONE" == "true" ]]; then
    pass "Follower restart event completed"
else
    fail "Follower restart event never triggered (soak too short?)"
fi

# ── Summary ─────────────────────────────────────────────────────────────

echo ""
echo "═══════════════════════════════════════════════════════════════"
TOTAL=$((PASSED + FAILED))
if (( FAILED == 0 )); then
    echo -e "${GREEN} PASS: All ${TOTAL} assertions passed (soak: ${HA_DURATION})${NC}"
else
    echo -e "${RED} FAIL: ${FAILED}/${TOTAL} assertions failed (soak: ${HA_DURATION})${NC}"
fi
echo "═══════════════════════════════════════════════════════════════"

exit $FAILED
