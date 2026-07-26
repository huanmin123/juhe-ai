#!/usr/bin/env bash
set -euo pipefail

MODE=check
SLOT=
CONTROL_PORT=
GATEWAY_BASE_PORT=
GATEWAY_COUNT=3
INGRESS_PORT=3099
TIMEOUT_SECONDS=3600
POLL_SECONDS=5

usage() {
  cat <<'EOF'
Usage: wait-performance-slot-drain.sh --slot main|temporary|standalone --control-port PORT --gateway-base-port PORT [options]
  --check                  check once (default)
  --wait                   wait until drained or timeout
  --gateway-count 1..32
  --ingress-port PORT
  --timeout-seconds 1..86400
  --poll-seconds 1..60
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --check) MODE=check; shift ;;
    --wait) MODE=wait; shift ;;
    --slot) SLOT="${2:-}"; shift 2 ;;
    --control-port) CONTROL_PORT="${2:-}"; shift 2 ;;
    --gateway-base-port) GATEWAY_BASE_PORT="${2:-}"; shift 2 ;;
    --gateway-count) GATEWAY_COUNT="${2:-}"; shift 2 ;;
    --ingress-port) INGRESS_PORT="${2:-}"; shift 2 ;;
    --timeout-seconds) TIMEOUT_SECONDS="${2:-}"; shift 2 ;;
    --poll-seconds) POLL_SECONDS="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown option: $1" >&2; usage >&2; exit 2 ;;
  esac
done

case "$SLOT" in main|temporary|standalone) ;; *) echo '--slot must be main, temporary or standalone' >&2; exit 2 ;; esac
assert_number() {
  value="$1" min="$2" max="$3" name="$4"
  printf '%s' "$value" | grep -Eq '^[0-9]+$' || { echo "$name must be numeric" >&2; exit 2; }
  [ "$value" -ge "$min" ] && [ "$value" -le "$max" ] || { echo "$name must be between $min and $max" >&2; exit 2; }
}
assert_number "$CONTROL_PORT" 1 65535 control-port
assert_number "$GATEWAY_BASE_PORT" 1 65535 gateway-base-port
assert_number "$GATEWAY_COUNT" 1 32 gateway-count
assert_number "$INGRESS_PORT" 1 65535 ingress-port
assert_number "$TIMEOUT_SECONDS" 1 86400 timeout-seconds
assert_number "$POLL_SECONDS" 1 60 poll-seconds
[ "$((GATEWAY_BASE_PORT + GATEWAY_COUNT - 1))" -le 65535 ] || { echo 'gateway port range exceeds 65535' >&2; exit 2; }
command -v curl >/dev/null
command -v lsof >/dev/null

assert_lsof_capability() {
  probe_output="$(lsof -nP -a -p "$$" -d cwd -Fn 2>&1)" || {
    echo "lsof capability probe failed: ${probe_output:-no diagnostic}" >&2
    return 1
  }
  printf '%s\n' "$probe_output" | grep -qx "p$$" \
    && printf '%s\n' "$probe_output" | grep -q '^n/' || {
      echo 'lsof capability probe did not return the current process cwd' >&2
      return 1
    }
  probe_output="$(lsof -nP -a -c nginx -iTCP:"$INGRESS_PORT" -sTCP:LISTEN -Fn 2>&1)" || {
    echo "lsof cannot inspect the nginx ingress listener on port $INGRESS_PORT: ${probe_output:-no diagnostic}" >&2
    return 1
  }
  printf '%s\n' "$probe_output" | grep -q '^p[0-9][0-9]*$' \
    && printf '%s\n' "$probe_output" | grep -Eq "^n.*:${INGRESS_PORT}$" || {
      echo "lsof did not prove a visible nginx ingress listener on port $INGRESS_PORT" >&2
      return 1
    }
}

active_slot() {
  curl -fsS --max-time 3 -D - -o /dev/null "http://127.0.0.1:$INGRESS_PORT/__aisys__/health" \
    | tr -d '\r' \
    | sed -n 's/^X-Juhe-Active-Upstream:[[:space:]]*performance-\(main\|temporary\)[[:space:]]*$/\1/ip' \
    | head -1
}

established_count() {
  port="$1"
  error_file="$(mktemp -t juhe-ai-drain-lsof.XXXXXX)" || return 1
  if output="$(lsof -nP -a -c nginx -iTCP:"$port" -sTCP:ESTABLISHED 2>"$error_file")"; then
    status=0
  else
    status=$?
  fi
  if [ "$status" -gt 1 ] || [ -s "$error_file" ]; then
    echo "lsof connection probe failed for port $port" >&2
    [ ! -s "$error_file" ] || sed 's/^/lsof: /' "$error_file" >&2
    rm -f -- "$error_file"
    return 1
  fi
  rm -f -- "$error_file"
  if [ "$status" -eq 1 ]; then
    printf '0'
    return 0
  fi
  printf '%s\n' "$output" | sed '1d' | wc -l | tr -d '[:space:]'
}

slot_connection_count() {
  total="$(established_count "$CONTROL_PORT")"
  index=0
  while [ "$index" -lt "$GATEWAY_COUNT" ]; do
    count="$(established_count "$((GATEWAY_BASE_PORT + index))")"
    total=$((total + count))
    index=$((index + 1))
  done
  printf '%s' "$total"
}

started_at="$(date +%s)"
stable_zero=0
required_stable_zero=3
assert_lsof_capability
while :; do
  if ! active="$(active_slot)"; then
    echo "could not query ingress $INGRESS_PORT; refusing to infer a drained slot" >&2
    exit 1
  fi
  case "$SLOT:$active" in
    main:temporary|temporary:main|standalone:main|standalone:temporary) ;;
    "$SLOT:$SLOT") echo "slot $SLOT is still active on ingress $INGRESS_PORT; refusing to call it drained" >&2; exit 1 ;;
    *) echo "ingress $INGRESS_PORT did not explicitly identify another active performance slot; got ${active:-no route identity}" >&2; exit 1 ;;
  esac
  connections="$(slot_connection_count)"
  printf 'slot=%s active=%s established_nginx_connections=%s\n' "$SLOT" "${active:-unknown}" "$connections"
  if [ "$connections" -eq 0 ]; then stable_zero=$((stable_zero + 1)); else stable_zero=0; fi
  if [ "$stable_zero" -ge "$required_stable_zero" ]; then
    printf 'PERFORMANCE_SLOT_DRAINED slot=%s\n' "$SLOT"
    exit 0
  fi
  if [ "$MODE" = check ] && [ "$connections" -ne 0 ]; then exit 1; fi
  now="$(date +%s)"
  [ "$MODE" = check ] || [ "$((now - started_at))" -lt "$TIMEOUT_SECONDS" ] || {
    echo "slot $SLOT did not drain within $TIMEOUT_SECONDS seconds" >&2
    exit 1
  }
  sleep "$POLL_SECONDS"
done
