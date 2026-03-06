#!/bin/bash
set -euo pipefail

GATEWAY="http://localhost:8000"
VIDEO_PATH="./test.mp4"
POLL_INTERVAL=3
MAX_WAIT=300

while [[ $# -gt 0 ]]; do
  case "$1" in
    --video-path) VIDEO_PATH="$2"; shift 2 ;;
    *) echo "Unknown arg: $1"; exit 1 ;;
  esac
done

if [ ! -f "$VIDEO_PATH" ]; then
  echo "ERROR: Video file not found at $VIDEO_PATH"
  echo "Usage: $0 --video-path /path/to/video.mp4"
  exit 1
fi

PASS=0
FAIL=0
RESULTS=()

# --- Helpers ---

wait_for_gateway() {
  echo "Waiting for gateway at $GATEWAY ..."
  local elapsed=0
  while [ $elapsed -lt 30 ]; do
    if curl -sf "$GATEWAY/health" > /dev/null 2>&1; then
      echo "Gateway is ready."
      return 0
    fi
    sleep 2
    elapsed=$((elapsed + 2))
  done
  echo "ERROR: Gateway not reachable after 30 seconds"
  exit 1
}

poll_job() {
  local job_id="$1"
  local elapsed=0

  while [ $elapsed -lt $MAX_WAIT ]; do
    local resp
    resp=$(curl -sf "$GATEWAY/jobs/$job_id" 2>/dev/null) || true

    if [ -z "$resp" ]; then
      echo "  [poll] No response, retrying..."
      sleep $POLL_INTERVAL
      elapsed=$((elapsed + POLL_INTERVAL))
      continue
    fi

    local job_status
    job_status=$(echo "$resp" | python3 -c "import sys,json; print(json.load(sys.stdin)['status'])" 2>/dev/null || echo "unknown")

    local stage_summary
    stage_summary=$(echo "$resp" | python3 -c "
import sys, json
j = json.load(sys.stdin)
for s in j.get('stages', []):
    print(f\"    stage={s['id'][:8]}.. type={s['type']} status={s['status']}\")
" 2>/dev/null || echo "    (parse error)")

    echo "  [poll +${elapsed}s] job_status=$job_status"
    echo "$stage_summary"

    case "$job_status" in
      completed)
        echo "$resp" | python3 -c "
import sys, json
j = json.load(sys.stdin)
for s in j.get('stages', []):
    url = s.get('presigned_url', 'N/A')
    print(f\"  Output [{s['type']}]: {url[:80]}...\")
" 2>/dev/null || true
        return 0
        ;;
      failed)
        echo "  JOB FAILED"
        echo "$resp" | python3 -c "
import sys, json
j = json.load(sys.stdin)
for s in j.get('stages', []):
    if s.get('error'):
        print(f\"    Error [{s['type']}]: {s['error']}\")
" 2>/dev/null || true
        return 1
        ;;
    esac

    sleep $POLL_INTERVAL
    elapsed=$((elapsed + POLL_INTERVAL))
  done

  echo "  TIMEOUT after ${MAX_WAIT}s"
  return 1
}

run_test() {
  local name="$1"
  local stages="$2"

  echo ""
  echo "========================================"
  echo "TEST: $name (stages=$stages)"
  echo "========================================"

  local resp
  resp=$(curl -sf -X POST "$GATEWAY/jobs" \
    -F "file=@$VIDEO_PATH" \
    -F "stages=$stages" 2>&1) || {
    echo "  FAIL — POST /jobs returned error"
    FAIL=$((FAIL + 1))
    RESULTS+=("FAIL: $name")
    return
  }

  local job_id
  job_id=$(echo "$resp" | python3 -c "import sys,json; print(json.load(sys.stdin)['id'])" 2>/dev/null) || {
    echo "  FAIL — could not parse job_id from response"
    echo "  Response: $resp"
    FAIL=$((FAIL + 1))
    RESULTS+=("FAIL: $name")
    return
  }

  echo "  Job created — id=$job_id"

  if poll_job "$job_id"; then
    echo "  PASS"
    PASS=$((PASS + 1))
    RESULTS+=("PASS: $name")
  else
    echo "  FAIL"
    FAIL=$((FAIL + 1))
    RESULTS+=("FAIL: $name")
  fi
}

# --- Main ---

wait_for_gateway

run_test "Extract Only" "extract"
run_test "Transcode Only" "transcode"
run_test "Multi-Stage (transcode+extract)" "transcode,extract"

echo ""
echo "========================================"
echo "SUMMARY"
echo "========================================"
for r in "${RESULTS[@]}"; do
  echo "  $r"
done
echo ""
echo "Passed: $PASS  Failed: $FAIL"

if [ $FAIL -gt 0 ]; then
  exit 1
fi
exit 0
