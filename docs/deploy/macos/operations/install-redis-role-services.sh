#!/usr/bin/env bash
set -euo pipefail
umask 077

MODE=dry-run
ACTION=install
SCOPE=''
ROLE=''
BASE_DIR=''
ENV_FILE=''
LABEL=''
REDIS_SERVER=/usr/local/opt/redis/bin/redis-server
REDIS_CLI=/usr/local/opt/redis/bin/redis-cli
EXPECTED_BASE_DIR=/Users/huanmin/juhe-ai-lite
HAD_LOADED_SERVICE=0
OLD_CONFIG_PATH=''
BACKUP_ROOT=''
APPLY_STARTED=0
APPLY_COMPLETED=0
BACKUP_RETENTION=6

usage() {
  cat <<'EOF'
Usage: install-redis-role-services.sh [--dry-run|--apply] --action <install|remove> \
  --scope <main|temporary|migration> --role <cache|state|queue> --base-dir /Users/huanmin/juhe-ai-lite \
  --env-file <absolute-credential-env> --label <expected-label> [--redis-server <path>] [--redis-cli <path>]

Apply must run as root. The deployment controller supplies sudo authentication outside this script;
application prepare/cleanup scripts never prompt for or persist sudo credentials.
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --dry-run) MODE=dry-run; shift ;;
    --apply) MODE=apply; shift ;;
    --action) ACTION="${2:?missing action}"; shift 2 ;;
    --scope) SCOPE="${2:?missing scope}"; shift 2 ;;
    --role) ROLE="${2:?missing role}"; shift 2 ;;
    --base-dir) BASE_DIR="${2:?missing base dir}"; shift 2 ;;
    --env-file) ENV_FILE="${2:?missing env file}"; shift 2 ;;
    --label) LABEL="${2:?missing label}"; shift 2 ;;
    --redis-server) REDIS_SERVER="${2:?missing redis-server}"; shift 2 ;;
    --redis-cli) REDIS_CLI="${2:?missing redis-cli}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown option: $1" >&2; usage >&2; exit 2 ;;
  esac
done

case "$ACTION" in install|remove) ;; *) echo 'action must be install or remove' >&2; exit 2;; esac
case "$SCOPE" in main|temporary|migration) ;; *) echo 'scope must be main, temporary or migration' >&2; exit 2;; esac
case "$ROLE" in cache|state|queue) ;; *) echo 'role must be cache, state or queue' >&2; exit 2;; esac
[ "$SCOPE" != migration ] || [ "$ROLE" = state ] || { echo 'migration scope only supports the state scratch role' >&2; exit 2; }
for path in "$BASE_DIR" "$ENV_FILE" "$REDIS_SERVER" "$REDIS_CLI"; do case "$path" in /*) ;; *) echo "path must be absolute: $path" >&2; exit 2;; esac; done
[ "$BASE_DIR" = "$EXPECTED_BASE_DIR" ] || { echo "base-dir must be $EXPECTED_BASE_DIR" >&2; exit 2; }
[ -r "$ENV_FILE" ] || { echo 'credential env file is not readable' >&2; exit 1; }

case "$SCOPE/$ROLE" in
  main/cache)
    PORT=6379
    EXPECTED_LABEL=top.huanmin.juhe-ai-lite.redis-cache
    ROLE_ROOT="$BASE_DIR/shared/redis-cache"
    DATA_DIR="$ROLE_ROOT"
    LOG_DIR="$BASE_DIR/logs"
    LOG_FILE="$LOG_DIR/redis-cache.log"
    CONFIG_PATH="$BASE_DIR/shared/redis-cache.conf"
    ;;
  main/state|main/queue)
    if [ "$ROLE" = state ]; then PORT=6380; EXPECTED_LABEL=top.huanmin.juhe-ai-lite.redis-state
    else PORT=6381; EXPECTED_LABEL=top.huanmin.juhe-ai-lite.redis-queue; fi
    ROLE_ROOT="$BASE_DIR/redis/main/$ROLE"
    DATA_DIR="$ROLE_ROOT/data"
    LOG_DIR="$ROLE_ROOT/logs"
    LOG_FILE="$LOG_DIR/redis.log"
    CONFIG_PATH="$ROLE_ROOT/redis.conf"
    ;;
  temporary/cache|temporary/state|temporary/queue)
    case "$ROLE" in cache) PORT=16379;; state) PORT=16380;; queue) PORT=16381;; esac
    EXPECTED_LABEL="top.huanmin.juhe-ai-lite.redis.temporary.$ROLE"
    ROLE_ROOT="$BASE_DIR/redis/temporary/$ROLE"
    DATA_DIR="$ROLE_ROOT/data"
    LOG_DIR="$ROLE_ROOT/logs"
    LOG_FILE="$LOG_DIR/redis.log"
    CONFIG_PATH="$ROLE_ROOT/redis.conf"
    ;;
  migration/state)
    PORT=16382
    EXPECTED_LABEL=top.huanmin.juhe-ai-lite.redis.migration-scratch
    ROLE_ROOT="$BASE_DIR/redis/migration/state-scratch"
    DATA_DIR="$ROLE_ROOT/data"
    LOG_DIR="$ROLE_ROOT/logs"
    LOG_FILE="$LOG_DIR/redis.log"
    CONFIG_PATH="$ROLE_ROOT/redis.conf"
    ;;
  *) echo "unsupported Redis role mapping: $SCOPE/$ROLE" >&2; exit 2;;
esac
[ "$LABEL" = "$EXPECTED_LABEL" ] || { echo "label must be $EXPECTED_LABEL for $SCOPE/$ROLE" >&2; exit 2; }
PLIST_PATH="/Library/LaunchDaemons/$LABEL.plist"
URL_KEY="JUHE_AI_REDIS_$(printf '%s' "$ROLE" | tr '[:lower:]' '[:upper:]')_URL"
REDIS_URL="$(awk -F= -v key="$URL_KEY" '$1==key {print substr($0,index($0,"=")+1); exit}' "$ENV_FILE")"
[ -n "$REDIS_URL" ] || { echo "$URL_KEY is missing" >&2; exit 1; }
export JUHE_AI_REDIS_ROLE_URL="$REDIS_URL"
URL_HOST="$(node -e 'process.stdout.write(new URL(process.env.JUHE_AI_REDIS_ROLE_URL).hostname)')"
URL_PORT="$(node -e 'const u=new URL(process.env.JUHE_AI_REDIS_ROLE_URL);process.stdout.write(u.port||"6379")')"
REDIS_PASSWORD="$(node -e 'process.stdout.write(decodeURIComponent(new URL(process.env.JUHE_AI_REDIS_ROLE_URL).password||""))')"
REDIS_PASSWORD_CONFIG="$(node -e 'process.stdout.write(JSON.stringify(decodeURIComponent(new URL(process.env.JUHE_AI_REDIS_ROLE_URL).password||"")))')"
unset JUHE_AI_REDIS_ROLE_URL
[ "$URL_HOST" = 127.0.0.1 ] || { echo "$URL_KEY must use canonical 127.0.0.1" >&2; exit 1; }
if [ "$SCOPE" = main ]; then
  [ "$URL_PORT" = "$PORT" ] || { echo "$URL_KEY must point to port $PORT" >&2; exit 1; }
fi

render_config() {
  local target="$1"
  {
    printf 'bind 127.0.0.1\nprotected-mode yes\nport %s\ndaemonize no\nsupervised no\n' "$PORT"
    printf 'dir %s\nlogfile %s\npidfile %s\n' "$DATA_DIR" "$LOG_FILE" "$ROLE_ROOT/redis.pid"
    [ -z "$REDIS_PASSWORD" ] || printf 'requirepass %s\n' "$REDIS_PASSWORD_CONFIG"
    case "$ROLE" in
      cache) printf 'maxmemory 768mb\nmaxmemory-policy allkeys-lru\nappendonly no\nsave ""\n' ;;
      state) printf 'maxmemory 2gb\nmaxmemory-policy noeviction\nappendonly no\nsave ""\n' ;;
      queue)
        printf 'maxmemory 2gb\nmaxmemory-policy noeviction\nappendonly yes\nappendfsync everysec\nsave ""\n'
        printf 'auto-aof-rewrite-percentage 100\nauto-aof-rewrite-min-size 1gb\n'
        ;;
    esac
  } > "$target"
}

render_plist() {
  local target="$1"
  cat > "$target" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>$LABEL</string>
<key>ProgramArguments</key><array><string>$REDIS_SERVER</string><string>$CONFIG_PATH</string></array>
<key>RunAtLoad</key><true/><key>KeepAlive</key><true/>
<key>WorkingDirectory</key><string>$ROLE_ROOT</string>
<key>StandardOutPath</key><string>$LOG_DIR/launchd.out.log</string>
<key>StandardErrorPath</key><string>$LOG_DIR/launchd.err.log</string>
<key>ProcessType</key><string>Background</string>
</dict></plist>
EOF
}

launchd_pid() {
  launchctl print "system/$LABEL" 2>/dev/null | awk '/^[[:space:]]*pid = [0-9]+/ {print $3; exit}'
}

port_pid() {
  lsof -tiTCP:"$PORT" -sTCP:LISTEN 2>/dev/null | sort -u
}

assert_owner_or_free() {
  local job_pid port_pids
  if launchctl print "system/$LABEL" >/dev/null 2>&1; then
    job_pid="$(launchd_pid)"; port_pids="$(port_pid)"
    [ -n "$job_pid" ] && [ "$port_pids" = "$job_pid" ] || {
      echo "launchd/port owner mismatch label=$LABEL launchd=${job_pid:-missing} port=${port_pids:-free}" >&2
      return 1
    }
  else
    [ -z "$(port_pid)" ] || { echo "port $PORT is owned by an unmanaged process" >&2; return 1; }
  fi
}

wait_for_service_stopped() {
  for _ in $(seq 1 30); do
    if ! launchctl print "system/$LABEL" >/dev/null 2>&1 && [ -z "$(port_pid)" ]; then return 0; fi
    sleep 1
  done
  echo "Redis role did not stop: $LABEL port=$PORT" >&2
  return 1
}

wait_for_service_ready() {
  local job_pid port_pids info_pid
  for _ in $(seq 1 30); do
    if launchctl print "system/$LABEL" >/dev/null 2>&1 \
      && REDISCLI_AUTH="$REDIS_PASSWORD" "$REDIS_CLI" -h 127.0.0.1 -p "$PORT" --no-auth-warning PING 2>/dev/null | grep -Fxq PONG; then
      job_pid="$(launchd_pid)"; port_pids="$(port_pid)"
      info_pid="$(REDISCLI_AUTH="$REDIS_PASSWORD" "$REDIS_CLI" -h 127.0.0.1 -p "$PORT" --no-auth-warning INFO server 2>/dev/null | awk -F: '$1=="process_id" {gsub(/\r/,"",$2);print $2;exit}')"
      if [ -n "$job_pid" ] && [ "$port_pids" = "$job_pid" ] && [ "$info_pid" = "$job_pid" ]; then return 0; fi
    fi
    sleep 1
  done
  echo "Redis role did not become owner-bound ready: $LABEL port=$PORT" >&2
  return 1
}

restore_previous_service() {
  local rollback_ok=1
  if launchctl print "system/$LABEL" >/dev/null 2>&1; then launchctl bootout "system/$LABEL" || rollback_ok=0; fi
  wait_for_service_stopped || rollback_ok=0
  rm -f -- "$PLIST_PATH" "$CONFIG_PATH"
  if [ -f "$BACKUP_ROOT/service.plist" ]; then cp "$BACKUP_ROOT/service.plist" "$PLIST_PATH" || rollback_ok=0; fi
  if [ -n "$OLD_CONFIG_PATH" ] && [ -f "$BACKUP_ROOT/previous-redis.conf" ]; then
    mkdir -p "$(dirname "$OLD_CONFIG_PATH")" || rollback_ok=0
    cp "$BACKUP_ROOT/previous-redis.conf" "$OLD_CONFIG_PATH" || rollback_ok=0
  fi
  if [ "$HAD_LOADED_SERVICE" = 1 ]; then
    launchctl bootstrap system "$PLIST_PATH" || rollback_ok=0
    launchctl kickstart -k "system/$LABEL" || rollback_ok=0
    wait_for_service_ready || rollback_ok=0
  else
    wait_for_service_stopped || rollback_ok=0
  fi
  [ "$rollback_ok" = 1 ]
}

backup_is_recovery_referenced() {
  local candidate="$1" marker
  for marker in "$BASE_DIR"/redis/recovery-required-*; do
    [ -f "$marker" ] || continue
    grep -Fxq "backup=$candidate" "$marker" && return 0
  done
  return 1
}

rotate_successful_backups() {
  local kept=0 candidate
  find "$BASE_DIR/redis/rollback" -mindepth 1 -maxdepth 1 -type d -name "*-$LABEL" -print \
    | sort -r \
    | while IFS= read -r candidate; do
        if backup_is_recovery_referenced "$candidate"; then continue; fi
        kept=$((kept + 1))
        if [ "$kept" -gt "$BACKUP_RETENTION" ]; then
          case "$candidate" in "$BASE_DIR/redis/rollback/"*) rm -rf -- "$candidate";; *) return 1;; esac
        fi
      done
}

finish() {
  local status=$? marker
  trap - EXIT
  if [ "$status" -ne 0 ] && [ "$APPLY_STARTED" = 1 ] && [ "$APPLY_COMPLETED" != 1 ]; then
    if restore_previous_service; then
      printf 'REDIS_ROLE_ROLLBACK_OK backup=%s\n' "$BACKUP_ROOT" >&2
    else
      marker="$BASE_DIR/redis/recovery-required-$LABEL-$(date +%Y%m%d%H%M%S)"
      printf 'backup=%s\nlabel=%s\nport=%s\n' "$BACKUP_ROOT" "$LABEL" "$PORT" > "$marker"
      chmod 600 "$marker"
      printf 'REDIS_ROLE_ROLLBACK_FAILED marker=%s backup=%s\n' "$marker" "$BACKUP_ROOT" >&2
    fi
  fi
  exit "$status"
}
trap finish EXIT

printf 'redis-role action=%s mode=%s scope=%s role=%s port=%s label=%s root=%s\n' \
  "$ACTION" "$MODE" "$SCOPE" "$ROLE" "$PORT" "$LABEL" "$ROLE_ROOT"
[ "$MODE" = apply ] || exit 0
[ "$(id -u)" = 0 ] || { echo 'apply must run as root via the deployment controller' >&2; exit 1; }
[ -x "$REDIS_SERVER" ] && [ -x "$REDIS_CLI" ] || { echo 'redis-server/redis-cli is not executable' >&2; exit 1; }
assert_owner_or_free

APPLY_STARTED=1
BACKUP_ROOT="$BASE_DIR/redis/rollback/$(date +%Y%m%d%H%M%S)-$$-$LABEL"
mkdir -p "$BACKUP_ROOT"
chmod 700 "$BACKUP_ROOT"
if launchctl print "system/$LABEL" >/dev/null 2>&1; then HAD_LOADED_SERVICE=1; fi
if [ -f "$PLIST_PATH" ]; then
  cp "$PLIST_PATH" "$BACKUP_ROOT/service.plist"
  chmod 600 "$BACKUP_ROOT/service.plist"
  OLD_CONFIG_PATH="$(/usr/libexec/PlistBuddy -c 'Print :ProgramArguments:1' "$PLIST_PATH" 2>/dev/null || true)"
  case "$OLD_CONFIG_PATH" in "$BASE_DIR"/*) ;; *) echo "previous config path is outside base: $OLD_CONFIG_PATH" >&2; exit 1;; esac
  if [ -f "$OLD_CONFIG_PATH" ]; then
    cp "$OLD_CONFIG_PATH" "$BACKUP_ROOT/previous-redis.conf"
    chmod 600 "$BACKUP_ROOT/previous-redis.conf"
  fi
fi

if [ "$HAD_LOADED_SERVICE" = 1 ]; then launchctl bootout "system/$LABEL"; fi
wait_for_service_stopped
if [ "$ACTION" = remove ]; then
  rm -f -- "$PLIST_PATH"
  if [ "$SCOPE" = temporary ] || [ "$SCOPE" = migration ]; then rm -rf -- "$ROLE_ROOT"; fi
  APPLY_COMPLETED=1
  rotate_successful_backups
  printf 'REDIS_ROLE_SERVICE_REMOVED scope=%s role=%s backup=%s\n' "$SCOPE" "$ROLE" "$BACKUP_ROOT"
  exit 0
fi

mkdir -p "$DATA_DIR" "$LOG_DIR"
config_tmp="$BACKUP_ROOT/redis.conf.new"
plist_tmp="$BACKUP_ROOT/service.plist.new"
render_config "$config_tmp"
render_plist "$plist_tmp"
chmod 600 "$config_tmp" "$plist_tmp"
plutil -lint "$plist_tmp" >/dev/null
cp "$config_tmp" "$CONFIG_PATH.tmp.$$"
chmod 600 "$CONFIG_PATH.tmp.$$"
mv "$CONFIG_PATH.tmp.$$" "$CONFIG_PATH"
cp "$plist_tmp" "$PLIST_PATH.tmp.$$"
chmod 600 "$PLIST_PATH.tmp.$$"
mv "$PLIST_PATH.tmp.$$" "$PLIST_PATH"
launchctl bootstrap system "$PLIST_PATH"
launchctl kickstart -k "system/$LABEL"
wait_for_service_ready
APPLY_COMPLETED=1
rotate_successful_backups
printf 'REDIS_ROLE_SERVICE_OK scope=%s role=%s port=%s label=%s backup=%s\n' "$SCOPE" "$ROLE" "$PORT" "$LABEL" "$BACKUP_ROOT"
