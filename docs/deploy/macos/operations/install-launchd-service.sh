#!/usr/bin/env bash
set -euo pipefail

MODE=dry-run
SCOPE=user
BASE_DIR="${HOME}/juhe-ai-lite"
LABEL=com.example.juhe-ai
SERVICE_USER=''
NODE_PATH='/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin'
RETIRED_WATCHDOG_LABEL=''
HAD_LOADED_SERVICE=0
HEALTH_PORT=''
HEALTH_BASE_URL=''
HEALTH_PATH='/__aisys__/health'
API_HEALTH_PATH='/__aisys__/api/health'

usage() {
  cat <<'EOF'
Usage: install-launchd-service.sh [--dry-run|--apply] [options]
  --scope <user|system>             launchd domain; default user
  --base-dir <absolute-path>        release root containing current and bin; default ~/juhe-ai-lite
  --label <reverse-dns-label>       service label; default com.example.juhe-ai
  --user <name>                     required for system scope
  --node-path <PATH>                PATH used by run.sh
  --retired-watchdog-label <label>  optional label that must remain disabled and unloaded
  --health-port <port>              required by apply unless --health-base-url is used
  --health-base-url <loopback-url>  http://127.0.0.1:<port> or http://[::1]:<port>
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --dry-run) MODE=dry-run; shift ;;
    --apply) MODE=apply; shift ;;
    --scope) SCOPE="${2:?missing --scope value}"; shift 2 ;;
    --base-dir) BASE_DIR="${2:?missing --base-dir value}"; shift 2 ;;
    --label) LABEL="${2:?missing --label value}"; shift 2 ;;
    --user) SERVICE_USER="${2:?missing --user value}"; shift 2 ;;
    --node-path) NODE_PATH="${2:?missing --node-path value}"; shift 2 ;;
    --retired-watchdog-label) RETIRED_WATCHDOG_LABEL="${2:?missing label}"; shift 2 ;;
    --health-port) HEALTH_PORT="${2:?missing health port}"; shift 2 ;;
    --health-base-url) HEALTH_BASE_URL="${2:?missing health base URL}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown option: $1" >&2; usage >&2; exit 2 ;;
  esac
done

case "$SCOPE" in user|system) ;; *) echo 'scope must be user or system' >&2; exit 2;; esac
case "$LABEL" in ''|.*|*[!A-Za-z0-9._-]*) echo 'invalid launchd label' >&2; exit 2;; esac
case "$BASE_DIR" in /*) ;; *) echo 'base-dir must be absolute' >&2; exit 2;; esac
case "$BASE_DIR$LABEL$SERVICE_USER$NODE_PATH" in *$'\n'*|*'<'*|*'>'*|*'&'*|*'|'*|*'"'*|*'\'*|*'$'*|*'`'*) echo 'arguments contain unsupported template characters' >&2; exit 2;; esac
if [ "$SCOPE" = system ] && [ -z "$SERVICE_USER" ]; then echo '--user is required for system scope' >&2; exit 2; fi
if [ -n "$HEALTH_PORT" ]; then
  case "$HEALTH_PORT" in ''|*[!0-9]*) echo 'health port must be numeric' >&2; exit 2;; esac
  [ "$HEALTH_PORT" -ge 1 ] && [ "$HEALTH_PORT" -le 65535 ] || { echo 'health port out of range' >&2; exit 2; }
fi
if [ -n "$HEALTH_BASE_URL" ]; then
  case "$HEALTH_BASE_URL" in
    http://127.0.0.1:*) HEALTH_URL_PORT="${HEALTH_BASE_URL#http://127.0.0.1:}" ;;
    'http://[::1]:'*) HEALTH_URL_PORT="${HEALTH_BASE_URL#http://\[::1\]:}" ;;
    *) echo 'health base URL must be an explicit loopback HTTP URL with port' >&2; exit 2 ;;
  esac
  case "$HEALTH_URL_PORT" in ''|*[!0-9]*) echo 'health base URL port must be numeric' >&2; exit 2;; esac
  [ "$HEALTH_URL_PORT" -ge 1 ] && [ "$HEALTH_URL_PORT" -le 65535 ] || { echo 'health base URL port out of range' >&2; exit 2; }
fi
[ -z "$HEALTH_PORT" ] || [ -z "$HEALTH_BASE_URL" ] || { echo 'use only one of --health-port or --health-base-url' >&2; exit 2; }

SCRIPT_PATH="${JUHE_AI_OPERATION_SCRIPT_PATH:-${BASH_SOURCE:-$0}}"
SCRIPT_DIR="$(cd "$(dirname "$SCRIPT_PATH")" && pwd)"
TEMPLATE="$SCRIPT_DIR/templates/com.juhe-ai.plist.tpl"
RUN_SCRIPT="$BASE_DIR/bin/run.sh"
CURRENT_DIR="$BASE_DIR/current"
LOG_DIR="$BASE_DIR/logs"
if [ "$SCOPE" = user ]; then
  DOMAIN="gui/$(id -u)"
  PLIST_PATH="$HOME/Library/LaunchAgents/$LABEL.plist"
  USER_KEYS=''
else
  DOMAIN=system
  PLIST_PATH="/Library/LaunchDaemons/$LABEL.plist"
  USER_KEYS="<key>UserName</key><string>$SERVICE_USER</string>"
fi

assert_retired_watchdog() {
  [ -n "$RETIRED_WATCHDOG_LABEL" ] || return 0
  if launchctl print "$DOMAIN/$RETIRED_WATCHDOG_LABEL" >/dev/null 2>&1; then
    echo "retired watchdog is still loaded: $RETIRED_WATCHDOG_LABEL" >&2
    return 1
  fi
  escaped_label="$(printf '%s' "$RETIRED_WATCHDOG_LABEL" | sed 's/[.]/\\./g')"
  launchctl print-disabled "$DOMAIN" 2>/dev/null \
    | grep -Eq "\"${escaped_label}\"[[:space:]]*=>[[:space:]]*disabled" || {
      echo "retired watchdog is not persistently disabled: $RETIRED_WATCHDOG_LABEL" >&2
      return 1
    }
}

render_plist() {
  sed \
    -e "s|__LABEL__|$LABEL|g" \
    -e "s|__RUN_SCRIPT__|$RUN_SCRIPT|g" \
    -e "s|__CURRENT_DIR__|$CURRENT_DIR|g" \
    -e "s|__USER_KEYS__|$USER_KEYS|g" \
    -e "s|__STDOUT_PATH__|$LOG_DIR/launchd.out.log|g" \
    -e "s|__STDERR_PATH__|$LOG_DIR/launchd.err.log|g" \
    "$TEMPLATE"
}

wait_for_main_health() {
  local attempt consecutive=0
  for attempt in $(seq 1 30); do
    if launchctl print "$DOMAIN/$LABEL" >/dev/null 2>&1 \
      && curl -fsS --max-time 2 "$HEALTH_BASE_URL$HEALTH_PATH" >/dev/null \
      && curl -fsS --max-time 2 "$HEALTH_BASE_URL$API_HEALTH_PATH" >/dev/null; then
      consecutive=$((consecutive + 1))
      [ "$consecutive" -ge 3 ] && return 0
    else
      consecutive=0
    fi
    [ "$attempt" -eq 30 ] || sleep 1
  done
  echo "main service did not remain healthy at $HEALTH_BASE_URL" >&2
  return 1
}

printf 'mode=%s scope=%s domain=%s label=%s base=%s plist=%s\n' "$MODE" "$SCOPE" "$DOMAIN" "$LABEL" "$BASE_DIR" "$PLIST_PATH"
printf 'plan: create fixed run.sh -> validate plist -> bootstrap main service with KeepAlive -> verify launchd state\n'
[ "$MODE" = apply ] || exit 0

if [ -n "$HEALTH_PORT" ]; then HEALTH_BASE_URL="http://127.0.0.1:$HEALTH_PORT"; fi
[ -n "$HEALTH_BASE_URL" ] || { echo '--health-port or --health-base-url is required with --apply' >&2; exit 1; }

command -v launchctl >/dev/null
command -v plutil >/dev/null
command -v curl >/dev/null
[ -f "$TEMPLATE" ]
[ -f "$CURRENT_DIR/start.sh" ] || { echo "missing current/start.sh: $CURRENT_DIR" >&2; exit 1; }
if [ "$SCOPE" = system ]; then [ "$(id -u)" -eq 0 ] || { echo 'system scope requires root' >&2; exit 1; }; fi
assert_retired_watchdog
if launchctl print "$DOMAIN/$LABEL" >/dev/null 2>&1; then
  HAD_LOADED_SERVICE=1
  [ -f "$PLIST_PATH" ] || {
    echo "loaded service has no restorable plist at $PLIST_PATH" >&2
    exit 1
  }
fi

mkdir -p "$BASE_DIR/bin" "$LOG_DIR" "$(dirname "$PLIST_PATH")"
RUN_TMP="$RUN_SCRIPT.tmp.$$"
PLIST_TMP="$PLIST_PATH.tmp.$$"
RUN_BACKUP="$RUN_SCRIPT.backup.$$"
PLIST_BACKUP="$PLIST_PATH.backup.$$"
HAD_RUN_SCRIPT=0
HAD_PLIST=0
INSTALL_MUTATED=0

rollback_install() {
  launchctl bootout "$DOMAIN" "$PLIST_PATH" >/dev/null 2>&1 || true
  if [ "$HAD_RUN_SCRIPT" = 1 ]; then mv -f -- "$RUN_BACKUP" "$RUN_SCRIPT"; else rm -f -- "$RUN_SCRIPT"; fi
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

on_install_exit() {
  local exit_code="$1"
  set +e
  rm -f -- "$RUN_TMP" "$PLIST_TMP"
  if [ "$exit_code" -ne 0 ] && [ "$INSTALL_MUTATED" = 1 ]; then
    echo 'launchd installation failed; restoring the previous main service definition' >&2
    rollback_install || echo 'launchd rollback failed; inspect the previous plist and service state manually' >&2
  fi
  rm -f -- "$RUN_BACKUP" "$PLIST_BACKUP"
  return "$exit_code"
}
trap 'on_install_exit "$?"' EXIT
cat > "$RUN_TMP" <<EOF
#!/usr/bin/env bash
set -euo pipefail
export PATH="$NODE_PATH"
export NODE_ENV=production
cd "$CURRENT_DIR"
exec /bin/bash ./start.sh
EOF
chmod 755 "$RUN_TMP"
render_plist > "$PLIST_TMP"
chmod 644 "$PLIST_TMP"
plutil -lint "$PLIST_TMP" >/dev/null
[ ! -f "$RUN_SCRIPT" ] || { cp -p -- "$RUN_SCRIPT" "$RUN_BACKUP"; HAD_RUN_SCRIPT=1; }
[ ! -f "$PLIST_PATH" ] || { cp -p -- "$PLIST_PATH" "$PLIST_BACKUP"; HAD_PLIST=1; }
INSTALL_MUTATED=1
mv "$RUN_TMP" "$RUN_SCRIPT"
mv "$PLIST_TMP" "$PLIST_PATH"
launchctl bootout "$DOMAIN" "$PLIST_PATH" >/dev/null 2>&1 || true
launchctl bootstrap "$DOMAIN" "$PLIST_PATH"
launchctl kickstart -k "$DOMAIN/$LABEL"
launchctl print "$DOMAIN/$LABEL" >/dev/null
wait_for_main_health
INSTALL_MUTATED=0
rm -f -- "$RUN_BACKUP" "$PLIST_BACKUP"
trap - EXIT
printf 'launchd service installed: %s/%s\n' "$DOMAIN" "$LABEL"
