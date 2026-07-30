#!/usr/bin/env bash
set -euo pipefail

MODE=dry-run
SCOPE=user
SERVICE_USER=
BASE_DIR="${HOME}/juhe-ai-lite"
RELEASE_DIR=
LABEL_PREFIX=com.example.juhe-ai.performance
CONTROL_PORT=3200
GATEWAY_BASE_PORT=3101
GATEWAY_COUNT=3
USAGE_WORKERS=2
LOG_WORKERS=2
INGRESS_PORT=3000
NGINX_CONFIG=
NGINX_BIN=nginx
NGINX_MAIN_CONFIG=
RUNTIME_DIR=
NGINX_UPSTREAM_SUFFIX=
NODE_PATH="/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
VERIFIED_HEALTH_JSON=
VERIFIED_GATEWAY_METRICS_ROLE_PIDS=
SUDO_BIN=
TEST_BIN=

usage() {
  cat <<'EOF'
Usage: install-performance-topology.sh [--dry-run|--apply] [options]
  --scope user|system
  --service-user USER_FOR_SYSTEM_SCOPE
  --base-dir ABSOLUTE_PATH
  --release-dir ABSOLUTE_RELEASE_PATH
  --label-prefix LAUNCHD_LABEL_PREFIX
  --control-port PORT
  --gateway-base-port PORT
  --gateway-count 1..32
  --usage-workers 1..32
  --log-workers 1..32
  --ingress-port PORT
  --nginx-config ABSOLUTE_INCLUDED_CONF_PATH
  --nginx-bin ABSOLUTE_PATH
  --nginx-main-config ABSOLUTE_PATH
  --runtime-dir ABSOLUTE_ISOLATED_RUNTIME_ROOT
  --nginx-upstream-suffix [A-Za-z0-9_]{1,48}
  --node-path PATH_VALUE
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --dry-run) MODE=dry-run; shift ;;
    --apply) MODE=apply; shift ;;
    --scope) SCOPE="${2:-}"; shift 2 ;;
    --service-user) SERVICE_USER="${2:-}"; shift 2 ;;
    --base-dir) BASE_DIR="${2:-}"; shift 2 ;;
    --release-dir) RELEASE_DIR="${2:-}"; shift 2 ;;
    --label-prefix) LABEL_PREFIX="${2:-}"; shift 2 ;;
    --control-port) CONTROL_PORT="${2:-}"; shift 2 ;;
    --gateway-base-port) GATEWAY_BASE_PORT="${2:-}"; shift 2 ;;
    --gateway-count) GATEWAY_COUNT="${2:-}"; shift 2 ;;
    --usage-workers) USAGE_WORKERS="${2:-}"; shift 2 ;;
    --log-workers) LOG_WORKERS="${2:-}"; shift 2 ;;
    --ingress-port) INGRESS_PORT="${2:-}"; shift 2 ;;
    --nginx-config) NGINX_CONFIG="${2:-}"; shift 2 ;;
    --nginx-bin) NGINX_BIN="${2:-}"; shift 2 ;;
    --nginx-main-config) NGINX_MAIN_CONFIG="${2:-}"; shift 2 ;;
    --runtime-dir) RUNTIME_DIR="${2:-}"; shift 2 ;;
    --nginx-upstream-suffix) NGINX_UPSTREAM_SUFFIX="${2:-}"; shift 2 ;;
    --node-path) NODE_PATH="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown option: $1" >&2; usage >&2; exit 2 ;;
  esac
done

case "$SCOPE" in user|system) ;; *) echo '--scope must be user or system' >&2; exit 2 ;; esac
if [ "$SCOPE" = system ]; then
  [ -n "$SERVICE_USER" ] || { echo '--service-user is required for system scope' >&2; exit 2; }
  printf '%s' "$SERVICE_USER" | grep -Eq '^[A-Za-z_][A-Za-z0-9_.-]{0,63}$' \
    || { echo 'invalid service user' >&2; exit 2; }
  [ "$SERVICE_USER" != root ] || { echo '--service-user must not be root' >&2; exit 2; }
elif [ -n "$SERVICE_USER" ]; then
  echo '--service-user is only valid for system scope' >&2
  exit 2
fi
case "$BASE_DIR" in /*) ;; *) echo '--base-dir must be absolute' >&2; exit 2 ;; esac
if [ -z "$RELEASE_DIR" ]; then RELEASE_DIR="$BASE_DIR/current"; fi
case "$RELEASE_DIR" in /*) ;; *) echo '--release-dir must be absolute' >&2; exit 2 ;; esac
if [ -n "$RUNTIME_DIR" ]; then
  case "$RUNTIME_DIR" in /*) ;; *) echo '--runtime-dir must be absolute' >&2; exit 2 ;; esac
fi
if [ -n "$NGINX_UPSTREAM_SUFFIX" ]; then
  printf '%s' "$NGINX_UPSTREAM_SUFFIX" | grep -Eq '^[A-Za-z0-9_]{1,48}$' \
    || { echo 'invalid nginx upstream suffix' >&2; exit 2; }
fi
if { [ -n "$RUNTIME_DIR" ] && [ -z "$NGINX_UPSTREAM_SUFFIX" ]; } \
  || { [ -z "$RUNTIME_DIR" ] && [ -n "$NGINX_UPSTREAM_SUFFIX" ]; }; then
  echo '--runtime-dir and --nginx-upstream-suffix must be provided together' >&2
  exit 2
fi
case "$BASE_DIR$RELEASE_DIR$RUNTIME_DIR$NODE_PATH$NGINX_CONFIG$NGINX_BIN$NGINX_MAIN_CONFIG" in
  *'$'*|*'`'*|*'"'*|*'\'*|*'|'*|*'&'*|*';'*|*$'\n'*|*$'\r'*)
    echo 'paths contain unsafe shell characters' >&2
    exit 2
    ;;
esac
printf '%s' "$LABEL_PREFIX" | grep -Eq '^[A-Za-z0-9][A-Za-z0-9.-]{0,100}$' || { echo 'invalid launchd label prefix' >&2; exit 2; }
INSTALL_TOKEN="$LABEL_PREFIX.$$"

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
if [ -n "$RUNTIME_DIR" ]; then
  BIN_DIR="$RUNTIME_DIR/bin"
  LOG_DIR="$RUNTIME_DIR/logs"
  RUNTIME_LOG_DIR="$LOG_DIR/runtime"
  SPOOL_DIR="$RUNTIME_DIR/usage-spool"
else
  BIN_DIR="$BASE_DIR/bin/performance"
  LOG_DIR="$BASE_DIR/logs"
  RUNTIME_LOG_DIR="$LOG_DIR/runtime"
  SPOOL_DIR="$BASE_DIR/shared/usage-spool"
fi
if [ -n "$NGINX_UPSTREAM_SUFFIX" ]; then
  GATEWAY_UPSTREAM="juhe_ai_gateway_pool_${NGINX_UPSTREAM_SUFFIX}"
  CONTROL_UPSTREAM="juhe_ai_control_${NGINX_UPSTREAM_SUFFIX}"
else
  GATEWAY_UPSTREAM=juhe_ai_gateway_pool
  CONTROL_UPSTREAM=juhe_ai_control
fi
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
if [ "$NGINX_BIN" != nginx ]; then
  case "$NGINX_BIN" in /*) ;; *) echo '--nginx-bin must be absolute' >&2; exit 2 ;; esac
fi
if [ -n "$NGINX_MAIN_CONFIG" ]; then
  case "$NGINX_MAIN_CONFIG" in /*) ;; *) echo '--nginx-main-config must be absolute' >&2; exit 2 ;; esac
fi

printf 'mode=%s scope=%s base=%s release=%s runtime=%s upstream_suffix=%s control=%s gateways=%s-%s usage=%s log=%s ingress=%s nginx=%s nginx_bin=%s nginx_main=%s service_user=%s\n' \
  "$MODE" "$SCOPE" "$BASE_DIR" "$CURRENT_DIR" "${RUNTIME_DIR:-default}" "${NGINX_UPSTREAM_SUFFIX:-default}" "$CONTROL_PORT" "$GATEWAY_BASE_PORT" "$LAST_GATEWAY_PORT" \
  "$USAGE_WORKERS" "$LOG_WORKERS" "$INGRESS_PORT" "$NGINX_CONFIG" "$NGINX_BIN" \
  "${NGINX_MAIN_CONFIG:-default}" "${SERVICE_USER:-current}"
printf 'plan: restart and verify %s gateway publishers one by one, restart control/workers last, then nginx least_conn cutover\n' "$GATEWAY_COUNT"
[ "$MODE" = apply ] || exit 0

[ -d "$CURRENT_DIR" ] || { echo "missing release directory: $CURRENT_DIR" >&2; exit 1; }
CURRENT_DIR="$(cd "$CURRENT_DIR" && pwd -P)"
case "$CURRENT_DIR" in
  *'$'*|*'`'*|*'"'*|*'\'*|*'|'*|*'&'*|*';'*|*$'\n'*|*$'\r'*)
    echo 'resolved release path contains unsafe shell characters' >&2
    exit 2
    ;;
esac
[ -f "$CURRENT_DIR/backend/dist/server.js" ] || { echo "missing built server: $CURRENT_DIR/backend/dist/server.js" >&2; exit 1; }
[ -f "$CURRENT_DIR/backend/dist/scripts/preflight/check-node-sqlite.js" ] || { echo 'missing runtime preflight script' >&2; exit 1; }
[ -f "$CURRENT_DIR/backend/dist/scripts/preflight/check-performance-process-metrics-registry.js" ] || { echo 'missing performance metrics registry preflight script' >&2; exit 1; }
[ -f "$CURRENT_DIR/backend/.env" ] || { echo 'missing release backend/.env' >&2; exit 1; }
NODE_BIN="$(command -v node)"
command -v launchctl >/dev/null
command -v plutil >/dev/null
command -v curl >/dev/null
if [ "$NGINX_BIN" = nginx ]; then NGINX_BIN="$(command -v nginx)"; fi
[ -x "$NGINX_BIN" ] || { echo "nginx binary is not executable: $NGINX_BIN" >&2; exit 1; }
if [ -n "$NGINX_MAIN_CONFIG" ]; then
  [ -f "$NGINX_MAIN_CONFIG" ] && [ ! -L "$NGINX_MAIN_CONFIG" ] \
    || { echo "nginx main config is not a regular file: $NGINX_MAIN_CONFIG" >&2; exit 1; }
fi
if [ "$SCOPE" = system ]; then
  [ "$(id -u)" -eq 0 ] || { echo 'system scope requires root' >&2; exit 1; }
  id "$SERVICE_USER" >/dev/null 2>&1 || { echo "service user does not exist: $SERVICE_USER" >&2; exit 1; }
  [ "$(id -u "$SERVICE_USER")" -ne 0 ] || { echo '--service-user must resolve to a non-root uid' >&2; exit 1; }
fi

assert_runtime_directory() {
  path="$1"
  [ -d "$path" ] && [ ! -L "$path" ] \
    || { echo "runtime path must be a real directory: $path" >&2; return 1; }
  resolved_path="$(cd "$path" && pwd -P)" || return 1
  case "$resolved_path" in
    "$RESOLVED_BASE_DIR"/*) ;;
    *) echo "runtime path escapes the physical base directory: $path" >&2; return 1 ;;
  esac
}

assert_isolated_runtime_parent() {
  isolated_runtime_root="$1"
  probe_path="$isolated_runtime_root"
  while [ ! -e "$probe_path" ]; do
    parent_path="$(dirname "$probe_path")"
    [ "$parent_path" != "$probe_path" ] \
      || { echo "isolated runtime root has no existing parent: $isolated_runtime_root" >&2; return 1; }
    probe_path="$parent_path"
  done
  [ -d "$probe_path" ] && [ ! -L "$probe_path" ] \
    || { echo "isolated runtime root has an unsafe existing ancestor: $probe_path" >&2; return 1; }
  resolved_probe_path="$(cd "$probe_path" && pwd -P)" || return 1
  case "$resolved_probe_path" in
    "$RESOLVED_BASE_DIR"|"$RESOLVED_BASE_DIR"/*) ;;
    *) echo "isolated runtime root escapes the physical base directory: $isolated_runtime_root" >&2; return 1 ;;
  esac
}

runtime_managed_paths() {
  if [ -n "$RUNTIME_DIR" ]; then
    printf '%s\n' "$BASE_DIR" "$RUNTIME_DIR" "$BIN_DIR" "$LOG_DIR" "$RUNTIME_LOG_DIR" "$SPOOL_DIR"
  else
    printf '%s\n' "$BASE_DIR" "$LOG_DIR" "$RUNTIME_LOG_DIR" "$BASE_DIR/shared" "$SPOOL_DIR"
  fi
}

migrate_runtime_ownership() {
  for runtime_root in "$LOG_DIR" "$SPOOL_DIR"; do
    find "$runtime_root" -xdev -exec chown -h "$SERVICE_USER" {} +
  done
}

assert_release_read_only() {
  if ! "$SUDO_BIN" -n -u "$SERVICE_USER" /bin/bash -s -- "$CURRENT_DIR" <<'EOF'
set -euo pipefail
release_dir="$1"
while IFS= read -r -d '' release_entry; do
  if [ -w "$release_entry" ]; then
    printf '%s\n' "$release_entry" >&2
    exit 1
  fi
done < <(find "$release_dir" -xdev \( -type d -o -type f \) -print0)
EOF
  then
    echo 'release entry must not be writable by the service user' >&2
    return 1
  fi
}

if [ "$SCOPE" = system ]; then
  while IFS= read -r managed_path; do
    [ ! -L "$managed_path" ] || { echo "system runtime path must not be a symbolic link: $managed_path" >&2; exit 1; }
  done <<EOF
$(runtime_managed_paths)
EOF
fi
if [ -n "$RUNTIME_DIR" ]; then
  [ -d "$BASE_DIR" ] || { echo '--runtime-dir requires an existing base directory' >&2; exit 1; }
  RESOLVED_BASE_DIR="$(cd "$BASE_DIR" && pwd -P)" || exit 1
  [ "$RESOLVED_BASE_DIR" != / ] || { echo 'isolated runtime base directory must not resolve to /' >&2; exit 1; }
  assert_isolated_runtime_parent "$RUNTIME_DIR"
fi
mkdir -p "$BIN_DIR" "$LOG_DIR" "$RUNTIME_LOG_DIR" "$SPOOL_DIR" "$PLIST_DIR" "$(dirname "$NGINX_CONFIG")"
if [ -n "$RUNTIME_DIR" ]; then
  for runtime_path in "$RUNTIME_DIR" "$BIN_DIR" "$LOG_DIR" "$RUNTIME_LOG_DIR" "$SPOOL_DIR"; do assert_runtime_directory "$runtime_path"; done
fi
if [ "$SCOPE" = system ]; then
  SUDO_BIN="$(command -v sudo 2>/dev/null || true)"
  for candidate in /usr/bin/test /bin/test; do
    if [ -x "$candidate" ]; then TEST_BIN="$candidate"; break; fi
  done
  [ -n "$SUDO_BIN" ] && [ -n "$TEST_BIN" ] || { echo 'system scope requires sudo and test' >&2; exit 1; }
  "$SUDO_BIN" -n -u "$SERVICE_USER" "$TEST_BIN" -d "$BASE_DIR" \
    || { echo 'service user cannot access the base directory' >&2; exit 1; }
  if "$SUDO_BIN" -n -u "$SERVICE_USER" "$TEST_BIN" -w "$BASE_DIR"; then
    echo 'system base directory must not be writable by the service user' >&2
    exit 1
  fi
  RESOLVED_BASE_DIR="$(cd "$BASE_DIR" && pwd -P)"
  [ "$RESOLVED_BASE_DIR" != / ] || { echo 'system base directory must not resolve to /' >&2; exit 1; }
  for runtime_path in "$LOG_DIR" "$RUNTIME_LOG_DIR" "$SPOOL_DIR"; do assert_runtime_directory "$runtime_path"; done
  if "$SUDO_BIN" -n -u "$SERVICE_USER" "$TEST_BIN" -w "$CURRENT_DIR"; then
    echo 'release directory must not be writable by the service user' >&2
    exit 1
  fi
  assert_release_read_only
  for readable in \
    "$CURRENT_DIR/backend/dist/server.js" \
    "$CURRENT_DIR/backend/dist/scripts/preflight/check-node-sqlite.js" \
    "$CURRENT_DIR/backend/.env"; do
    "$SUDO_BIN" -n -u "$SERVICE_USER" "$TEST_BIN" -r "$readable" \
      || { echo "service user cannot read required release file: $readable" >&2; exit 1; }
    if "$SUDO_BIN" -n -u "$SERVICE_USER" "$TEST_BIN" -w "$readable"; then
      echo "required release file must not be writable by the service user: $readable" >&2
      exit 1
    fi
  done
  "$SUDO_BIN" -n -u "$SERVICE_USER" "$TEST_BIN" -x "$NODE_BIN" \
    || { echo "service user cannot execute node: $NODE_BIN" >&2; exit 1; }
  migrate_runtime_ownership
  for runtime_path in "$LOG_DIR" "$RUNTIME_LOG_DIR" "$SPOOL_DIR"; do assert_runtime_directory "$runtime_path"; done
  for writable in "$LOG_DIR" "$RUNTIME_LOG_DIR" "$SPOOL_DIR"; do
    "$SUDO_BIN" -n -u "$SERVICE_USER" "$TEST_BIN" -w "$writable" \
      || { echo "service user cannot write runtime directory: $writable" >&2; exit 1; }
  done
fi
STAGE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/juhe-ai-performance.XXXXXX")"
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

nginx_test() {
  if [ -n "$NGINX_MAIN_CONFIG" ]; then
    "$NGINX_BIN" -t -c "$NGINX_MAIN_CONFIG"
  else
    "$NGINX_BIN" -t
  fi
}

nginx_reload() {
  if [ -n "$NGINX_MAIN_CONFIG" ]; then
    "$NGINX_BIN" -s reload -c "$NGINX_MAIN_CONFIG"
  else
    "$NGINX_BIN" -s reload
  fi
}

activation_service_names() {
  index=1
  while [ "$index" -le "$GATEWAY_COUNT" ]; do
    printf 'gateway-%s\n' "$index"
    index=$((index + 1))
  done
  printf '%s\n' control-1
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

service_label() { printf '%s.%s' "$LABEL_PREFIX" "$1"; }
service_run_path() { printf '%s/%s.sh' "$BIN_DIR" "$1"; }
service_plist_path() { printf '%s/%s.plist' "$PLIST_DIR" "$(service_label "$1")"; }

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
    printf 'export JUHE_AI_INSTANCE_ID=%s\n' "$name"
    printf 'export JUHE_AI_HOST=127.0.0.1\n'
    printf 'export JUHE_AI_PORT=%s\n' "$port"
    if [ "$role" = gateway ]; then
      printf 'export JUHE_AI_ACCOUNT_HEALTH_CHECK_DISPATCH_URL=http://127.0.0.1:%s\n' "$CONTROL_PORT"
    fi
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
  stdout_path="$(xml_escape "$LOG_DIR/launchd.$name.out.log")"
  stderr_path="$(xml_escape "$LOG_DIR/launchd.$name.err.log")"
  service_user_xml=
  if [ "$SCOPE" = system ]; then service_user_xml="<key>UserName</key><string>$(xml_escape "$SERVICE_USER")</string>"; fi
  cat > "$output" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>$label</string>
<key>ProgramArguments</key><array><string>/bin/bash</string><string>$run_path</string></array>
<key>WorkingDirectory</key><string>$work_dir</string>
$service_user_xml
<key>KeepAlive</key><true/><key>RunAtLoad</key><true/><key>ThrottleInterval</key><integer>5</integer>
<key>SoftResourceLimits</key><dict><key>NumberOfFiles</key><integer>65536</integer></dict>
<key>HardResourceLimits</key><dict><key>NumberOfFiles</key><integer>131072</integer></dict>
<key>StandardOutPath</key><string>$stdout_path</string>
<key>StandardErrorPath</key><string>$stderr_path</string>
</dict></plist>
EOF
  chmod 644 "$output"
}

render_nginx() {
  output="$1"
  {
    printf 'upstream %s {\n' "$GATEWAY_UPSTREAM"
    printf '%s\n' '    least_conn;'
    index=1
    while [ "$index" -le "$GATEWAY_COUNT" ]; do
      printf '    server 127.0.0.1:%s max_fails=2 fail_timeout=5s;\n' "$((GATEWAY_BASE_PORT + index - 1))"
      index=$((index + 1))
    done
    printf '%s\n' '    keepalive 256;' '}' ''
    printf 'upstream %s {\n' "$CONTROL_UPSTREAM"
    printf '    server 127.0.0.1:%s;\n' "$CONTROL_PORT"
    printf '%s\n' '    keepalive 32;' '}' '' 'server {'
    printf '    listen 127.0.0.1:%s;\n' "$INGRESS_PORT"
    printf '    add_header X-Juhe-Topology-Install "%s" always;\n' "$INSTALL_TOKEN"
    printf '%s\n' \
      '    client_max_body_size 256m;' \
      '    location = /__aisys__ {' \
      "        proxy_pass http://$CONTROL_UPSTREAM;" \
      '    }' \
      '    location ^~ /__aisys__/ {' \
      "        proxy_pass http://$CONTROL_UPSTREAM;" \
      '        proxy_http_version 1.1;' \
      '        proxy_set_header Host $host;' \
      '        proxy_set_header X-Real-IP $http_x_real_ip;' \
      '        proxy_set_header X-Forwarded-For $http_x_forwarded_for;' \
      '        proxy_set_header X-Forwarded-Proto $http_x_forwarded_proto;' \
      '    }' \
      '    location = /__aipublic__ {' \
      "        proxy_pass http://$CONTROL_UPSTREAM;" \
      '    }' \
      '    location ^~ /__aipublic__/ {' \
      "        proxy_pass http://$CONTROL_UPSTREAM;" \
      '        proxy_http_version 1.1;' \
      '        proxy_set_header Host $host;' \
      '        proxy_set_header X-Real-IP $http_x_real_ip;' \
      '        proxy_set_header X-Forwarded-For $http_x_forwarded_for;' \
      '        proxy_set_header X-Forwarded-Proto $http_x_forwarded_proto;' \
      '    }' \
      '    location = /__aiinternal__ {' \
      "        proxy_pass http://$CONTROL_UPSTREAM;" \
      '    }' \
      '    location ^~ /__aiinternal__/ {' \
      "        proxy_pass http://$CONTROL_UPSTREAM;" \
      '        proxy_http_version 1.1;' \
      '        proxy_set_header Host $host;' \
      '        proxy_set_header X-Real-IP $http_x_real_ip;' \
      '        proxy_set_header X-Forwarded-For $http_x_forwarded_for;' \
      '        proxy_set_header X-Forwarded-Proto $http_x_forwarded_proto;' \
      '    }' \
      '    location / {' \
      "        proxy_pass http://$GATEWAY_UPSTREAM;" \
      '        proxy_http_version 1.1;' \
      '        proxy_set_header Host $host;' \
      '        proxy_set_header X-Real-IP $http_x_real_ip;' \
      '        proxy_set_header X-Forwarded-For $http_x_forwarded_for;' \
      '        proxy_set_header X-Forwarded-Proto $http_x_forwarded_proto;' \
      '        proxy_set_header Connection "";' \
      '        proxy_request_buffering off;' \
      '        proxy_buffering off;' \
      '        proxy_read_timeout 900s;' \
      '        proxy_send_timeout 900s;' \
      '        proxy_next_upstream off;' \
      '    }' \
      '}'
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
      && health_identity_matches "$health_json" "$name" "$role" \
      && curl -fsS --max-time 2 "http://127.0.0.1:$port/__aisys__/api/health" >/dev/null; then
      consecutive=$((consecutive + 1))
      if [ "$consecutive" -ge 3 ]; then
        VERIFIED_HEALTH_JSON="$health_json"
        return 0
      fi
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
      && health_identity_matches "$health_json" control-1 control \
      && curl -fsS --max-time 2 -D - -o /dev/null "http://127.0.0.1:$INGRESS_PORT/__aisys__/health" \
        | tr -d '\r' | grep -Fqx "X-Juhe-Topology-Install: $INSTALL_TOKEN" \
      && curl -fsS --max-time 2 "http://127.0.0.1:$INGRESS_PORT/__aisys__/api/health" >/dev/null; then
      consecutive=$((consecutive + 1))
      [ "$consecutive" -ge 3 ] && return 0
    else
      consecutive=0
    fi
    sleep 1
    attempt=$((attempt + 1))
  done
  echo "nginx ingress did not remain on control-1 after reload on port $INGRESS_PORT" >&2
  return 1
}

wait_for_metrics_registry() {
  name="$1"
  observed_after_ms="$2"
  set -- node "$CURRENT_DIR/backend/dist/scripts/preflight/check-performance-process-metrics-registry.js" --timeout-ms 30000 --observed-after-ms "$observed_after_ms"
  if [ "$name" = control-1 ]; then
    set -- "$@" --role "control:$name" --role "db-service:$name"
    index=1
    while [ "$index" -le "$GATEWAY_COUNT" ]; do
      set -- "$@" --role "gateway:gateway-$index" --role "db-service:gateway-$index"
      index=$((index + 1))
    done
    index=1
    while [ "$index" -le "$USAGE_WORKERS" ]; do set -- "$@" --role "usage-worker:$index"; index=$((index + 1)); done
    index=1
    while [ "$index" -le "$LOG_WORKERS" ]; do set -- "$@" --role "log-worker:$index"; index=$((index + 1)); done
    set -- "$@" --role stats-worker:1 --role ops-worker:1
  else
    set -- "$@" --role "gateway:$name" --role "db-service:$name"
  fi
  current_role_pid_lines="$(metrics_registry_role_pids "$VERIFIED_HEALTH_JSON")"
  role_pid_lines="$current_role_pid_lines"
  if [ "$name" = control-1 ] && [ -n "$VERIFIED_GATEWAY_METRICS_ROLE_PIDS" ]; then
    role_pid_lines="$VERIFIED_GATEWAY_METRICS_ROLE_PIDS
$current_role_pid_lines"
  fi
  [ -n "$role_pid_lines" ] || { echo "$name health topology did not provide metrics PIDs" >&2; return 1; }
  while IFS= read -r role_pid; do
    [ -n "$role_pid" ] || continue
    set -- "$@" --role-pid "$role_pid"
  done <<EOF
$role_pid_lines
EOF
  if ! NODE_ENV=production \
    JUHE_AI_RUNTIME_MODE=performance \
    JUHE_AI_PERFORMANCE_NODE_ROLE=control \
    JUHE_AI_PROCESS_ROLE=server \
    JUHE_AI_INSTANCE_ID=metrics-registry-preflight \
    JUHE_AI_GATEWAY_REPLICAS="$GATEWAY_COUNT" \
    JUHE_AI_USAGE_WORKER_REPLICAS="$USAGE_WORKERS" \
    JUHE_AI_LOG_WORKER_REPLICAS="$LOG_WORKERS" \
    JUHE_AI_STATS_WORKER_REPLICAS=1 \
    JUHE_AI_OPS_WORKER_REPLICAS=1 \
    JUHE_AI_LOG_FILE_ENABLED=false \
    JUHE_AI_LOG_CONSOLE_ENABLED=false \
    "$@"; then
    return 1
  fi
  if [ "$name" != control-1 ]; then
    if [ -n "$VERIFIED_GATEWAY_METRICS_ROLE_PIDS" ]; then
      VERIFIED_GATEWAY_METRICS_ROLE_PIDS="$VERIFIED_GATEWAY_METRICS_ROLE_PIDS
$current_role_pid_lines"
    else
      VERIFIED_GATEWAY_METRICS_ROLE_PIDS="$current_role_pid_lines"
    fi
  fi
}

metrics_registry_role_pids() {
  node -e '
    const health = JSON.parse(process.argv[1])
    const mappings = []
    const add = (role, pid) => {
      if (typeof role !== "string" || !role || !Number.isSafeInteger(pid) || pid <= 1) process.exit(2)
      mappings.push(`${role}=${pid}`)
    }
    add(`${health.nodeRole}:${health.instanceId}`, health.processPid)
    add(`db-service:${health.instanceId}`, health.dbServicePid)
    for (const worker of health.workerProcesses ?? []) {
      add(`${worker.role}:${worker.replicaIndex + 1}`, worker.pid)
    }
    process.stdout.write(mappings.join("\n"))
  ' "$1"
}

performance_metrics_registry_time_ms() {
  NODE_ENV=production \
  JUHE_AI_RUNTIME_MODE=performance \
  JUHE_AI_PERFORMANCE_NODE_ROLE=control \
  JUHE_AI_PROCESS_ROLE=server \
  JUHE_AI_INSTANCE_ID=metrics-registry-preflight \
  JUHE_AI_LOG_FILE_ENABLED=false \
  JUHE_AI_LOG_CONSOLE_ENABLED=false \
  node "$CURRENT_DIR/backend/dist/scripts/preflight/check-performance-process-metrics-registry.js" --print-redis-time-ms
}

health_identity_matches() {
  node -e '
    const health = JSON.parse(process.argv[1])
    if (health.status !== "ok" || health.instanceId !== process.argv[2] || health.nodeRole !== process.argv[3]) process.exit(1)
  ' "$1" "$2" "$3" >/dev/null 2>&1
}

rollback() {
  set +e
  rollback_failed=0
  nginx_candidate=
  if [ -f "$NGINX_BACKUP" ]; then
    mv -f -- "$NGINX_BACKUP" "$NGINX_CONFIG"
  else
    nginx_candidate="$NGINX_CONFIG.performance-candidate.$$"
    if [ -f "$NGINX_CONFIG" ]; then mv -f -- "$NGINX_CONFIG" "$nginx_candidate"; fi
  fi
  if ! nginx_test >/dev/null 2>&1 || ! nginx_reload >/dev/null 2>&1; then
    if [ -n "$nginx_candidate" ] && [ -f "$nginx_candidate" ]; then mv -f -- "$nginx_candidate" "$NGINX_CONFIG"; fi
    echo 'previous nginx state could not be reloaded; preserving candidate services for manual recovery' >&2
    return 1
  fi
  if [ -n "$nginx_candidate" ]; then rm -f -- "$nginx_candidate"; fi
  for name in $(service_names); do
    plist="$(service_plist_path "$name")"
    run_script="$(service_run_path "$name")"
    launchctl bootout "$DOMAIN" "$plist" >/dev/null 2>&1 || true
    if [ -f "$plist.performance-backup.$$" ]; then mv -f -- "$plist.performance-backup.$$" "$plist"; else rm -f -- "$plist"; fi
    if [ -f "$run_script.performance-backup.$$" ]; then mv -f -- "$run_script.performance-backup.$$" "$run_script"; else rm -f -- "$run_script"; fi
    if [ -f "$STAGE_DIR/$name.was-loaded" ] && [ -f "$plist" ]; then
      if ! launchctl bootstrap "$DOMAIN" "$plist" >/dev/null 2>&1; then
        echo "previous launchd service could not be restored: $(service_label "$name")" >&2
        rollback_failed=1
      fi
    fi
  done
  return "$rollback_failed"
}

on_exit() {
  code="$?"
  if [ "$code" -ne 0 ] && [ "$MUTATED" = 1 ]; then
    echo 'performance topology installation failed; rolling back launchd and nginx files' >&2
    rollback || echo 'performance topology rollback was incomplete; manual recovery is required' >&2
  fi
  rm -rf -- "$STAGE_DIR"
  exit "$code"
}
trap on_exit EXIT INT TERM

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

for name in $(activation_service_names); do
  plist="$(service_plist_path "$name")"
  run_script="$(service_run_path "$name")"
  mv -f -- "$STAGE_DIR/$name.sh" "$run_script"
  mv -f -- "$STAGE_DIR/$name.plist" "$plist"
  launchctl bootout "$DOMAIN" "$plist" >/dev/null 2>&1 || true
  metrics_fence_ms="$(performance_metrics_registry_time_ms)"
  launchctl bootstrap "$DOMAIN" "$plist"
  launchctl kickstart -k "$DOMAIN/$(service_label "$name")"
  wait_for_health "$name"
  wait_for_metrics_registry "$name" "$metrics_fence_ms"
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
trap - EXIT INT TERM
rm -rf -- "$STAGE_DIR"
printf 'performance topology installed: control=1 gateway=%s usage=%s log=%s stats=1 ops=1 ingress=127.0.0.1:%s\n' \
  "$GATEWAY_COUNT" "$USAGE_WORKERS" "$LOG_WORKERS" "$INGRESS_PORT"
