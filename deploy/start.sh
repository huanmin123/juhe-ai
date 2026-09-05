#!/usr/bin/env bash
set -euo pipefail

APP_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)"
cd "$APP_DIR"
export TZ=UTC
export NODE_ENV="${NODE_ENV:-production}"
export JUHE_AI_LOG_CONSOLE_ENABLED="${JUHE_AI_LOG_CONSOLE_ENABLED:-false}"

if ! command -v node >/dev/null 2>&1; then
  echo 'Node.js LTS is required. Install Node.js 22.x LTS (>=22.13.0) or 24.x LTS (>=24.11.0) before running this script.' >&2
  exit 1
fi

RUNTIME_CHECK_SCRIPT='backend/dist/scripts/preflight/check-node-sqlite.js'
GO_PROJECT_START_SCRIPT='scripts/start-go-project.mjs'
server_pid=''
go_gateway_pid=''
go_jobs_pid=''
go_gateway_pid_file='backend/runtime/juhe-ai-gateway.pid'
go_jobs_pid_file='backend/runtime/juhe-ai-jobs.pid'
go_gateway_log_file='backend/logs/juhe-ai-gateway.log'
go_jobs_log_file='backend/logs/juhe-ai-jobs.log'

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
  # 32 bytes from the OS CSPRNG as 64 hex chars; identical shape to the
  # previous node-only implementation so existing deployments see no change.
  random_hex="$(od -An -tx1 -N32 /dev/urandom 2>/dev/null | tr -d ' \n' || true)"
  if [ "${#random_hex}" -eq 64 ]; then
    printf '%s' "$random_hex"
  else
    node -e "console.log(require('node:crypto').randomBytes(32).toString('hex'))"
  fi
}

resolve_deploy_mode() {
  mode="${JUHE_AI_DEPLOY_MODE:-$(read_dotenv_value JUHE_AI_DEPLOY_MODE hybrid)}"
  case "$mode" in
    hybrid|go|node) printf '%s' "$mode" ;;
    *)
      echo "JUHE_AI_DEPLOY_MODE must be hybrid, go, or node (got: $mode)." >&2
      return 1
      ;;
  esac
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

go_project_process() {
  pid_path="$1"
  binary_name="$2"
  [ -f "$pid_path" ] || return 1
  IFS= read -r pid < "$pid_path" || true
  case "$pid" in
    ''|*[!0-9]*) rm -f -- "$pid_path"; return 1 ;;
  esac
  kill -0 "$pid" 2>/dev/null || { rm -f -- "$pid_path"; return 1; }
  command_line="$(ps -p "$pid" -o command= 2>/dev/null || true)"
  case "$command_line" in
    *"$binary_name"*) printf '%s' "$pid"; return 0 ;;
    *) rm -f -- "$pid_path"; return 1 ;;
  esac
}

stop_go_project() {
  pid_path="$1"
  binary_name="$2"
  pid="$(go_project_process "$pid_path" "$binary_name" || true)"
  [ -n "$pid" ] || return 0
  kill -TERM "$pid"
  attempts=0
  while kill -0 "$pid" 2>/dev/null && [ "$attempts" -lt 10 ]; do
    sleep 1
    attempts=$((attempts + 1))
  done
  if kill -0 "$pid" 2>/dev/null; then
    echo "$binary_name did not stop within 10 seconds (PID $pid)." >&2
    return 1
  fi
  rm -f -- "$pid_path"
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

start_go_project() {
  project="$1"
  health_url="$2"
  binary="$APP_DIR/backend-go/juhe-ai-$project"
  pid_path="backend/runtime/juhe-ai-$project.pid"
  log_path="backend/logs/juhe-ai-$project.log"
  if [ ! -f "$binary" ] || [ ! -x "$binary" ]; then
    echo "Go $project binary not found or not executable: $binary. Rebuild the release package for this Unix platform." >&2
    return 1
  fi
  if [ ! -f "$APP_DIR/$GO_PROJECT_START_SCRIPT" ]; then
    echo "Go project launcher not found: $APP_DIR/$GO_PROJECT_START_SCRIPT. Rebuild the release package." >&2
    return 1
  fi
  mkdir -p backend/runtime backend/logs
  existing_pid="$(go_project_process "$pid_path" "juhe-ai-$project" || true)"
  if [ -n "$existing_pid" ]; then
    echo "juhe-ai-go-$project is already running (PID $existing_pid); stop the existing release before starting another one." >&2
    return 1
  fi
  go_project_pid="$(node "$APP_DIR/$GO_PROJECT_START_SCRIPT" "$project" "$binary" "$APP_DIR/backend" "$APP_DIR/$log_path")"
  case "$go_project_pid" in
    ''|*[!0-9]*) echo "juhe-ai-go-$project returned an invalid PID: $go_project_pid" >&2; return 1 ;;
  esac
  printf '%s' "$go_project_pid" > "$pid_path"
  if ! go_project_process "$pid_path" "juhe-ai-$project" >/dev/null; then
    [ -f "$log_path" ] && tail -n 20 "$log_path" >&2
    echo "juhe-ai-go-$project exited during startup." >&2
    return 1
  fi
  if ! wait_for_http_status "$go_project_pid" "${health_url%/}/health" 200 "juhe-ai-go-$project"; then
    [ -f "$log_path" ] && tail -n 20 "$log_path" >&2
    return 1
  fi
  printf '%s' "$go_project_pid"
}

on_exit() {
  exit_code=$?
  trap - EXIT INT TERM
  if ! stop_server_process && [ "$exit_code" -eq 0 ]; then exit_code=1; fi
  if ! stop_go_project "$go_jobs_pid_file" 'juhe-ai-jobs' && [ "$exit_code" -eq 0 ]; then exit_code=1; fi
  if ! stop_go_project "$go_gateway_pid_file" 'juhe-ai-gateway' && [ "$exit_code" -eq 0 ]; then exit_code=1; fi
  exit "$exit_code"
}

run_go_maintenance_bootstrap() {
  binary="$APP_DIR/backend-go/juhe-ai-maintenance"
  if [ ! -f "$binary" ] || [ ! -x "$binary" ]; then
    echo "Go maintenance binary not found or not executable: $binary. Rebuild the release package for this Unix platform." >&2
    return 1
  fi
  database_driver="$(printf '%s' "${JUHE_AI_DATABASE_DRIVER:-$(read_dotenv_value JUHE_AI_DATABASE_DRIVER sqlite)}" | tr '[:upper:]' '[:lower:]')"
  bootstrap_args=()
  case "$database_driver" in
    sqlite)
      business_path="$(read_dotenv_value JUHE_AI_DATABASE_PATH './data/juhe-ai.sqlite3')"
      chat_path="$(read_dotenv_value JUHE_AI_CHAT_DATABASE_PATH './data/juhe-ai-chat.sqlite3')"
      dataset_path="$(read_dotenv_value JUHE_AI_DATASET_DATABASE_PATH './data/juhe-ai-dataset.sqlite3')"
      usage_catalog_path="$(read_dotenv_value JUHE_AI_USAGE_CATALOG_DATABASE_PATH './data/juhe-ai-usage-catalog.sqlite3')"
      stats_path="$(read_dotenv_value JUHE_AI_STATS_DATABASE_PATH './data/juhe-ai-stats.sqlite3')"
      codex_shard_root="$(read_dotenv_value JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT './data/codex-context/state-shards')"
      paths="business=${business_path},chat=${chat_path},dataset=${dataset_path},usage-catalog=${usage_catalog_path},stats=${stats_path},codex-context-shard-root=${codex_shard_root}"
      codex_shard_count="$(read_dotenv_value JUHE_AI_CODEX_CONTEXT_STATE_SHARD_COUNT '')"
      case "$codex_shard_count" in
        '') ;;
        *[!0-9]*|'0') echo "JUHE_AI_CODEX_CONTEXT_STATE_SHARD_COUNT must be an integer between 1 and 256 (got: $codex_shard_count)." >&2; return 1 ;;
        *) paths="${paths},codex-context-shard-count=${codex_shard_count}" ;;
      esac
      bootstrap_args=(--ensure-schema --driver sqlite --paths "$paths")
      ;;
    postgres)
      postgres_dsn="${JUHE_AI_POSTGRES_URL:-$(read_dotenv_value JUHE_AI_POSTGRES_URL '')}"
      if [ -z "$postgres_dsn" ]; then
        echo 'JUHE_AI_GO_MAINTENANCE_BOOTSTRAP=true with postgres driver requires JUHE_AI_POSTGRES_URL.' >&2
        return 1
      fi
      bootstrap_args=(--ensure-schema --driver postgres --dsn "$postgres_dsn")
      ;;
    *)
      echo "Unsupported JUHE_AI_DATABASE_DRIVER for go maintenance bootstrap: $database_driver (expected sqlite or postgres)." >&2
      return 1
      ;;
  esac
  go_maintenance_seed="$(printf '%s' "${JUHE_AI_GO_MAINTENANCE_SEED:-$(read_dotenv_value JUHE_AI_GO_MAINTENANCE_SEED false)}" | tr '[:upper:]' '[:lower:]')"
  if [ "$go_maintenance_seed" = 'true' ]; then
    bootstrap_args+=(--seed)
  fi
  if [ -n "${JUHE_AI_SECRET:-}" ]; then
    bootstrap_args+=(--secret "$JUHE_AI_SECRET")
  fi
  echo 'Running optional Go maintenance preflight (ensure-schema; seed when enabled)...'
  # Relative backend/.env storage paths resolve against backend/, matching the
  # Node per-file storage layout; the maintenance command is idempotent.
  (cd backend && "$binary" "${bootstrap_args[@]}")
}

if [ ! -f backend/.env ]; then
  cp backend/.env.example backend/.env
  echo 'Created backend/.env from backend/.env.example'
  echo 'Configure all JUHE_AI_*_INSTANCE_ID values and F3/F4 input secrets before production use.'
fi

ensure_deployment_defaults
mkdir -p backend/data

DEPLOY_MODE="$(resolve_deploy_mode)" || exit 1

if [ "$DEPLOY_MODE" != 'go' ]; then
  if ! command -v pnpm >/dev/null 2>&1; then
    if command -v corepack >/dev/null 2>&1; then
      corepack enable
      corepack prepare pnpm@latest --activate
    else
      echo 'pnpm is required. Install pnpm or enable corepack first.' >&2
      exit 1
    fi
  fi
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
fi

HOST="${JUHE_AI_HOST:-$(read_dotenv_value JUHE_AI_HOST '127.0.0.1')}"
PORT="${JUHE_AI_PORT:-$(read_dotenv_value JUHE_AI_PORT '3000')}"
export JUHE_AI_AUDIT_LOG_INPUT_URL="${JUHE_AI_AUDIT_LOG_INPUT_URL:-$(read_dotenv_value JUHE_AI_AUDIT_LOG_INPUT_URL 'http://127.0.0.1:3303')}"
export JUHE_AI_OPERATION_LOG_INPUT_URL="${JUHE_AI_OPERATION_LOG_INPUT_URL:-$(read_dotenv_value JUHE_AI_OPERATION_LOG_INPUT_URL 'http://127.0.0.1:3304')}"

echo "Starting juhe-ai at http://${HOST}:${PORT} (deploy mode: ${DEPLOY_MODE})"
if [ "$DEPLOY_MODE" = 'hybrid' ]; then
  echo 'The Web/API process supervises its Node worker and DB service; Go jobs owns F1/F2 and Go gateway owns F3/F4.'
fi
OWNER_LOCK_ENABLED="${JUHE_AI_OWNER_LOCK_ENABLED:-$(read_dotenv_value JUHE_AI_OWNER_LOCK_ENABLED false)}"
OWNER_LOCK_ENABLED_NORMALIZED="$(printf '%s' "$OWNER_LOCK_ENABLED" | tr '[:upper:]' '[:lower:]')"
SERVER_WITH_OWNER_LOCK=false
if [ "$DEPLOY_MODE" = 'go' ] && [ "$OWNER_LOCK_ENABLED_NORMALIZED" = 'true' ]; then
  echo 'JUHE_AI_OWNER_LOCK_ENABLED=true has no Go-mode server wrapper yet; go mode refuses to start without this deployment guard.' >&2
  exit 1
fi
if [ "$DEPLOY_MODE" != 'go' ] && [ "$OWNER_LOCK_ENABLED_NORMALIZED" = 'true' ]; then
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

if [ "$DEPLOY_MODE" != 'go' ]; then
  if [ "$SERVER_WITH_OWNER_LOCK" = 'true' ]; then
    node scripts/run-with-owner-lock.mjs --lock-path "$OWNER_LOCK_PATH" --release-root "$APP_DIR" --deployment-epoch "$OWNER_LOCK_EPOCH" --role server --version "$NODE_VERSION" -- node backend/dist/server.js &
  else
    node backend/dist/server.js &
  fi
  server_pid=$!
  wait_for_http_status "$server_pid" "http://${HOST}:${PORT}/__aisys__/api/health" 200 'juhe-ai Web/API process'
fi

if [ "$DEPLOY_MODE" != 'node' ]; then
  if [ "$DEPLOY_MODE" = 'go' ]; then
    configured_system_api="$(printf '%s' "${JUHE_AI_GATEWAY_SYSTEM_API_ENABLED:-$(read_dotenv_value JUHE_AI_GATEWAY_SYSTEM_API_ENABLED '')}" | tr '[:upper:]' '[:lower:]')"
    case "$configured_system_api" in
      ''|true) export JUHE_AI_GATEWAY_SYSTEM_API_ENABLED=true ;;
      *)
        echo "Deploy mode 'go' requires JUHE_AI_GATEWAY_SYSTEM_API_ENABLED=true (got: $configured_system_api); the gateway must own the main HTTP entry." >&2
        exit 1
        ;;
    esac
    GO_MAINTENANCE_BOOTSTRAP="$(printf '%s' "${JUHE_AI_GO_MAINTENANCE_BOOTSTRAP:-$(read_dotenv_value JUHE_AI_GO_MAINTENANCE_BOOTSTRAP false)}" | tr '[:upper:]' '[:lower:]')"
    if [ "$GO_MAINTENANCE_BOOTSTRAP" = 'true' ]; then
      run_go_maintenance_bootstrap || exit 1
    fi
  fi
  gateway_health_url="${JUHE_AI_GATEWAY_HEALTH_URL:-$(read_dotenv_value JUHE_AI_GATEWAY_HEALTH_URL 'http://127.0.0.1:3306')}"
  jobs_health_url="${JUHE_AI_JOBS_HEALTH_URL:-$(read_dotenv_value JUHE_AI_JOBS_HEALTH_URL 'http://127.0.0.1:3305')}"
  go_gateway_pid="$(start_go_project gateway "$gateway_health_url")"
  go_jobs_pid="$(start_go_project jobs "$jobs_health_url")"
  audit_input_url="${JUHE_AI_AUDIT_LOG_INPUT_URL:-$(read_dotenv_value JUHE_AI_AUDIT_LOG_INPUT_URL 'http://127.0.0.1:3303')}"
  operation_input_url="${JUHE_AI_OPERATION_LOG_INPUT_URL:-$(read_dotenv_value JUHE_AI_OPERATION_LOG_INPUT_URL 'http://127.0.0.1:3304')}"
  wait_for_http_status "$go_gateway_pid" "${audit_input_url%/}/__aiinternal__/health" 204 'juhe-ai-go-gateway F3'
  wait_for_http_status "$go_gateway_pid" "${operation_input_url%/}/__aiinternal__/v1/operation-logs/health" 204 'juhe-ai-go-gateway F4'
  if [ "$DEPLOY_MODE" = 'go' ]; then
    wait_for_http_status "$go_gateway_pid" "http://${HOST}:${PORT}/__aisys__/api/health" 200 'juhe-ai-go-gateway system API'
  fi
  echo "Started juhe-ai-go-gateway (PID $go_gateway_pid) and juhe-ai-go-jobs (PID $go_jobs_pid)."
fi

if [ "$DEPLOY_MODE" = 'go' ]; then
  while kill -0 "$go_gateway_pid" 2>/dev/null && kill -0 "$go_jobs_pid" 2>/dev/null; do
    sleep 1
  done
  if ! kill -0 "$go_gateway_pid" 2>/dev/null; then
    [ -f "$go_gateway_log_file" ] && tail -n 20 "$go_gateway_log_file" >&2
    echo "juhe-ai-go-gateway exited unexpectedly (PID $go_gateway_pid)." >&2
    exit 1
  fi
  [ -f "$go_jobs_log_file" ] && tail -n 20 "$go_jobs_log_file" >&2
  echo "juhe-ai-go-jobs exited unexpectedly (PID $go_jobs_pid)." >&2
  exit 1
fi
if [ "$DEPLOY_MODE" = 'node' ]; then
  while kill -0 "$server_pid" 2>/dev/null; do
    sleep 1
  done
  echo "juhe-ai Web/API process exited unexpectedly (PID $server_pid)." >&2
  exit 1
fi

while kill -0 "$server_pid" 2>/dev/null && kill -0 "$go_gateway_pid" 2>/dev/null && kill -0 "$go_jobs_pid" 2>/dev/null; do
  sleep 1
done
if ! kill -0 "$go_gateway_pid" 2>/dev/null && kill -0 "$server_pid" 2>/dev/null; then
  [ -f "$go_gateway_log_file" ] && tail -n 20 "$go_gateway_log_file" >&2
  echo "juhe-ai-go-gateway exited unexpectedly (PID $go_gateway_pid)." >&2
  exit 1
fi
if ! kill -0 "$go_jobs_pid" 2>/dev/null && kill -0 "$server_pid" 2>/dev/null; then
  [ -f "$go_jobs_log_file" ] && tail -n 20 "$go_jobs_log_file" >&2
  echo "juhe-ai-go-jobs exited unexpectedly (PID $go_jobs_pid)." >&2
  exit 1
fi
if ! kill -0 "$server_pid" 2>/dev/null; then
  echo "juhe-ai Web/API process exited unexpectedly (PID $server_pid)." >&2
  exit 1
fi
exit 1
