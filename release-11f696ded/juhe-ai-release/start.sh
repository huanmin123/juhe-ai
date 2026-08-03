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
  exec node scripts/run-with-owner-lock.mjs --lock-path "$OWNER_LOCK_PATH" --release-root "$APP_DIR" --deployment-epoch "$OWNER_LOCK_EPOCH" --role server --version "$NODE_VERSION" -- node backend/dist/server.js
fi
exec node backend/dist/server.js
