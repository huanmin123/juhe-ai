#!/usr/bin/env bash
set -euo pipefail

ACTION="${1:-}"
[ -n "$ACTION" ] || { echo 'Usage: manage-sing-box.sh <existing|brew|launchd> [--dry-run|--apply] [options]' >&2; exit 2; }
shift
MODE=dry-run
CONFIG_PATH="$HOME/.config/sing-box/config.json"
PORT=7890
LABEL=com.example.juhe-ai.sing-box
LOG_DIR="$HOME/Library/Logs/juhe-ai"
PROBE_URL='https://api.openai.com/'
HAD_LOADED_SERVICE=0
SING_BOX_BIN_OPTION=''

while [ "$#" -gt 0 ]; do
  case "$1" in
    --dry-run) MODE=dry-run; shift ;;
    --apply) MODE=apply; shift ;;
    --config) CONFIG_PATH="${2:?missing config}"; shift 2 ;;
    --port) PORT="${2:?missing port}"; shift 2 ;;
    --label) LABEL="${2:?missing label}"; shift 2 ;;
    --log-dir) LOG_DIR="${2:?missing log-dir}"; shift 2 ;;
    --probe-url) PROBE_URL="${2:?missing probe URL}"; shift 2 ;;
    --binary) SING_BOX_BIN_OPTION="${2:?missing sing-box binary}"; shift 2 ;;
    -h|--help) echo 'Actions: existing (verify and adopt listener), brew (Homebrew service), launchd (managed binary); launchd supports --binary <absolute-path>'; exit 0 ;;
    *) echo "Unknown option: $1" >&2; exit 2 ;;
  esac
done

case "$ACTION" in existing|brew|launchd) ;; *) echo 'action must be existing, brew, or launchd' >&2; exit 2;; esac
case "$PORT" in ''|*[!0-9]*) echo 'port must be numeric' >&2; exit 2;; esac
[ "$PORT" -ge 1 ] && [ "$PORT" -le 65535 ] || { echo 'port out of range' >&2; exit 2; }
case "$LABEL" in ''|.*|*[!A-Za-z0-9._-]*) echo 'invalid launchd label' >&2; exit 2;; esac
case "$CONFIG_PATH" in /*) ;; *) echo 'config path must be absolute' >&2; exit 2;; esac
case "$LOG_DIR" in /*) ;; *) echo 'log directory must be absolute' >&2; exit 2;; esac
case "$CONFIG_PATH$LABEL$LOG_DIR" in *$'\n'*|*'<'*|*'>'*|*'&'*|*'|'*|*'"'*|*'\'*|*'$'*|*'`'*) echo 'arguments contain unsupported template characters' >&2; exit 2;; esac
case "$PROBE_URL" in http://*|https://*) ;; *) echo 'probe URL must use http or https' >&2; exit 2;; esac
case "$PROBE_URL" in *$'\n'*|*$'\r'*) echo 'probe URL contains a line break' >&2; exit 2;; esac
if [ -n "$SING_BOX_BIN_OPTION" ]; then
  case "$SING_BOX_BIN_OPTION" in /*) ;; *) echo 'sing-box binary path must be absolute' >&2; exit 2;; esac
  case "$SING_BOX_BIN_OPTION" in *$'\n'*|*'<'*|*'>'*|*'&'*|*'|'*|*'"'*|*'\'*|*'$'*|*'`'*) echo 'sing-box binary path contains unsupported template characters' >&2; exit 2;; esac
fi

PROXY_URL="socks5h://127.0.0.1:$PORT"

has_proxy_listener() {
  lsof -nP -tiTCP:"$PORT" -sTCP:LISTEN 2>/dev/null | grep -q .
}

assert_loopback_sing_box_proxy() {
  local pids pid endpoints executable status
  pids="$(lsof -nP -tiTCP:"$PORT" -sTCP:LISTEN 2>/dev/null | sort -u || true)"
  case "$pids" in ''|*$'\n'*) echo "expected exactly one listener on port $PORT" >&2; return 1;; esac
  pid="$pids"
  endpoints="$(lsof -nP -a -p "$pid" -iTCP:"$PORT" -sTCP:LISTEN -Fn 2>/dev/null | sed -n 's/^n//p' | sort -u || true)"
  [ -n "$endpoints" ] || { echo "listener endpoint could not be determined for PID $pid" >&2; return 1; }
  while IFS= read -r endpoint; do
    case "$endpoint" in "127.0.0.1:$PORT"|"[::1]:$PORT") ;;
      *) echo "proxy listener is not loopback-only: $endpoint" >&2; return 1 ;;
    esac
  done <<< "$endpoints"
  executable="$(ps -p "$pid" -o comm= 2>/dev/null | sed -n '1p' || true)"
  [ "${executable##*/}" = sing-box ] || { echo "listener PID $pid is not sing-box: ${executable:-unknown}" >&2; return 1; }
  status="$(curl -sS -o /dev/null --max-time 15 --proxy "$PROXY_URL" -w '%{http_code}' "$PROBE_URL")" || {
    echo 'proxy connectivity probe failed' >&2
    return 1
  }
  case "$status" in [1-5][0-9][0-9]) ;; *) echo "proxy probe returned no HTTP response: $status" >&2; return 1;; esac
}

wait_for_listener() {
  local attempt
  for attempt in $(seq 1 15); do
    if assert_loopback_sing_box_proxy; then return 0; fi
    [ "$attempt" -eq 15 ] || sleep 1
  done
  echo "verified sing-box proxy did not become ready on 127.0.0.1:$PORT" >&2
  return 1
}

check_config_if_possible() {
  local candidate="$SING_BOX_BIN_OPTION"
  if [ -z "$candidate" ]; then candidate="$(command -v sing-box 2>/dev/null || true)"; fi
  if [ -n "$candidate" ] && [ -f "$CONFIG_PATH" ]; then "$candidate" check -c "$CONFIG_PATH" >/dev/null; fi
}

printf 'mode=%s action=%s config=%s expected_listener=127.0.0.1:%s\n' "$MODE" "$ACTION" "$CONFIG_PATH" "$PORT"
case "$ACTION" in
  existing) printf 'plan: verify a loopback sing-box listener and proxy connectivity without changing services\n' ;;
  brew) printf 'plan: verify an existing listener; otherwise install/check config/start brew service and verify the proxy\n' ;;
  launchd) printf 'plan: verify an existing listener; otherwise transactionally install user launchd and verify the proxy\n' ;;
esac
[ "$MODE" = apply ] || exit 0

command -v lsof >/dev/null
command -v ps >/dev/null
command -v curl >/dev/null
if has_proxy_listener; then
  assert_loopback_sing_box_proxy
  check_config_if_possible
  printf 'verified existing loopback sing-box proxy on port %s; no service changes made\n' "$PORT"
  exit 0
fi
if [ "$ACTION" = existing ]; then echo "no existing listener on port $PORT" >&2; exit 1; fi
[ -f "$CONFIG_PATH" ] || { echo "sing-box config missing: $CONFIG_PATH" >&2; exit 1; }

if [ "$ACTION" = brew ]; then
  command -v brew >/dev/null || { echo 'Homebrew is required for brew action' >&2; exit 1; }
  command -v sing-box >/dev/null 2>&1 || brew install sing-box
  SING_BOX_BIN="$(command -v sing-box)"
  "$SING_BOX_BIN" check -c "$CONFIG_PATH"
  brew services restart sing-box
  wait_for_listener
  exit 0
fi

command -v launchctl >/dev/null
command -v plutil >/dev/null
SING_BOX_BIN="$SING_BOX_BIN_OPTION"
if [ -z "$SING_BOX_BIN" ]; then SING_BOX_BIN="$(command -v sing-box || true)"; fi
[ -n "$SING_BOX_BIN" ] || { echo 'sing-box binary is required for launchd action' >&2; exit 1; }
case "$SING_BOX_BIN" in /*) ;; *) echo 'sing-box binary path must be absolute' >&2; exit 1;; esac
case "$SING_BOX_BIN" in *$'\n'*|*'<'*|*'>'*|*'&'*|*'|'*|*'"'*|*'\'*|*'$'*|*'`'*) echo 'sing-box binary path contains unsupported template characters' >&2; exit 1;; esac
"$SING_BOX_BIN" check -c "$CONFIG_PATH"
SCRIPT_PATH="${JUHE_AI_OPERATION_SCRIPT_PATH:-${BASH_SOURCE:-$0}}"
SCRIPT_DIR="$(cd "$(dirname "$SCRIPT_PATH")" && pwd -P)"
TEMPLATE="$SCRIPT_DIR/templates/com.juhe-ai.sing-box.plist.tpl"
[ -f "$TEMPLATE" ] || { echo "sing-box launchd template missing: $TEMPLATE" >&2; exit 1; }
PLIST_PATH="$HOME/Library/LaunchAgents/$LABEL.plist"
[ ! -L "$PLIST_PATH" ] || { echo "refusing to replace symlink plist: $PLIST_PATH" >&2; exit 1; }
DOMAIN="gui/$(id -u)"
if launchctl print "$DOMAIN/$LABEL" >/dev/null 2>&1; then
  HAD_LOADED_SERVICE=1
  [ -f "$PLIST_PATH" ] || { echo "loaded sing-box service has no restorable plist at $PLIST_PATH" >&2; exit 1; }
fi

PLIST_TMP="$PLIST_PATH.tmp.$$"
PLIST_BACKUP="$PLIST_PATH.backup.$$"
HAD_PLIST=0
LAUNCHD_MUTATED=0

rollback_launchd() {
  launchctl bootout "$DOMAIN" "$PLIST_PATH" >/dev/null 2>&1 || true
  if [ "$HAD_PLIST" = 1 ]; then
    mv -f -- "$PLIST_BACKUP" "$PLIST_PATH"
    if [ "$HAD_LOADED_SERVICE" = 1 ]; then
      launchctl bootstrap "$DOMAIN" "$PLIST_PATH"
      launchctl kickstart -k "$DOMAIN/$LABEL"
      launchctl print "$DOMAIN/$LABEL" >/dev/null
    fi
  else
    rm -f -- "$PLIST_PATH"
  fi
}

on_launchd_exit() {
  local exit_code="$1"
  set +e
  rm -f -- "$PLIST_TMP"
  if [ "$exit_code" -ne 0 ] && [ "$LAUNCHD_MUTATED" = 1 ]; then
    echo 'sing-box launchd update failed; restoring the previous plist and loaded state' >&2
    rollback_launchd || echo 'sing-box launchd rollback failed; inspect launchd state manually' >&2
  fi
  rm -f -- "$PLIST_BACKUP"
  return "$exit_code"
}

mkdir -p "$HOME/Library/LaunchAgents" "$LOG_DIR"
trap 'on_launchd_exit "$?"' EXIT
sed \
  -e "s|__LABEL__|$LABEL|g" \
  -e "s|__SING_BOX_BIN__|$SING_BOX_BIN|g" \
  -e "s|__CONFIG_PATH__|$CONFIG_PATH|g" \
  -e "s|__STDOUT_PATH__|$LOG_DIR/sing-box.out.log|g" \
  -e "s|__STDERR_PATH__|$LOG_DIR/sing-box.err.log|g" \
  "$TEMPLATE" > "$PLIST_TMP"
chmod 644 "$PLIST_TMP"
plutil -lint "$PLIST_TMP" >/dev/null
[ ! -f "$PLIST_PATH" ] || { cp -p -- "$PLIST_PATH" "$PLIST_BACKUP"; HAD_PLIST=1; }
LAUNCHD_MUTATED=1
mv "$PLIST_TMP" "$PLIST_PATH"
launchctl bootout "$DOMAIN" "$PLIST_PATH" >/dev/null 2>&1 || true
launchctl bootstrap "$DOMAIN" "$PLIST_PATH"
launchctl kickstart -k "$DOMAIN/$LABEL"
launchctl print "$DOMAIN/$LABEL" >/dev/null
wait_for_listener
LAUNCHD_MUTATED=0
rm -f -- "$PLIST_BACKUP"
trap - EXIT
printf 'sing-box launchd service installed and proxy verified: %s/%s\n' "$DOMAIN" "$LABEL"
