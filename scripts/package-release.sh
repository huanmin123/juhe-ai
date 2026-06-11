#!/usr/bin/env bash
set -euo pipefail

OUTPUT_DIR="release"
PACKAGE_NAME="juhe-ai-release"
ARCHIVE_FORMAT="both"
FRONTEND_API_BASE_URL="/__aisys__/api"
FRONTEND_GATEWAY_BASE_URL=""
INCLUDE_LOCAL_ENV="0"

usage() {
  cat <<'USAGE'
Usage: bash ./scripts/package-release.sh [options]

Options:
  --output-dir <dir>                 Output directory. Default: release
  --package-name <name>              Package folder/archive name. Default: juhe-ai-release
  --archive-format <tar.gz|zip|both> Archive format. Default: both
  --frontend-api-base-url <url>      Frontend API base URL injected at build time. Default: /__aisys__/api
  --frontend-gateway-base-url <url>  Frontend gateway base URL injected at build time. Default: infer from browser origin
  --include-local-env                Copy local backend/.env and frontend/.env as .env.example.local
  -h, --help                         Show this help
USAGE
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --output-dir)
      OUTPUT_DIR="$2"
      shift 2
      ;;
    --package-name)
      PACKAGE_NAME="$2"
      shift 2
      ;;
    --archive-format)
      ARCHIVE_FORMAT="$2"
      shift 2
      ;;
    --frontend-api-base-url)
      FRONTEND_API_BASE_URL="$2"
      shift 2
      ;;
    --frontend-gateway-base-url)
      FRONTEND_GATEWAY_BASE_URL="$2"
      shift 2
      ;;
    --include-local-env)
      INCLUDE_LOCAL_ENV="1"
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "Unknown option: $1" >&2
      usage >&2
      exit 1
      ;;
  esac
done

case "$ARCHIVE_FORMAT" in
  tar.gz|zip|both) ;;
  *)
    echo "Invalid --archive-format: $ARCHIVE_FORMAT" >&2
    exit 1
    ;;
esac

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
RELEASE_ROOT="$REPO_ROOT/$OUTPUT_DIR"
PACKAGE_ROOT="$RELEASE_ROOT/$PACKAGE_NAME"
TAR_ARCHIVE_PATH="$RELEASE_ROOT/$PACKAGE_NAME.tar.gz"
ZIP_ARCHIVE_PATH="$RELEASE_ROOT/$PACKAGE_NAME.zip"

copy_required_item() {
  local source_path="$1"
  local destination_path="$2"

  if [ ! -e "$source_path" ]; then
    echo "Required path not found: $source_path" >&2
    exit 1
  fi

  cp -R "$source_path" "$destination_path"
}

copy_release_backend_package_json() {
  local source_path="$1"
  local destination_path="$2"

  if [ ! -f "$source_path" ]; then
    echo "Required path not found: $source_path" >&2
    exit 1
  fi

  node - "$source_path" "$destination_path" <<'NODE'
const fs = require('node:fs')

const [sourcePath, destinationPath] = process.argv.slice(2)
const packageJson = JSON.parse(fs.readFileSync(sourcePath, 'utf8'))

packageJson.scripts = {
  'check:runtime': 'node dist/scripts/preflight/check-node-sqlite.js',
  start: 'node dist/scripts/preflight/check-node-sqlite.js && node dist/server.js'
}

fs.writeFileSync(destinationPath, `${JSON.stringify(packageJson, null, 2)}\n`, 'utf8')
NODE
}

create_zip_archive() {
  if command -v zip >/dev/null 2>&1; then
    (cd "$RELEASE_ROOT" && zip -qry "$ZIP_ARCHIVE_PATH" "$PACKAGE_NAME")
    return
  fi

  if command -v ditto >/dev/null 2>&1; then
    ditto -c -k --sequesterRsrc --keepParent "$PACKAGE_ROOT" "$ZIP_ARCHIVE_PATH"
    return
  fi

  if [ "$ARCHIVE_FORMAT" = "zip" ]; then
    echo "zip or ditto is required to create a zip archive." >&2
    exit 1
  fi

  echo "==> Skipped zip archive because neither zip nor ditto is available"
}

cd "$REPO_ROOT"

if ! command -v node >/dev/null 2>&1; then
  echo "Node.js LTS is required for packaging. Install Node.js 22.x LTS (>=22.13.0) or 24.x LTS (>=24.11.0) first." >&2
  exit 1
fi

if ! command -v pnpm >/dev/null 2>&1; then
  if command -v corepack >/dev/null 2>&1; then
    corepack enable
    corepack prepare pnpm@latest --activate
  else
    echo "pnpm is required. Install pnpm or enable corepack first." >&2
    exit 1
  fi
fi

pnpm --filter juhe-ai-backend check:runtime

export VITE_JUHE_AI_API_BASE_URL="$FRONTEND_API_BASE_URL"
export VITE_JUHE_AI_GATEWAY_BASE_URL="$FRONTEND_GATEWAY_BASE_URL"

echo "==> Building workspace"
echo "==> Frontend API base URL: $FRONTEND_API_BASE_URL"
if [ -n "$FRONTEND_GATEWAY_BASE_URL" ]; then
  echo "==> Frontend gateway base URL: $FRONTEND_GATEWAY_BASE_URL"
else
  echo "==> Frontend gateway base URL: inferred from browser origin"
fi
pnpm build

echo "==> Preparing release folder"
rm -rf "$PACKAGE_ROOT"
mkdir -p "$PACKAGE_ROOT/backend" "$PACKAGE_ROOT/frontend" "$PACKAGE_ROOT/docs"

copy_required_item "$REPO_ROOT/package.json" "$PACKAGE_ROOT/package.json"
copy_required_item "$REPO_ROOT/pnpm-lock.yaml" "$PACKAGE_ROOT/pnpm-lock.yaml"
copy_required_item "$REPO_ROOT/pnpm-workspace.yaml" "$PACKAGE_ROOT/pnpm-workspace.yaml"
copy_release_backend_package_json "$REPO_ROOT/backend/package.json" "$PACKAGE_ROOT/backend/package.json"
copy_required_item "$REPO_ROOT/backend/.env.example" "$PACKAGE_ROOT/backend/.env.example"
copy_required_item "$REPO_ROOT/backend/dist" "$PACKAGE_ROOT/backend/dist"
copy_required_item "$REPO_ROOT/frontend/package.json" "$PACKAGE_ROOT/frontend/package.json"
copy_required_item "$REPO_ROOT/frontend/.env.example" "$PACKAGE_ROOT/frontend/.env.example"
copy_required_item "$REPO_ROOT/frontend/dist" "$PACKAGE_ROOT/frontend/dist"
copy_required_item "$REPO_ROOT/deploy/start.sh" "$PACKAGE_ROOT/start.sh"
copy_required_item "$REPO_ROOT/deploy/start.ps1" "$PACKAGE_ROOT/start.ps1"
copy_required_item "$REPO_ROOT/deploy/README.md" "$PACKAGE_ROOT/README.md"
copy_required_item "$REPO_ROOT/docs/deploy" "$PACKAGE_ROOT/docs/deploy"

TMP_START_SCRIPT="$(mktemp)"
tr -d '\r' < "$PACKAGE_ROOT/start.sh" > "$TMP_START_SCRIPT"
mv "$TMP_START_SCRIPT" "$PACKAGE_ROOT/start.sh"
chmod +x "$PACKAGE_ROOT/start.sh"

if [ "$INCLUDE_LOCAL_ENV" = "1" ] && [ -f "$REPO_ROOT/backend/.env" ]; then
  cp "$REPO_ROOT/backend/.env" "$PACKAGE_ROOT/backend/.env.example.local"
  echo "==> Copied backend/.env as backend/.env.example.local; review secrets before sharing"
fi

if [ "$INCLUDE_LOCAL_ENV" = "1" ] && [ -f "$REPO_ROOT/frontend/.env" ]; then
  cp "$REPO_ROOT/frontend/.env" "$PACKAGE_ROOT/frontend/.env.example.local"
  echo "==> Copied frontend/.env as frontend/.env.example.local; frontend dist is already built"
fi

if [ "$ARCHIVE_FORMAT" = "tar.gz" ] || [ "$ARCHIVE_FORMAT" = "both" ]; then
  echo "==> Creating tar.gz archive"
  rm -f "$TAR_ARCHIVE_PATH"
  tar -czf "$TAR_ARCHIVE_PATH" -C "$RELEASE_ROOT" "$PACKAGE_NAME"
  echo "==> Done: $TAR_ARCHIVE_PATH"
fi

if [ "$ARCHIVE_FORMAT" = "zip" ] || [ "$ARCHIVE_FORMAT" = "both" ]; then
  echo "==> Creating zip archive"
  rm -f "$ZIP_ARCHIVE_PATH"
  create_zip_archive
  if [ -f "$ZIP_ARCHIVE_PATH" ]; then
    echo "==> Done: $ZIP_ARCHIVE_PATH"
  fi
fi

echo "Upload the archive to the target server, extract it, then run start.sh on Linux/macOS or start.ps1 on Windows."
