#!/usr/bin/env bash
set -euo pipefail

APP_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$APP_DIR"
export NODE_ENV="${NODE_ENV:-production}"
export JUHE_AI_LOG_CONSOLE_ENABLED="${JUHE_AI_LOG_CONSOLE_ENABLED:-false}"

if ! command -v node >/dev/null 2>&1; then
  echo "Node.js LTS is required. Install Node.js 22.x LTS (>=22.13.0) or 24.x LTS (>=24.11.0) before running this script." >&2
  exit 1
fi

RUNTIME_CHECK_SCRIPT="backend/dist/scripts/preflight/check-node-sqlite.js"
if [ ! -f "$RUNTIME_CHECK_SCRIPT" ]; then
  echo "Runtime preflight script not found: $RUNTIME_CHECK_SCRIPT. Please rebuild the release package." >&2
  exit 1
fi
node "$RUNTIME_CHECK_SCRIPT"

if ! command -v pnpm >/dev/null 2>&1; then
  if command -v corepack >/dev/null 2>&1; then
    corepack enable
    corepack prepare pnpm@latest --activate
  else
    echo "pnpm is required. Install pnpm or enable corepack first." >&2
    exit 1
  fi
fi

ripgrep_dependency_ready() {
  (cd backend && node --input-type=module -e "import('@vscode/ripgrep').then(({ rgPath }) => import('node:fs').then(({ existsSync }) => process.exit(existsSync(rgPath) ? 0 : 1))).catch(() => process.exit(1))" >/dev/null 2>&1)
}

runtime_log_indexer_pid=""
server_pid=""
runtime_log_indexer_pid_file="backend/runtime/juhe-ai-runtime-log-indexer.pid"
runtime_log_indexer_log_file="backend/logs/juhe-ai-runtime-log-indexer.log"

runtime_log_indexer_process() {
  pid_path="$1"
  if [ ! -f "$pid_path" ]; then
    return 1
  fi
  IFS= read -r pid < "$pid_path" || true
  case "$pid" in
    ''|*[!0-9]*)
      rm -f -- "$pid_path"
      return 1
      ;;
  esac
  if ! kill -0 "$pid" 2>/dev/null; then
    rm -f -- "$pid_path"
    return 1
  fi
  command_line="$(ps -p "$pid" -o command= 2>/dev/null || true)"
  case "$command_line" in
    *juhe-ai-runtime-log-indexer*)
      printf '%s' "$pid"
      return 0
      ;;
    *)
      rm -f -- "$pid_path"
      return 1
      ;;
  esac
}

stop_runtime_log_indexer() {
  pid="$(runtime_log_indexer_process "$runtime_log_indexer_pid_file" || true)"
  if [ -z "$pid" ]; then
    return 0
  fi
  kill -TERM "$pid"
  attempts=0
  while kill -0 "$pid" 2>/dev/null && [ "$attempts" -lt 10 ]; do
    sleep 1
    attempts=$((attempts + 1))
  done
  if kill -0 "$pid" 2>/dev/null; then
    echo "juhe-ai-runtime-log-indexer did not stop within 10 seconds (PID $pid)." >&2
    return 1
  fi
  rm -f -- "$runtime_log_indexer_pid_file"
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

wait_for_server_ready() {
  attempts=0
  health_url="http://${HOST}:${PORT}/__aisys__/health"
  while kill -0 "$server_pid" 2>/dev/null && [ "$attempts" -lt 60 ]; do
    if node --input-type=module -e '
const response = await fetch(process.argv[1])
if (!response.ok) process.exit(1)
' "$health_url" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
    attempts=$((attempts + 1))
  done
  if ! kill -0 "$server_pid" 2>/dev/null; then
    echo "juhe-ai Web/API process exited before becoming ready." >&2
  else
    echo "juhe-ai Web/API process did not become ready within 60 seconds." >&2
  fi
  return 1
}

on_exit() {
  exit_code=$?
  trap - EXIT INT TERM
  if ! stop_server_process && [ "$exit_code" -eq 0 ]; then
    exit_code=1
  fi
  if ! stop_runtime_log_indexer && [ "$exit_code" -eq 0 ]; then
    exit_code=1
  fi
  exit "$exit_code"
}

start_runtime_log_indexer() {
  runtime_log_indexer_binary="$APP_DIR/backend-go/juhe-ai-runtime-log-indexer"
  if [ ! -f "$runtime_log_indexer_binary" ] || [ ! -x "$runtime_log_indexer_binary" ]; then
    echo "Go runtime-log indexer binary not found or not executable: $runtime_log_indexer_binary. Rebuild the release package for this Unix platform." >&2
    return 1
  fi
  mkdir -p backend/runtime backend/logs
  existing_pid="$(runtime_log_indexer_process "$runtime_log_indexer_pid_file" || true)"
  if [ -n "$existing_pid" ]; then
    echo "juhe-ai-runtime-log-indexer is already running (PID $existing_pid); stop the existing release before starting another one." >&2
    return 1
  fi

  runtime_log_indexer_pid="$(node --input-type=module -e '
import { appendFileSync, closeSync, existsSync, openSync, readFileSync } from "node:fs"
import { randomUUID } from "node:crypto"
import { isAbsolute, resolve } from "node:path"
import { createRequire } from "node:module"
import { spawn } from "node:child_process"

const [binaryPath, backendRoot, logPath] = process.argv.slice(1)
const require = createRequire(resolve(backendRoot, "package.json"))
const { parse } = require("dotenv")
const baseEnvPath = resolve(backendRoot, ".env")
const capacityEnvPath = resolve(backendRoot, ".env.capacity")

function readEnv(path) {
  return existsSync(path) ? parse(readFileSync(path)) : {}
}

function configured(name) {
  if (Object.hasOwn(process.env, name)) return { defined: true, value: process.env[name] ?? "" }
  if (Object.hasOwn(overlayEnv, name)) return { defined: true, value: overlayEnv[name] ?? "" }
  if (Object.hasOwn(capacityEnv, name)) return { defined: true, value: capacityEnv[name] ?? "" }
  if (Object.hasOwn(baseEnv, name)) return { defined: true, value: baseEnv[name] ?? "" }
  return { defined: false, value: "" }
}

function isCapacityEnvironmentVariable(name) {
  return name.startsWith("JUHE_AI_CONCURRENCY_")
    || name.startsWith("JUHE_AI_ACCOUNT_")
    || name.startsWith("JUHE_AI_BACKGROUND_")
    || name.startsWith("JUHE_AI_GATEWAY_")
    || name.startsWith("JUHE_AI_DB_")
    || name.startsWith("JUHE_AI_CHAT_DB_SERVICE_")
    || name.startsWith("JUHE_AI_REDIS_STREAM_")
    || name.startsWith("JUHE_AI_USAGE_SPOOL_")
    || name === "JUHE_AI_SYSTEM_API_DB_SERVICE_MAX_IN_FLIGHT"
    || /^JUHE_AI_(GATEWAY|USAGE|LOG|STATS|OPS)_WORKER_REPLICAS$/.test(name)
}

const disableBaseEnv = String(process.env.JUHE_AI_DISABLE_BASE_ENV ?? "").trim().toLowerCase() === "true"
const baseEnv = disableBaseEnv ? {} : readEnv(baseEnvPath)
const overlayName = (process.env.JUHE_AI_ENV_FILE ?? baseEnv.JUHE_AI_ENV_FILE ?? "").trim()
const overlayEnv = overlayName ? readEnv(isAbsolute(overlayName) ? overlayName : resolve(backendRoot, overlayName)) : {}
const capacityEnv = Object.fromEntries(Object.entries(readEnv(capacityEnvPath)).filter(([name]) => isCapacityEnvironmentVariable(name)))
const childEnv = { ...process.env }
const names = [
  "JUHE_AI_DATABASE_DRIVER",
  "JUHE_AI_RUNTIME_MODE",
  "JUHE_AI_RUNTIME_LOG_STORE",
  "JUHE_AI_RUNTIME_LOG_OWNER_LEASE",
  "JUHE_AI_RUNTIME_LOG_ONCE",
  "JUHE_AI_RUNTIME_LOG_POLL_INTERVAL",
  "JUHE_AI_RUNTIME_LOG_RETENTION_INTERVAL",
  "JUHE_AI_RUNTIME_LOG_RETENTION_DAYS",
  "JUHE_AI_RUNTIME_LOG_BATCH_SIZE",
  "JUHE_AI_DATABASE_PATH",
  "JUHE_AI_DATASET_DATABASE_PATH",
  "JUHE_AI_POSTGRES_URL",
  "JUHE_AI_LOG_DIR",
  "JUHE_AI_LOG_FILE_ENABLED",
  "JUHE_AI_LOG_RETENTION_DAYS",
  "JUHE_AI_LOG_MAX_FILES",
  "JUHE_AI_RG_PATH"
]
for (const name of names) {
  const value = configured(name)
  if (value.defined) childEnv[name] = value.value
}

const configuredInstance = configured("JUHE_AI_RUNTIME_LOG_INSTANCE_ID")
if (configuredInstance.defined) {
  if (!configuredInstance.value.trim()) throw new Error("JUHE_AI_RUNTIME_LOG_INSTANCE_ID is configured but empty.")
  childEnv.JUHE_AI_RUNTIME_LOG_INSTANCE_ID = configuredInstance.value
} else {
  if (disableBaseEnv) {
    throw new Error("JUHE_AI_RUNTIME_LOG_INSTANCE_ID must be set outside backend/.env when JUHE_AI_DISABLE_BASE_ENV=true.")
  }
  const instanceId = `runtime-log-indexer-${randomUUID()}`
  appendFileSync(baseEnvPath, `\nJUHE_AI_RUNTIME_LOG_INSTANCE_ID=${instanceId}\n`, "utf8")
  childEnv.JUHE_AI_RUNTIME_LOG_INSTANCE_ID = instanceId
}

const runtimeMode = (childEnv.JUHE_AI_RUNTIME_MODE ?? "").trim().toLowerCase()
const hasPerformanceHints = ["JUHE_AI_POSTGRES_URL", "JUHE_AI_REDIS_CACHE_URL", "JUHE_AI_REDIS_STATE_URL", "JUHE_AI_REDIS_QUEUE_URL"]
  .some((name) => Boolean(configured(name).value.trim()))
if (!(childEnv.JUHE_AI_RUNTIME_LOG_STORE ?? "").trim() && !(childEnv.JUHE_AI_DATABASE_DRIVER ?? "").trim()) {
  childEnv.JUHE_AI_DATABASE_DRIVER = runtimeMode === "performance" || (!runtimeMode && hasPerformanceHints) ? "postgres" : "sqlite"
}

function absoluteBackendPath(value, fallback) {
  const selected = (value ?? fallback).trim()
  return isAbsolute(selected) ? selected : resolve(backendRoot, selected)
}

childEnv.JUHE_AI_LOG_DIR = absoluteBackendPath(childEnv.JUHE_AI_LOG_DIR, "./logs")
const runtimeLogStore = (childEnv.JUHE_AI_RUNTIME_LOG_STORE ?? "").trim() || (childEnv.JUHE_AI_DATABASE_DRIVER ?? "").trim()
if (runtimeLogStore.toLowerCase() === "sqlite") {
  childEnv.JUHE_AI_DATABASE_PATH = absoluteBackendPath(childEnv.JUHE_AI_DATABASE_PATH, "./data/juhe-ai.sqlite3")
  childEnv.JUHE_AI_DATASET_DATABASE_PATH = absoluteBackendPath(childEnv.JUHE_AI_DATASET_DATABASE_PATH, "./data/juhe-ai-dataset.sqlite3")
}

const logFd = openSync(logPath, "a")
try {
  const child = spawn(binaryPath, [], {
    cwd: process.cwd(),
    detached: true,
    env: childEnv,
    stdio: ["ignore", logFd, logFd]
  })
  if (!child.pid) throw new Error("Unable to start juhe-ai-runtime-log-indexer.")
  child.unref()
  process.stdout.write(String(child.pid))
} finally {
  closeSync(logFd)
}
' "$runtime_log_indexer_binary" "$APP_DIR/backend" "$runtime_log_indexer_log_file")"
  case "$runtime_log_indexer_pid" in
    ''|*[!0-9]*)
      echo "juhe-ai-runtime-log-indexer returned an invalid PID: $runtime_log_indexer_pid" >&2
      return 1
      ;;
  esac
  printf '%s' "$runtime_log_indexer_pid" > "$runtime_log_indexer_pid_file"
  sleep 1
  if ! runtime_log_indexer_process "$runtime_log_indexer_pid_file" >/dev/null; then
    if [ -f "$runtime_log_indexer_log_file" ]; then
      tail -n 20 "$runtime_log_indexer_log_file" >&2
    fi
    echo "juhe-ai-runtime-log-indexer exited during startup." >&2
    return 1
  fi
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
    $0 ~ "^[[:space:]]*" key "=" {
      print key "=" next_value
      updated = 1
      next
    }
    { print }
    END {
      if (!updated) {
        print key "=" next_value
      }
    }
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
    echo "Generated JUHE_AI_SECRET and saved it to backend/.env. Keep this value when migrating existing data."
  elif [ -z "${JUHE_AI_SECRET:-}" ] && [ -n "$file_secret" ]; then
    export JUHE_AI_SECRET="$file_secret"
  fi

  file_origins="$(read_dotenv_value JUHE_AI_ALLOWED_ORIGINS '')"
  if [ -z "${JUHE_AI_ALLOWED_ORIGINS:-}" ] && [ -z "$file_origins" ]; then
    public_origin="$(read_dotenv_value JUHE_AI_PUBLIC_ORIGIN '')"
    public_origin="${JUHE_AI_PUBLIC_ORIGIN:-$public_origin}"
    public_port="${JUHE_AI_PUBLIC_PORT:-${JUHE_AI_PORT:-$(read_dotenv_value JUHE_AI_PORT 3000)}}"
    if [ -n "$public_origin" ]; then
      default_origins="$public_origin"
    else
      default_origins="http://localhost:${public_port},http://127.0.0.1:${public_port}"
    fi
    set_dotenv_value JUHE_AI_ALLOWED_ORIGINS "$default_origins"
    export JUHE_AI_ALLOWED_ORIGINS="$default_origins"
    echo "Set JUHE_AI_ALLOWED_ORIGINS to $default_origins. Adjust backend/.env if using a public domain or reverse proxy."
  elif [ -z "${JUHE_AI_ALLOWED_ORIGINS:-}" ] && [ -n "$file_origins" ]; then
    export JUHE_AI_ALLOWED_ORIGINS="$file_origins"
  fi
}

if [ ! -f backend/.env ]; then
  cp backend/.env.example backend/.env
  echo "Created backend/.env from backend/.env.example"
  echo "Please review backend/.env before production use, especially JUHE_AI_SECRET."
fi

ensure_deployment_defaults

mkdir -p backend/data
if [ ! -d node_modules ] || [ ! -d backend/node_modules ] || ! ripgrep_dependency_ready; then
  echo "Installing production dependencies..."
  pnpm install --prod --frozen-lockfile --filter juhe-ai-backend...
else
  echo "Using existing node_modules. Remove node_modules and backend/node_modules to force reinstall."
fi

HOST="$(grep -E '^JUHE_AI_HOST=' backend/.env | tail -n 1 | cut -d= -f2- || true)"
PORT="$(grep -E '^JUHE_AI_PORT=' backend/.env | tail -n 1 | cut -d= -f2- || true)"
HOST="${HOST:-127.0.0.1}"
PORT="${PORT:-3000}"

echo "Starting juhe-ai at http://${HOST}:${PORT}"
echo "The Web/API process will supervise separate background worker and DB service processes."
OWNER_LOCK_ENABLED="${JUHE_AI_OWNER_LOCK_ENABLED:-$(read_dotenv_value JUHE_AI_OWNER_LOCK_ENABLED false)}"
OWNER_LOCK_ENABLED_NORMALIZED="$(printf '%s' "$OWNER_LOCK_ENABLED" | tr '[:upper:]' '[:lower:]')"
SERVER_WITH_OWNER_LOCK=false
if [ "$OWNER_LOCK_ENABLED_NORMALIZED" = "true" ]; then
  OWNER_MANIFEST_PATH="$APP_DIR/deploy/owner-manifest.json"
  MANIFEST_EPOCH="$(node -e "const fs=require('node:fs'); process.stdout.write(JSON.parse(fs.readFileSync('deploy/owner-manifest.json','utf8')).deploymentEpoch)")"
  if [ -z "$MANIFEST_EPOCH" ]; then
    echo "Unable to read deploy/owner-manifest.json deploymentEpoch." >&2
    exit 1
  fi
  node scripts/validate-owner-manifest.mjs deploy/owner-manifest.json
  OWNER_LOCK_PATH="${JUHE_AI_OWNER_LOCK_PATH:-$(read_dotenv_value JUHE_AI_OWNER_LOCK_PATH '')}"
  case "$OWNER_LOCK_PATH" in
    /*) ;;
    *)
      echo "JUHE_AI_OWNER_LOCK_PATH must be an absolute shared path outside the release directory." >&2
      exit 1
      ;;
  esac
  OWNER_LOCK_EPOCH="${JUHE_AI_OWNER_LOCK_DEPLOYMENT_EPOCH:-$(read_dotenv_value JUHE_AI_OWNER_LOCK_DEPLOYMENT_EPOCH "$MANIFEST_EPOCH")}"
  if [ "$OWNER_LOCK_EPOCH" != "$MANIFEST_EPOCH" ]; then
    echo "JUHE_AI_OWNER_LOCK_DEPLOYMENT_EPOCH does not match deploy/owner-manifest.json." >&2
    exit 1
  fi
  NODE_VERSION="$(node -p "require('./package.json').version")"
  node scripts/validate-owner-manifest.mjs --require-deployment-epoch="$OWNER_LOCK_EPOCH" --require-node-version="$NODE_VERSION" deploy/owner-manifest.json
  export JUHE_AI_OWNER_MANIFEST_PATH="$OWNER_MANIFEST_PATH"
  export JUHE_AI_OWNER_LOCK_DEPLOYMENT_EPOCH="$OWNER_LOCK_EPOCH"
  SERVER_WITH_OWNER_LOCK=true
fi

trap on_exit EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
if [ "$SERVER_WITH_OWNER_LOCK" = "true" ]; then
  node scripts/run-with-owner-lock.mjs --lock-path "$OWNER_LOCK_PATH" --release-root "$APP_DIR" --deployment-epoch "$OWNER_LOCK_EPOCH" --role server --version "$NODE_VERSION" -- node backend/dist/server.js &
else
  node backend/dist/server.js &
fi
server_pid=$!
if ! wait_for_server_ready; then
  exit 1
fi
start_runtime_log_indexer
echo "Started juhe-ai-runtime-log-indexer (PID $runtime_log_indexer_pid)."
while kill -0 "$server_pid" 2>/dev/null && kill -0 "$runtime_log_indexer_pid" 2>/dev/null; do
  sleep 1
done
if ! kill -0 "$runtime_log_indexer_pid" 2>/dev/null && kill -0 "$server_pid" 2>/dev/null; then
  if [ -f "$runtime_log_indexer_log_file" ]; then
    tail -n 20 "$runtime_log_indexer_log_file" >&2
  fi
  echo "juhe-ai-runtime-log-indexer exited unexpectedly (PID $runtime_log_indexer_pid)." >&2
  exit 1
fi
set +e
wait "$server_pid"
server_exit_code=$?
set -e
exit "$server_exit_code"
