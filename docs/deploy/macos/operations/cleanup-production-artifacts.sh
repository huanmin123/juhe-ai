#!/usr/bin/env bash
set -euo pipefail

# This is intentionally a narrow, evidence-first cleanup tool. It never deletes
# the active release, database files, audit payload blobs, or business backups.
MODE=dry-run
BASE_DIR="${HOME}/juhe-ai-lite"
KEEP_RELEASE_COUNT=2
PRUNE_RELEASES=0
PRUNE_STALE_LINKS=0
PRUNE_AUDIT_HOT=0
STALE_LINK_MIN_AGE_HOURS=24
AUDIT_HOT_DIR=
AUDIT_SUCCESS_HOT_RETENTION_HOURS=
AUDIT_SUCCESS_SAMPLE_RATE=

usage() {
  cat <<'EOF'
Usage: cleanup-production-artifacts.sh [--dry-run|--apply] [options]

Default mode is --dry-run. --apply is required before any deletion.

  --base-dir ABSOLUTE_PATH
      Production runtime base. Default: $HOME/juhe-ai-lite
  --prune-releases
      Remove only unreferenced historical release directories.
  --keep-release-count N
      Preserve at least N newest releases, including the active release. Minimum: 2.
  --prune-stale-links
      Remove stale current.next.* deployment-preparation symlinks.
  --stale-link-min-age-hours N
      Minimum age before a current.next.* link is removable. Default: 24.
  --prune-audit-hot
      Remove only audit-hot-YYYYMMDDHH.ndjson files older than the configured hot window.
  --audit-hot-dir ABSOLUTE_PATH
      Audit hot-search directory. Default: <base-dir>/shared/data/audit/search-hot.
  --audit-success-hot-retention-hours N
      Required with --prune-audit-hot. Must match the running configuration (0..168).
  --audit-success-sample-rate RATE
      Required with --prune-audit-hot. Must match the running configuration (0..1).

The script refuses release cleanup while a fresh current.next.* link exists, and
skips any release it cannot prove is unreferenced by protected links, processes,
LaunchDaemon plists, or runtime launch scripts.
EOF
}

die() {
  printf 'ERROR %s\n' "$*" >&2
  exit 2
}

require_nonnegative_integer() {
  local value="$1"
  local name="$2"
  [[ "$value" =~ ^[0-9]+$ ]] || die "$name must be a non-negative integer"
}

canonical_directory() {
  local path="$1"
  [ -d "$path" ] || return 1
  (CDPATH= cd -- "$path" && pwd -P)
}

path_within() {
  local path="$1"
  local parent="$2"
  case "$path" in
    "$parent"|"$parent"/*) return 0 ;;
    *) return 1 ;;
  esac
}

file_mtime_epoch() {
  local path="$1"
  local value
  if value="$(stat -f '%m' "$path" 2>/dev/null)" && [[ "$value" =~ ^[0-9]+$ ]]; then
    printf '%s\n' "$value"
    return 0
  fi
  stat -c '%Y' "$path"
}

directory_kib() {
  du -sk "$1" 2>/dev/null | awk '{ print $1 }'
}

has_runtime_entries() {
  local path="$1"
  local entry
  [ -d "$path" ] || return 1
  entry="$(/bin/ls -A "$path" 2>/dev/null | /usr/bin/head -n 1)" || return 1
  [ -n "$entry" ]
}

release_parent_for_path() {
  local path="$1"
  local relative_path
  local release_name
  case "$path" in
    "$RELEASES_DIR"/*) ;;
    *) return 1 ;;
  esac
  relative_path="${path#"$RELEASES_DIR"/}"
  release_name="${relative_path%%/*}"
  [[ "$release_name" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$ ]] || return 1
  [ -d "$RELEASES_DIR/$release_name" ] && [ ! -L "$RELEASES_DIR/$release_name" ] || return 1
  canonical_directory "$RELEASES_DIR/$release_name"
}

release_has_runtime_contamination() {
  local release_path="$1"
  local app_root
  local runtime_path
  for app_root in "$release_path" "$release_path"/*; do
    [ -d "$app_root" ] || continue
    for runtime_path in \
      "$app_root/backend/data" \
      "$app_root/backend/logs" \
      "$app_root/logs"; do
      if has_runtime_entries "$runtime_path"; then
        printf 'RUNTIME_CONTAMINATION release=%s path=%s kib=%s\n' \
          "$release_path" "$runtime_path" "$(directory_kib "$runtime_path")"
      fi
    done
  done
}

append_release_protection() {
  local path="$1"
  local release_parent
  release_parent="$(release_parent_for_path "$path" 2>/dev/null || true)"
  [ -n "$release_parent" ] || return 0
  printf '%s\n' "$release_parent" >> "$PROTECTED_RELEASES_RAW"
}

collect_protected_releases() {
  local link_path
  local resolved_link
  : > "$PROTECTED_RELEASES_RAW"

  if [ -e "$BASE_DIR/current" ]; then
    resolved_link="$(canonical_directory "$BASE_DIR/current" 2>/dev/null || true)"
    [ -z "$resolved_link" ] || append_release_protection "$resolved_link"
  fi

  # Runtime data and historical releases can be large. Neither contains formal
  # slot links, so skip both trees while looking for rollback/current links.
  while IFS= read -r -d '' link_path; do
    if [ "$PRUNE_STALE_LINKS" -eq 1 ] && grep -F -x -q "$link_path" "$STALE_NEXT_LINKS"; then
      # Both dry-run and apply must assess the release graph after this proven
      # stale deployment-preparation link has been removed.
      continue
    fi
    resolved_link="$(canonical_directory "$link_path" 2>/dev/null || true)"
    [ -z "$resolved_link" ] || append_release_protection "$resolved_link"
  done < "$SLOT_LINKS"

  sort -u "$PROTECTED_RELEASES_RAW" > "$PROTECTED_RELEASES"
}

release_is_protected() {
  local release_path="$1"
  grep -F -x -q "$release_path" "$PROTECTED_RELEASES"
}

mark_newest_retained_releases() {
  local release_path
  local release_name
  local mtime
  : > "$RELEASES_BY_MTIME"
  while IFS= read -r -d '' release_path; do
    release_name="$(basename "$release_path")"
    if [[ ! "$release_name" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$ ]]; then
      printf 'BLOCKED_RELEASE invalid-name=%s\n' "$release_path"
      continue
    fi
    [ ! -L "$release_path" ] || { printf 'BLOCKED_RELEASE symbolic-link=%s\n' "$release_path"; continue; }
    mtime="$(file_mtime_epoch "$release_path")" || die "cannot read release mtime: $release_path"
    printf '%s\t%s\n' "$mtime" "$(canonical_directory "$release_path")" >> "$RELEASES_BY_MTIME"
  done < "$RELEASE_DIRS"

  sort -rn "$RELEASES_BY_MTIME" | awk -F '\t' -v count="$KEEP_RELEASE_COUNT" 'NR <= count { print $2 }' > "$RETAINED_BY_AGE"
}

release_is_retained_by_age() {
  local release_path="$1"
  grep -F -x -q "$release_path" "$RETAINED_BY_AGE"
}

reference_in_files() {
  local release_path="$1"
  local scan_root="$2"
  local scan_file="$3"
  local entry
  local grep_status
  [ -d "$scan_root" ] || return 1
  if ! find "$scan_root" -xdev -type f -print0 > "$scan_file"; then
    printf 'REFERENCE_SCAN_FAILED release=%s root=%s\n' "$release_path" "$scan_root" >&2
    return 2
  fi
  while IFS= read -r -d '' entry; do
    if grep -F -q "$release_path" "$entry" 2>/dev/null; then
      printf 'REFERENCE_FOUND release=%s file=%s\n' "$release_path" "$entry" >&2
      return 0
    fi
    grep_status=$?
    if [ "$grep_status" -gt 1 ]; then
      printf 'REFERENCE_SCAN_FAILED release=%s file=%s\n' "$release_path" "$entry" >&2
      return 2
    fi
  done < "$scan_file"
  return 1
}

release_reference_state() {
  local release_path="$1"
  local scan_root
  local state
  if release_is_protected "$release_path"; then
    printf 'protected-link'
    return 0
  fi
  if release_is_retained_by_age "$release_path"; then
    printf 'retained-by-age'
    return 0
  fi
  while IFS= read -r process_command; do
    case "$process_command" in
      *"$release_path"*) printf 'active-process'; return 0 ;;
    esac
  done < "$PROCESS_LIST"
  for scan_root in "$BASE_DIR/bin" "$HOME/Library/LaunchAgents" /Library/LaunchDaemons; do
    state=1
    reference_in_files "$release_path" "$scan_root" "$TEMP_DIR/reference-scan.$RANDOM" || state=$?
    case "$state" in
      0) printf 'runtime-reference'; return 0 ;;
      2) printf 'unverifiable-reference-scan'; return 0 ;;
    esac
  done
  printf 'unreferenced'
}

current_next_link_is_stale() {
  local link_path="$1"
  local link_mtime
  link_mtime="$(file_mtime_epoch "$link_path")" || return 1
  [ $((NOW_EPOCH - link_mtime)) -ge $((STALE_LINK_MIN_AGE_HOURS * 3600)) ]
}

collect_current_next_links() {
  local link_path
  : > "$STALE_NEXT_LINKS"
  : > "$FRESH_NEXT_LINKS"
  while IFS= read -r -d '' link_path; do
    if current_next_link_is_stale "$link_path"; then
      printf '%s\n' "$link_path" >> "$STALE_NEXT_LINKS"
    else
      printf '%s\n' "$link_path" >> "$FRESH_NEXT_LINKS"
    fi
  done < "$SLOT_LINKS"
}

collect_slot_links() {
  if ! find "$BASE_DIR" -xdev \
    -path "$RELEASES_DIR" -prune -o \
    -path "$BASE_DIR/shared" -prune -o \
    -path "$BASE_DIR/logs" -prune -o \
    -type l -print0 > "$SLOT_LINKS"; then
    die "cannot inspect symbolic links under base directory: $BASE_DIR"
  fi
}

collect_release_directories() {
  if ! find "$RELEASES_DIR" -xdev -mindepth 1 -maxdepth 1 -type d -print0 > "$RELEASE_DIRS"; then
    die "cannot inspect release directories: $RELEASES_DIR"
  fi
}

collect_process_list() {
  if /bin/ps ax -o command= > "$PROCESS_LIST" 2>/dev/null; then
    return 0
  fi
  if /bin/ps ax -o args= > "$PROCESS_LIST" 2>/dev/null; then
    return 0
  fi
  die 'cannot inspect running processes; refusing release cleanup'
}

audit_hot_bucket_epoch() {
  local file_name="$1"
  local stamp="${file_name#audit-hot-}"
  stamp="${stamp%.ndjson}"
  [[ "$stamp" =~ ^[0-9]{10}$ ]] || return 1
  if date -u -j -f '%Y%m%d%H' "$stamp" '+%s' >/dev/null 2>&1; then
    date -u -j -f '%Y%m%d%H' "$stamp" '+%s'
    return 0
  fi
  date -u -d "${stamp:0:4}-${stamp:4:2}-${stamp:6:2} ${stamp:8:2}:00:00" '+%s'
}

validate_audit_policy() {
  [ -n "$AUDIT_SUCCESS_HOT_RETENTION_HOURS" ] || die '--prune-audit-hot requires --audit-success-hot-retention-hours'
  [ -n "$AUDIT_SUCCESS_SAMPLE_RATE" ] || die '--prune-audit-hot requires --audit-success-sample-rate'
  require_nonnegative_integer "$AUDIT_SUCCESS_HOT_RETENTION_HOURS" '--audit-success-hot-retention-hours'
  [ "$AUDIT_SUCCESS_HOT_RETENTION_HOURS" -le 168 ] || die '--audit-success-hot-retention-hours must be at most 168'
  printf '%s' "$AUDIT_SUCCESS_SAMPLE_RATE" | grep -Eq '^(0|1|0\.[0-9]{1,4}|1\.0{1,4})$' \
    || die '--audit-success-sample-rate must be between 0 and 1 with at most four decimals'
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --dry-run) MODE=dry-run; shift ;;
    --apply) MODE=apply; shift ;;
    --base-dir) BASE_DIR="${2:-}"; shift 2 ;;
    --keep-release-count) KEEP_RELEASE_COUNT="${2:-}"; shift 2 ;;
    --prune-releases) PRUNE_RELEASES=1; shift ;;
    --prune-stale-links) PRUNE_STALE_LINKS=1; shift ;;
    --stale-link-min-age-hours) STALE_LINK_MIN_AGE_HOURS="${2:-}"; shift 2 ;;
    --prune-audit-hot) PRUNE_AUDIT_HOT=1; shift ;;
    --audit-hot-dir) AUDIT_HOT_DIR="${2:-}"; shift 2 ;;
    --audit-success-hot-retention-hours) AUDIT_SUCCESS_HOT_RETENTION_HOURS="${2:-}"; shift 2 ;;
    --audit-success-sample-rate) AUDIT_SUCCESS_SAMPLE_RATE="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown option: $1" ;;
  esac
done

case "$BASE_DIR" in /*) ;; *) die '--base-dir must be absolute' ;; esac
require_nonnegative_integer "$KEEP_RELEASE_COUNT" '--keep-release-count'
[ "$KEEP_RELEASE_COUNT" -ge 2 ] || die '--keep-release-count must be at least 2'
require_nonnegative_integer "$STALE_LINK_MIN_AGE_HOURS" '--stale-link-min-age-hours'
[ -n "$AUDIT_HOT_DIR" ] || AUDIT_HOT_DIR="$BASE_DIR/shared/data/audit/search-hot"
case "$AUDIT_HOT_DIR" in /*) ;; *) die '--audit-hot-dir must be absolute' ;; esac
[ "$PRUNE_AUDIT_HOT" -eq 0 ] || validate_audit_policy

BASE_DIR="$(canonical_directory "$BASE_DIR")" || die "base directory does not exist: $BASE_DIR"
RELEASES_DIR="$BASE_DIR/releases"
[ -d "$RELEASES_DIR" ] && [ ! -L "$RELEASES_DIR" ] || die "release directory must be a real directory: $RELEASES_DIR"
NOW_EPOCH="$(date -u '+%s')"
TEMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/juhe-ai-production-cleanup.XXXXXX")" || die 'cannot create temporary directory'
trap 'rm -rf "$TEMP_DIR"' EXIT HUP INT TERM
PROTECTED_RELEASES_RAW="$TEMP_DIR/protected-releases.raw"
PROTECTED_RELEASES="$TEMP_DIR/protected-releases"
RELEASES_BY_MTIME="$TEMP_DIR/releases-by-mtime"
RETAINED_BY_AGE="$TEMP_DIR/retained-by-age"
STALE_NEXT_LINKS="$TEMP_DIR/stale-next-links"
FRESH_NEXT_LINKS="$TEMP_DIR/fresh-next-links"
SLOT_LINKS="$TEMP_DIR/slot-links"
RELEASE_DIRS="$TEMP_DIR/release-directories"
PROCESS_LIST="$TEMP_DIR/processes"
AUDIT_HOT_FILES="$TEMP_DIR/audit-hot-files"

printf 'MODE=%s base=%s keep_release_count=%s\n' "$MODE" "$BASE_DIR" "$KEEP_RELEASE_COUNT"
collect_slot_links
collect_current_next_links
if [ -s "$FRESH_NEXT_LINKS" ]; then
  while IFS= read -r link_path; do
    printf 'BLOCKED_CURRENT_NEXT fresh-link=%s\n' "$link_path"
  done < "$FRESH_NEXT_LINKS"
  if [ "$MODE" = apply ] && { [ "$PRUNE_RELEASES" -eq 1 ] || [ "$PRUNE_STALE_LINKS" -eq 1 ]; }; then
    die 'fresh current.next.* deployment link exists; do not clean during an active deployment'
  fi
fi

if [ "$PRUNE_STALE_LINKS" -eq 1 ]; then
  while IFS= read -r link_path; do
    [ -n "$link_path" ] || continue
    if [ "$MODE" = apply ]; then
      rm -f "$link_path"
      printf 'DELETED_STALE_LINK path=%s\n' "$link_path"
    else
      printf 'WOULD_DELETE_STALE_LINK path=%s\n' "$link_path"
    fi
  done < "$STALE_NEXT_LINKS"
fi

collect_protected_releases
collect_release_directories
collect_process_list
mark_newest_retained_releases
while IFS= read -r -d '' release_path; do
  release_path="$(canonical_directory "$release_path")" || continue
  release_has_runtime_contamination "$release_path"
  release_state="$(release_reference_state "$release_path")"
  if [ "$release_state" != unreferenced ]; then
    printf 'SKIPPED_RELEASE path=%s reason=%s kib=%s\n' "$release_path" "$release_state" "$(directory_kib "$release_path")"
    continue
  fi
  if [ "$PRUNE_RELEASES" -eq 0 ]; then
    printf 'CANDIDATE_RELEASE path=%s kib=%s\n' "$release_path" "$(directory_kib "$release_path")"
  elif [ "$MODE" = apply ]; then
    release_kib="$(directory_kib "$release_path")"
    rm -rf "$release_path"
    printf 'DELETED_RELEASE path=%s kib=%s\n' "$release_path" "$release_kib"
  else
    printf 'WOULD_DELETE_RELEASE path=%s kib=%s\n' "$release_path" "$(directory_kib "$release_path")"
  fi
done < "$RELEASE_DIRS"

if [ "$PRUNE_AUDIT_HOT" -eq 1 ]; then
  if [ ! -d "$AUDIT_HOT_DIR" ]; then
    printf 'AUDIT_HOT_DIRECTORY_ABSENT path=%s\n' "$AUDIT_HOT_DIR"
  else
    audit_hot_physical="$(canonical_directory "$AUDIT_HOT_DIR")" || die "cannot resolve audit hot directory: $AUDIT_HOT_DIR"
    path_within "$audit_hot_physical" "$BASE_DIR" || die "audit hot directory escapes base directory: $AUDIT_HOT_DIR"
    audit_cutoff_epoch=$((NOW_EPOCH - AUDIT_SUCCESS_HOT_RETENTION_HOURS * 3600))
    if ! find "$audit_hot_physical" -xdev -type f -name 'audit-hot-??????????.ndjson' -print0 > "$AUDIT_HOT_FILES"; then
      die "cannot inspect audit hot files: $audit_hot_physical"
    fi
    while IFS= read -r -d '' audit_hot_file; do
      audit_bucket_epoch="$(audit_hot_bucket_epoch "$(basename "$audit_hot_file")" 2>/dev/null || true)"
      [ -n "$audit_bucket_epoch" ] || { printf 'SKIPPED_AUDIT_HOT invalid-name=%s\n' "$audit_hot_file"; continue; }
      if [ $((audit_bucket_epoch + 3600)) -gt "$audit_cutoff_epoch" ]; then
        continue
      fi
      if [ "$MODE" = apply ]; then
        audit_hot_kib="$(directory_kib "$audit_hot_file")"
        rm -f "$audit_hot_file"
        printf 'DELETED_AUDIT_HOT path=%s kib=%s\n' "$audit_hot_file" "$audit_hot_kib"
      else
        printf 'WOULD_DELETE_AUDIT_HOT path=%s kib=%s\n' "$audit_hot_file" "$(directory_kib "$audit_hot_file")"
      fi
    done < "$AUDIT_HOT_FILES"
  fi
fi

printf 'CLEANUP_COMPLETE mode=%s\n' "$MODE"
