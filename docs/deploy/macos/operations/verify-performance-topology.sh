#!/usr/bin/env bash
set -euo pipefail

MODE=dry-run
SCOPE=user
RELEASE=
LABEL_PREFIX=com.example.juhe-ai.performance
CONTROL_PORT=3200
GATEWAY_BASE_PORT=3101
GATEWAY_COUNT=3
USAGE_WORKERS=2
LOG_WORKERS=2
INGRESS_PORT=3000
SAMPLES=3
SKIP_INGRESS=0

usage() {
  cat <<'EOF'
Usage: verify-performance-topology.sh [--dry-run|--apply] --release ABSOLUTE_PATH [options]
  --scope user|system
  --label-prefix LAUNCHD_LABEL_PREFIX
  --control-port PORT
  --gateway-base-port PORT
  --gateway-count 1..32
  --usage-workers 1..32
  --log-workers 1..32
  --ingress-port PORT
  --samples 1..60
  --skip-ingress
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --dry-run) MODE=dry-run; shift ;;
    --apply) MODE=apply; shift ;;
    --scope) SCOPE="${2:-}"; shift 2 ;;
    --release) RELEASE="${2:-}"; shift 2 ;;
    --label-prefix) LABEL_PREFIX="${2:-}"; shift 2 ;;
    --control-port) CONTROL_PORT="${2:-}"; shift 2 ;;
    --gateway-base-port) GATEWAY_BASE_PORT="${2:-}"; shift 2 ;;
    --gateway-count) GATEWAY_COUNT="${2:-}"; shift 2 ;;
    --usage-workers) USAGE_WORKERS="${2:-}"; shift 2 ;;
    --log-workers) LOG_WORKERS="${2:-}"; shift 2 ;;
    --ingress-port) INGRESS_PORT="${2:-}"; shift 2 ;;
    --samples) SAMPLES="${2:-}"; shift 2 ;;
    --skip-ingress) SKIP_INGRESS=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown option: $1" >&2; usage >&2; exit 2 ;;
  esac
done

case "$SCOPE" in user|system) ;; *) echo '--scope must be user or system' >&2; exit 2 ;; esac
case "$RELEASE" in /*) ;; *) echo '--release must be absolute' >&2; exit 2 ;; esac
case "$RELEASE$LABEL_PREFIX" in
  *'$'*|*'`'*|*'"'*|*'\'*|*'|'*|*'&'*|*';'*|*$'\n'*|*$'\r'*)
    echo 'release or label contains unsafe shell characters' >&2
    exit 2
    ;;
esac
printf '%s' "$LABEL_PREFIX" | grep -Eq '^[A-Za-z0-9][A-Za-z0-9.-]{0,100}$' \
  || { echo 'invalid launchd label prefix' >&2; exit 2; }

assert_number() {
  local value="$1"
  local min="$2"
  local max="$3"
  local name="$4"
  printf '%s' "$value" | grep -Eq '^[0-9]+$' || { echo "$name must be numeric" >&2; exit 2; }
  [ "$value" -ge "$min" ] && [ "$value" -le "$max" ] \
    || { echo "$name must be between $min and $max" >&2; exit 2; }
}

assert_number "$CONTROL_PORT" 1 65535 control-port
assert_number "$GATEWAY_BASE_PORT" 1 65535 gateway-base-port
assert_number "$GATEWAY_COUNT" 1 32 gateway-count
assert_number "$USAGE_WORKERS" 1 32 usage-workers
assert_number "$LOG_WORKERS" 1 32 log-workers
assert_number "$INGRESS_PORT" 1 65535 ingress-port
assert_number "$SAMPLES" 1 60 samples
LAST_GATEWAY_PORT=$((GATEWAY_BASE_PORT + GATEWAY_COUNT - 1))
[ "$LAST_GATEWAY_PORT" -le 65535 ] || { echo 'gateway port range exceeds 65535' >&2; exit 2; }
[ "$CONTROL_PORT" -lt "$GATEWAY_BASE_PORT" ] || [ "$CONTROL_PORT" -gt "$LAST_GATEWAY_PORT" ] \
  || { echo 'control port overlaps gateway ports' >&2; exit 2; }
[ "$INGRESS_PORT" -ne "$CONTROL_PORT" ] || { echo 'ingress port overlaps control port' >&2; exit 2; }
[ "$INGRESS_PORT" -lt "$GATEWAY_BASE_PORT" ] || [ "$INGRESS_PORT" -gt "$LAST_GATEWAY_PORT" ] \
  || { echo 'ingress port overlaps gateway ports' >&2; exit 2; }

printf 'mode=%s release=%s scope=%s control=%s gateways=%s-%s usage=%s log=%s ingress=%s samples=%s skip_ingress=%s\n' \
  "$MODE" "$RELEASE" "$SCOPE" "$CONTROL_PORT" "$GATEWAY_BASE_PORT" "$LAST_GATEWAY_PORT" \
  "$USAGE_WORKERS" "$LOG_WORKERS" "$INGRESS_PORT" "$SAMPLES" "$SKIP_INGRESS"
[ "$MODE" = apply ] || exit 0

for command_name in launchctl lsof ps curl node awk sed grep sort; do
  command -v "$command_name" >/dev/null || { echo "missing command: $command_name" >&2; exit 1; }
done
[ -d "$RELEASE" ] || { echo "release does not exist: $RELEASE" >&2; exit 1; }
RELEASE="$(CDPATH= cd -- "$RELEASE" && pwd -P)"
[ -f "$RELEASE/backend/dist/server.js" ] || { echo 'release server build is missing' >&2; exit 1; }

if [ "$SCOPE" = user ]; then DOMAIN="gui/$(id -u)"; else DOMAIN=system; fi

service_names() {
  printf '%s\n' control-1
  index=1
  while [ "$index" -le "$GATEWAY_COUNT" ]; do
    printf 'gateway-%s\n' "$index"
    index=$((index + 1))
  done
}

service_role() { case "$1" in control-1) printf control ;; *) printf gateway ;; esac; }
service_instance() { printf '%s' "$1"; }
service_port() {
  local index
  case "$1" in
    control-1) printf '%s' "$CONTROL_PORT" ;;
    gateway-*) index="${1#gateway-}"; printf '%s' "$((GATEWAY_BASE_PORT + index - 1))" ;;
  esac
}
service_label() { printf '%s.%s' "$LABEL_PREFIX" "$1"; }

launchd_pid() {
  launchctl print "$DOMAIN/$(service_label "$1")" 2>/dev/null \
    | awk -F'= ' '/^[[:space:]]*pid = [0-9]+/ {print $2; exit}'
}

pid_cwd() {
  lsof -a -p "$1" -d cwd -Fn 2>/dev/null | sed -n 's/^n//p' | head -n 1 || true
}

process_tree() {
  ps -axo pid=,ppid=,command= | awk -v root="$1" '
    { pid[NR]=$1; ppid[NR]=$2; line[NR]=$0 }
    END {
      wanted[root]=1; changed=1
      while (changed) {
        changed=0
        for (i=1; i<=NR; i++) if (wanted[ppid[i]] && !wanted[pid[i]]) { wanted[pid[i]]=1; changed=1 }
      }
      for (i=1; i<=NR; i++) if (wanted[pid[i]]) print line[i]
    }
  '
}

assert_health_payload() {
  local body="$1"
  local name="$2"
  local role="$3"
  local instance
  instance="$(service_instance "$name")"
  node -e '
    const health = JSON.parse(process.argv[1])
    const [instance, role, usageText, logText] = process.argv.slice(2)
    if (health.status !== "ok" || health.service !== "juhe-ai") process.exit(10)
    if (health.runtimeMode !== "performance" || health.nodeRole !== role || health.instanceId !== instance) process.exit(11)
    if (health.workerTopologyReady !== true || !Array.isArray(health.workerProcesses)) process.exit(12)
    const expected = role === "control"
      ? { "usage-worker": Number(usageText), "log-worker": Number(logText), "stats-worker": 1, "ops-worker": 1 }
      : {}
    const actual = {}
    for (const worker of health.workerProcesses) {
      if (!worker || worker.ready !== true || !Number.isInteger(worker.pid) || worker.pid <= 1) process.exit(13)
      actual[worker.role] = (actual[worker.role] || 0) + 1
    }
    if (Object.keys(actual).length !== Object.keys(expected).length) process.exit(14)
    for (const [workerRole, count] of Object.entries(expected)) {
      if (actual[workerRole] !== count) process.exit(14)
    }
    process.stdout.write(health.workerProcesses.map((worker) => worker.pid).sort((a, b) => a - b).join(","))
  ' "$body" "$instance" "$role" "$USAGE_WORKERS" "$LOG_WORKERS"
}

assert_api_health_payload() {
  node -e '
    const health = JSON.parse(process.argv[1])
    if (health.status !== "ok" || health.service !== "juhe-ai-db-service") process.exit(1)
  ' "$1"
}

verify_service() {
  local name="$1"
  local role port pid root_cwd root_command listeners tree server_count db_count worker_count expected_workers member cwd health api_health health_worker_pids tree_worker_pids
  role="$(service_role "$name")"
  port="$(service_port "$name")"
  pid="$(launchd_pid "$name")"
  case "$pid" in ''|*[!0-9]*) echo "$name launchd PID is missing" >&2; return 1 ;; esac
  [ "$pid" -gt 1 ] && kill -0 "$pid" 2>/dev/null || { echo "$name PID is not alive: $pid" >&2; return 1; }
  root_cwd="$(pid_cwd "$pid")"
  [ "$root_cwd" = "$RELEASE" ] \
    || { echo "$name cwd does not match release: actual=$root_cwd expected=$RELEASE" >&2; return 1; }
  root_command="$(ps -p "$pid" -o command= 2>/dev/null || true)"
  case "$root_command" in
    *backend/dist/server.js*) ;;
    *) echo "$name PID is not server.js: $root_command" >&2; return 1 ;;
  esac
  listeners="$(lsof -tiTCP:"$port" -sTCP:LISTEN 2>/dev/null | sort -u || true)"
  [ "$listeners" = "$pid" ] || { echo "$name does not exclusively own port $port" >&2; return 1; }

  tree="$(process_tree "$pid")"
  server_count="$(printf '%s\n' "$tree" | grep -Ec 'backend/dist/server\.js' || true)"
  db_count="$(printf '%s\n' "$tree" | grep -Ec 'backend/dist/db-service\.js' || true)"
  worker_count="$(printf '%s\n' "$tree" | grep -Ec 'backend/dist/worker\.js' || true)"
  expected_workers=0
  [ "$role" != control ] || expected_workers=$((USAGE_WORKERS + LOG_WORKERS + 2))
  [ "$server_count" -eq 1 ] && [ "$db_count" -eq 1 ] && [ "$worker_count" -eq "$expected_workers" ] \
    || { echo "$name topology mismatch: server=$server_count db=$db_count worker=$worker_count" >&2; return 1; }

  while IFS= read -r member; do
    [ -n "$member" ] || continue
    cwd="$(pid_cwd "$member")"
    [ "$cwd" = "$RELEASE" ] || [ "$cwd" = "$RELEASE/backend" ] \
      || { echo "$name process $member cwd escaped release" >&2; return 1; }
  done <<EOF
$(printf '%s\n' "$tree" | awk '/backend\/dist\/(server|db-service|worker)\.js/ {print $1}')
EOF

  health="$(curl -fsS --max-time 3 "http://127.0.0.1:$port/__aisys__/health")"
  health_worker_pids="$(assert_health_payload "$health" "$name" "$role")"
  tree_worker_pids="$(printf '%s\n' "$tree" | awk '/backend\/dist\/worker\.js/ {print $1}' | sort -n | tr '\n' ',' | sed 's/,$//')"
  [ "$health_worker_pids" = "$tree_worker_pids" ] \
    || { echo "$name health worker PIDs do not match process tree" >&2; return 1; }
  api_health="$(curl -fsS --max-time 3 "http://127.0.0.1:$port/__aisys__/api/health")"
  assert_api_health_payload "$api_health"
  printf '%s:%s:%s\n' "$name" "$pid" "$(printf '%s\n' "$tree" | awk '/backend\/dist\/(server|db-service|worker)\.js/ {print $1}' | sort -n | tr '\n' ',')"
}

verify_ingress() {
  local headers body api_health
  headers="$(mktemp "${TMPDIR:-/tmp}/juhe-ai-topology-headers.XXXXXX")"
  body="$(mktemp "${TMPDIR:-/tmp}/juhe-ai-topology-body.XXXXXX")"
  if ! curl -fsS --max-time 3 -D "$headers" -o "$body" "http://127.0.0.1:$INGRESS_PORT/__aisys__/health"; then
    rm -f -- "$headers" "$body"
    return 1
  fi
  if ! grep -Eiq "^X-Juhe-Topology-Slot:[[:space:]]*$LABEL_PREFIX[[:space:]]*$" "$headers"; then
    echo 'ingress topology slot header mismatch' >&2
    rm -f -- "$headers" "$body"
    return 1
  fi
  if ! assert_health_payload "$(cat "$body")" control-1 control >/dev/null; then
    rm -f -- "$headers" "$body"
    return 1
  fi
  if ! api_health="$(curl -fsS --max-time 3 "http://127.0.0.1:$INGRESS_PORT/__aisys__/api/health")" \
    || ! assert_api_health_payload "$api_health"; then
    rm -f -- "$headers" "$body"
    return 1
  fi
  rm -f -- "$headers" "$body"
}

stable_signature=
sample=1
while [ "$sample" -le "$SAMPLES" ]; do
  signature=
  for name in $(service_names); do
    service_signature="$(verify_service "$name")"
    signature="$signature$service_signature;"
  done
  [ "$SKIP_INGRESS" = 1 ] || verify_ingress
  if [ -z "$stable_signature" ]; then
    stable_signature="$signature"
  elif [ "$signature" != "$stable_signature" ]; then
    echo 'performance topology PID signature changed during stable samples' >&2
    exit 1
  fi
  [ "$sample" -eq "$SAMPLES" ] || sleep 1
  sample=$((sample + 1))
done

printf 'PERFORMANCE_TOPOLOGY_OK control=1 gateways=%s db_services=%s workers=%s samples=%s ingress_checked=%s\n' \
  "$GATEWAY_COUNT" "$((GATEWAY_COUNT + 1))" "$((USAGE_WORKERS + LOG_WORKERS + 2))" "$SAMPLES" "$((1 - SKIP_INGRESS))"
