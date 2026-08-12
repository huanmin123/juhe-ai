#!/usr/bin/env bash
set -euo pipefail

APP_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)"
cd "$APP_DIR"
export NODE_ENV="${NODE_ENV:-production}"
export JUHE_AI_LOG_CONSOLE_ENABLED="${JUHE_AI_LOG_CONSOLE_ENABLED:-false}"

if ! command -v node >/dev/null 2>&1; then
  echo 'Node.js LTS is required. Install Node.js 22.x LTS (>=22.13.0) or 24.x LTS (>=24.11.0) before running this script.' >&2
  exit 1
fi

if ! command -v pnpm >/dev/null 2>&1; then
  if command -v corepack >/dev/null 2>&1; then
    corepack enable
    corepack prepare pnpm@latest --activate
  else
    echo 'pnpm is required. Install pnpm or enable corepack first.' >&2
    exit 1
  fi
fi

RUNTIME_CHECK_SCRIPT='backend/dist/scripts/preflight/check-node-sqlite.js'
SIDECAR_START_SCRIPT='scripts/start-go-sidecar.mjs'
server_pid=''
go_sidecar_pid=''
go_sidecar_pid_file='backend/runtime/juhe-ai-go-sidecar.pid'
go_sidecar_log_file='backend/logs/juhe-ai-go-sidecar.log'

ripgrep_dependency_ready() {
  (cd backend && node --input-type=module -e "import('@vscode/ripgrep').then(({ rgPath }) => import('node:fs').then(({ existsSync }) => process.exit(existsSync(rgPath) ? 0 : 1))).catch(() => process.exit(1))" >/dev/null 2>&1)
}

read_dotenv_value() {
  name="$1"
  fallback="${2:-}"
  if [ ! -f backend/.env ]; then
    printf '%s' "$fallback"
    return
  fi
  value="$(grep -E "^[[:space:]]*${name}=" backend/.env | tail -n 1 | cut -d= -f2- || true)"
  value="${value%\"}"
  value="${value#\"}"
  value="${value%\'}"
  value="${value#\'}"
  if [ -n "$value" ]; then
    printf '%s' "$value"
  else
    printf '%s' "$fallback"
  fi
}

set_dotenv_value() {
  name="$1"
  value="$2"
  tmp_file="$(mktemp)"
  awk -v key="$name" -v next_value="$value" '
    BEGIN { updated = 0 }
    $0 ~ "^[[:space:]]*" key "=" { print key "=" next_value; updated = 1; next }
    { print }
    END { if (!updated) print key "=" next_value }
  ' backend/.env > "$tmp_file"
  mv "$tmp_file" backend/.env
}

generate_secret() {
  node -e "console.log(require('node:crypto').randomBytes(32).toString('hex'))"
}

ensure_deployment_defaults() {
  file_secret="$(read_dotenv_value JUHE_AI_SECRET '')"
  if [ -z "${JUHE_AI_SECRET:-}" ] && [ -z "$file_secret" ]; then
    generated_secret="$(generate_secret)"
    set_dotenv_value JUHE_AI_SECRET "$generated_secret"
    export JUHE_AI_SECRET="$generated_secret"
    echo 'Generated JUHE_AI_SECRET and saved it to backend/.env. Keep this value when migrating existing data.'
  elif [ -z "${JUHE_AI_SECRET:-}" ]; then
    export JUHE_AI_SECRET="$file_secret"
  fi

  file_origins="$(read_dotenv_value JUHE_AI_ALLOWED_ORIGINS '')"
  if [ -z "${JUHE_AI_ALLOWED_ORIGINS:-}" ] && [ -z "$file_origins" ]; then
    public_origin="${JUHE_AI_PUBLIC_ORIGIN:-$(read_dotenv_value JUHE_AI_PUBLIC_ORIGIN '')}"
    public_port="${JUHE_AI_PUBLIC_PORT:-${JUHE_AI_PORT:-$(read_dotenv_value JUHE_AI_PORT 3000)}}"
    if [ -n "$public_origin" ]; then
      default_origins="$public_origin"
    else
      default_origins="http://localhost:${public_port},http://127.0.0.1:${public_port}"
    fi
    set_dotenv_value JUHE_AI_ALLOWED_ORIGINS "$default_origins"
    export JUHE_AI_ALLOWED_ORIGINS="$default_origins"
    echo "Set JUHE_AI_ALLOWED_ORIGINS to $default_origins. Adjust backend/.env if using a public domain or reverse proxy."
  elif [ -z "${JUHE_AI_ALLOWED_ORIGINS:-}" ]; then
    export JUHE_AI_ALLOWED_ORIGINS="$file_origins"
  fi
}

go_sidecar_process() {
  pid_path="$1"
  [ -f "$pid_path" ] || return 1
  IFS= read -r pid < "$pid_path" || true
  case "$pid" in
    ''|*[!0-9]*) rm -f -- "$pid_path"; return 1 ;;
  esac
  kill -0 "$pid" 2>/dev/null || { rm -f -- "$pid_path"; return 1; }
  command_line="$(ps -p "$pid" -o command= 2>/dev/null || true)"
  case "$command_line" in
    *juhe-ai-go-sidecar*) printf '%s' "$pid"; return 0 ;;
    *) rm -f -- "$pid_path"; return 1 ;;
  esac
}

stop_go_sidecar() {
  pid="$(go_sidecar_process "$go_sidecar_pid_file" || true)"
  [ -n "$pid" ] || return 0
  kill -TERM "$pid"
  attempts=0
  while kill -0 "$pid" 2>/dev/null && [ "$attempts" -lt 10 ]; do
    sleep 1
    attempts=$((attempts + 1))
  done
  if kill -0 "$pid" 2>/dev/null; then
    echo "juhe-ai-go-sidecar did not stop within 10 seconds (PID $pid)." >&2
    return 1
  fi
  rm -f -- "$go_sidecar_pid_file"
}

stop_server_process() {
  if [ -z "$server_pid" ] || ! kill -0 "$server_pid" 2>/dev/null; then
    return 0
  fi
  kill -TERM "$server_pid"
  attempts=0
  while kill -0 "$server_pid" 2>/dev/null && [ "$attempts" -lt 10 ]; do
    sleep 1
    attempts=$((attempts + 1))
  done
  if kill -0 "$server_pid" 2>/dev/null; then
    echo "juhe-ai Web/API process did not stop within 10 seconds (PID $server_pid)." >&2
    return 1
  fi
}

wait_for_http_status() {
  process_id="$1"
  url="$2"
  expected_status="$3"
  description="$4"
  attempts=0
  while kill -0 "$process_id" 2>/dev/null && [ "$attempts" -lt 60 ]; do
    if node --input-type=module -e '
const response = await fetch(process.argv[1])
process.exit(response.status === Number(process.argv[2]) ? 0 : 1)
' "$url" "$expected_status" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
    attempts=$((attempts + 1))
  done
  if ! kill -0 "$process_id" 2>/dev/null; then
    echo "$description exited before becoming ready." >&2
  else
    echo "$description did not become ready within 60 seconds." >&2
  fi
  return 1
}

start_go_sidecar() {
  binary="$APP_DIR/backend-go/juhe-ai-go-sidecar"
  if [ ! -f "$binary" ] || [ ! -x "$binary" ]; then
    echo "Go sidecar binary not found or not executable: $binary. Rebuild the release package for this Unix platform." >&2
    return 1
  fi
  if [ ! -f "$APP_DIR/$SIDECAR_START_SCRIPT" ]; then
    echo "Go sidecar launcher not found: $APP_DIR/$SIDECAR_START_SCRIPT. Rebuild the release package." >&2
    return 1
  fi
  mkdir -p backend/runtime backend/logs
  existing_pid="$(go_sidecar_process "$go_sidecar_pid_file" || true)"
  if [ -n "$existing_pid" ]; then
    echo "juhe-ai-go-sidecar is already running (PID $existing_pid); stop the existing release before starting another one." >&2
    return 1
  fi
  go_sidecar_pid="$(node "$APP_DIR/$SIDECAR_START_SCRIPT" "$binary" "$APP_DIR/backend" "$APP_DIR/$go_sidecar_log_file")"
  case "$go_sidecar_pid" in
    ''|*[!0-9]*) echo "juhe-ai-go-sidecar returned an invalid PID: $go_sidecar_pid" >&2; return 1 ;;
  esac
  printf '%s' "$go_sidecar_pid" > "$go_sidecar_pid_file"
  if ! go_sidecar_process "$go_sidecar_pid_file" >/dev/null; then
    [ -f "$go_sidecar_log_file" ] && tail -n 20 "$go_sidecar_log_file" >&2
    echo 'juhe-ai-go-sidecar exited during startup.' >&2
    return 1
  fi
  input_url="${JUHE_AI_AUDIT_LOG_INPUT_URL:-$(read_dotenv_value JUHE_AI_AUDIT_LOG_INPUT_URL 'http://127.0.0.1:3303')}"
  input_url="${input_url%/}"
  if ! wait_for_http_status "$go_sidecar_pid" "$input_url/__aiinternal__/health" 204 'juhe-ai-go-sidecar'; then
    [ -f "$go_sidecar_log_file" ] && tail -n 20 "$go_sidecar_log_file" >&2
    return 1
  fi
}

on_exit() {
  exit_code=$?
  trap - EXIT INT TERM
  if ! stop_server_process && [ "$exit_code" -eq 0 ]; then exit_code=1; fi
  if ! stop_go_sidecar && [ "$exit_code" -eq 0 ]; then exit_code=1; fi
  exit "$exit_code"
}

if [ ! -f backend/.env ]; then
  cp backend/.env.example backend/.env
  echo 'Created backend/.env from backend/.env.example'
  echo 'Configure all JUHE_AI_*_INSTANCE_ID values and JUHE_AI_AUDIT_LOG_INPUT_SECRET before production use.'
fi

ensure_deployment_defaults
mkdir -p backend/data

if [ ! -f "$RUNTIME_CHECK_SCRIPT" ]; then
  echo "Runtime preflight script not found: $RUNTIME_CHECK_SCRIPT. Please rebuild the release package." >&2
  exit 1
fi
if [ ! -d node_modules ] || [ ! -d backend/node_modules ] || ! ripgrep_dependency_ready; then
  echo 'Installing production dependencies...'
  pnpm install --prod --frozen-lockfile --filter juhe-ai-backend...
else
  echo 'Using existing node_modules. Remove node_modules and backend/node_modules to force reinstall.'
fi
node "$RUNTIME_CHECK_SCRIPT"

HOST="${JUHE_AI_HOST:-$(read_dotenv_value JUHE_AI_HOST '127.0.0.1')}"
PORT="${JUHE_AI_PORT:-$(read_dotenv_value JUHE_AI_PORT '3000')}"
export JUHE_AI_AUDIT_LOG_INPUT_URL="${JUHE_AI_AUDIT_LOG_INPUT_URL:-$(read_dotenv_value JUHE_AI_AUDIT_LOG_INPUT_URL 'http://127.0.0.1:3303')}"

echo "Starting juhe-ai at http://${HOST}:${PORT}"
echo 'The Web/API process supervises its Node worker and DB service; one Go sidecar owns F1, F2 and F3.'
OWNER_LOCK_ENABLED="${JUHE_AI_OWNER_LOCK_ENABLED:-$(read_dotenv_value JUHE_AI_OWNER_LOCK_ENABLED false)}"
OWNER_LOCK_ENABLED_NORMALIZED="$(printf '%s' "$OWNER_LOCK_ENABLED" | tr '[:upper:]' '[:lower:]')"
SERVER_WITH_OWNER_LOCK=false
if [ "$OWNER_LOCK_ENABLED_NORMALIZED" = 'true' ]; then
  OWNER_MANIFEST_PATH="$APP_DIR/deploy/owner-manifest.json"
  MANIFEST_EPOCH="$(node -e "const fs=require('node:fs'); process.stdout.write(JSON.parse(fs.readFileSync('deploy/owner-manifest.json','utf8')).deploymentEpoch)")"
  [ -n "$MANIFEST_EPOCH" ] || { echo 'Unable to read deploy/owner-manifest.json deploymentEpoch.' >&2; exit 1; }
  node scripts/validate-owner-manifest.mjs deploy/owner-manifest.json
  OWNER_LOCK_PATH="${JUHE_AI_OWNER_LOCK_PATH:-$(read_dotenv_value JUHE_AI_OWNER_LOCK_PATH '')}"
  case "$OWNER_LOCK_PATH" in /*) ;; *) echo 'JUHE_AI_OWNER_LOCK_PATH must be an absolute shared path outside the release directory.' >&2; exit 1 ;; esac
  OWNER_LOCK_EPOCH="${JUHE_AI_OWNER_LOCK_DEPLOYMENT_EPOCH:-$(read_dotenv_value JUHE_AI_OWNER_LOCK_DEPLOYMENT_EPOCH "$MANIFEST_EPOCH")}"
  [ "$OWNER_LOCK_EPOCH" = "$MANIFEST_EPOCH" ] || { echo 'JUHE_AI_OWNER_LOCK_DEPLOYMENT_EPOCH does not match deploy/owner-manifest.json.' >&2; exit 1; }
  NODE_VERSION="$(node -p "require('./package.json').version")"
  node scripts/validate-owner-manifest.mjs --require-deployment-epoch="$OWNER_LOCK_EPOCH" --require-node-version="$NODE_VERSION" deploy/owner-manifest.json
  export JUHE_AI_OWNER_MANIFEST_PATH="$OWNER_MANIFEST_PATH"
  export JUHE_AI_OWNER_LOCK_DEPLOYMENT_EPOCH="$OWNER_LOCK_EPOCH"
  SERVER_WITH_OWNER_LOCK=true
fi

trap on_exit EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

if [ "$SERVER_WITH_OWNER_LOCK" = 'true' ]; then
  node scripts/run-with-owner-lock.mjs --lock-path "$OWNER_LOCK_PATH" --release-root "$APP_DIR" --deployment-epoch "$OWNER_LOCK_EPOCH" --role server --version "$NODE_VERSION" -- node backend/dist/server.js &
else
  node backend/dist/server.js &
fi
server_pid=$!
wait_for_http_status "$server_pid" "http://${HOST}:${PORT}/__aisys__/api/health" 200 'juhe-ai Web/API process'
start_go_sidecar
echo "Started juhe-ai-go-sidecar (PID $go_sidecar_pid)."

while kill -0 "$server_pid" 2>/dev/null && kill -0 "$go_sidecar_pid" 2>/dev/null; do
  sleep 1
done
if ! kill -0 "$go_sidecar_pid" 2>/dev/null && kill -0 "$server_pid" 2>/dev/null; then
  [ -f "$go_sidecar_log_file" ] && tail -n 20 "$go_sidecar_log_file" >&2
  echo "juhe-ai-go-sidecar exited unexpectedly (PID $go_sidecar_pid)." >&2
  exit 1
fi
if ! kill -0 "$server_pid" 2>/dev/null; then
  echo "juhe-ai Web/API process exited unexpectedly (PID $server_pid)." >&2
  exit 1
fi
exit 1
