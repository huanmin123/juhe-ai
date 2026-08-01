#!/usr/bin/env bash
# Installs the intentionally narrow root WireGuard reconciler. It never manages juhe-ai,
# Caddy, Nginx, DNS, databases, or the retired generic external watchdog.
set -euo pipefail
umask 077

MODE=dry-run
ACTION=install
LABEL='com.juhe-ai.wireguard-reconciler'
MANIFEST=''
STATE_DIR='/var/db/juhe-ai/wireguard-reconciler'
INSTALL_DIR='/usr/local/libexec/juhe-ai'
CANONICAL_CONFIG_DIR='/usr/local/libexec/juhe-ai/wireguard-config'
WG_BIN='/usr/local/bin/wg'
INTERVAL_SECONDS=30
SCRIPT_SHA256=''
MIGRATOR_SHA256=''
PROBE_HELPER=''
MAINTENANCE_LOCK=''
RELEASE_LOCK=''

usage() {
  cat <<'EOF'
Usage: install-wireguard-reconciler.sh [--dry-run|--apply] --manifest <absolute-path> [options]

  --manifest <absolute-path>        root-owned private edge manifest
  --label <launchd-label>           default com.juhe-ai.wireguard-reconciler
  --state-dir <absolute-path>       default /var/db/juhe-ai/wireguard-reconciler
  --install-dir <absolute-path>     default /usr/local/libexec/juhe-ai
  --wg-bin <absolute-path>          default /usr/local/bin/wg
  --probe-helper <absolute-path>    root-owned adapter for the independent 203 TLS nonce probe
  --maintenance-lock <path>         suppress recovery while this root-owned marker exists
  --release-lock <path>             suppress recovery during a root-owned deployment marker
  --interval-seconds <seconds>      launchd interval; default 30
  --script-sha256 <hex>             required by --apply; hash of source helper
  --migrator-sha256 <hex>           required by --apply; hash of root-wrapper migrator
  --remove                          unload and remove only the reconciler (keeps root WireGuard jobs and audit state)

All changes require --apply and root. --apply verifies the source hash both before and
after copying, writes a root-only helper, transactionally replaces only this plist, and
restores its prior helper/plist/loaded state if bootstrap fails.
EOF
}

die() { printf '%s\n' "$*" >&2; exit 2; }
positive_integer() { case "$1" in ''|*[!0-9]*) return 1 ;; *) [ "$1" -gt 0 ] ;; esac; }
safe_path() {
  case "$1" in /) return 1 ;; /*) ;; *) return 1 ;; esac
  case "$1" in *$'\n'*|*'\r'*|*'\t'*|*' '*|*'$'*|*'`'*|*'"'*|*"'"*|*'\\'*|*'|'*|*';'*|*'&'*|*'<'*|*'>'*) return 1 ;; esac
  return 0
}
safe_label() { printf '%s' "$1" | /usr/bin/grep -Eq '^com\.juhe-ai\.wireguard-reconciler$'; }
sha256() { /usr/bin/shasum -a 256 "$1" | /usr/bin/awk '{print $1}'; }
sha_ok() { printf '%s' "$1" | /usr/bin/grep -Eq '^[0-9A-Fa-f]{64}$'; }
root_path_chain() {
  local path="$1" owner mode
  while [ "$path" != / ]; do
    [ ! -L "$path" ] && [ -e "$path" ] || return 1
    owner="$(/usr/bin/stat -f '%u' "$path")"
    mode="$(/usr/bin/stat -f '%Lp' "$path")"
    [ "$owner" = 0 ] || return 1
    case "$mode" in ?[2367][0-7]|??[2367]) return 1 ;; esac
    path="$(/usr/bin/dirname "$path")"
  done
  return 0
}
ensure_root_directory() {
  local path="$1" mode="$2"
  root_path_chain "$(/usr/bin/dirname "$path")" || return 1
  if [ ! -e "$path" ]; then /bin/mkdir -p "$path"; fi
  root_path_chain "$path" || return 1
  /bin/chmod "$mode" "$path"
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --dry-run) MODE=dry-run; shift ;;
    --apply) MODE=apply; shift ;;
    --remove) ACTION=remove; shift ;;
    --manifest) MANIFEST="${2:?missing --manifest value}"; shift 2 ;;
    --label) LABEL="${2:?missing --label value}"; shift 2 ;;
    --state-dir) STATE_DIR="${2:?missing --state-dir value}"; shift 2 ;;
    --install-dir) INSTALL_DIR="${2:?missing --install-dir value}"; shift 2 ;;
    --wg-bin) WG_BIN="${2:?missing --wg-bin value}"; shift 2 ;;
    --probe-helper) PROBE_HELPER="${2:?missing --probe-helper value}"; shift 2 ;;
    --maintenance-lock) MAINTENANCE_LOCK="${2:?missing --maintenance-lock value}"; shift 2 ;;
    --release-lock) RELEASE_LOCK="${2:?missing --release-lock value}"; shift 2 ;;
    --interval-seconds) INTERVAL_SECONDS="${2:?missing --interval-seconds value}"; shift 2 ;;
    --script-sha256) SCRIPT_SHA256="${2:?missing --script-sha256 value}"; shift 2 ;;
    --migrator-sha256) MIGRATOR_SHA256="${2:?missing --migrator-sha256 value}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown option: $1" ;;
  esac
done

safe_label "$LABEL" || die 'label must be the fixed WireGuard reconciler label'
if [ "$ACTION" = remove ]; then
  safe_path "$INSTALL_DIR" || die 'install-dir must be an absolute template-safe path'
  printf 'mode=%s action=remove label=%s helper=%s plist=%s\n' "$MODE" "$LABEL" "$INSTALL_DIR/wireguard-reconciler.sh" "/Library/LaunchDaemons/$LABEL.plist"
  [ "$MODE" = apply ] || exit 0
  [ "$(/usr/bin/id -u)" -eq 0 ] || die '--remove --apply requires root'
  [ ! -e "$INSTALL_DIR" ] || root_path_chain "$INSTALL_DIR" || die 'install-dir is not root-only; refusing removal'
  /bin/launchctl bootout system "/Library/LaunchDaemons/$LABEL.plist" >/dev/null 2>&1 || true
  /bin/rm -f "/Library/LaunchDaemons/$LABEL.plist" "$INSTALL_DIR/wireguard-reconciler.sh" "$INSTALL_DIR/wireguard-reconciler.manifest"
  printf 'WireGuard reconciler removed; root-managed WireGuard job wrappers and audit state were retained\n'
  exit 0
fi
[ -n "$MANIFEST" ] || die '--manifest is required'
for path in "$MANIFEST" "$STATE_DIR" "$INSTALL_DIR" "$WG_BIN"; do safe_path "$path" || die 'paths must be absolute and template-safe'; done
[ -z "$MAINTENANCE_LOCK" ] || safe_path "$MAINTENANCE_LOCK" || die 'maintenance-lock must be absolute and template-safe'
[ -n "$PROBE_HELPER" ] && safe_path "$PROBE_HELPER" || die 'probe-helper is required and must be absolute and template-safe'
[ -z "$RELEASE_LOCK" ] || safe_path "$RELEASE_LOCK" || die 'release-lock must be absolute and template-safe'
positive_integer "$INTERVAL_SECONDS" || die 'interval-seconds must be a positive integer'
[ "$INTERVAL_SECONDS" -ge 15 ] && [ "$INTERVAL_SECONDS" -le 3600 ] || die 'interval-seconds must be between 15 and 3600'

SCRIPT_PATH="${JUHE_AI_OPERATION_SCRIPT_PATH:-${BASH_SOURCE:-$0}}"
SCRIPT_DIR="$(cd "$(dirname "$SCRIPT_PATH")" && pwd)"
SOURCE_HELPER="$SCRIPT_DIR/wireguard-reconciler.sh"
SOURCE_MIGRATOR="$SCRIPT_DIR/migrate-wireguard-root-wrappers.sh"
HELPER_PATH="$INSTALL_DIR/wireguard-reconciler.sh"
HELPER_MANIFEST="$INSTALL_DIR/wireguard-reconciler.manifest"
PLIST_PATH="/Library/LaunchDaemons/$LABEL.plist"
DOMAIN=system

printf 'mode=%s label=%s manifest=%s state=%s helper=%s plist=%s\n' "$MODE" "$LABEL" "$MANIFEST" "$STATE_DIR" "$HELPER_PATH" "$PLIST_PATH"
printf 'plan: install root-only WireGuard reconciler -> bootstrap one bounded launchd pass every %ss -> no HTTP/application recovery actions\n' "$INTERVAL_SECONDS"
[ "$MODE" = apply ] || exit 0

[ "$(/usr/bin/id -u)" -eq 0 ] || die '--apply requires root'
[ ! -L "$MANIFEST" ] && [ -f "$MANIFEST" ] || die 'manifest must be a non-symlink regular file'
[ "$(/usr/bin/stat -f '%u' "$MANIFEST")" = 0 ] || die 'manifest must be owned by root'
root_path_chain "$MANIFEST" || die 'manifest and every parent must be root-owned and non-writable'
[ ! -L "$SOURCE_HELPER" ] && [ -f "$SOURCE_HELPER" ] || die 'source helper must be a regular file'
[ ! -L "$SOURCE_MIGRATOR" ] && [ -f "$SOURCE_MIGRATOR" ] || die 'source migrator must be a regular file'
[ ! -L "$WG_BIN" ] && [ -x "$WG_BIN" ] || die "wg binary must be a non-symlink executable: $WG_BIN"
root_path_chain "$WG_BIN" || die 'wg binary and every parent must be root-owned and non-writable'
[ ! -L "$PROBE_HELPER" ] && [ -x "$PROBE_HELPER" ] || die 'probe-helper must be a non-symlink executable'
[ "$(/usr/bin/stat -f '%u' "$PROBE_HELPER")" = 0 ] || die 'probe-helper must be root-owned'
root_path_chain "$PROBE_HELPER" || die 'probe-helper and every parent must be root-owned and non-writable'
# Validate the actual 64-character SHA-256 values and their source bindings. The
# historical literal glob counters below were generated with the wrong lengths.
sha_ok "$SCRIPT_SHA256" || die '--script-sha256 must be a SHA-256 hex digest'
[ "$(sha256 "$SOURCE_HELPER")" = "$(printf '%s' "$SCRIPT_SHA256" | /usr/bin/tr '[:upper:]' '[:lower:]')" ] || die 'source helper SHA-256 does not match --script-sha256'
sha_ok "$MIGRATOR_SHA256" || die '--migrator-sha256 must be a SHA-256 hex digest'
[ "$(sha256 "$SOURCE_MIGRATOR")" = "$(printf '%s' "$MIGRATOR_SHA256" | /usr/bin/tr '[:upper:]' '[:lower:]')" ] || die 'source migrator SHA-256 does not match --migrator-sha256'
if false; then
case "$SCRIPT_SHA256" in [0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f]) ;;
  *) die '--script-sha256 must be a SHA-256 hex digest' ;;
esac
[ "$(sha256 "$SOURCE_HELPER")" = "$(printf '%s' "$SCRIPT_SHA256" | /usr/bin/tr '[:upper:]' '[:lower:]')" ] || die 'source helper SHA-256 does not match --script-sha256'
case "$MIGRATOR_SHA256" in [0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f]) ;;
  *) die '--migrator-sha256 must be a SHA-256 hex digest' ;;
esac
[ "$(sha256 "$SOURCE_MIGRATOR")" = "$(printf '%s' "$MIGRATOR_SHA256" | /usr/bin/tr '[:upper:]' '[:lower:]')" ] || die 'source migrator SHA-256 does not match --migrator-sha256'

fi
ensure_root_directory "$INSTALL_DIR" 755 || die 'install-dir must already be root-only or be created below a root-only parent'
ensure_root_directory "$STATE_DIR" 700 || die 'state-dir must already be root-only or be created below a root-only parent'
/usr/bin/plutil -lint "$PLIST_PATH" >/dev/null 2>&1 || true

# Copy the verified migrator to the protected install directory before root executes it.
# Hashing a user-writable release file and then executing it in place would leave a race.
MIGRATOR_TMP="$INSTALL_DIR/.migrate-wireguard-root-wrappers.sh.tmp.$$"
MIGRATION_JOURNAL="$INSTALL_DIR/.wireguard-installer-rollback.$$"
MIGRATION_COMPLETE=0
MIGRATION_ROLLED_BACK=0
/bin/cp "$SOURCE_MIGRATOR" "$MIGRATOR_TMP"
/usr/sbin/chown root:wheel "$MIGRATOR_TMP"
/bin/chmod 700 "$MIGRATOR_TMP"
/bin/bash -n "$MIGRATOR_TMP" || { /bin/rm -f "$MIGRATOR_TMP"; die 'copied migrator has invalid shell syntax'; }
[ "$(sha256 "$MIGRATOR_TMP")" = "$(printf '%s' "$MIGRATOR_SHA256" | /usr/bin/tr '[:upper:]' '[:lower:]')" ] || { /bin/rm -f "$MIGRATOR_TMP"; die 'migrator changed while copying into root-only directory'; }
if ! /bin/bash "$MIGRATOR_TMP" --apply --manifest "$MANIFEST" --install-dir "$INSTALL_DIR" --rollback-journal "$MIGRATION_JOURNAL"; then
  /bin/rm -f "$MIGRATOR_TMP"
  die 'root WireGuard wrapper migration failed'
fi
MIGRATION_COMPLETE=1

HELPER_TMP="$HELPER_PATH.tmp.$$"
MANIFEST_TMP="$HELPER_MANIFEST.tmp.$$"
PLIST_TMP="$PLIST_PATH.tmp.$$"
HELPER_BACKUP="$HELPER_PATH.backup.$$"
MANIFEST_BACKUP="$HELPER_MANIFEST.backup.$$"
PLIST_BACKUP="$PLIST_PATH.backup.$$"
HAD_HELPER=0
HAD_HELPER_MANIFEST=0
HAD_PLIST=0
HAD_LOADED=0
INSTALL_MUTATED=0

rollback_install() {
  /bin/launchctl bootout "$DOMAIN" "$PLIST_PATH" >/dev/null 2>&1 || true
  if [ "$MIGRATION_COMPLETE" = 1 ]; then
    /bin/bash "$MIGRATOR_TMP" --rollback --rollback-journal "$MIGRATION_JOURNAL" --install-dir "$INSTALL_DIR" || return 1
    MIGRATION_ROLLED_BACK=1
  fi
  if [ "$HAD_HELPER" = 1 ]; then /bin/mv -f "$HELPER_BACKUP" "$HELPER_PATH"; else /bin/rm -f "$HELPER_PATH"; fi
  if [ "$HAD_HELPER_MANIFEST" = 1 ]; then /bin/mv -f "$MANIFEST_BACKUP" "$HELPER_MANIFEST"; else /bin/rm -f "$HELPER_MANIFEST"; fi
  if [ "$HAD_PLIST" = 1 ]; then
    /bin/mv -f "$PLIST_BACKUP" "$PLIST_PATH"
    if [ "$HAD_LOADED" = 1 ]; then /bin/launchctl bootstrap "$DOMAIN" "$PLIST_PATH" && /bin/launchctl kickstart -k "$DOMAIN/$LABEL"; fi
  else
    /bin/rm -f "$PLIST_PATH"
  fi
}
on_exit() {
  local status="$1"
  set +e
  /bin/rm -f "$HELPER_TMP" "$MANIFEST_TMP" "$PLIST_TMP"
  if [ "$status" -ne 0 ] && [ "$INSTALL_MUTATED" = 1 ]; then rollback_install || echo 'wireguard reconciler rollback failed' >&2; fi
  /bin/rm -f "$HELPER_BACKUP" "$MANIFEST_BACKUP" "$PLIST_BACKUP" "$MIGRATOR_TMP"
  if [ "$status" -eq 0 ] || [ "$MIGRATION_ROLLED_BACK" = 1 ]; then /bin/rm -rf "$MIGRATION_JOURNAL"; fi
  return "$status"
}
trap 'on_exit "$?"' EXIT

if /bin/launchctl print "$DOMAIN/$LABEL" >/dev/null 2>&1; then
  HAD_LOADED=1
  [ -f "$PLIST_PATH" ] || die 'loaded reconciler has no restorable plist'
fi
[ ! -f "$HELPER_PATH" ] || { /bin/cp -p "$HELPER_PATH" "$HELPER_BACKUP"; HAD_HELPER=1; }
[ ! -f "$HELPER_MANIFEST" ] || { /bin/cp -p "$HELPER_MANIFEST" "$MANIFEST_BACKUP"; HAD_HELPER_MANIFEST=1; }
[ ! -f "$PLIST_PATH" ] || { /bin/cp -p "$PLIST_PATH" "$PLIST_BACKUP"; HAD_PLIST=1; }
/bin/cp "$SOURCE_HELPER" "$HELPER_TMP"
/usr/sbin/chown root:wheel "$HELPER_TMP"
/bin/chmod 700 "$HELPER_TMP"
[ "$(sha256 "$HELPER_TMP")" = "$(printf '%s' "$SCRIPT_SHA256" | /usr/bin/tr '[:upper:]' '[:lower:]')" ] || die 'helper changed while installing'

while IFS=$'\t' read -r edge logical edge_label source_config source_wrapper peer config_hash wrapper_hash extra; do
  case "$edge" in ''|\#*) continue ;; esac
  printf '%s\t%s\t%s\t%s\t%s\t%s\n' "$edge" "$logical" "$edge_label" "$CANONICAL_CONFIG_DIR/$logical.conf" "$INSTALL_DIR/wireguard/$edge/run-wireguard.sh" "$peer" >> "$MANIFEST_TMP"
done < "$MANIFEST"
/usr/sbin/chown root:wheel "$MANIFEST_TMP"
/bin/chmod 600 "$MANIFEST_TMP"

cat > "$PLIST_TMP" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>$LABEL</string>
  <key>ProgramArguments</key><array>
    <string>/bin/bash</string><string>$HELPER_PATH</string><string>--once</string>
    <string>--manifest</string><string>$HELPER_MANIFEST</string>
    <string>--state-dir</string><string>$STATE_DIR</string>
    <string>--wg-bin</string><string>$WG_BIN</string>
    <string>--probe-helper</string><string>$PROBE_HELPER</string>
EOF
if [ -n "$MAINTENANCE_LOCK" ]; then
  cat >> "$PLIST_TMP" <<EOF
    <string>--maintenance-lock</string><string>$MAINTENANCE_LOCK</string>
EOF
fi
if [ -n "$RELEASE_LOCK" ]; then
  cat >> "$PLIST_TMP" <<EOF
    <string>--release-lock</string><string>$RELEASE_LOCK</string>
EOF
fi
cat >> "$PLIST_TMP" <<EOF
  </array>
  <key>RunAtLoad</key><true/>
  <key>StartInterval</key><integer>$INTERVAL_SECONDS</integer>
  <key>ProcessType</key><string>Background</string>
  <key>ThrottleInterval</key><integer>10</integer>
</dict></plist>
EOF
/usr/sbin/chown root:wheel "$PLIST_TMP"
/bin/chmod 644 "$PLIST_TMP"
/usr/bin/plutil -lint "$PLIST_TMP" >/dev/null

INSTALL_MUTATED=1
/bin/mv -f "$HELPER_TMP" "$HELPER_PATH"
/bin/mv -f "$MANIFEST_TMP" "$HELPER_MANIFEST"
/bin/mv -f "$PLIST_TMP" "$PLIST_PATH"
/bin/launchctl bootout "$DOMAIN" "$PLIST_PATH" >/dev/null 2>&1 || true
/bin/launchctl bootstrap "$DOMAIN" "$PLIST_PATH"
/bin/launchctl print "$DOMAIN/$LABEL" >/dev/null
INSTALL_MUTATED=0
/bin/rm -f "$HELPER_BACKUP" "$MANIFEST_BACKUP" "$PLIST_BACKUP" "$MIGRATOR_TMP"
/bin/rm -rf "$MIGRATION_JOURNAL"
trap - EXIT
printf 'WireGuard reconciler installed: %s/%s\n' "$DOMAIN" "$LABEL"
