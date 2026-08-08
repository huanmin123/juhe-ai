#!/usr/bin/env bash
set -euo pipefail

OUTPUT_DIR="release"
PACKAGE_NAME="juhe-ai-release"
ARCHIVE_FORMAT="both"
FRONTEND_API_BASE_URL="/__aisys__/api"
FRONTEND_GATEWAY_BASE_URL=""
EXPECTED_COMMIT=""
TARGET_GOOS=""
TARGET_GOARCH=""

usage() {
  cat <<'USAGE'
Usage: bash ./scripts/package-release.sh [options]

Options:
  --output-dir <dir>                 Output directory. Default: release
  --package-name <name>              Package folder/archive name. Default: juhe-ai-release
  --archive-format <tar.gz|zip|both> Archive format. Default: both
  --frontend-api-base-url <url>      Frontend API base URL injected at build time. Default: /__aisys__/api
  --frontend-gateway-base-url <url>  Frontend gateway base URL injected at build time. Default: infer from browser origin
  --expected-commit <sha>            Require the release source to match this full commit SHA
  --goos <linux|darwin>              Go indexer target OS. Default: current Unix host OS
  --goarch <amd64|arm64>             Go indexer target architecture. Default: current Unix host architecture
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
    --expected-commit)
      EXPECTED_COMMIT="$2"
      shift 2
      ;;
    --goos)
      TARGET_GOOS="$2"
      shift 2
      ;;
    --goarch)
      TARGET_GOARCH="$2"
      shift 2
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

detect_host_goos() {
  case "$(uname -s)" in
    Linux) printf '%s' 'linux' ;;
    Darwin) printf '%s' 'darwin' ;;
    *)
      echo "Unable to infer Go target OS from uname -s. Pass --goos linux or --goos darwin explicitly." >&2
      exit 1
      ;;
  esac
}

detect_host_goarch() {
  case "$(uname -m)" in
    x86_64|amd64) printf '%s' 'amd64' ;;
    aarch64|arm64) printf '%s' 'arm64' ;;
    *)
      echo "Unable to infer Go target architecture from uname -m. Pass --goarch amd64 or --goarch arm64 explicitly." >&2
      exit 1
      ;;
  esac
}

if [ -z "$TARGET_GOOS" ]; then
  TARGET_GOOS="$(detect_host_goos)"
fi

if [ -z "$TARGET_GOARCH" ]; then
  TARGET_GOARCH="$(detect_host_goarch)"
fi

case "$TARGET_GOOS" in
  linux|darwin) ;;
  *)
    echo "Invalid --goos: $TARGET_GOOS. Expected linux or darwin." >&2
    exit 1
    ;;
esac

case "$TARGET_GOARCH" in
  amd64|arm64) ;;
  *)
    echo "Invalid --goarch: $TARGET_GOARCH. Expected amd64 or arm64." >&2
    exit 1
    ;;
esac

SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
VALIDATOR_PATH="$SCRIPT_DIR/validate-release-package.mjs"

case "$PACKAGE_NAME" in
  ""|[!A-Za-z0-9]*|*[!A-Za-z0-9._-]*)
    echo "Package name must be 1-80 characters and contain only letters, numbers, dot, underscore, or hyphen; it must start with a letter or number." >&2
    exit 1
    ;;
esac

if [ "${#PACKAGE_NAME}" -gt 80 ]; then
  echo "Package name must be 1-80 characters and contain only letters, numbers, dot, underscore, or hyphen; it must start with a letter or number." >&2
  exit 1
fi

case "$OUTPUT_DIR" in
  ""|/*)
    echo "Output directory must be a non-empty relative path inside the repository." >&2
    exit 1
    ;;
esac

case "$OUTPUT_DIR" in
  *\\*|*:*)
    echo "Output directory must use repository-relative POSIX path syntax." >&2
    exit 1
    ;;
esac

case "/$OUTPUT_DIR/" in
  */../*)
    echo "Output directory must not contain parent-directory traversal segments (..)." >&2
    exit 1
    ;;
esac

if [ ! -f "$VALIDATOR_PATH" ]; then
  echo "Release package validator not found: $VALIDATOR_PATH" >&2
  exit 1
fi

if [ -L "$VALIDATOR_PATH" ]; then
  echo "Release package validator must be a regular repository file: $VALIDATOR_PATH" >&2
  exit 1
fi

RELEASE_ROOT="$REPO_ROOT/$OUTPUT_DIR"
PACKAGE_ROOT="$RELEASE_ROOT/$PACKAGE_NAME"
TAR_ARCHIVE_PATH="$RELEASE_ROOT/$PACKAGE_NAME.tar.gz"
ZIP_ARCHIVE_PATH="$RELEASE_ROOT/$PACKAGE_NAME.zip"

assert_safe_output_ancestors() {
  local current_path="$REPO_ROOT"
  local component
  local remaining_path="$OUTPUT_DIR"

  while :; do
    case "$remaining_path" in
      */*)
        component="${remaining_path%%/*}"
        remaining_path="${remaining_path#*/}"
        ;;
      *)
        component="$remaining_path"
        remaining_path=""
        ;;
    esac

    if [ -z "$component" ] || [ "$component" = "." ]; then
      :
    else
      current_path="$current_path/$component"
      if [ -L "$current_path" ]; then
        echo "Output directory must not traverse a symbolic link: $current_path" >&2
        exit 1
      fi

      if [ -e "$current_path" ] && [ ! -d "$current_path" ]; then
        echo "Output directory contains a non-directory path component: $current_path" >&2
        exit 1
      fi
    fi

    if [ -z "$remaining_path" ]; then
      break
    fi
  done
}

assert_safe_removal_target() {
  local target_path="$1"
  local expected_parent="$2"
  local recursive_check="${3:-0}"

  if [ "$(dirname "$target_path")" != "$expected_parent" ]; then
    echo "Refusing to remove a path outside the release directory: $target_path" >&2
    exit 1
  fi

  case "$target_path" in
    "$expected_parent"/*) ;;
    *)
      echo "Refusing to remove a path outside the release directory: $target_path" >&2
      exit 1
      ;;
  esac

  if [ -L "$target_path" ]; then
    echo "Refusing to remove a symbolic-link target: $target_path" >&2
    exit 1
  fi

  if [ "$recursive_check" = "1" ] && [ -e "$target_path" ]; then
    node "$VALIDATOR_PATH" --quiet --links-only "$target_path"
  fi
}

assert_safe_output_ancestors

copy_required_item() {
  local source_path="$1"
  local destination_path="$2"

  if [ ! -e "$source_path" ]; then
    echo "Required path not found: $source_path" >&2
    exit 1
  fi

  node "$VALIDATOR_PATH" --quiet --links-only "$source_path"
  cp -R "$source_path" "$destination_path"
}

copy_release_backend_package_json() {
  local source_path="$1"
  local destination_path="$2"

  if [ ! -f "$source_path" ]; then
    echo "Required path not found: $source_path" >&2
    exit 1
  fi

  node "$VALIDATOR_PATH" --quiet --links-only "$source_path"
  node - "$source_path" "$destination_path" <<'NODE'
const fs = require('node:fs')

const [sourcePath, destinationPath] = process.argv.slice(2)
const packageJson = JSON.parse(fs.readFileSync(sourcePath, 'utf8'))

packageJson.scripts = {
  'check:runtime': 'node dist/scripts/preflight/check-node-sqlite.js',
  'maintenance:backfill-account-balance': 'node dist/scripts/maintenance/run-account-balance-backfill.js',
  'ops:drain-redis-streams': 'node dist/scripts/operations/drain-redis-streams.js',
  'ops:redis-queue-fence': 'node dist/scripts/operations/manage-redis-queue-fence.js',
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

bash "$SCRIPT_DIR/assert-release-source.sh" "$REPO_ROOT" "$EXPECTED_COMMIT"
RELEASE_SOURCE_COMMIT="$(git -C "$REPO_ROOT" rev-parse HEAD)"

if ! command -v node >/dev/null 2>&1; then
  echo "Node.js LTS is required for packaging. Install Node.js 22.x LTS (>=22.13.0) or 24.x LTS (>=24.11.0) first." >&2
  exit 1
fi

if ! command -v go >/dev/null 2>&1; then
  echo "Go is required to build backend-go/juhe-ai-runtime-log-indexer for $TARGET_GOOS/$TARGET_GOARCH." >&2
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
export VITE_JUHE_AI_BUILD_ID="$RELEASE_SOURCE_COMMIT"

echo "==> Building workspace"
echo "==> Frontend API base URL: $FRONTEND_API_BASE_URL"
if [ -n "$FRONTEND_GATEWAY_BASE_URL" ]; then
  echo "==> Frontend gateway base URL: $FRONTEND_GATEWAY_BASE_URL"
else
  echo "==> Frontend gateway base URL: inferred from browser origin"
fi
pnpm build

bash "$SCRIPT_DIR/assert-release-source.sh" "$REPO_ROOT" "$RELEASE_SOURCE_COMMIT"

echo "==> Preparing release folder"
assert_safe_output_ancestors
mkdir -p "$RELEASE_ROOT"
assert_safe_output_ancestors
RELEASE_ROOT="$(cd "$RELEASE_ROOT" && pwd -P)"
case "$RELEASE_ROOT" in
  "$REPO_ROOT"/*) ;;
  *)
    echo "Output directory must resolve strictly inside the repository." >&2
    exit 1
    ;;
esac
PACKAGE_ROOT="$RELEASE_ROOT/$PACKAGE_NAME"
TAR_ARCHIVE_PATH="$RELEASE_ROOT/$PACKAGE_NAME.tar.gz"
ZIP_ARCHIVE_PATH="$RELEASE_ROOT/$PACKAGE_NAME.zip"
assert_safe_removal_target "$PACKAGE_ROOT" "$RELEASE_ROOT" 1
rm -rf "$PACKAGE_ROOT"
mkdir -p "$PACKAGE_ROOT/backend" "$PACKAGE_ROOT/backend-go" "$PACKAGE_ROOT/frontend" "$PACKAGE_ROOT/docs" "$PACKAGE_ROOT/scripts" "$PACKAGE_ROOT/deploy"
printf '%s\n' "$RELEASE_SOURCE_COMMIT" > "$PACKAGE_ROOT/RELEASE_SOURCE_COMMIT"

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
copy_required_item "$REPO_ROOT/scripts/run-with-owner-lock.mjs" "$PACKAGE_ROOT/scripts/run-with-owner-lock.mjs"
copy_required_item "$REPO_ROOT/scripts/validate-owner-manifest.mjs" "$PACKAGE_ROOT/scripts/validate-owner-manifest.mjs"
copy_required_item "$REPO_ROOT/deploy/owner-manifest.json" "$PACKAGE_ROOT/deploy/owner-manifest.json"
copy_required_item "$REPO_ROOT/deploy/owner-manifest.schema.json" "$PACKAGE_ROOT/deploy/owner-manifest.schema.json"
copy_required_item "$REPO_ROOT/deploy/README.md" "$PACKAGE_ROOT/README.md"
copy_required_item "$REPO_ROOT/docs/deploy" "$PACKAGE_ROOT/docs/deploy"

INDEXER_SOURCE_DIR="$REPO_ROOT/backend-go"
INDEXER_BINARY_PATH="$PACKAGE_ROOT/backend-go/juhe-ai-runtime-log-indexer"
if [ ! -f "$INDEXER_SOURCE_DIR/go.mod" ]; then
  echo "Go indexer module file not found: $INDEXER_SOURCE_DIR/go.mod" >&2
  exit 1
fi

echo "==> Building Go runtime log indexer for $TARGET_GOOS/$TARGET_GOARCH"
(
  cd "$INDEXER_SOURCE_DIR"
  CGO_ENABLED=0 GOOS="$TARGET_GOOS" GOARCH="$TARGET_GOARCH" go build -mod=readonly -trimpath -ldflags="-s -w" -o "$INDEXER_BINARY_PATH" ./cmd/juhe-ai-runtime-log-indexer
)

if [ ! -f "$INDEXER_BINARY_PATH" ] || [ -L "$INDEXER_BINARY_PATH" ]; then
  echo "Go runtime log indexer build did not produce a regular file: $INDEXER_BINARY_PATH" >&2
  exit 1
fi
chmod +x "$INDEXER_BINARY_PATH"

TMP_START_SCRIPT="$(mktemp)"
tr -d '\r' < "$PACKAGE_ROOT/start.sh" > "$TMP_START_SCRIPT"
mv "$TMP_START_SCRIPT" "$PACKAGE_ROOT/start.sh"
chmod +x "$PACKAGE_ROOT/start.sh"

node "$VALIDATOR_PATH" --quiet "$PACKAGE_ROOT"

if [ "$ARCHIVE_FORMAT" = "tar.gz" ] || [ "$ARCHIVE_FORMAT" = "both" ]; then
  echo "==> Creating tar.gz archive"
  assert_safe_removal_target "$TAR_ARCHIVE_PATH" "$RELEASE_ROOT"
  rm -f "$TAR_ARCHIVE_PATH"
  tar -czf "$TAR_ARCHIVE_PATH" -C "$RELEASE_ROOT" "$PACKAGE_NAME"
  echo "==> Done: $TAR_ARCHIVE_PATH"
fi

if [ "$ARCHIVE_FORMAT" = "zip" ] || [ "$ARCHIVE_FORMAT" = "both" ]; then
  echo "==> Creating zip archive"
  assert_safe_removal_target "$ZIP_ARCHIVE_PATH" "$RELEASE_ROOT"
  rm -f "$ZIP_ARCHIVE_PATH"
  create_zip_archive
  if [ -f "$ZIP_ARCHIVE_PATH" ]; then
    echo "==> Done: $ZIP_ARCHIVE_PATH"
  fi
fi

echo "Upload the archive to the target server, extract it, then run start.sh on Linux/macOS or start.ps1 on Windows."
