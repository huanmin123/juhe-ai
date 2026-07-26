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
CANDIDATE_INGRESS_PORT=3000
NGINX_BIN=
NGINX_CONFIG=
ACTIVE_APP_FILE=
ACTIVE_DB_FILE=
ACTIVE_LABEL_FILE=
PUBLIC_HEALTH_BASE_URL=
CANDIDATE_LABEL=
EXPECTED_CURRENT_LABEL=
SAMPLES=3
CONFIG_MUTATED=0
SWITCH_COMMITTED=0
STAGE_DIR=
LOCK_DIR=

usage() {
  cat <<'EOF'
Usage: switch-performance-slot.sh [--dry-run|--apply] [options]
  --release ABSOLUTE_PATH
  --scope user|system
  --label-prefix LAUNCHD_LABEL_PREFIX
  --control-port PORT
  --gateway-base-port PORT
  --gateway-count 1..32
  --usage-workers 1..32
  --log-workers 1..32
  --candidate-ingress-port PORT
  --nginx-bin ABSOLUTE_PATH
  --nginx-config ABSOLUTE_PATH
  --active-app-file ABSOLUTE_PATH
  --active-db-file ABSOLUTE_PATH
  --active-label-file ABSOLUTE_PATH
  --public-health-base-url http://HOST:PORT
  --candidate-label LABEL
  --expected-current-label LABEL
  --samples 1..60

After nginx reload succeeds, failed post-switch checks do not switch back. The
command exits non-zero with FORWARD_FIX_REQUIRED and leaves the candidate active.
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --dry-run) MODE=dry-run; shift ;;
    --apply) MODE=apply; shift ;;
    --release) RELEASE="${2:-}"; shift 2 ;;
    --scope) SCOPE="${2:-}"; shift 2 ;;
    --label-prefix) LABEL_PREFIX="${2:-}"; shift 2 ;;
    --control-port) CONTROL_PORT="${2:-}"; shift 2 ;;
    --gateway-base-port) GATEWAY_BASE_PORT="${2:-}"; shift 2 ;;
    --gateway-count) GATEWAY_COUNT="${2:-}"; shift 2 ;;
    --usage-workers) USAGE_WORKERS="${2:-}"; shift 2 ;;
    --log-workers) LOG_WORKERS="${2:-}"; shift 2 ;;
    --candidate-ingress-port) CANDIDATE_INGRESS_PORT="${2:-}"; shift 2 ;;
    --nginx-bin) NGINX_BIN="${2:-}"; shift 2 ;;
    --nginx-config) NGINX_CONFIG="${2:-}"; shift 2 ;;
    --active-app-file) ACTIVE_APP_FILE="${2:-}"; shift 2 ;;
    --active-db-file) ACTIVE_DB_FILE="${2:-}"; shift 2 ;;
    --active-label-file) ACTIVE_LABEL_FILE="${2:-}"; shift 2 ;;
    --public-health-base-url) PUBLIC_HEALTH_BASE_URL="${2:-}"; shift 2 ;;
    --candidate-label) CANDIDATE_LABEL="${2:-}"; shift 2 ;;
    --expected-current-label) EXPECTED_CURRENT_LABEL="${2:-}"; shift 2 ;;
    --samples) SAMPLES="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown option: $1" >&2; usage >&2; exit 2 ;;
  esac
done

case "$SCOPE" in user|system) ;; *) echo '--scope must be user or system' >&2; exit 2 ;; esac
for required_path in "$RELEASE" "$NGINX_BIN" "$NGINX_CONFIG" "$ACTIVE_APP_FILE" "$ACTIVE_DB_FILE" "$ACTIVE_LABEL_FILE"; do
  case "$required_path" in /*) ;; *) echo 'release and nginx paths must be absolute' >&2; exit 2 ;; esac
done
case "$PUBLIC_HEALTH_BASE_URL" in http://*|https://*) ;; *) echo 'public health base URL must use http or https' >&2; exit 2 ;; esac
PUBLIC_HEALTH_BASE_URL="${PUBLIC_HEALTH_BASE_URL%/}"
case "$RELEASE$NGINX_BIN$NGINX_CONFIG$ACTIVE_APP_FILE$ACTIVE_DB_FILE$ACTIVE_LABEL_FILE$PUBLIC_HEALTH_BASE_URL" in
  *'$'*|*'`'*|*'"'*|*'\'*|*'|'*|*'&'*|*';'*|*$'\n'*|*$'\r'*)
    echo 'path or URL contains unsafe shell characters' >&2
    exit 2
    ;;
esac
for label in "$LABEL_PREFIX" "$CANDIDATE_LABEL" "$EXPECTED_CURRENT_LABEL"; do
  printf '%s' "$label" | grep -Eq '^[A-Za-z0-9][A-Za-z0-9._~-]{0,100}$' \
    || { echo "invalid label: $label" >&2; exit 2; }
done

assert_number() {
  local value="$1" min="$2" max="$3" name="$4"
  printf '%s' "$value" | grep -Eq '^[0-9]+$' || { echo "$name must be numeric" >&2; exit 2; }
  [ "$value" -ge "$min" ] && [ "$value" -le "$max" ] \
    || { echo "$name must be between $min and $max" >&2; exit 2; }
}

assert_number "$CONTROL_PORT" 1 65535 control-port
assert_number "$GATEWAY_BASE_PORT" 1 65535 gateway-base-port
assert_number "$GATEWAY_COUNT" 1 32 gateway-count
assert_number "$USAGE_WORKERS" 1 32 usage-workers
assert_number "$LOG_WORKERS" 1 32 log-workers
assert_number "$CANDIDATE_INGRESS_PORT" 1 65535 candidate-ingress-port
assert_number "$SAMPLES" 1 60 samples

active_dir="$(dirname -- "$ACTIVE_APP_FILE")"
[ "$(dirname -- "$ACTIVE_DB_FILE")" = "$active_dir" ] \
  && [ "$(dirname -- "$ACTIVE_LABEL_FILE")" = "$active_dir" ] \
  || { echo 'active nginx files must share one directory' >&2; exit 2; }

printf 'mode=%s release=%s topology=%s candidate_ingress=%s current_label=%s candidate_label=%s public=%s\n' \
  "$MODE" "$RELEASE" "$LABEL_PREFIX" "$CANDIDATE_INGRESS_PORT" "$EXPECTED_CURRENT_LABEL" "$CANDIDATE_LABEL" "$PUBLIC_HEALTH_BASE_URL"
[ "$MODE" = apply ] || exit 0

for command_name in curl grep awk sed mktemp mv cp rm mkdir rmdir dirname node sleep; do
  command -v "$command_name" >/dev/null || { echo "missing command: $command_name" >&2; exit 1; }
done
[ -x "$NGINX_BIN" ] || { echo "nginx binary is not executable: $NGINX_BIN" >&2; exit 1; }
for file in "$NGINX_CONFIG" "$ACTIVE_APP_FILE" "$ACTIVE_DB_FILE" "$ACTIVE_LABEL_FILE"; do
  [ -f "$file" ] && [ ! -L "$file" ] || { echo "nginx file is not a regular file: $file" >&2; exit 1; }
done

TOPOLOGY_VERIFIER="$RELEASE/docs/deploy/macos/operations/verify-performance-topology.sh"
[ -f "$TOPOLOGY_VERIFIER" ] || { echo "missing topology verifier: $TOPOLOGY_VERIFIER" >&2; exit 1; }
/bin/bash "$TOPOLOGY_VERIFIER" --apply \
  --release "$RELEASE" --scope "$SCOPE" --label-prefix "$LABEL_PREFIX" \
  --control-port "$CONTROL_PORT" --gateway-base-port "$GATEWAY_BASE_PORT" --gateway-count "$GATEWAY_COUNT" \
  --usage-workers "$USAGE_WORKERS" --log-workers "$LOG_WORKERS" \
  --ingress-port "$CANDIDATE_INGRESS_PORT" --samples "$SAMPLES"

current_label="$(awk '$1 == "add_header" && $2 == "X-Juhe-Active-Upstream" { value=$3; gsub(/^"|"$/, "", value); print value; exit }' "$ACTIVE_LABEL_FILE")"
[ "$current_label" = "$EXPECTED_CURRENT_LABEL" ] \
  || { echo "active label changed: expected=$EXPECTED_CURRENT_LABEL actual=${current_label:-missing}" >&2; exit 1; }

LOCK_DIR="$active_dir/.performance-forward-switch.lock"
mkdir "$LOCK_DIR" 2>/dev/null || { echo "performance switch lock is already held: $LOCK_DIR" >&2; exit 1; }
STAGE_DIR="$(mktemp -d "$active_dir/.performance-forward-switch.XXXXXX")"

restore_uncommitted_config() {
  set +e
  cp -p -- "$STAGE_DIR/app.previous" "$ACTIVE_APP_FILE.restore.$$" \
    && cp -p -- "$STAGE_DIR/db.previous" "$ACTIVE_DB_FILE.restore.$$" \
    && cp -p -- "$STAGE_DIR/label.previous" "$ACTIVE_LABEL_FILE.restore.$$" \
    && mv -f -- "$ACTIVE_APP_FILE.restore.$$" "$ACTIVE_APP_FILE" \
    && mv -f -- "$ACTIVE_DB_FILE.restore.$$" "$ACTIVE_DB_FILE" \
    && mv -f -- "$ACTIVE_LABEL_FILE.restore.$$" "$ACTIVE_LABEL_FILE" \
    && "$NGINX_BIN" -t -c "$NGINX_CONFIG" >/dev/null 2>&1 \
    && "$NGINX_BIN" -s reload -c "$NGINX_CONFIG" >/dev/null 2>&1
}

on_exit() {
  code="$?"
  set +e
  if [ "$code" -ne 0 ] && [ "$CONFIG_MUTATED" = 1 ] && [ "$SWITCH_COMMITTED" != 1 ]; then
    restore_uncommitted_config || echo 'nginx pre-commit configuration restoration failed' >&2
  elif [ "$code" -ne 0 ] && [ "$SWITCH_COMMITTED" = 1 ]; then
    echo "FORWARD_FIX_REQUIRED label=$CANDIDATE_LABEL ingress=$CANDIDATE_INGRESS_PORT" >&2
  fi
  rm -rf -- "$STAGE_DIR"
  rmdir "$LOCK_DIR" 2>/dev/null || true
  exit "$code"
}
trap on_exit EXIT INT TERM

cp -p -- "$ACTIVE_APP_FILE" "$STAGE_DIR/app.previous"
cp -p -- "$ACTIVE_DB_FILE" "$STAGE_DIR/db.previous"
cp -p -- "$ACTIVE_LABEL_FILE" "$STAGE_DIR/label.previous"
printf 'server 127.0.0.1:%s;\n' "$CANDIDATE_INGRESS_PORT" > "$STAGE_DIR/app.next"
printf 'server 127.0.0.1:%s;\n' "$CANDIDATE_INGRESS_PORT" > "$STAGE_DIR/db.next"
printf 'add_header X-Juhe-Active-Upstream "%s" always;\n' "$CANDIDATE_LABEL" > "$STAGE_DIR/label.next"

cp -- "$STAGE_DIR/app.next" "$ACTIVE_APP_FILE.next.$$"
cp -- "$STAGE_DIR/db.next" "$ACTIVE_DB_FILE.next.$$"
cp -- "$STAGE_DIR/label.next" "$ACTIVE_LABEL_FILE.next.$$"
CONFIG_MUTATED=1
mv -f -- "$ACTIVE_APP_FILE.next.$$" "$ACTIVE_APP_FILE"
mv -f -- "$ACTIVE_DB_FILE.next.$$" "$ACTIVE_DB_FILE"
mv -f -- "$ACTIVE_LABEL_FILE.next.$$" "$ACTIVE_LABEL_FILE"
"$NGINX_BIN" -t -c "$NGINX_CONFIG"
"$NGINX_BIN" -s reload -c "$NGINX_CONFIG"
SWITCH_COMMITTED=1

sample=1
while [ "$sample" -le "$SAMPLES" ]; do
  headers="$(mktemp "${TMPDIR:-/tmp}/juhe-ai-forward-headers.XXXXXX")"
  body="$(mktemp "${TMPDIR:-/tmp}/juhe-ai-forward-body.XXXXXX")"
  if ! curl -fsS --max-time 5 -D "$headers" -o "$body" "$PUBLIC_HEALTH_BASE_URL/__aisys__/health" \
    || ! grep -Eiq "^X-Juhe-Active-Upstream:[[:space:]]*$CANDIDATE_LABEL[[:space:]]*$" "$headers" \
    || ! node -e 'const h=JSON.parse(require("fs").readFileSync(process.argv[1], "utf8")); if(h.status!=="ok"||h.service!=="juhe-ai"||h.nodeRole!=="control") process.exit(1)' "$body" \
    || ! curl -fsS --max-time 5 "$PUBLIC_HEALTH_BASE_URL/__aisys__/api/health" \
      | node -e 'let s="";process.stdin.on("data",c=>s+=c).on("end",()=>{const h=JSON.parse(s);if(h.status!=="ok"||h.service!=="juhe-ai-db-service")process.exit(1)})'; then
    rm -f -- "$headers" "$body"
    echo "post-switch health sample failed: $sample" >&2
    exit 1
  fi
  rm -f -- "$headers" "$body"
  [ "$sample" -eq "$SAMPLES" ] || sleep 1
  sample=$((sample + 1))
done

CONFIG_MUTATED=0
trap - EXIT INT TERM
rm -rf -- "$STAGE_DIR"
rmdir "$LOCK_DIR"
printf 'PERFORMANCE_SLOT_SWITCHED label=%s ingress=%s samples=%s rollback=disabled\n' \
  "$CANDIDATE_LABEL" "$CANDIDATE_INGRESS_PORT" "$SAMPLES"
