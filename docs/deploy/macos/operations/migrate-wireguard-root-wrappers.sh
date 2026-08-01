#!/usr/bin/env bash
# Move exact, pre-hashed WireGuard configs out of a service-user-writable release tree
# and replace every source wrapper with one audited root-owned lifecycle wrapper. This
# script is intentionally limited to the manifest allowlist.
set -euo pipefail
umask 077

MODE=dry-run
ACTION=migrate
MANIFEST=''
INSTALL_DIR='/usr/local/libexec/juhe-ai'
CANONICAL_CONFIG_DIR='/usr/local/libexec/juhe-ai/wireguard-config'
ROLLBACK_JOURNAL=''

usage() {
  cat <<'EOF'
Usage: migrate-wireguard-root-wrappers.sh [--dry-run|--apply] --manifest <absolute-path> [--install-dir <absolute-path>] [--rollback-journal <absolute-path>]
       migrate-wireguard-root-wrappers.sh --rollback --rollback-journal <absolute-path> [--install-dir <absolute-path>]

The private, root-owned source manifest is tab-separated and has no header:
  edge-id<TAB>logical-interface<TAB>launchd-label<TAB>source-config<TAB>source-wrapper<TAB>peer-ip<TAB>config-sha256<TAB>wrapper-sha256

The script copies only the checked config to /usr/local/libexec/juhe-ai/wireguard-config/<logical-interface>.conf,
generates an audited fixed lifecycle wrapper under <install-dir>/wireguard/<edge-id>/, rewrites
only the exact plist ProgramArguments entries that equal the source paths, then bootstraps only
the corresponding system job. Every altered plist and prior root artifact has a transaction-local
backup and is restored if any exact job fails to bootstrap.

When --rollback-journal is supplied with --apply, the root-only transaction journal is
retained after a successful migration so the installer can undo the whole migration if its
own LaunchDaemon install later fails. --rollback accepts only that retained journal.

Source files are only migration input, never an executable allowlist by themselves. Every
source config must be root-owned, free of WireGuard shell hooks, and match its manifest SHA.
The source wrapper is hash-checked only to bind the current plist and rollback metadata: it may
be an older lifecycle wrapper under a service-user-writable parent and is never copied or
executed. The generated target wrapper accepts exactly a wg-edge<N> interface and its matching
/usr/local/libexec/juhe-ai/wireguard-config/wg-edge<N>.conf path, invokes only /usr/local/bin/wg-quick and
/usr/local/bin/wg, and does not honor environment binary overrides.
EOF
}

die() { printf '%s\n' "$*" >&2; exit 2; }
safe_path() {
  case "$1" in /) return 1 ;; /*) ;; *) return 1 ;; esac
  case "$1" in *$'\n'*|*'\r'*|*'\t'*|*' '*|*'$'*|*'`'*|*'"'*|*"'"*|*'\\'*|*'|'*|*';'*|*'&'*|*'<'*|*'>'*) return 1 ;; esac
  return 0
}
edge_id_ok() { printf '%s' "$1" | /usr/bin/grep -Eq '^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$'; }
logical_ok() { printf '%s' "$1" | /usr/bin/grep -Eq '^wg-edge[0-9]+$'; }
label_ok() { printf '%s' "$1" | /usr/bin/grep -Eq '^[A-Za-z0-9][A-Za-z0-9._-]{2,127}$'; }
ipv4_ok() { printf '%s' "$1" | /usr/bin/awk -F. 'NF == 4 { for (i = 1; i <= 4; i++) if ($i !~ /^[0-9]+$/ || $i > 255) exit 1; exit 0 } { exit 1 }'; }
sha_ok() { printf '%s' "$1" | /usr/bin/grep -Eq '^[0-9A-Fa-f]{64}$'; }
sha256() { /usr/bin/shasum -a 256 "$1" | /usr/bin/awk '{print $1}'; }
root_not_group_or_other_writable() {
  local path="$1" mode uid
  [ ! -L "$path" ] && [ -e "$path" ] || return 1
  uid="$(/usr/bin/stat -f '%u' "$path")"
  mode="$(/usr/bin/stat -f '%Lp' "$path")"
  [ "$uid" = 0 ] || return 1
  case "$mode" in ?[2367][0-7]|??[2367]) return 1 ;; esac
  return 0
}
root_path_chain() {
  local path="$1"
  while [ "$path" != / ]; do
    root_not_group_or_other_writable "$path" || return 1
    path="$(/usr/bin/dirname "$path")"
  done
  return 0
}
config_has_forbidden_hook() {
  /usr/bin/grep -Eqi '^[[:space:]]*(PreUp|PostUp|PreDown|PostDown)[[:space:]]*=' "$1"
}
plist_binds_exact_pair() {
  local plist="$1" expected_wrapper="$2" expected_config="$3" index=0 arg wrapper_count=0 config_count=0
  while [ "$index" -le 15 ]; do
    arg="$(/usr/bin/plutil -extract "ProgramArguments.$index" raw -o - "$plist" 2>/dev/null || true)"
    [ "$arg" = "$expected_wrapper" ] && wrapper_count=$((wrapper_count + 1))
    [ "$arg" = "$expected_config" ] && config_count=$((config_count + 1))
    index=$((index + 1))
  done
  [ "$wrapper_count" -eq 1 ] && [ "$config_count" -eq 1 ]
}
write_fixed_wrapper() {
  local destination="$1"
  cat > "$destination" <<'EOF'
#!/usr/local/bin/bash
set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: $0 <interface-name> <wg-quick-config>" >&2
  exit 64
fi

interface_name="$1"
config_path="$2"
if [[ ! "$interface_name" =~ ^wg-edge[0-9]+$ ]]; then
  echo "invalid WireGuard interface: $interface_name" >&2
  exit 64
fi
expected_config="/usr/local/libexec/juhe-ai/wireguard-config/${interface_name}.conf"
if [[ "$config_path" != "$expected_config" ]]; then
  echo "invalid WireGuard config path for $interface_name" >&2
  exit 64
fi
if [[ -L "$config_path" || ! -f "$config_path" ]]; then
  echo "WireGuard config is not a regular file: $config_path" >&2
  exit 1
fi
if [[ "$(/usr/bin/stat -f '%u' "$config_path")" != 0 || "$(/usr/bin/stat -f '%Lp' "$config_path")" != 600 ]]; then
  echo "WireGuard config must be root-owned mode 600: $config_path" >&2
  exit 1
fi

wg_quick='/usr/local/bin/wg-quick'
wg_bin='/usr/local/bin/wg'
for binary_path in "$wg_quick" "$wg_bin"; do
  if [[ -L "$binary_path" || ! -x "$binary_path" || "$(/usr/bin/stat -f '%u' "$binary_path")" != 0 ]]; then
    echo "WireGuard binary is not a root-owned executable: $binary_path" >&2
    exit 1
  fi
done

stopping=false
child_pid=''
real_interface=''
name_file="/var/run/wireguard/${interface_name}.name"

cleanup() {
  if [[ "$stopping" == true ]]; then
    return
  fi
  stopping=true
  if [[ -n "$child_pid" ]]; then
    kill "$child_pid" >/dev/null 2>&1 || true
    wait "$child_pid" 2>/dev/null || true
  fi
  "$wg_quick" down "$config_path" >/dev/null 2>&1 || true
  if [[ -z "$real_interface" && -s "$name_file" ]]; then
    real_interface="$(/bin/cat "$name_file")"
  fi
  case "$real_interface" in
    ''|*[!A-Za-z0-9_.-]*) real_interface='' ;;
  esac
  if [[ -n "$real_interface" ]]; then
    rm -f "/var/run/wireguard/${real_interface}.sock"
  fi
  rm -f "$name_file"
}

trap cleanup EXIT
trap 'cleanup; exit 130' INT
trap 'cleanup; exit 143' TERM

"$wg_quick" down "$config_path" >/dev/null 2>&1 || true
"$wg_quick" up "$config_path" &
child_pid=$!

ready_attempt=1
while [ "$ready_attempt" -le 30 ]; do
  if [[ -s "$name_file" ]]; then
    real_interface="$(/bin/cat "$name_file")"
    case "$real_interface" in
      ''|*[!A-Za-z0-9_.-]*) real_interface='' ;;
    esac
    if [[ -n "$real_interface" ]] && "$wg_bin" show "$real_interface" >/dev/null 2>&1; then
      break
    fi
  fi
  if ! kill -0 "$child_pid" >/dev/null 2>&1; then
    wait "$child_pid" || exit $?
    echo "WireGuard interface did not become ready: $interface_name" >&2
    exit 1
  fi
  sleep 1
  ready_attempt=$((ready_attempt + 1))
done

if [[ -z "$real_interface" ]] || ! "$wg_bin" show "$real_interface" >/dev/null 2>&1; then
  echo "WireGuard interface did not become ready: $interface_name" >&2
  exit 1
fi

while "$wg_bin" show "$real_interface" >/dev/null 2>&1; do
  sleep 5
done

echo "WireGuard interface exited unexpectedly: $interface_name ($real_interface)" >&2
exit 1
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --dry-run) MODE=dry-run; shift ;;
    --apply) MODE=apply; shift ;;
    --rollback) ACTION=rollback; MODE=apply; shift ;;
    --manifest) MANIFEST="${2:?missing --manifest value}"; shift 2 ;;
    --install-dir) INSTALL_DIR="${2:?missing --install-dir value}"; shift 2 ;;
    --rollback-journal) ROLLBACK_JOURNAL="${2:?missing --rollback-journal value}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown option: $1" ;;
  esac
done

safe_path "$INSTALL_DIR" || die 'install-dir must be an absolute safe path'
if [ "$ACTION" = rollback ]; then
  [ -n "$ROLLBACK_JOURNAL" ] || die '--rollback requires --rollback-journal'
  safe_path "$ROLLBACK_JOURNAL" || die 'rollback-journal must be an absolute safe path'
  [ "$(/usr/bin/id -u)" -eq 0 ] || die '--rollback requires root'
  [ ! -L "$ROLLBACK_JOURNAL" ] && [ -d "$ROLLBACK_JOURNAL" ] || die 'rollback journal must be a regular non-symlink directory'
  root_path_chain "$ROLLBACK_JOURNAL" || die 'rollback journal and every parent must be root-owned and non-writable'
  ROLLBACK_MAP="$ROLLBACK_JOURNAL/applied.map"
  [ ! -L "$ROLLBACK_MAP" ] && [ -f "$ROLLBACK_MAP" ] || die 'rollback journal has no regular applied map'
  root_not_group_or_other_writable "$ROLLBACK_MAP" || die 'rollback map must be root-owned and non-writable'
  while IFS=$'\t' read -r edge logical label target_wrapper target_config target_plist source_wrapper old_plist old_dir old_config had_loaded; do
    /bin/launchctl bootout system "/Library/LaunchDaemons/$label.plist" >/dev/null 2>&1 || true
    [ -n "$old_plist" ] && [ -e "$old_plist" ] && /bin/mv -f "$old_plist" "/Library/LaunchDaemons/$label.plist" || exit 1
    /bin/rm -f "$target_config"
    if [ -n "$old_config" ] && [ -e "$old_config" ]; then /bin/mv -f "$old_config" "$target_config" || exit 1; fi
    if [ -n "$old_dir" ] && [ -e "$old_dir" ]; then
      /bin/rm -rf "$INSTALL_DIR/wireguard/$edge"
      /bin/mv -f "$old_dir" "$INSTALL_DIR/wireguard/$edge" || exit 1
    else
      /bin/rm -rf "$INSTALL_DIR/wireguard/$edge"
    fi
    if [ "$had_loaded" = 1 ]; then /bin/launchctl bootstrap system "/Library/LaunchDaemons/$label.plist" && /bin/launchctl kickstart -k "system/$label" || exit 1; fi
  done < "$ROLLBACK_MAP"
  /bin/rm -rf "$ROLLBACK_JOURNAL"
  printf 'root WireGuard wrapper migration rollback completed\n'
  exit 0
fi

[ -n "$MANIFEST" ] || die '--manifest is required'
safe_path "$MANIFEST" || die 'manifest must be an absolute safe path'
[ ! -L "$MANIFEST" ] && [ -f "$MANIFEST" ] || die 'manifest must be a regular non-symlink file'
root_not_group_or_other_writable "$MANIFEST" || die 'manifest must be root-owned and not writable by group or others'
root_path_chain "$MANIFEST" || die 'manifest and every parent must be root-owned and non-writable'

SCRIPT_PATH="${JUHE_AI_OPERATION_SCRIPT_PATH:-${BASH_SOURCE:-$0}}"
RUN_ID="$$.$(/bin/date +%s)"
STAGE_DIR="$INSTALL_DIR/.wireguard-stage.$RUN_ID"
if [ -n "$ROLLBACK_JOURNAL" ]; then
  safe_path "$ROLLBACK_JOURNAL" || die 'rollback-journal must be an absolute safe path'
  [ ! -e "$ROLLBACK_JOURNAL" ] || die 'rollback journal already exists'
  STAGE_DIR="$ROLLBACK_JOURNAL"
fi
MAP_FILE="$STAGE_DIR/migration.map"
ROLLBACK_MAP="$STAGE_DIR/applied.map"

validate_manifest() {
  local edge logical label config wrapper peer config_hash wrapper_hash extra line_no=0 count=0 plist wrapper_index config_index index arg
  while IFS=$'\t' read -r edge logical label config wrapper peer config_hash wrapper_hash extra; do
    line_no=$((line_no + 1))
    case "$edge" in ''|\#*) continue ;; esac
    [ -z "${extra:-}" ] || die "manifest line $line_no has too many fields"
    edge_id_ok "$edge" && logical_ok "$logical" && label_ok "$label" && ipv4_ok "$peer" || die "manifest line $line_no has invalid identity fields"
    safe_path "$config" && safe_path "$wrapper" || die "manifest line $line_no has unsafe source paths"
    sha_ok "$config_hash" && sha_ok "$wrapper_hash" || die "manifest line $line_no has invalid source hashes"
    [ ! -L "$config" ] && [ -f "$config" ] || die "manifest line $line_no config is not a regular file"
    [ ! -L "$wrapper" ] && [ -f "$wrapper" ] && [ -x "$wrapper" ] || die "manifest line $line_no wrapper is not executable"
    root_not_group_or_other_writable "$config" || die "manifest line $line_no config must be root-owned and non-writable"
    [ "$(sha256 "$config")" = "$(printf '%s' "$config_hash" | /usr/bin/tr '[:upper:]' '[:lower:]')" ] || die "manifest line $line_no config hash mismatch"
    [ "$(sha256 "$wrapper")" = "$(printf '%s' "$wrapper_hash" | /usr/bin/tr '[:upper:]' '[:lower:]')" ] || die "manifest line $line_no wrapper hash mismatch"
    /usr/bin/grep -Eq '^[[:space:]]*PersistentKeepalive[[:space:]]*=[[:space:]]*25[[:space:]]*$' "$config" || die "manifest line $line_no lacks PersistentKeepalive = 25"
    config_has_forbidden_hook "$config" && die "manifest line $line_no config contains forbidden WireGuard shell hook"
    plist="/Library/LaunchDaemons/$label.plist"
    root_not_group_or_other_writable "$plist" || die "manifest line $line_no plist is not root-only"
    /usr/bin/plutil -lint "$plist" >/dev/null || die "manifest line $line_no plist is invalid"
    wrapper_index=''
    config_index=''
    index=0
    while [ "$index" -le 15 ]; do
      arg="$(/usr/bin/plutil -extract "ProgramArguments.$index" raw -o - "$plist" 2>/dev/null || true)"
      [ -n "$arg" ] || { index=$((index + 1)); continue; }
      [ "$arg" = "$wrapper" ] && { [ -z "$wrapper_index" ] || die "manifest line $line_no wrapper appears multiple times"; wrapper_index="$index"; }
      [ "$arg" = "$config" ] && { [ -z "$config_index" ] || die "manifest line $line_no config appears multiple times"; config_index="$index"; }
      index=$((index + 1))
    done
    [ -n "$wrapper_index" ] && [ -n "$config_index" ] || die "manifest line $line_no plist does not bind exact wrapper and config"
    count=$((count + 1))
  done < "$MANIFEST"
  [ "$count" -eq 8 ] || die 'the root WireGuard allowlist must contain exactly eight edges'
  /usr/bin/awk -F '\t' '!/^($|#)/ { if (id[$1]++ || logical[$2]++ || label[$3]++) exit 1 }' "$MANIFEST" || die 'manifest contains duplicate edge, logical interface, or label'
}

validate_manifest
printf 'mode=%s manifest=%s install-dir=%s\n' "$MODE" "$MANIFEST" "$INSTALL_DIR"
printf 'plan: verify eight exact plists and source hashes -> generate fixed root-only wrappers and copy configs -> replace exact ProgramArguments -> bootstrap each exact job\n'
[ "$MODE" = apply ] || exit 0

[ "$(/usr/bin/id -u)" -eq 0 ] || die '--apply requires root'
/bin/mkdir -p "$INSTALL_DIR"
/usr/sbin/chown root:wheel "$INSTALL_DIR"
/bin/chmod 755 "$INSTALL_DIR"
/bin/mkdir -p "$CANONICAL_CONFIG_DIR"
/usr/sbin/chown root:wheel "$CANONICAL_CONFIG_DIR"
/bin/chmod 700 "$CANONICAL_CONFIG_DIR"
/bin/mkdir "$STAGE_DIR"
/usr/sbin/chown root:wheel "$STAGE_DIR"
/bin/chmod 700 "$STAGE_DIR"
trap '/bin/rm -rf "$STAGE_DIR"' EXIT HUP INT TERM
: > "$MAP_FILE"
: > "$ROLLBACK_MAP"

stage_all() {
  local edge logical label config wrapper peer config_hash wrapper_hash extra plist wrapper_index='' config_index='' index arg edge_stage target_wrapper target_config target_plist
  while IFS=$'\t' read -r edge logical label config wrapper peer config_hash wrapper_hash extra; do
    case "$edge" in ''|\#*) continue ;; esac
    edge_stage="$STAGE_DIR/$edge"
    /bin/mkdir "$edge_stage"
    target_wrapper="$INSTALL_DIR/wireguard/$edge/run-wireguard.sh"
    target_config="$CANONICAL_CONFIG_DIR/$logical.conf"
    target_plist="$STAGE_DIR/$edge/$label.plist"
    /bin/cp "$config" "$edge_stage/wg.conf"
    [ "$(sha256 "$wrapper")" = "$(printf '%s' "$wrapper_hash" | /usr/bin/tr '[:upper:]' '[:lower:]')" ] || die "wrapper changed while staging edge=$edge"
    [ "$(sha256 "$edge_stage/wg.conf")" = "$(printf '%s' "$config_hash" | /usr/bin/tr '[:upper:]' '[:lower:]')" ] || die "config changed while staging edge=$edge"
    write_fixed_wrapper "$edge_stage/run-wireguard.sh"
    /usr/sbin/chown -R root:wheel "$edge_stage"
    /bin/chmod 700 "$edge_stage" "$edge_stage/run-wireguard.sh"
    /bin/chmod 600 "$edge_stage/wg.conf"
    root_path_chain "$edge_stage/run-wireguard.sh" || die "staged wrapper parent is not root-only edge=$edge"
    root_path_chain "$edge_stage/wg.conf" || die "staged config parent is not root-only edge=$edge"
    /bin/cp "/Library/LaunchDaemons/$label.plist" "$target_plist"
    wrapper_index=''; config_index=''; index=0
    while [ "$index" -le 15 ]; do
      arg="$(/usr/bin/plutil -extract "ProgramArguments.$index" raw -o - "$target_plist" 2>/dev/null || true)"
      [ "$arg" = "$wrapper" ] && wrapper_index="$index"
      [ "$arg" = "$config" ] && config_index="$index"
      index=$((index + 1))
    done
    [ -n "$wrapper_index" ] && [ -n "$config_index" ] || die "staged plist drifted edge=$edge"
    /usr/bin/plutil -replace "ProgramArguments.$wrapper_index" -string "$target_wrapper" "$target_plist"
    /usr/bin/plutil -replace "ProgramArguments.$config_index" -string "$target_config" "$target_plist"
    /usr/bin/plutil -lint "$target_plist" >/dev/null
    printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$edge" "$logical" "$label" "$target_wrapper" "$target_config" "$INSTALL_DIR/wireguard/$edge/$label.plist" "$wrapper" >> "$MAP_FILE"
  done < "$MANIFEST"
}

rollback_applied() {
  local edge logical label target_wrapper target_config target_plist source_wrapper old_plist old_dir old_config had_loaded
  while IFS=$'\t' read -r edge logical label target_wrapper target_config target_plist source_wrapper old_plist old_dir old_config had_loaded; do
    /bin/launchctl bootout system "/Library/LaunchDaemons/$label.plist" >/dev/null 2>&1 || true
    [ -n "$old_plist" ] && /bin/mv -f "$old_plist" "/Library/LaunchDaemons/$label.plist" || true
    /bin/rm -f "$target_config"
    if [ -n "$old_config" ] && [ -e "$old_config" ]; then /bin/mv -f "$old_config" "$target_config" || true; fi
    if [ -n "$old_dir" ] && [ -e "$old_dir" ]; then
      /bin/rm -rf "$INSTALL_DIR/wireguard/$edge"
      /bin/mv -f "$old_dir" "$INSTALL_DIR/wireguard/$edge"
    else
      /bin/rm -rf "$INSTALL_DIR/wireguard/$edge"
    fi
    if [ "$had_loaded" = 1 ]; then /bin/launchctl bootstrap system "/Library/LaunchDaemons/$label.plist" && /bin/launchctl kickstart -k "system/$label" || true; fi
  done < "$ROLLBACK_MAP"
}

apply_all() {
  local edge logical label target_wrapper target_config target_plist source_wrapper old_plist old_dir old_config had_loaded
  /bin/mkdir -p "$INSTALL_DIR/wireguard" || return 1
  /usr/sbin/chown root:wheel "$INSTALL_DIR/wireguard" || return 1
  /bin/chmod 755 "$INSTALL_DIR/wireguard" || return 1
  /bin/mkdir -p "$CANONICAL_CONFIG_DIR" || return 1
  /usr/sbin/chown root:wheel "$CANONICAL_CONFIG_DIR" || return 1
  /bin/chmod 700 "$CANONICAL_CONFIG_DIR" || return 1
  while IFS=$'\t' read -r edge logical label target_wrapper target_config target_plist source_wrapper; do
    old_plist="$STAGE_DIR/old.$edge.plist"
    old_dir=''
    old_config=''
    had_loaded=0
    if /bin/launchctl print "system/$label" >/dev/null 2>&1; then had_loaded=1; fi
    /bin/cp -p "/Library/LaunchDaemons/$label.plist" "$old_plist" || return 1
    if [ -e "$INSTALL_DIR/wireguard/$edge" ]; then
      old_dir="$STAGE_DIR/old-root-artifact-$edge"
      /bin/mv "$INSTALL_DIR/wireguard/$edge" "$old_dir" || return 1
    fi
    if [ -e "$target_config" ]; then
      old_config="$STAGE_DIR/old-root-config-$edge.conf"
      /bin/mv "$target_config" "$old_config" || return 1
    fi
    if ! printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$edge" "$logical" "$label" "$target_wrapper" "$target_config" "$target_plist" "$source_wrapper" "$old_plist" "$old_dir" "$old_config" "$had_loaded" >> "$ROLLBACK_MAP"; then
      # No plist/job was changed yet. Put a moved previous artifact back before
      # returning so the outer rollback remains complete even if the journal write fails.
      if [ -n "$old_dir" ] && [ -e "$old_dir" ]; then /bin/mv "$old_dir" "$INSTALL_DIR/wireguard/$edge" || true; fi
      if [ -n "$old_config" ] && [ -e "$old_config" ]; then /bin/mv "$old_config" "$target_config" || true; fi
      return 1
    fi
    if [ "$had_loaded" = 1 ]; then /bin/launchctl bootout system "/Library/LaunchDaemons/$label.plist" >/dev/null 2>&1 || return 1; fi
    /bin/mv "$STAGE_DIR/$edge" "$INSTALL_DIR/wireguard/$edge" || return 1
    /bin/mv "$INSTALL_DIR/wireguard/$edge/wg.conf" "$target_config" || return 1
    /usr/sbin/chown root:wheel "$target_config" || return 1
    /bin/chmod 600 "$target_config" || return 1
    /bin/mv "$INSTALL_DIR/wireguard/$edge/$label.plist" "/Library/LaunchDaemons/$label.plist" || return 1
    /usr/sbin/chown root:wheel "/Library/LaunchDaemons/$label.plist" || return 1
    /bin/chmod 644 "/Library/LaunchDaemons/$label.plist" || return 1
    /usr/bin/plutil -lint "/Library/LaunchDaemons/$label.plist" >/dev/null || return 1
    plist_binds_exact_pair "/Library/LaunchDaemons/$label.plist" "$target_wrapper" "$target_config" || return 1
    /bin/launchctl bootstrap system "/Library/LaunchDaemons/$label.plist" || return 1
    /bin/launchctl kickstart -k "system/$label" || return 1
    /bin/launchctl print "system/$label" >/dev/null || return 1
    plist_binds_exact_pair "/Library/LaunchDaemons/$label.plist" "$target_wrapper" "$target_config" || return 1
    root_path_chain "$INSTALL_DIR/wireguard/$edge/run-wireguard.sh" || return 1
    root_path_chain "$target_config" || return 1
  done < "$MAP_FILE"
}

stage_all
if ! apply_all; then
  echo 'root WireGuard wrapper migration failed; restoring changed jobs' >&2
  rollback_applied
  exit 1
fi
printf 'root WireGuard wrapper migration installed for eight exact jobs\n'
if [ -n "$ROLLBACK_JOURNAL" ]; then
  trap - EXIT HUP INT TERM
  printf 'root WireGuard wrapper migration journal retained: %s\n' "$ROLLBACK_JOURNAL"
fi
