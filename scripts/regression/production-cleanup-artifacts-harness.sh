#!/usr/bin/env bash
set -euo pipefail

[ "$#" -eq 1 ] || { echo 'usage: production-cleanup-artifacts-harness.sh CLEANUP_SCRIPT' >&2; exit 2; }
CLEANUP_SCRIPT="$1"
[ -f "$CLEANUP_SCRIPT" ] || { echo "cleanup script not found: $CLEANUP_SCRIPT" >&2; exit 2; }

ROOT="$(mktemp -d /tmp/juhe-ai-production-cleanup-test.XXXXXX)"
cleanup() { rm -rf "$ROOT"; }
trap cleanup EXIT HUP INT TERM

mkdir -p \
  "$ROOT/releases/active/juhe-ai-release" \
  "$ROOT/releases/rollback/juhe-ai-release" \
  "$ROOT/releases/stale/juhe-ai-release/backend/data" \
  "$ROOT/shared/data/audit/search-hot"
ln -s 'releases/active/juhe-ai-release' "$ROOT/current"
ln -s 'releases/stale/juhe-ai-release' "$ROOT/current.next.stale"
printf 'active\n' > "$ROOT/releases/active/juhe-ai-release/app"
printf 'rollback\n' > "$ROOT/releases/rollback/juhe-ai-release/app"
dd if=/dev/zero of="$ROOT/releases/stale/juhe-ai-release/backend/data/legacy-audit.ndjson" bs=1024 count=16 >/dev/null 2>&1
printf 'old\n' > "$ROOT/shared/data/audit/search-hot/audit-hot-2020010100.ndjson"
CURRENT_HOUR="$(date -u '+%Y%m%d%H')"
printf 'current\n' > "$ROOT/shared/data/audit/search-hot/audit-hot-${CURRENT_HOUR}.ndjson"
touch -t 202001010101 "$ROOT/releases/stale" "$ROOT/current.next.stale"
touch -t 202001010101 "$ROOT/shared/data/audit/search-hot/audit-hot-2020010100.ndjson"
touch -t 202401010101 "$ROOT/releases/rollback"
touch -t 202501010101 "$ROOT/releases/active"

"$CLEANUP_SCRIPT" --dry-run --base-dir "$ROOT" \
  --prune-stale-links --stale-link-min-age-hours 0 \
  --prune-releases --keep-release-count 2 \
  --prune-audit-hot --audit-success-hot-retention-hours 0 --audit-success-sample-rate 0 > "$ROOT/dry-run"
grep -Fq 'RUNTIME_CONTAMINATION release=' "$ROOT/dry-run"
grep -Fq 'WOULD_DELETE_STALE_LINK' "$ROOT/dry-run"
grep -Fq 'WOULD_DELETE_RELEASE' "$ROOT/dry-run"
grep -Fq 'WOULD_DELETE_AUDIT_HOT' "$ROOT/dry-run"
[ -d "$ROOT/releases/stale" ]
[ -L "$ROOT/current.next.stale" ]
[ -f "$ROOT/shared/data/audit/search-hot/audit-hot-2020010100.ndjson" ]

"$CLEANUP_SCRIPT" --apply --base-dir "$ROOT" \
  --prune-stale-links --stale-link-min-age-hours 0 \
  --prune-releases --keep-release-count 2 \
  --prune-audit-hot --audit-success-hot-retention-hours 0 --audit-success-sample-rate 0 > "$ROOT/apply"
[ -d "$ROOT/releases/active" ]
[ -d "$ROOT/releases/rollback" ]
[ ! -e "$ROOT/releases/stale" ]
[ ! -e "$ROOT/current.next.stale" ]
[ ! -e "$ROOT/shared/data/audit/search-hot/audit-hot-2020010100.ndjson" ]
[ -f "$ROOT/shared/data/audit/search-hot/audit-hot-${CURRENT_HOUR}.ndjson" ]

printf 'production cleanup artifact harness passed\n'
