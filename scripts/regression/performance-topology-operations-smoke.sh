#!/usr/bin/env bash
set -euo pipefail

OPERATIONS_ROOT="${1:?operations root is required}"
VERIFIER="$OPERATIONS_ROOT/verify-performance-topology.sh"
[ -f "$VERIFIER" ] || { echo "missing verifier: $VERIFIER" >&2; exit 1; }

ROOT="$(mktemp -d "${TMPDIR:-/tmp}/juhe-ai-performance-verifier.XXXXXX")"
/usr/bin/sleep 60 & CONTROL_PID=$!
/usr/bin/sleep 60 & GATEWAY_1_PID=$!
/usr/bin/sleep 60 & GATEWAY_2_PID=$!
/usr/bin/sleep 60 & GATEWAY_3_PID=$!
cleanup() {
  kill "$CONTROL_PID" "$GATEWAY_1_PID" "$GATEWAY_2_PID" "$GATEWAY_3_PID" 2>/dev/null || true
  rm -rf -- "$ROOT"
}
trap cleanup EXIT
FAKE_BIN="$ROOT/fake-bin"
RELEASE="$ROOT/release"
mkdir -p "$FAKE_BIN" "$RELEASE/backend/dist" "$RELEASE/docs/deploy/macos/operations"
: > "$RELEASE/backend/dist/server.js"
cp -- "$VERIFIER" "$RELEASE/docs/deploy/macos/operations/verify-performance-topology.sh"

cat > "$FAKE_BIN/launchctl" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
label="${2##*/}"
case "$label" in
  *.control-1) pid="$FAKE_CONTROL_PID" ;;
  *.gateway-1) pid="$FAKE_GATEWAY_1_PID" ;;
  *.gateway-2) pid="$FAKE_GATEWAY_2_PID" ;;
  *.gateway-3) pid="$FAKE_GATEWAY_3_PID" ;;
  *) exit 1 ;;
esac
printf '    pid = %s\n' "$pid"
SH

cat > "$FAKE_BIN/lsof" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
pid=
for arg in "$@"; do
  case "$arg" in
    -p) read_pid=1 ;;
    -tiTCP:*) port="${arg#-tiTCP:}" ;;
    *) if [ "${read_pid:-0}" = 1 ]; then pid="$arg"; read_pid=0; fi ;;
  esac
done
if [ -n "${port:-}" ]; then
  case "$port" in
    3200) printf '%s\n' "$FAKE_CONTROL_PID" ;;
    3101) printf '%s\n' "$FAKE_GATEWAY_1_PID" ;;
    3102) printf '%s\n' "$FAKE_GATEWAY_2_PID" ;;
    3103) printf '%s\n' "$FAKE_GATEWAY_3_PID" ;;
    *) exit 1 ;;
  esac
  exit 0
fi
if [ "$pid" = "$FAKE_CONTROL_PID" ] || [ "$pid" = "$FAKE_GATEWAY_1_PID" ] \
  || [ "$pid" = "$FAKE_GATEWAY_2_PID" ] || [ "$pid" = "$FAKE_GATEWAY_3_PID" ]; then
  printf 'n%s\n' "$FAKE_RELEASE"
else
  printf 'n%s/backend\n' "$FAKE_RELEASE"
fi
SH

cat > "$FAKE_BIN/ps" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
if [ "${1:-}" = -p ]; then
  printf 'node backend/dist/server.js\n'
  exit 0
fi
count=0
if [ -f "$FAKE_PS_COUNT" ]; then count="$(cat "$FAKE_PS_COUNT")"; fi
count=$((count + 1))
printf '%s\n' "$count" > "$FAKE_PS_COUNT"
worker_pid=51001
if [ "${FAKE_DRIFT:-0}" = 1 ] && [ "$count" -gt 4 ]; then worker_pid=51999; fi
cat <<EOF
$FAKE_CONTROL_PID 1 node backend/dist/server.js
52001 $FAKE_CONTROL_PID node backend/dist/db-service.js
$worker_pid $FAKE_CONTROL_PID node backend/dist/worker.js
51002 $FAKE_CONTROL_PID node backend/dist/worker.js
51003 $FAKE_CONTROL_PID node backend/dist/worker.js
51004 $FAKE_CONTROL_PID node backend/dist/worker.js
51005 $FAKE_CONTROL_PID node backend/dist/worker.js
51006 $FAKE_CONTROL_PID node backend/dist/worker.js
$FAKE_GATEWAY_1_PID 1 node backend/dist/server.js
52002 $FAKE_GATEWAY_1_PID node backend/dist/db-service.js
$FAKE_GATEWAY_2_PID 1 node backend/dist/server.js
52003 $FAKE_GATEWAY_2_PID node backend/dist/db-service.js
$FAKE_GATEWAY_3_PID 1 node backend/dist/server.js
52004 $FAKE_GATEWAY_3_PID node backend/dist/db-service.js
EOF
SH

cat > "$FAKE_BIN/curl" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
headers=
body_file=
url=
while [ "$#" -gt 0 ]; do
  case "$1" in
    -D) headers="$2"; shift 2 ;;
    -o) body_file="$2"; shift 2 ;;
    --max-time) shift 2 ;;
    -*) shift ;;
    *) url="$1"; shift ;;
  esac
done
case "$url" in
  http://127.0.0.1:*/__aisys__/api/health)
    payload='{"status":"ok","service":"juhe-ai-db-service"}'
    ;;
  http://127.0.0.1:3200/__aisys__/health|http://127.0.0.1:3000/__aisys__/health|http://127.0.0.1:3099/__aisys__/health)
    if [ "$url" = http://127.0.0.1:3099/__aisys__/health ] && [ "${FAKE_PUBLIC_HEALTH_FAIL:-0}" = 1 ]; then
      exit 22
    fi
    workers='[{"role":"usage-worker","pid":51001,"ready":true},{"role":"usage-worker","pid":51002,"ready":true},{"role":"log-worker","pid":51003,"ready":true},{"role":"log-worker","pid":51004,"ready":true},{"role":"stats-worker","pid":51005,"ready":true},{"role":"ops-worker","pid":51006,"ready":true}]'
    if [ "${FAKE_BAD_WORKER:-0}" = 1 ]; then
      workers='[{"role":"usage-worker","pid":51001,"ready":true},{"role":"usage-worker","pid":51002,"ready":true},{"role":"log-worker","pid":51003,"ready":true},{"role":"stats-worker","pid":51005,"ready":true},{"role":"ops-worker","pid":51006,"ready":true}]'
    fi
    payload="{\"status\":\"ok\",\"service\":\"juhe-ai\",\"runtimeMode\":\"performance\",\"nodeRole\":\"control\",\"instanceId\":\"control-1\",\"workerProcesses\":$workers,\"workerTopologyReady\":true}"
    ;;
  http://127.0.0.1:3101/__aisys__/health)
    payload='{"status":"ok","service":"juhe-ai","runtimeMode":"performance","nodeRole":"gateway","instanceId":"gateway-1","workerProcesses":[],"workerTopologyReady":true}'
    ;;
  http://127.0.0.1:3102/__aisys__/health)
    payload='{"status":"ok","service":"juhe-ai","runtimeMode":"performance","nodeRole":"gateway","instanceId":"gateway-2","workerProcesses":[],"workerTopologyReady":true}'
    ;;
  http://127.0.0.1:3103/__aisys__/health)
    payload='{"status":"ok","service":"juhe-ai","runtimeMode":"performance","nodeRole":"gateway","instanceId":"gateway-3","workerProcesses":[],"workerTopologyReady":true}'
    ;;
  *) echo "unexpected curl URL: $url" >&2; exit 22 ;;
esac
if [ -n "$headers" ]; then
  printf 'HTTP/1.1 200 OK\r\nX-Juhe-Topology-Slot: %s\r\nX-Juhe-Active-Upstream: candidate\r\n\r\n' "$FAKE_LABEL" > "$headers"
fi
if [ -n "$body_file" ]; then printf '%s' "$payload" > "$body_file"; else printf '%s' "$payload"; fi
SH

cat > "$FAKE_BIN/nginx" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
if [ "${1:-}" = -t ] && [ "${FAKE_NGINX_FAIL_FIRST_TEST:-0}" = 1 ]; then
  count=0
  if [ -f "$FAKE_NGINX_COUNT" ]; then count="$(cat "$FAKE_NGINX_COUNT")"; fi
  count=$((count + 1))
  printf '%s\n' "$count" > "$FAKE_NGINX_COUNT"
  [ "$count" -gt 1 ] || exit 1
fi
exit 0
SH

cat > "$FAKE_BIN/sleep" <<'SH'
#!/usr/bin/env bash
exit 0
SH

chmod +x "$FAKE_BIN"/*
export FAKE_RELEASE="$(CDPATH= cd -- "$RELEASE" && pwd -P)"
export FAKE_LABEL=com.example.juhe-ai.performance
export FAKE_PS_COUNT="$ROOT/ps-count"
export FAKE_NGINX_COUNT="$ROOT/nginx-count"
export FAKE_CONTROL_PID="$CONTROL_PID"
export FAKE_GATEWAY_1_PID="$GATEWAY_1_PID"
export FAKE_GATEWAY_2_PID="$GATEWAY_2_PID"
export FAKE_GATEWAY_3_PID="$GATEWAY_3_PID"

run_verifier() {
  (
    PATH="$FAKE_BIN:$PATH"
    export PATH
    hash -r
    ps() { "$FAKE_BIN/ps" "$@"; }
    sleep() { :; }
    set -- --apply --release "$RELEASE" --scope user --label-prefix "$FAKE_LABEL" \
      --control-port 3200 --gateway-base-port 3101 --gateway-count 3 \
      --usage-workers 2 --log-workers 2 --ingress-port 3000 --samples 3
    . "$VERIFIER"
  )
}

: > "$FAKE_PS_COUNT"
run_verifier | grep -F 'PERFORMANCE_TOPOLOGY_OK' >/dev/null

: > "$FAKE_PS_COUNT"
if FAKE_BAD_WORKER=1 run_verifier >/dev/null 2>&1; then
  echo 'verifier accepted a missing log worker' >&2
  exit 10
fi

: > "$FAKE_PS_COUNT"
if FAKE_DRIFT=1 run_verifier >/dev/null 2>&1; then
  echo 'verifier accepted a changed worker PID' >&2
  exit 11
fi

export FAKE_SWITCH_VERIFIER_LOG="$ROOT/switch-verifier.log"
: > "$FAKE_SWITCH_VERIFIER_LOG"
cat > "$RELEASE/docs/deploy/macos/operations/verify-performance-topology.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$FAKE_SWITCH_VERIFIER_LOG"
SH

SWITCH="$OPERATIONS_ROOT/switch-performance-slot.sh"
run_switch() {
  case_root="$1"
  mkdir -p "$case_root"
  printf 'server 127.0.0.1:3001;\n' > "$case_root/active-app.conf"
  printf 'server 127.0.0.1:3002;\n' > "$case_root/active-db.conf"
  printf 'add_header X-Juhe-Active-Upstream "main" always;\n' > "$case_root/active-label.conf"
  : > "$case_root/nginx.conf"
  : > "$FAKE_PS_COUNT"
  : > "$FAKE_NGINX_COUNT"
  (
    PATH="$FAKE_BIN:$PATH"
    export PATH
    hash -r
    ps() { "$FAKE_BIN/ps" "$@"; }
    sleep() { :; }
    export FAKE_NGINX_COUNT
    export FAKE_NGINX_FAIL_FIRST_TEST="${FAKE_NGINX_FAIL_FIRST_TEST:-0}"
    export FAKE_PUBLIC_HEALTH_FAIL="${FAKE_PUBLIC_HEALTH_FAIL:-0}"
    /bin/bash "$SWITCH" --apply --release "$RELEASE" --scope user \
      --label-prefix "$FAKE_LABEL" --control-port 3200 --gateway-base-port 3101 --gateway-count 3 \
      --usage-workers 2 --log-workers 2 --candidate-ingress-port 3000 \
      --nginx-bin "$FAKE_BIN/nginx" --nginx-config "$case_root/nginx.conf" \
      --active-app-file "$case_root/active-app.conf" --active-db-file "$case_root/active-db.conf" \
      --active-label-file "$case_root/active-label.conf" --public-health-base-url http://127.0.0.1:3099 \
      --candidate-label candidate --expected-current-label main --samples 2
  )
}

PRECOMMIT_ROOT="$ROOT/switch-precommit"
if FAKE_NGINX_FAIL_FIRST_TEST=1 run_switch "$PRECOMMIT_ROOT" >"$ROOT/precommit.out" 2>&1; then
  echo 'forward switch accepted a failed nginx configuration test' >&2
  exit 12
fi
grep -Fx 'server 127.0.0.1:3001;' "$PRECOMMIT_ROOT/active-app.conf" >/dev/null
grep -Fx 'server 127.0.0.1:3002;' "$PRECOMMIT_ROOT/active-db.conf" >/dev/null
grep -F '"main"' "$PRECOMMIT_ROOT/active-label.conf" >/dev/null

POSTCOMMIT_ROOT="$ROOT/switch-postcommit"
if FAKE_PUBLIC_HEALTH_FAIL=1 run_switch "$POSTCOMMIT_ROOT" >"$ROOT/postcommit.out" 2>&1; then
  echo 'forward switch accepted failed post-commit public health' >&2
  exit 13
fi
grep -F 'FORWARD_FIX_REQUIRED label=candidate ingress=3000' "$ROOT/postcommit.out" >/dev/null \
  || { cat "$ROOT/postcommit.out" >&2; echo 'missing forward-fix marker after committed switch failure' >&2; exit 14; }
grep -Fx 'server 127.0.0.1:3000;' "$POSTCOMMIT_ROOT/active-app.conf" >/dev/null
grep -Fx 'server 127.0.0.1:3000;' "$POSTCOMMIT_ROOT/active-db.conf" >/dev/null
grep -F '"candidate"' "$POSTCOMMIT_ROOT/active-label.conf" >/dev/null

SUCCESS_ROOT="$ROOT/switch-success"
run_switch "$SUCCESS_ROOT" | grep -F 'PERFORMANCE_SLOT_SWITCHED label=candidate ingress=3000 samples=2 rollback=disabled' >/dev/null
grep -Fx 'server 127.0.0.1:3000;' "$SUCCESS_ROOT/active-app.conf" >/dev/null
grep -Fx 'server 127.0.0.1:3000;' "$SUCCESS_ROOT/active-db.conf" >/dev/null
grep -F '"candidate"' "$SUCCESS_ROOT/active-label.conf" >/dev/null
[ "$(wc -l < "$FAKE_SWITCH_VERIFIER_LOG" | tr -d ' ')" -eq 3 ]
grep -F -- '--apply' "$FAKE_SWITCH_VERIFIER_LOG" >/dev/null
grep -F -- '--release' "$FAKE_SWITCH_VERIFIER_LOG" >/dev/null

printf 'performance topology and forward switch executable smoke passed\n'
