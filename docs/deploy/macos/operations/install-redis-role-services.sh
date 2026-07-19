#!/usr/bin/env bash
set -euo pipefail

MODE=dry-run
ACTION=install
SCOPE=''
ROLE=''
BASE_DIR=''
ENV_FILE=''
LABEL=''
REDIS_SERVER=/usr/local/bin/redis-server
REDIS_CLI=/usr/local/bin/redis-cli
BACKUP_ROOT=''
HAD_LOADED_SERVICE=0
APPLY_STARTED=0
APPLY_COMPLETED=0

usage() {
  cat <<'EOF'
Usage: install-redis-role-services.sh [--dry-run|--apply] --action <install|remove> \
  --scope <main|temporary> --role <cache|state|queue> --base-dir <absolute-path> \
  --env-file <absolute-path> --label <launchd-label> [--redis-server <path>] [--redis-cli <path>]
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
case "$SCOPE" in main|temporary) ;; *) echo 'scope must be main or temporary' >&2; exit 2;; esac
case "$ROLE" in cache|state|queue) ;; *) echo 'role must be cache, state or queue' >&2; exit 2;; esac
for path in "$BASE_DIR" "$ENV_FILE" "$REDIS_SERVER" "$REDIS_CLI"; do case "$path" in /*) ;; *) echo "path must be absolute: $path" >&2; exit 2;; esac; done
case "$LABEL" in ''|.*|*[!A-Za-z0-9._-]*) echo 'invalid launchd label' >&2; exit 2;; esac
[ -r "$ENV_FILE" ] || { echo 'env file is not readable' >&2; exit 1; }

if [ "$SCOPE" = main ]; then
  case "$ROLE" in cache) PORT=6379;; state) PORT=6380;; queue) PORT=6381;; esac
else
  case "$ROLE" in cache) PORT=16379;; state) PORT=16380;; queue) PORT=16381;; esac
fi

ROLE_ROOT="$BASE_DIR/redis/$SCOPE/$ROLE"
DATA_DIR="$ROLE_ROOT/data"
LOG_DIR="$ROLE_ROOT/logs"
CONFIG_PATH="$ROLE_ROOT/redis.conf"
PLIST_PATH="/Library/LaunchDaemons/$LABEL.plist"
URL_KEY="JUHE_AI_REDIS_$(printf '%s' "$ROLE" | tr '[:lower:]' '[:upper:]')_URL"
REDIS_URL="$(awk -F= -v key="$URL_KEY" '$1==key {print substr($0,index($0,"=")+1); exit}' "$ENV_FILE")"
[ -n "$REDIS_URL" ] || { echo "$URL_KEY is missing" >&2; exit 1; }
export JUHE_AI_REDIS_ROLE_URL="$REDIS_URL"
read -r URL_HOST URL_PORT REDIS_PASSWORD < <(node -e '
const u=new URL(process.env.JUHE_AI_REDIS_ROLE_URL);
const password=decodeURIComponent(u.password || "").replace(/ /g,"%20");
process.stdout.write(`${u.hostname} ${u.port || "6379"} ${password}\n`)
')
unset JUHE_AI_REDIS_ROLE_URL
REDIS_PASSWORD="${REDIS_PASSWORD//%20/ }"
[ "$URL_HOST" = 127.0.0.1 ] && [ "$URL_PORT" = "$PORT" ] || {
  echo "$URL_KEY must point to redis://127.0.0.1:$PORT" >&2
  exit 1
}

render_config() {
  local target="$1"
  {
    printf 'bind 127.0.0.1\nprotected-mode yes\nport %s\ndaemonize no\nsupervised no\n' "$PORT"
    printf 'dir %s\nlogfile %s\npidfile %s\n' "$DATA_DIR" "$LOG_DIR/redis.log" "$ROLE_ROOT/redis.pid"
    [ -z "$REDIS_PASSWORD" ] || printf 'requirepass %s\n' "$REDIS_PASSWORD"
    case "$ROLE" in
      cache)
        printf 'maxmemory 768mb\nmaxmemory-policy allkeys-lru\nappendonly no\nsave ""\n'
        ;;
      state)
        printf 'maxmemory 2gb\nmaxmemory-policy noeviction\nappendonly no\nsave ""\n'
        ;;
      queue)
        printf 'maxmemory 2gb\nmaxmemory-policy noeviction\nappendonly yes\nappendfsync everysec\nsave ""\n'
        printf 'auto-aof-rewrite-percentage 100\nauto-aof-rewrite-min-size 1gb\nno-appendfsync-on-rewrite yes\n'
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

wait_for_service_stopped() {
  for _ in $(seq 1 30); do
    if ! launchctl print "system/$LABEL" >/dev/null 2>&1 \
      && ! lsof -nP -iTCP:"$PORT" -sTCP:LISTEN >/dev/null 2>&1; then return 0; fi
    sleep 1
  done
  echo "Redis role did not stop: $LABEL port=$PORT" >&2
  return 1
}

wait_for_service_ready() {
  for _ in $(seq 1 30); do
    if launchctl print "system/$LABEL" >/dev/null 2>&1 \
      && REDISCLI_AUTH="$REDIS_PASSWORD" "$REDIS_CLI" -h 127.0.0.1 -p "$PORT" --no-auth-warning PING 2>/dev/null | grep -Fxq PONG; then
      return 0
    fi
    sleep 1
  done
  echo "Redis role did not become ready: $LABEL port=$PORT" >&2
  return 1
}

rollback_service() {
  set +e
  [ "$APPLY_STARTED" = 1 ] || return 0
  sudo launchctl bootout "system/$LABEL" >/dev/null 2>&1 || true
  wait_for_service_stopped >/dev/null 2>&1 || true
  if [ -n "$BACKUP_ROOT" ] && [ -d "$BACKUP_ROOT" ]; then
    sudo rm -f -- "$CONFIG_PATH" "$PLIST_PATH"
    [ ! -f "$BACKUP_ROOT/redis.conf" ] || sudo cp "$BACKUP_ROOT/redis.conf" "$CONFIG_PATH"
    [ ! -f "$BACKUP_ROOT/service.plist" ] || sudo cp "$BACKUP_ROOT/service.plist" "$PLIST_PATH"
    if [ "$HAD_LOADED_SERVICE" = 1 ] && [ -f "$PLIST_PATH" ]; then
      sudo launchctl bootstrap system "$PLIST_PATH" >/dev/null 2>&1 || true
      sudo launchctl kickstart -k "system/$LABEL" >/dev/null 2>&1 || true
    fi
  fi
}

cleanup() {
  status=$?
  if [ "$status" -ne 0 ] && [ "$APPLY_COMPLETED" != 1 ]; then rollback_service; fi
  [ -z "$BACKUP_ROOT" ] || rm -rf -- "$BACKUP_ROOT"
  exit "$status"
}
trap cleanup EXIT

printf 'redis-role action=%s mode=%s scope=%s role=%s port=%s label=%s root=%s\n' \
  "$ACTION" "$MODE" "$SCOPE" "$ROLE" "$PORT" "$LABEL" "$ROLE_ROOT"
[ "$MODE" = apply ] || exit 0
[ -x "$REDIS_SERVER" ] && [ -x "$REDIS_CLI" ] || { echo 'redis-server/redis-cli is not executable' >&2; exit 1; }
sudo -n true
APPLY_STARTED=1
BACKUP_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/juhe-ai-redis-role.XXXXXX")"
if launchctl print "system/$LABEL" >/dev/null 2>&1; then HAD_LOADED_SERVICE=1; fi
[ ! -f "$CONFIG_PATH" ] || sudo cp "$CONFIG_PATH" "$BACKUP_ROOT/redis.conf"
[ ! -f "$PLIST_PATH" ] || sudo cp "$PLIST_PATH" "$BACKUP_ROOT/service.plist"
sudo chown "$(id -u):$(id -g)" "$BACKUP_ROOT"/* 2>/dev/null || true

if [ "$HAD_LOADED_SERVICE" = 1 ]; then sudo launchctl bootout "system/$LABEL"; fi
wait_for_service_stopped
if [ "$ACTION" = remove ]; then
  sudo rm -f -- "$PLIST_PATH"
  if [ "$SCOPE" = temporary ]; then sudo rm -rf -- "$ROLE_ROOT"; fi
  APPLY_COMPLETED=1
  exit 0
fi

sudo mkdir -p "$DATA_DIR" "$LOG_DIR"
config_tmp="$BACKUP_ROOT/redis.conf.new"
plist_tmp="$BACKUP_ROOT/service.plist.new"
render_config "$config_tmp"
render_plist "$plist_tmp"
chmod 600 "$config_tmp" "$plist_tmp"
plutil -lint "$plist_tmp" >/dev/null
sudo cp "$config_tmp" "$CONFIG_PATH.tmp.$$"
sudo chmod 600 "$CONFIG_PATH.tmp.$$"
sudo mv "$CONFIG_PATH.tmp.$$" "$CONFIG_PATH"
sudo cp "$plist_tmp" "$PLIST_PATH.tmp.$$"
sudo chmod 600 "$PLIST_PATH.tmp.$$"
sudo mv "$PLIST_PATH.tmp.$$" "$PLIST_PATH"
sudo launchctl bootstrap system "$PLIST_PATH"
sudo launchctl kickstart -k "system/$LABEL"
wait_for_service_ready
APPLY_COMPLETED=1
printf 'REDIS_ROLE_SERVICE_OK scope=%s role=%s port=%s label=%s\n' "$SCOPE" "$ROLE" "$PORT" "$LABEL"
