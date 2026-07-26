#!/usr/bin/env bash
set -euo pipefail

MODE=dry-run
SCOPE=user
BASE_DIR="${HOME}/juhe-ai-lite"
SLOT=
LABEL_PREFIX=com.example.juhe-ai.performance
CONTROL_PORT=
GATEWAY_BASE_PORT=
GATEWAY_COUNT=3
INGRESS_PORT=3099
DEPLOYMENT_LOCK_LIBRARY=
DRAIN_SCRIPT="$(cd "$(dirname "$0")" && pwd)/wait-performance-slot-drain.sh"

usage() {
  cat <<'EOF'
Usage: retire-performance-slot.sh --slot main|temporary [--dry-run|--apply] [options]
  --scope user|system
  --base-dir ABSOLUTE_PATH
  --label-prefix LAUNCHD_LABEL_PREFIX
  --control-port PORT
  --gateway-base-port PORT
  --gateway-count 1..32
  --ingress-port PORT
  --drain-script ABSOLUTE_PATH
  --deployment-lock-library ABSOLUTE_DEPLOYMENT_LOCK_SH
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --dry-run) MODE=dry-run; shift ;;
    --apply) MODE=apply; shift ;;
    --scope) SCOPE="${2:-}"; shift 2 ;;
    --base-dir) BASE_DIR="${2:-}"; shift 2 ;;
    --slot) SLOT="${2:-}"; shift 2 ;;
    --label-prefix) LABEL_PREFIX="${2:-}"; shift 2 ;;
    --control-port) CONTROL_PORT="${2:-}"; shift 2 ;;
    --gateway-base-port) GATEWAY_BASE_PORT="${2:-}"; shift 2 ;;
    --gateway-count) GATEWAY_COUNT="${2:-}"; shift 2 ;;
    --ingress-port) INGRESS_PORT="${2:-}"; shift 2 ;;
    --drain-script) DRAIN_SCRIPT="${2:-}"; shift 2 ;;
    --deployment-lock-library) DEPLOYMENT_LOCK_LIBRARY="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown option: $1" >&2; usage >&2; exit 2 ;;
  esac
done

case "$SCOPE" in user|system) ;; *) echo '--scope must be user or system' >&2; exit 2 ;; esac
case "$SLOT" in main|temporary) ;; *) echo '--slot must be main or temporary' >&2; exit 2 ;; esac
case "$BASE_DIR" in /*) ;; *) echo '--base-dir must be absolute' >&2; exit 2 ;; esac
case "$DRAIN_SCRIPT" in /*) ;; *) echo '--drain-script must be absolute' >&2; exit 2 ;; esac
if [ -n "$DEPLOYMENT_LOCK_LIBRARY" ]; then
  case "$DEPLOYMENT_LOCK_LIBRARY" in /*) ;; *) echo '--deployment-lock-library must be absolute' >&2; exit 2 ;; esac
fi
case "$BASE_DIR$DRAIN_SCRIPT$DEPLOYMENT_LOCK_LIBRARY" in
  *'$'*|*'`'*|*'"'*|*'\'*|*'|'*|*'&'*|*';'*|*$'\n'*|*$'\r'*) echo 'paths contain unsafe shell characters' >&2; exit 2 ;;
esac
printf '%s' "$LABEL_PREFIX" | grep -Eq '^[A-Za-z0-9][A-Za-z0-9.-]{0,100}$' || { echo 'invalid launchd label prefix' >&2; exit 2; }

if [ -z "$CONTROL_PORT" ]; then
  if [ "$SLOT" = main ]; then CONTROL_PORT=3200; else CONTROL_PORT=3300; fi
fi
if [ -z "$GATEWAY_BASE_PORT" ]; then
  if [ "$SLOT" = main ]; then GATEWAY_BASE_PORT=3211; else GATEWAY_BASE_PORT=3311; fi
fi
assert_number() {
  value="$1" min="$2" max="$3" name="$4"
  printf '%s' "$value" | grep -Eq '^[0-9]+$' || { echo "$name must be numeric" >&2; exit 2; }
  [ "$value" -ge "$min" ] && [ "$value" -le "$max" ] || { echo "$name must be between $min and $max" >&2; exit 2; }
}
assert_number "$CONTROL_PORT" 1 65535 control-port
assert_number "$GATEWAY_BASE_PORT" 1 65535 gateway-base-port
assert_number "$GATEWAY_COUNT" 1 32 gateway-count
assert_number "$INGRESS_PORT" 1 65535 ingress-port

if [ "$SCOPE" = user ]; then
  DOMAIN="gui/$(id -u)"
  PLIST_DIR="$HOME/Library/LaunchAgents"
else
  DOMAIN=system
  PLIST_DIR=/Library/LaunchDaemons
fi
BIN_DIR="$BASE_DIR/bin/performance/$SLOT"
printf 'mode=%s scope=%s slot=%s control=%s gateways=%s-%s ingress=%s\n' \
  "$MODE" "$SCOPE" "$SLOT" "$CONTROL_PORT" "$GATEWAY_BASE_PORT" "$((GATEWAY_BASE_PORT + GATEWAY_COUNT - 1))" "$INGRESS_PORT"
[ "$MODE" = apply ] || exit 0

[ -f "$DRAIN_SCRIPT" ] || { echo 'slot drain script is missing' >&2; exit 1; }
if [ "$SCOPE" = system ]; then [ "$(id -u)" -eq 0 ] || { echo 'system scope requires root' >&2; exit 1; }; fi
if [ -n "$DEPLOYMENT_LOCK_LIBRARY" ]; then
  [ -f "$DEPLOYMENT_LOCK_LIBRARY" ] || { echo 'deployment lock library does not exist' >&2; exit 1; }
  # shellcheck source=/dev/null
  source "$DEPLOYMENT_LOCK_LIBRARY"
fi

/bin/bash "$DRAIN_SCRIPT" --check --slot "$SLOT" --control-port "$CONTROL_PORT" \
  --gateway-base-port "$GATEWAY_BASE_PORT" --gateway-count "$GATEWAY_COUNT" --ingress-port "$INGRESS_PORT"

service_names() {
  printf '%s\n' control-1
  index=1
  while [ "$index" -le "$GATEWAY_COUNT" ]; do printf 'gateway-%s\n' "$index"; index=$((index + 1)); done
}
service_label() { printf '%s.%s.%s' "$LABEL_PREFIX" "$SLOT" "$1"; }
service_plist_path() { printf '%s/%s.plist' "$PLIST_DIR" "$(service_label "$1")"; }
service_run_path() { printf '%s/%s.sh' "$BIN_DIR" "$1"; }

RETIRE_DIR=
MUTATED=0
rollback() {
  set +e
  [ -n "$RETIRE_DIR" ] || return 0
  for name in $(service_names); do
    plist="$(service_plist_path "$name")"
    run_script="$(service_run_path "$name")"
    [ ! -f "$RETIRE_DIR/$name.plist" ] || mv -f -- "$RETIRE_DIR/$name.plist" "$plist"
    [ ! -f "$RETIRE_DIR/$name.sh" ] || mv -f -- "$RETIRE_DIR/$name.sh" "$run_script"
    [ ! -f "$plist" ] || launchctl bootstrap "$DOMAIN" "$plist" >/dev/null 2>&1 || true
  done
}
on_exit() {
  code="$?"
  if [ "$code" -ne 0 ] && [ "$MUTATED" = 1 ]; then rollback; fi
  if [ -n "$DEPLOYMENT_LOCK_LIBRARY" ] && [ "${DEPLOYMENT_LOCK_HELD:-0}" = 1 ]; then release_deployment_lock || true; fi
  exit "$code"
}
trap on_exit EXIT INT TERM

if [ -n "$DEPLOYMENT_LOCK_LIBRARY" ]; then
  assert_retired_watchdog_disabled
  acquire_deployment_lock "$BASE_DIR" "retire-performance-$SLOT"
fi
RETIRE_DIR="$BASE_DIR/retired/performance-$SLOT-$(date -u +%Y%m%dT%H%M%SZ)-$$"
mkdir -p "$RETIRE_DIR"
MUTATED=1
for name in $(service_names); do
  plist="$(service_plist_path "$name")"
  run_script="$(service_run_path "$name")"
  [ -f "$plist" ] || { echo "missing slot plist: $plist" >&2; exit 1; }
  [ -f "$run_script" ] || { echo "missing slot run script: $run_script" >&2; exit 1; }
  launchctl bootout "$DOMAIN/$(service_label "$name")" >/dev/null 2>&1 || true
  mv -f -- "$plist" "$RETIRE_DIR/$name.plist"
  mv -f -- "$run_script" "$RETIRE_DIR/$name.sh"
done
for name in $(service_names); do
  if launchctl print "$DOMAIN/$(service_label "$name")" >/dev/null 2>&1; then
    echo "slot service is still loaded: $(service_label "$name")" >&2
    exit 1
  fi
done
MUTATED=0
if [ -n "$DEPLOYMENT_LOCK_LIBRARY" ]; then
  release_deployment_lock || { echo 'slot retired but deployment lock release failed; manual recovery is required' >&2; exit 1; }
fi
trap - EXIT INT TERM
printf 'PERFORMANCE_SLOT_RETIRED slot=%s recovery=%s\n' "$SLOT" "$RETIRE_DIR"
