#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="${1:-}"
EXPECTED_COMMIT="${2:-}"
ALLOWED_UNTRACKED_DIRECTORY="${3:-}"

if [ -z "$REPO_ROOT" ]; then
  echo "Usage: bash ./scripts/assert-release-source.sh <repo-root> [expected-commit] [allowed-untracked-directory]" >&2
  exit 2
fi
if ! command -v git >/dev/null 2>&1; then
  echo "git is required to validate the release source." >&2
  exit 1
fi

REPO_ROOT="$(cd "$REPO_ROOT" && pwd -P)"
GIT_TOP_LEVEL="$(git -C "$REPO_ROOT" rev-parse --show-toplevel)"
GIT_TOP_LEVEL="$(cd "$GIT_TOP_LEVEL" && pwd -P)"
if [ "$REPO_ROOT" != "$GIT_TOP_LEVEL" ]; then
  echo "Release source must be the Git worktree root: $GIT_TOP_LEVEL" >&2
  exit 1
fi

COMMIT="$(git -C "$REPO_ROOT" rev-parse HEAD)"
if [ -n "$EXPECTED_COMMIT" ] && [ "$COMMIT" != "$EXPECTED_COMMIT" ]; then
  echo "Release source commit $COMMIT does not match expected commit $EXPECTED_COMMIT." >&2
  exit 1
fi

STATUS="$(git -C "$REPO_ROOT" status --porcelain=v1 --untracked-files=all)"
if [ -n "$ALLOWED_UNTRACKED_DIRECTORY" ]; then
  case "$ALLOWED_UNTRACKED_DIRECTORY" in
    /*|*'..'*|*'\'*|*:*|"")
      echo "Allowed untracked directory must be a non-empty relative POSIX path inside the repository." >&2
      exit 2
      ;;
  esac

  STATUS="$(printf '%s\n' "$STATUS" | awk -v directory="$ALLOWED_UNTRACKED_DIRECTORY/" '$0 !~ /^\?\? / || index(substr($0, 4), directory) != 1')"
fi
if [ -n "$STATUS" ]; then
  echo "Release source is not clean. Build from a clean checkout of the fixed release SHA." >&2
  printf '%s\n' "$STATUS" | sed -n '1,20p' >&2
  exit 1
fi

echo "RELEASE_SOURCE_OK commit=$COMMIT"
