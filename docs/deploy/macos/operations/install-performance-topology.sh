#!/usr/bin/env bash
set -euo pipefail

MODE=dry-run
SCOPE=user
BASE_DIR="${HOME}/juhe-ai-lite"
RELEASE_DIR=
SLOT=main
LABEL_PREFIX=com.example.juhe-ai.performance
CONTROL_PORT=
GATEWAY_BASE_PORT=
GATEWAY_COUNT=3
USAGE_WORKERS=2
LOG_WORKERS=2
INGRESS_PORT=3099
NGINX_CONFIG=
NGINX_MAIN_CONFIG=
NGINX_CONFIG_KIND=included
SERVICE_USER=
SERVICE_GROUP=
DEPLOYMENT_LOCK_LIBRARY=
DRAIN_SCRIPT="$(cd "$(dirname "$0")" && pwd)/wait-performance-slot-drain.sh"
NODE_PATH="/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"

usage() {
  cat <<'EOF'
Usage: install-performance-topology.sh [--dry-run|--apply] [options]
  --scope user|system
  --base-dir ABSOLUTE_PATH
  --release-dir ABSOLUTE_RELEASE_PATH
  --slot main|temporary
  --label-prefix LAUNCHD_LABEL_PREFIX
  --control-port PORT
  --gateway-base-port PORT
  --gateway-count 1..32
  --usage-workers 1..32
  --log-workers 1..32
  --ingress-port PORT
  --nginx-config ABSOLUTE_INCLUDED_CONF_PATH
  --nginx-main-config ABSOLUTE_MAIN_CONF_PATH
  --nginx-config-kind included|main
  --service-user USER       required for --scope system
  --service-group GROUP     required for --scope system
  --deployment-lock-library ABSOLUTE_DEPLOYMENT_LOCK_SH
  --drain-script ABSOLUTE_WAIT_SLOT_DRAIN_SH
  --node-path PATH_VALUE
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --dry-run) MODE=dry-run; shift ;;
    --apply) MODE=apply; shift ;;
    --scope) SCOPE="${2:-}"; shift 2 ;;
    --base-dir) BASE_DIR="${2:-}"; shift 2 ;;
    --release-dir) RELEASE_DIR="${2:-}"; shift 2 ;;
    --slot) SLOT="${2:-}"; shift 2 ;;
    --label-prefix) LABEL_PREFIX="${2:-}"; shift 2 ;;
    --control-port) CONTROL_PORT="${2:-}"; shift 2 ;;
    --gateway-base-port) GATEWAY_BASE_PORT="${2:-}"; shift 2 ;;
    --gateway-count) GATEWAY_COUNT="${2:-}"; shift 2 ;;
    --usage-workers) USAGE_WORKERS="${2:-}"; shift 2 ;;
    --log-workers) LOG_WORKERS="${2:-}"; shift 2 ;;
    --ingress-port) INGRESS_PORT="${2:-}"; shift 2 ;;
    --nginx-config) NGINX_CONFIG="${2:-}"; shift 2 ;;
    --nginx-main-config) NGINX_MAIN_CONFIG="${2:-}"; shift 2 ;;
    --nginx-config-kind) NGINX_CONFIG_KIND="${2:-}"; shift 2 ;;
    --service-user) SERVICE_USER="${2:-}"; shift 2 ;;
    --service-group) SERVICE_GROUP="${2:-}"; shift 2 ;;
    --deployment-lock-library) DEPLOYMENT_LOCK_LIBRARY="${2:-}"; shift 2 ;;
    --drain-script) DRAIN_SCRIPT="${2:-}"; shift 2 ;;
    --node-path) NODE_PATH="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown option: $1" >&2; usage >&2; exit 2 ;;
  esac
done

case "$SCOPE" in user|system) ;; *) echo '--scope must be user or system' >&2; exit 2 ;; esac
case "$SLOT" in main|temporary) ;; *) echo '--slot must be main or temporary' >&2; exit 2 ;; esac
case "$NGINX_CONFIG_KIND" in included|main) ;; *) echo '--nginx-config-kind must be included or main' >&2; exit 2 ;; esac
case "$BASE_DIR" in /*) ;; *) echo '--base-dir must be absolute' >&2; exit 2 ;; esac
if [ -z "$RELEASE_DIR" ]; then RELEASE_DIR="$BASE_DIR/current"; fi
if [ -z "$CONTROL_PORT" ]; then
  if [ "$SLOT" = main ]; then CONTROL_PORT=3200; else CONTROL_PORT=3300; fi
fi
if [ -z "$GATEWAY_BASE_PORT" ]; then
  if [ "$SLOT" = main ]; then GATEWAY_BASE_PORT=3211; else GATEWAY_BASE_PORT=3311; fi
fi
case "$RELEASE_DIR" in /*) ;; *) echo '--release-dir must be absolute' >&2; exit 2 ;; esac
if [ -n "$NGINX_MAIN_CONFIG" ]; then
  case "$NGINX_MAIN_CONFIG" in /*) ;; *) echo '--nginx-main-config must be absolute' >&2; exit 2 ;; esac
fi
if [ -n "$DEPLOYMENT_LOCK_LIBRARY" ]; then
  case "$DEPLOYMENT_LOCK_LIBRARY" in /*) ;; *) echo '--deployment-lock-library must be absolute' >&2; exit 2 ;; esac
fi
case "$DRAIN_SCRIPT" in /*) ;; *) [ "$MODE" = dry-run ] || { echo '--drain-script must be absolute' >&2; exit 2; } ;; esac
if [ "$SCOPE" = system ]; then
  [ -n "$SERVICE_USER" ] || { echo '--service-user is required for system scope' >&2; exit 2; }
  [ -n "$SERVICE_GROUP" ] || { echo '--service-group is required for system scope' >&2; exit 2; }
  printf '%s' "$SERVICE_USER" | grep -Eq '^[A-Za-z_][A-Za-z0-9._-]*$' || { echo 'invalid service user' >&2; exit 2; }
  printf '%s' "$SERVICE_GROUP" | grep -Eq '^[A-Za-z_][A-Za-z0-9._-]*$' || { echo 'invalid service group' >&2; exit 2; }
fi
case "$BASE_DIR$RELEASE_DIR$NODE_PATH$NGINX_CONFIG$NGINX_MAIN_CONFIG$SERVICE_USER$SERVICE_GROUP$DEPLOYMENT_LOCK_LIBRARY$DRAIN_SCRIPT" in
  *'$'*|*'`'*|*'"'*|*'\'*|*'|'*|*'&'*|*';'*|*$'\n'*|*$'\r'*)
    echo 'paths contain unsafe shell characters' >&2
    exit 2
    ;;
esac
printf '%s' "$LABEL_PREFIX" | grep -Eq '^[A-Za-z0-9][A-Za-z0-9.-]{0,100}$' || { echo 'invalid launchd label prefix' >&2; exit 2; }

assert_number() {
  value="$1"
  min="$2"
  max="$3"
  name="$4"
  printf '%s' "$value" | grep -Eq '^[0-9]+$' || { echo "$name must be numeric" >&2; exit 2; }
  [ "$value" -ge "$min" ] && [ "$value" -le "$max" ] || { echo "$name must be between $min and $max" >&2; exit 2; }
}

assert_number "$CONTROL_PORT" 1 65535 control-port
assert_number "$GATEWAY_BASE_PORT" 1 65535 gateway-base-port
assert_number "$GATEWAY_COUNT" 1 32 gateway-count
assert_number "$USAGE_WORKERS" 1 32 usage-workers
assert_number "$LOG_WORKERS" 1 32 log-workers
assert_number "$INGRESS_PORT" 1 65535 ingress-port
LAST_GATEWAY_PORT=$((GATEWAY_BASE_PORT + GATEWAY_COUNT - 1))
[ "$LAST_GATEWAY_PORT" -le 65535 ] || { echo 'gateway port range exceeds 65535' >&2; exit 2; }
[ "$CONTROL_PORT" -lt "$GATEWAY_BASE_PORT" ] || [ "$CONTROL_PORT" -gt "$LAST_GATEWAY_PORT" ] || { echo 'control port overlaps gateway ports' >&2; exit 2; }
[ "$INGRESS_PORT" -ne "$CONTROL_PORT" ] || { echo 'ingress port overlaps control port' >&2; exit 2; }
[ "$INGRESS_PORT" -lt "$GATEWAY_BASE_PORT" ] || [ "$INGRESS_PORT" -gt "$LAST_GATEWAY_PORT" ] || { echo 'ingress port overlaps gateway ports' >&2; exit 2; }

CURRENT_DIR="$RELEASE_DIR"
BIN_DIR="$BASE_DIR/bin/performance/$SLOT"
LOG_DIR="$BASE_DIR/logs"
RUNTIME_LOG_DIR="$LOG_DIR/runtime"
SPOOL_DIR="$BASE_DIR/shared/usage-spool"
if [ "$SCOPE" = user ]; then
  DOMAIN="gui/$(id -u)"
  PLIST_DIR="$HOME/Library/LaunchAgents"
else
  DOMAIN=system
  PLIST_DIR=/Library/LaunchDaemons
fi
if [ -z "$NGINX_CONFIG" ]; then
  NGINX_CONFIG="$BASE_DIR/config/nginx/juhe-ai-performance.conf"
fi
case "$NGINX_CONFIG" in /*) ;; *) echo '--nginx-config must be absolute' >&2; exit 2 ;; esac
if [ "$NGINX_CONFIG_KIND" = main ]; then
  if [ -z "$NGINX_MAIN_CONFIG" ]; then NGINX_MAIN_CONFIG="$NGINX_CONFIG"; fi
  [ "$NGINX_MAIN_CONFIG" = "$NGINX_CONFIG" ] || { echo 'main config kind requires nginx config and main config to be identical' >&2; exit 2; }
fi

printf 'mode=%s scope=%s slot=%s base=%s release=%s control=%s gateways=%s-%s usage=%s log=%s ingress=%s nginx=%s nginx_main=%s nginx_kind=%s user=%s group=%s\n' \
  "$MODE" "$SCOPE" "$SLOT" "$BASE_DIR" "$CURRENT_DIR" "$CONTROL_PORT" "$GATEWAY_BASE_PORT" "$LAST_GATEWAY_PORT" \
  "$USAGE_WORKERS" "$LOG_WORKERS" "$INGRESS_PORT" "$NGINX_CONFIG" "${NGINX_MAIN_CONFIG:-default}" "$NGINX_CONFIG_KIND" \
  "${SERVICE_USER:-current}" "${SERVICE_GROUP:-current}"
printf 'plan: 1 control + %s gateway launchd jobs, then nginx least_conn cutover after every local health check\n' "$GATEWAY_COUNT"
[ "$MODE" = apply ] || exit 0

[ -f "$CURRENT_DIR/backend/dist/server.js" ] || { echo "missing built server: $CURRENT_DIR/backend/dist/server.js" >&2; exit 1; }
[ -f "$CURRENT_DIR/backend/dist/scripts/preflight/check-node-sqlite.js" ] || { echo 'missing runtime preflight script' >&2; exit 1; }
[ -f "$CURRENT_DIR/backend/.env" ] || { echo 'missing current/backend/.env' >&2; exit 1; }
command -v node >/dev/null
command -v launchctl >/dev/null
command -v plutil >/dev/null
command -v curl >/dev/null
command -v nginx >/dev/null
if [ "$SCOPE" = system ]; then [ "$(id -u)" -eq 0 ] || { echo 'system scope requires root' >&2; exit 1; }; fi
if [ "$SCOPE" = system ]; then
  id "$SERVICE_USER" >/dev/null 2>&1 || { echo 'service user does not exist' >&2; exit 1; }
  dscl . -read "/Groups/$SERVICE_GROUP" >/dev/null 2>&1 || { echo 'service group does not exist' >&2; exit 1; }
fi
[ -z "$NGINX_MAIN_CONFIG" ] || [ -f "$NGINX_MAIN_CONFIG" ] || { echo 'nginx main config does not exist' >&2; exit 1; }
if [ -n "$DEPLOYMENT_LOCK_LIBRARY" ]; then
  [ -f "$DEPLOYMENT_LOCK_LIBRARY" ] || { echo 'deployment lock library does not exist' >&2; exit 1; }
  # shellcheck source=/dev/null
  source "$DEPLOYMENT_LOCK_LIBRARY"
fi

STAGE_DIR=
MUTATED=0
NGINX_BACKUP="$NGINX_CONFIG.performance-backup.$$"

service_names() {
  printf '%s\n' control-1
  index=1
  while [ "$index" -le "$GATEWAY_COUNT" ]; do
    printf 'gateway-%s\n' "$index"
    index=$((index + 1))
  done
}

service_port() {
  case "$1" in
    control-1) printf '%s' "$CONTROL_PORT" ;;
    gateway-*) index="${1#gateway-}"; printf '%s' "$((GATEWAY_BASE_PORT + index - 1))" ;;
  esac
}

service_role() {
  case "$1" in control-1) printf control ;; *) printf gateway ;; esac
}

service_instance_id() { printf '%s-%s' "$SLOT" "$1"; }
service_label() { printf '%s.%s.%s' "$LABEL_PREFIX" "$SLOT" "$1"; }
service_run_path() { printf '%s/%s.sh' "$BIN_DIR" "$1"; }
service_plist_path() { printf '%s/%s.plist' "$PLIST_DIR" "$(service_label "$1")"; }

assert_existing_slot_is_inactive_and_drained() {
  existing_count=0
  for name in $(service_names); do
    [ ! -e "$(service_plist_path "$name")" ] || existing_count=$((existing_count + 1))
    [ ! -e "$(service_run_path "$name")" ] || existing_count=$((existing_count + 1))
  done
  [ "$existing_count" -gt 0 ] || return 0
  expected_count=$(((GATEWAY_COUNT + 1) * 2))
  [ "$existing_count" -eq "$expected_count" ] || {
    echo "slot $SLOT has a partial launchd/run-script installation; manual recovery is required" >&2
    return 1
  }
  [ -f "$DRAIN_SCRIPT" ] || { echo 'slot drain script is missing' >&2; return 1; }
  /bin/bash "$DRAIN_SCRIPT" --check --slot "$SLOT" --control-port "$CONTROL_PORT" \
    --gateway-base-port "$GATEWAY_BASE_PORT" --gateway-count "$GATEWAY_COUNT" --ingress-port "$INGRESS_PORT"
}

render_run_script() {
  name="$1"
  role="$(service_role "$name")"
  port="$(service_port "$name")"
  output="$2"
  {
    printf '%s\n' '#!/usr/bin/env bash' 'set -euo pipefail'
    printf 'export PATH="%s"\n' "$NODE_PATH"
    printf '%s\n' 'export NODE_ENV=production'
    printf 'export JUHE_AI_RUNTIME_MODE=performance\n'
    printf 'export JUHE_AI_PERFORMANCE_NODE_ROLE=%s\n' "$role"
    printf 'export JUHE_AI_INSTANCE_ID=%s\n' "$(service_instance_id "$name")"
    printf 'export JUHE_AI_HOST=127.0.0.1\n'
    printf 'export JUHE_AI_PORT=%s\n' "$port"
    printf 'export JUHE_AI_DB_SERVICE_HTTP_PORT=0\n'
    printf 'export JUHE_AI_GATEWAY_REPLICAS=%s\n' "$GATEWAY_COUNT"
    printf 'export JUHE_AI_USAGE_WORKER_REPLICAS=%s\n' "$USAGE_WORKERS"
    printf 'export JUHE_AI_LOG_WORKER_REPLICAS=%s\n' "$LOG_WORKERS"
    printf 'export JUHE_AI_STATS_WORKER_REPLICAS=1\n'
    printf 'export JUHE_AI_OPS_WORKER_REPLICAS=1\n'
    printf 'export JUHE_AI_LOG_DIR="%s"\n' "$RUNTIME_LOG_DIR"
    printf 'export JUHE_AI_USAGE_SPOOL_DIR="%s"\n' "$SPOOL_DIR"
    printf 'cd "%s"\n' "$CURRENT_DIR"
    printf '%s\n' 'node backend/dist/scripts/preflight/check-node-sqlite.js'
    printf '%s\n' 'exec node backend/dist/server.js'
  } > "$output"
  chmod 755 "$output"
}

xml_escape() { printf '%s' "$1" | sed 's/&/\&amp;/g; s/</\&lt;/g; s/>/\&gt;/g; s/"/\&quot;/g'; }

render_plist() {
  name="$1"
  output="$2"
  label="$(xml_escape "$(service_label "$name")")"
  run_path="$(xml_escape "$(service_run_path "$name")")"
  work_dir="$(xml_escape "$CURRENT_DIR")"
  stdout_path="$(xml_escape "$LOG_DIR/launchd.$SLOT.$name.out.log")"
  stderr_path="$(xml_escape "$LOG_DIR/launchd.$SLOT.$name.err.log")"
  cat > "$output" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>$label</string>
<key>ProgramArguments</key><array><string>/bin/bash</string><string>$run_path</string></array>
<key>WorkingDirectory</key><string>$work_dir</string>
$(if [ "$SCOPE" = system ]; then printf '<key>UserName</key><string>%s</string>\n<key>GroupName</key><string>%s</string>\n' "$(xml_escape "$SERVICE_USER")" "$(xml_escape "$SERVICE_GROUP")"; fi)
<key>KeepAlive</key><true/><key>RunAtLoad</key><true/><key>ThrottleInterval</key><integer>5</integer>
<key>SoftResourceLimits</key><dict><key>NumberOfFiles</key><integer>65536</integer></dict>
<key>HardResourceLimits</key><dict><key>NumberOfFiles</key><integer>131072</integer></dict>
<key>StandardOutPath</key><string>$stdout_path</string>
<key>StandardErrorPath</key><string>$stderr_path</string>
</dict></plist>
EOF
  chmod 644 "$output"
}

nginx_test() {
  if [ -n "$NGINX_MAIN_CONFIG" ]; then nginx -c "$NGINX_MAIN_CONFIG" -t; else nginx -t; fi
}

nginx_reload() {
  if [ -n "$NGINX_MAIN_CONFIG" ]; then nginx -c "$NGINX_MAIN_CONFIG" -s reload; else nginx -s reload; fi
}

render_nginx_http_body() {
  printf '%s\n' \
    'map $http_x_real_ip $juhe_real_ip { default $http_x_real_ip; "" $remote_addr; }' \
    'map $http_x_forwarded_for $juhe_forwarded_for { default $http_x_forwarded_for; "" $remote_addr; }' \
    'map $http_x_forwarded_proto $juhe_forwarded_proto { default $http_x_forwarded_proto; "" $scheme; }' \
    "log_format juhe_performance '\$remote_addr \$request status=\$status upstream=\$upstream_addr upstream_status=\$upstream_status request_time=\$request_time upstream_time=\$upstream_response_time time=\$time_iso8601';" \
    '' \
    'upstream juhe_ai_gateway_pool {' \
    '    least_conn;'
  index=1
  while [ "$index" -le "$GATEWAY_COUNT" ]; do
    printf '    server 127.0.0.1:%s max_fails=2 fail_timeout=5s;\n' "$((GATEWAY_BASE_PORT + index - 1))"
    index=$((index + 1))
  done
  printf '%s\n' '    keepalive 256;' '}' '' 'upstream juhe_ai_control {'
  printf '    server 127.0.0.1:%s;\n' "$CONTROL_PORT"
  printf '%s\n' '    keepalive 32;' '}' '' 'server {'
  printf '    listen 127.0.0.1:%s;\n' "$INGRESS_PORT"
  printf '%s\n' \
    '    server_name _;' \
    '    client_max_body_size 256m;' \
    "    access_log $LOG_DIR/nginx/access.log juhe_performance;" \
    "    error_log $LOG_DIR/nginx/error.log warn;" \
    "    add_header X-Juhe-Active-Upstream performance-$SLOT always;" \
    '    location ^~ /__aisys__ {' \
    '        proxy_pass http://juhe_ai_control;' \
    '        proxy_http_version 1.1;' \
    '        proxy_set_header Host $host;' \
    '        proxy_set_header Connection "";' \
    '        proxy_set_header X-Real-IP $juhe_real_ip;' \
    '        proxy_set_header X-Forwarded-For $juhe_forwarded_for;' \
    '        proxy_set_header X-Forwarded-Proto $juhe_forwarded_proto;' \
    '        proxy_set_header X-Forwarded-Host $host;' \
    '        proxy_request_buffering off;' \
    '        proxy_buffering off;' \
    '        proxy_read_timeout 3600s;' \
    '        proxy_send_timeout 3600s;' \
    '        proxy_socket_keepalive on;' \
    '        proxy_next_upstream off;' \
    '    }' \
    '    location ^~ /__aipublic__ {' \
    '        proxy_pass http://juhe_ai_control;' \
    '        proxy_http_version 1.1;' \
    '        proxy_set_header Host $host;' \
    '        proxy_set_header Connection "";' \
    '        proxy_set_header X-Real-IP $juhe_real_ip;' \
    '        proxy_set_header X-Forwarded-For $juhe_forwarded_for;' \
    '        proxy_set_header X-Forwarded-Proto $juhe_forwarded_proto;' \
    '        proxy_set_header X-Forwarded-Host $host;' \
    '        proxy_request_buffering off;' \
    '        proxy_buffering off;' \
    '        proxy_read_timeout 3600s;' \
    '        proxy_send_timeout 3600s;' \
    '        proxy_socket_keepalive on;' \
    '        proxy_next_upstream off;' \
    '    }' \
    '    location ^~ /__aiinternal__ {' \
    '        proxy_pass http://juhe_ai_control;' \
    '        proxy_http_version 1.1;' \
    '        proxy_set_header Host $host;' \
    '        proxy_set_header Connection "";' \
    '        proxy_set_header X-Real-IP $juhe_real_ip;' \
    '        proxy_set_header X-Forwarded-For $juhe_forwarded_for;' \
    '        proxy_set_header X-Forwarded-Proto $juhe_forwarded_proto;' \
    '        proxy_set_header X-Forwarded-Host $host;' \
    '        proxy_request_buffering off;' \
    '        proxy_buffering off;' \
    '        proxy_read_timeout 3600s;' \
    '        proxy_send_timeout 3600s;' \
    '        proxy_socket_keepalive on;' \
    '        proxy_next_upstream off;' \
    '    }' \
    '    location / {' \
    '        proxy_pass http://juhe_ai_gateway_pool;' \
    '        proxy_http_version 1.1;' \
    '        proxy_set_header Host $host;' \
    '        proxy_set_header Connection "";' \
    '        proxy_set_header X-Real-IP $juhe_real_ip;' \
    '        proxy_set_header X-Forwarded-For $juhe_forwarded_for;' \
    '        proxy_set_header X-Forwarded-Proto $juhe_forwarded_proto;' \
    '        proxy_set_header X-Forwarded-Host $host;' \
    '        proxy_request_buffering off;' \
    '        proxy_buffering off;' \
    '        proxy_read_timeout 3600s;' \
    '        proxy_send_timeout 3600s;' \
    '        proxy_socket_keepalive on;' \
    '        proxy_next_upstream error timeout http_502 http_503 http_504;' \
    '        proxy_next_upstream_tries 2;' \
    '    }' \
    '}'
}

render_nginx() {
  output="$1"
  if [ "$NGINX_CONFIG_KIND" = included ]; then
    render_nginx_http_body > "$output"
    return
  fi
  {
    printf '%s\n' \
      'worker_processes auto;' \
      "pid $BASE_DIR/nginx-switch/nginx.pid;" \
      '' \
      'events {' \
      '    worker_connections 2048;' \
      '}' \
      '' \
      'http {' \
      '    include /usr/local/etc/nginx/mime.types;' \
      '    default_type application/octet-stream;' \
      '    sendfile on;' \
      '    tcp_nodelay on;' \
      '    gzip off;' \
      '    keepalive_timeout 65;'
    render_nginx_http_body | sed 's/^/    /'
    printf '%s\n' '}'
  } > "$output"
}

wait_for_health() {
  name="$1"
  port="$(service_port "$name")"
  role="$(service_role "$name")"
  consecutive=0
  attempt=1
  while [ "$attempt" -le 40 ]; do
    health_json=
    if launchctl print "$DOMAIN/$(service_label "$name")" >/dev/null 2>&1 \
      && health_json="$(curl -fsS --max-time 2 "http://127.0.0.1:$port/__aisys__/health")" \
      && health_identity_matches "$health_json" "$(service_instance_id "$name")" "$role" \
      && curl -fsS --max-time 2 "http://127.0.0.1:$port/__aisys__/api/health" >/dev/null; then
      consecutive=$((consecutive + 1))
      [ "$consecutive" -ge 3 ] && return 0
    else
      consecutive=0
    fi
    sleep 1
    attempt=$((attempt + 1))
  done
  echo "$name did not remain healthy on port $port" >&2
  return 1
}

wait_for_ingress() {
  consecutive=0
  attempt=1
  while [ "$attempt" -le 20 ]; do
    health_json=
    if health_json="$(curl -fsS --max-time 2 "http://127.0.0.1:$INGRESS_PORT/__aisys__/health")" \
      && health_identity_matches "$health_json" "$(service_instance_id control-1)" control \
      && curl -fsS --max-time 2 "http://127.0.0.1:$INGRESS_PORT/__aisys__/api/health" >/dev/null; then
      consecutive=$((consecutive + 1))
      [ "$consecutive" -ge 3 ] && return 0
    else
      consecutive=0
    fi
    sleep 1
    attempt=$((attempt + 1))
  done
  echo "nginx ingress did not route to the new $SLOT control-1 topology on port $INGRESS_PORT" >&2
  return 1
}

health_identity_matches() {
  node -e '
    const health = JSON.parse(process.argv[1])
    if (health.status !== "ok" || health.instanceId !== process.argv[2] || health.nodeRole !== process.argv[3]) process.exit(1)
  ' "$1" "$2" "$3" >/dev/null 2>&1
}

rollback() {
  set +e
  remove_empty_nginx_config=0
  if [ -f "$NGINX_BACKUP" ]; then
    mv -f -- "$NGINX_BACKUP" "$NGINX_CONFIG"
  else
    : > "$NGINX_CONFIG"
    remove_empty_nginx_config=1
  fi
  nginx_test >/dev/null 2>&1 && nginx_reload >/dev/null 2>&1 || true
  [ "$remove_empty_nginx_config" = 0 ] || rm -f -- "$NGINX_CONFIG"
  for name in $(service_names); do
    plist="$(service_plist_path "$name")"
    run_script="$(service_run_path "$name")"
    launchctl bootout "$DOMAIN" "$plist" >/dev/null 2>&1 || true
    if [ -f "$plist.performance-backup.$$" ]; then mv -f -- "$plist.performance-backup.$$" "$plist"; else rm -f -- "$plist"; fi
    if [ -f "$run_script.performance-backup.$$" ]; then mv -f -- "$run_script.performance-backup.$$" "$run_script"; else rm -f -- "$run_script"; fi
    if [ -f "$STAGE_DIR/$name.was-loaded" ] && [ -f "$plist" ]; then launchctl bootstrap "$DOMAIN" "$plist" >/dev/null 2>&1 || true; fi
  done
}

on_exit() {
  code="$?"
  if [ "$code" -ne 0 ] && [ "$MUTATED" = 1 ]; then
    echo 'performance topology installation failed; rolling back launchd and nginx files' >&2
    rollback
  fi
  [ -z "$STAGE_DIR" ] || rm -rf -- "$STAGE_DIR"
  if [ -n "$DEPLOYMENT_LOCK_LIBRARY" ] && [ "${DEPLOYMENT_LOCK_HELD:-0}" = 1 ]; then
    release_deployment_lock || true
  fi
  exit "$code"
}
trap on_exit EXIT INT TERM

assert_existing_slot_is_inactive_and_drained
if [ -n "$DEPLOYMENT_LOCK_LIBRARY" ]; then
  assert_retired_watchdog_disabled
  acquire_deployment_lock "$BASE_DIR" "performance-topology-$SLOT"
fi
mkdir -p "$BIN_DIR" "$LOG_DIR" "$RUNTIME_LOG_DIR" "$SPOOL_DIR" "$PLIST_DIR" "$(dirname "$NGINX_CONFIG")"
if [ "$SCOPE" = system ]; then
  chown "$SERVICE_USER:$SERVICE_GROUP" "$LOG_DIR" "$RUNTIME_LOG_DIR" "$SPOOL_DIR"
fi
STAGE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/juhe-ai-performance.XXXXXX")"

for name in $(service_names); do
  render_run_script "$name" "$STAGE_DIR/$name.sh"
  render_plist "$name" "$STAGE_DIR/$name.plist"
  plutil -lint "$STAGE_DIR/$name.plist" >/dev/null
done
render_nginx "$STAGE_DIR/nginx.conf"

for name in $(service_names); do
  plist="$(service_plist_path "$name")"
  run_script="$(service_run_path "$name")"
  launchctl print "$DOMAIN/$(service_label "$name")" >/dev/null 2>&1 && touch "$STAGE_DIR/$name.was-loaded"
  [ ! -f "$plist" ] || cp -p -- "$plist" "$plist.performance-backup.$$"
  [ ! -f "$run_script" ] || cp -p -- "$run_script" "$run_script.performance-backup.$$"
done
[ ! -f "$NGINX_CONFIG" ] || cp -p -- "$NGINX_CONFIG" "$NGINX_BACKUP"
MUTATED=1

for name in $(service_names); do
  plist="$(service_plist_path "$name")"
  run_script="$(service_run_path "$name")"
  mv -f -- "$STAGE_DIR/$name.sh" "$run_script"
  mv -f -- "$STAGE_DIR/$name.plist" "$plist"
  launchctl bootout "$DOMAIN" "$plist" >/dev/null 2>&1 || true
  launchctl bootstrap "$DOMAIN" "$plist"
  launchctl kickstart -k "$DOMAIN/$(service_label "$name")"
done
for name in $(service_names); do wait_for_health "$name"; done

mv -f -- "$STAGE_DIR/nginx.conf" "$NGINX_CONFIG"
nginx_test
nginx_reload
wait_for_ingress

for name in $(service_names); do
  rm -f -- "$(service_plist_path "$name").performance-backup.$$" "$(service_run_path "$name").performance-backup.$$"
done
rm -f -- "$NGINX_BACKUP"
MUTATED=0
if [ -n "$DEPLOYMENT_LOCK_LIBRARY" ]; then
  release_deployment_lock || {
    echo 'performance topology is healthy but deployment lock release failed; manual recovery is required' >&2
    exit 1
  }
fi
trap - EXIT INT TERM
rm -rf -- "$STAGE_DIR"
printf 'performance topology installed: slot=%s control=1 gateway=%s usage=%s log=%s stats=1 ops=1 ingress=127.0.0.1:%s\n' \
  "$SLOT" "$GATEWAY_COUNT" "$USAGE_WORKERS" "$LOG_WORKERS" "$INGRESS_PORT"
