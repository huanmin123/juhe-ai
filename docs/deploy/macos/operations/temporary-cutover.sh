#!/usr/bin/env bash
set -euo pipefail

ACTION=''
MODE=dry-run
SWITCH_SCRIPT=''
MAIN_RELEASE=''
MAIN_PID=''
MAIN_PORT=''
TEMP_RELEASE=''
TEMP_PID=''
TEMP_PORT=''
HEALTH_PATH='/__aisys__/health'
API_HEALTH_PATH='/__aisys__/api/health'
INGRESS_HEALTH_URL=''
ROUTE_HEADER_NAME='X-Juhe-Active-Upstream'
MAIN_HEADER_VALUE=performance-main
TEMP_HEADER_VALUE=performance-temporary
SWITCH_ATTEMPTED=0
ROLLBACK_OK=0
TMP_HEADERS=''
MODEL_READINESS_KEY_FILE=''
MODEL_READINESS_RUNNER=''
MODEL_READINESS_INGRESS_BASE_URL=''
SKIP_MODEL_READINESS=0
MAIN_MODEL_BASE_URL=''
TEMP_MODEL_BASE_URL=''

usage() {
  cat <<'EOF'
Usage: temporary-cutover.sh --action <takeover|switchback> --switch-script <path>
  --main-release <path> --main-pid <pid> --main-port <port>
  --temporary-release <path> --temporary-pid <pid> --temporary-port <port>
  [--health-path <path>] [--api-health-path <path>]
  [--ingress-health-url <url>] [--dry-run|--apply]
  [--model-readiness-key-file <absolute-mode-0600-file>]
  [--model-readiness-runner <absolute-new-release-runner-path>]
  [--model-readiness-ingress-base-url <loopback-http-base-url>]
  [--main-model-base-url <loopback-http-base-url>]
  [--temporary-model-base-url <loopback-http-base-url>]
  [--skip-model-readiness-for-non-business-test]

The switch adapter must accept exactly one argument: main or temporary.
Dry-run prints the plan and does not inspect processes or change routing.
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --action) ACTION="${2:?missing action}"; shift 2 ;;
    --dry-run) MODE=dry-run; shift ;;
    --apply) MODE=apply; shift ;;
    --switch-script) SWITCH_SCRIPT="${2:?missing switch script}"; shift 2 ;;
    --main-release) MAIN_RELEASE="${2:?missing main release}"; shift 2 ;;
    --main-pid) MAIN_PID="${2:?missing main pid}"; shift 2 ;;
    --main-port) MAIN_PORT="${2:?missing main port}"; shift 2 ;;
    --temporary-release) TEMP_RELEASE="${2:?missing temporary release}"; shift 2 ;;
    --temporary-pid) TEMP_PID="${2:?missing temporary pid}"; shift 2 ;;
    --temporary-port) TEMP_PORT="${2:?missing temporary port}"; shift 2 ;;
    --health-path) HEALTH_PATH="${2:?missing health path}"; shift 2 ;;
    --api-health-path) API_HEALTH_PATH="${2:?missing API health path}"; shift 2 ;;
    --ingress-health-url) INGRESS_HEALTH_URL="${2:?missing ingress URL}"; shift 2 ;;
    --route-header-name) ROUTE_HEADER_NAME="${2:?missing header name}"; shift 2 ;;
    --main-header-value) MAIN_HEADER_VALUE="${2:?missing main header value}"; shift 2 ;;
    --temporary-header-value) TEMP_HEADER_VALUE="${2:?missing temporary header value}"; shift 2 ;;
    --model-readiness-key-file) MODEL_READINESS_KEY_FILE="${2:?missing model readiness key file}"; shift 2 ;;
    --model-readiness-runner) MODEL_READINESS_RUNNER="${2:?missing model readiness runner}"; shift 2 ;;
    --model-readiness-ingress-base-url) MODEL_READINESS_INGRESS_BASE_URL="${2:?missing model readiness ingress base URL}"; shift 2 ;;
    --main-model-base-url) MAIN_MODEL_BASE_URL="${2:?missing main model base URL}"; shift 2 ;;
    --temporary-model-base-url) TEMP_MODEL_BASE_URL="${2:?missing temporary model base URL}"; shift 2 ;;
    --skip-model-readiness-for-non-business-test) SKIP_MODEL_READINESS=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown option: $1" >&2; usage >&2; exit 2 ;;
  esac
done

case "$ACTION" in takeover|switchback) ;; *) echo 'action must be takeover or switchback' >&2; exit 2;; esac
[ -n "$SWITCH_SCRIPT" ] || { echo 'missing --switch-script' >&2; exit 2; }
[ -n "$MAIN_RELEASE" ] || { echo 'missing --main-release' >&2; exit 2; }
[ -n "$MAIN_PID" ] || { echo 'missing --main-pid' >&2; exit 2; }
[ -n "$MAIN_PORT" ] || { echo 'missing --main-port' >&2; exit 2; }
[ -n "$TEMP_RELEASE" ] || { echo 'missing --temporary-release' >&2; exit 2; }
[ -n "$TEMP_PID" ] || { echo 'missing --temporary-pid' >&2; exit 2; }
[ -n "$TEMP_PORT" ] || { echo 'missing --temporary-port' >&2; exit 2; }
for pid in "$MAIN_PID" "$TEMP_PID"; do case "$pid" in ''|*[!0-9]*) echo 'PID must be numeric' >&2; exit 2;; esac; [ "$pid" -gt 1 ]; done
for port in "$MAIN_PORT" "$TEMP_PORT"; do case "$port" in ''|*[!0-9]*) echo 'port must be numeric' >&2; exit 2;; esac; [ "$port" -ge 1 ] && [ "$port" -le 65535 ]; done
[ "$MAIN_PID" != "$TEMP_PID" ] || { echo 'main and temporary PIDs must differ' >&2; exit 2; }
[ "$MAIN_PORT" != "$TEMP_PORT" ] || { echo 'main and temporary ports must differ' >&2; exit 2; }
[ "$MAIN_RELEASE" != "$TEMP_RELEASE" ] || { echo 'main and temporary releases must differ' >&2; exit 2; }
case "$MAIN_RELEASE" in /*) ;; *) echo 'main release must be absolute' >&2; exit 2;; esac
case "$TEMP_RELEASE" in /*) ;; *) echo 'temporary release must be absolute' >&2; exit 2;; esac
case "$SWITCH_SCRIPT" in /*) ;; *) echo 'switch adapter path must be absolute' >&2; exit 2;; esac
case "$HEALTH_PATH" in /*) ;; *) echo 'health path must start with /' >&2; exit 2;; esac
case "$API_HEALTH_PATH" in /*) ;; *) echo 'API health path must start with /' >&2; exit 2;; esac
case "$ROUTE_HEADER_NAME" in ''|*[!A-Za-z0-9_-]*) echo 'route header name must be an HTTP token' >&2; exit 2;; esac
case "$MAIN_HEADER_VALUE" in ''|*[!A-Za-z0-9._~-]*) echo 'main header value contains unsupported characters' >&2; exit 2;; esac
case "$TEMP_HEADER_VALUE" in ''|*[!A-Za-z0-9._~-]*) echo 'temporary header value contains unsupported characters' >&2; exit 2;; esac
if [ -n "$INGRESS_HEALTH_URL" ]; then
  case "$INGRESS_HEALTH_URL" in http://*|https://*) ;; *) echo 'ingress health URL must use http or https' >&2; exit 2;; esac
fi
if [ -n "$MODEL_READINESS_KEY_FILE" ]; then
  case "$MODEL_READINESS_KEY_FILE" in /*) ;; *) echo 'model readiness key file must be absolute' >&2; exit 2;; esac
fi
if [ -n "$MODEL_READINESS_RUNNER" ]; then
  case "$MODEL_READINESS_RUNNER" in /*) ;; *) echo 'model readiness runner must be absolute' >&2; exit 2;; esac
fi
[ -z "$MODEL_READINESS_KEY_FILE" ] || [ "$SKIP_MODEL_READINESS" = 0 ] || {
  echo 'model readiness key file and skip flag are mutually exclusive' >&2
  exit 2
}
[ -n "$MODEL_READINESS_KEY_FILE" ] || [ "$SKIP_MODEL_READINESS" = 1 ] || {
  echo 'provide --model-readiness-key-file, or explicitly skip only for a non-business test' >&2
  exit 2
}
if [ "$SKIP_MODEL_READINESS" = 0 ]; then
  [ -n "$MODEL_READINESS_INGRESS_BASE_URL" ] || { echo 'missing --model-readiness-ingress-base-url' >&2; exit 2; }
  [ -n "$MODEL_READINESS_RUNNER" ] || { echo 'missing --model-readiness-runner' >&2; exit 2; }
fi
if [ -z "$MAIN_MODEL_BASE_URL" ]; then MAIN_MODEL_BASE_URL="http://127.0.0.1:$MAIN_PORT"; fi
if [ -z "$TEMP_MODEL_BASE_URL" ]; then TEMP_MODEL_BASE_URL="http://127.0.0.1:$TEMP_PORT"; fi
assert_loopback_model_base_url() {
  local name="$1" value="$2" port
  printf '%s' "$value" | grep -Eq '^http://127\.0\.0\.1:[0-9]+$' || {
    echo "$name must be an http://127.0.0.1:PORT base URL" >&2
    exit 2
  }
  port="${value##*:}"
  [ "$port" -ge 1 ] && [ "$port" -le 65535 ] || { echo "$name port is out of range" >&2; exit 2; }
}
if [ "$SKIP_MODEL_READINESS" = 0 ]; then
  assert_loopback_model_base_url main-model-base-url "$MAIN_MODEL_BASE_URL"
  assert_loopback_model_base_url temporary-model-base-url "$TEMP_MODEL_BASE_URL"
  assert_loopback_model_base_url model-readiness-ingress-base-url "$MODEL_READINESS_INGRESS_BASE_URL"
fi

if [ "$ACTION" = takeover ]; then TARGET=temporary; rollback_target=main; else TARGET=main; rollback_target=temporary; fi
printf 'mode=%s action=%s target=%s rollback_target=%s switch_adapter=%s\n' "$MODE" "$ACTION" "$TARGET" "$rollback_target" "$SWITCH_SCRIPT"
printf 'plan: verify both instances by PID/cwd/port/health -> switch -> prove ingress -> rollback on failure\n'
[ "$MODE" = apply ] || exit 0

assert_model_readiness_key_file() {
  local key_mode
  key_mode="$(stat -f '%Lp' "$1" 2>/dev/null || true)"
  case "$key_mode" in [0-7][0-7][0-7]) ;; *) key_mode="$(stat -c '%a' "$1" 2>/dev/null || true)" ;; esac
  [ "$key_mode" = 600 ] || { echo 'model readiness API key file mode must be 0600' >&2; return 1; }
}

[ -x "$SWITCH_SCRIPT" ] || { echo "switch adapter is not executable: $SWITCH_SCRIPT" >&2; exit 1; }
[ -n "$INGRESS_HEALTH_URL" ] || { echo '--ingress-health-url is required with --apply' >&2; exit 1; }
command -v lsof >/dev/null
command -v curl >/dev/null
if [ "$SKIP_MODEL_READINESS" = 0 ]; then
  [ -f "$MODEL_READINESS_KEY_FILE" ] || { echo 'model readiness API key file does not exist' >&2; exit 1; }
  assert_model_readiness_key_file "$MODEL_READINESS_KEY_FILE"
  [ -f "$MODEL_READINESS_RUNNER" ] || { echo 'model readiness runner does not exist' >&2; exit 1; }
  command -v node >/dev/null
else
  echo 'warning: model catalog business readiness is skipped for this non-business test' >&2
fi

pid_cwd() { lsof -a -p "$1" -d cwd -Fn 2>/dev/null | sed -n 's/^n//p' | head -n 1 || true; }
assert_pid_cwd_port_health() {
  local label="$1" pid="$2" release="$3" port="$4" real_release cwd listeners command
  kill -0 "$pid" 2>/dev/null || { echo "$label PID is not running: $pid" >&2; return 1; }
  real_release="$(cd "$release" && pwd -P)"
  [ "${real_release##*/}" = juhe-ai-release ] || { echo "$label release must end with juhe-ai-release" >&2; return 1; }
  cwd="$(pid_cwd "$pid")"
  [ "$cwd" = "$real_release" ] || { echo "$label PID cwd mismatch: pid=$pid cwd=$cwd release=$real_release" >&2; return 1; }
  command="$(ps -p "$pid" -o command= 2>/dev/null || true)"
  case "$command" in *backend/dist/server.js*|*start.sh*) ;; *) echo "$label PID is not the main service" >&2; return 1;; esac
  listeners="$(lsof -tiTCP:"$port" -sTCP:LISTEN 2>/dev/null | sort -u || true)"
  [ "$listeners" = "$pid" ] || { echo "$label port owner mismatch: port=$port listener=${listeners:-none} pid=$pid" >&2; return 1; }
  curl -fsS --max-time 5 "http://127.0.0.1:$port$HEALTH_PATH" >/dev/null
  curl -fsS --max-time 5 "http://127.0.0.1:$port$API_HEALTH_PATH" >/dev/null
}

verify_ingress() {
  local target="$1" expected
  if [ "$target" = main ]; then expected="$MAIN_HEADER_VALUE"; else expected="$TEMP_HEADER_VALUE"; fi
  TMP_HEADERS="$(mktemp -t juhe-ai-cutover.XXXXXX)"
  curl -fsS --max-time 8 -D "$TMP_HEADERS" -o /dev/null "$INGRESS_HEALTH_URL"
  grep -Eiq "^${ROUTE_HEADER_NAME}:[[:space:]]*${expected}[[:space:]]*$" "$TMP_HEADERS" || {
    echo "ingress route proof failed: expected $ROUTE_HEADER_NAME=$expected" >&2
    return 1
  }
  rm -f -- "$TMP_HEADERS"
  TMP_HEADERS=''
}

verify_model_catalog() {
  local base_url="$1"
  [ "$SKIP_MODEL_READINESS" = 0 ] || return 0
  node "$MODEL_READINESS_RUNNER" --base-url "$base_url" --api-key-file "$MODEL_READINESS_KEY_FILE"
}

on_exit() {
  local exit_code="$1"
  set +e
  [ -z "$TMP_HEADERS" ] || rm -f -- "$TMP_HEADERS"
  if [ "$exit_code" -ne 0 ] && [ "$SWITCH_ATTEMPTED" = 1 ]; then
    echo "cutover failed; rolling routing back to $rollback_target" >&2
    if "$SWITCH_SCRIPT" "$rollback_target"; then
      if verify_ingress "$rollback_target" \
        && verify_model_catalog "$MODEL_READINESS_INGRESS_BASE_URL"; then
        ROLLBACK_OK=1
      fi
    fi
    [ "$ROLLBACK_OK" = 1 ] || echo 'automatic routing rollback could not be proven; keep both services running and escalate' >&2
  fi
  return "$exit_code"
}
trap 'on_exit "$?"' EXIT

assert_pid_cwd_port_health main "$MAIN_PID" "$MAIN_RELEASE" "$MAIN_PORT"
assert_pid_cwd_port_health temporary "$TEMP_PID" "$TEMP_RELEASE" "$TEMP_PORT"
MAIN_REAL_RELEASE="$(cd "$MAIN_RELEASE" && pwd -P)"
TEMP_REAL_RELEASE="$(cd "$TEMP_RELEASE" && pwd -P)"
[ "$MAIN_REAL_RELEASE" != "$TEMP_REAL_RELEASE" ] || { echo 'main and temporary releases resolve to the same directory' >&2; exit 1; }
verify_model_catalog "$MAIN_MODEL_BASE_URL"
verify_model_catalog "$TEMP_MODEL_BASE_URL"
verify_ingress "$rollback_target"
verify_model_catalog "$MODEL_READINESS_INGRESS_BASE_URL"
SWITCH_ATTEMPTED=1
"$SWITCH_SCRIPT" "$TARGET"
if [ "$TARGET" = main ]; then
  assert_pid_cwd_port_health main "$MAIN_PID" "$MAIN_RELEASE" "$MAIN_PORT"
else
  assert_pid_cwd_port_health temporary "$TEMP_PID" "$TEMP_RELEASE" "$TEMP_PORT"
fi
verify_ingress "$TARGET"
verify_model_catalog "$MODEL_READINESS_INGRESS_BASE_URL"
SWITCH_ATTEMPTED=0
trap - EXIT
printf 'CUTOVER_OK target=%s; source service remains running until explicit cleanup\n' "$TARGET"
