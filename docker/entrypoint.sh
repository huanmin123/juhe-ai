#!/usr/bin/env sh
set -eu

export NODE_ENV="${NODE_ENV:-production}"
export JUHE_AI_HOST="${JUHE_AI_HOST:-0.0.0.0}"
export JUHE_AI_PORT="${JUHE_AI_PORT:-3000}"
export JUHE_AI_DB_SERVICE_HTTP_HOST="${JUHE_AI_DB_SERVICE_HTTP_HOST:-127.0.0.1}"
export JUHE_AI_DB_SERVICE_HTTP_PORT="${JUHE_AI_DB_SERVICE_HTTP_PORT:-0}"
export JUHE_AI_PROCESS_ROLE="${JUHE_AI_PROCESS_ROLE:-server}"
export JUHE_AI_RUNTIME_MODE="${JUHE_AI_RUNTIME_MODE:-standalone}"
export JUHE_AI_DATABASE_DRIVER="${JUHE_AI_DATABASE_DRIVER:-sqlite}"
export JUHE_AI_CACHE_DRIVER="${JUHE_AI_CACHE_DRIVER:-memory}"
export JUHE_AI_RUNTIME_STATE_DRIVER="${JUHE_AI_RUNTIME_STATE_DRIVER:-memory}"
export JUHE_AI_QUEUE_DRIVER="${JUHE_AI_QUEUE_DRIVER:-memory}"
export JUHE_AI_REDIS_STREAM_READ_COUNT="${JUHE_AI_REDIS_STREAM_READ_COUNT:-1000}"
export JUHE_AI_REDIS_STREAM_BLOCK_MS="${JUHE_AI_REDIS_STREAM_BLOCK_MS:-1000}"
export JUHE_AI_REDIS_STREAM_CLAIM_IDLE_MS="${JUHE_AI_REDIS_STREAM_CLAIM_IDLE_MS:-60000}"
export JUHE_AI_DB_POOL_MAX="${JUHE_AI_DB_POOL_MAX:-50}"
export JUHE_AI_DB_WRITE_MAX_CONCURRENCY="${JUHE_AI_DB_WRITE_MAX_CONCURRENCY:-100}"
export JUHE_AI_DB_WRITE_QUEUE_MAX_ITEMS="${JUHE_AI_DB_WRITE_QUEUE_MAX_ITEMS:-50000}"
export JUHE_AI_DATABASE_PATH="${JUHE_AI_DATABASE_PATH:-/app/backend/data/juhe-ai.sqlite3}"
export JUHE_AI_DATASET_DATABASE_PATH="${JUHE_AI_DATASET_DATABASE_PATH:-/app/backend/data/juhe-ai-dataset.sqlite3}"
export JUHE_AI_USAGE_CATALOG_DATABASE_PATH="${JUHE_AI_USAGE_CATALOG_DATABASE_PATH:-/app/backend/data/juhe-ai-usage-catalog.sqlite3}"
export JUHE_AI_STATS_DATABASE_PATH="${JUHE_AI_STATS_DATABASE_PATH:-/app/backend/data/juhe-ai-stats.sqlite3}"
export JUHE_AI_USAGE_SHARD_ROOT="${JUHE_AI_USAGE_SHARD_ROOT:-/app/backend/data/usage-shards}"
export JUHE_AI_USAGE_SHARD_COUNT="${JUHE_AI_USAGE_SHARD_COUNT:-16}"
export JUHE_AI_LOG_DIR="${JUHE_AI_LOG_DIR:-/app/backend/logs}"
export JUHE_AI_LOG_FILE_ENABLED="${JUHE_AI_LOG_FILE_ENABLED:-true}"
export JUHE_AI_LOG_CONSOLE_ENABLED="${JUHE_AI_LOG_CONSOLE_ENABLED:-true}"
export JUHE_AI_COOKIE_SECURE="${JUHE_AI_COOKIE_SECURE:-false}"
export JUHE_AI_COOKIE_SAME_SITE="${JUHE_AI_COOKIE_SAME_SITE:-lax}"
export JUHE_AI_TRUST_PROXY="${JUHE_AI_TRUST_PROXY:-false}"
export JUHE_AI_ALLOW_PRIVATE_UPSTREAM_BASE_URLS="${JUHE_AI_ALLOW_PRIVATE_UPSTREAM_BASE_URLS:-false}"
export JUHE_AI_UPSTREAM_BASE_URL_PRIVATE_ALLOWLIST="${JUHE_AI_UPSTREAM_BASE_URL_PRIVATE_ALLOWLIST:-}"

mkdir -p /app/backend/data /app/backend/logs "$JUHE_AI_USAGE_SHARD_ROOT"

if [ -z "${JUHE_AI_SECRET:-}" ]; then
  secret_file="${JUHE_AI_SECRET_FILE:-/app/backend/data/.juhe-ai-secret}"
  if [ -s "$secret_file" ]; then
    JUHE_AI_SECRET="$(cat "$secret_file")"
  else
    JUHE_AI_SECRET="$(node -e "console.log(require('node:crypto').randomBytes(32).toString('hex'))")"
    printf '%s' "$JUHE_AI_SECRET" > "$secret_file"
    chmod 600 "$secret_file" 2>/dev/null || true
  fi
  export JUHE_AI_SECRET
fi

if [ -z "${JUHE_AI_ALLOWED_ORIGINS:-}" ]; then
  if [ -n "${JUHE_AI_PUBLIC_ORIGIN:-}" ]; then
    export JUHE_AI_ALLOWED_ORIGINS="$JUHE_AI_PUBLIC_ORIGIN"
  else
    public_port="${JUHE_AI_PUBLIC_PORT:-$JUHE_AI_PORT}"
    export JUHE_AI_ALLOWED_ORIGINS="http://localhost:${public_port},http://127.0.0.1:${public_port}"
  fi
fi

node backend/dist/scripts/preflight/check-node-sqlite.js
exec "$@"
