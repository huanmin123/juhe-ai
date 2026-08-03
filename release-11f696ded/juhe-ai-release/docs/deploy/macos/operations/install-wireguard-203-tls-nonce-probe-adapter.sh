#!/usr/bin/env bash
set -euo pipefail
umask 077

MODE=dry-run
MAPPING=''
RUNTIME_MANIFEST=''
INSTALL_DIR='/usr/local/libexec/juhe-ai'
SCRIPT_SHA256=''

usage() {
  cat <<'EOF'
Usage: install-wireguard-203-tls-nonce-probe-adapter.sh [--dry-run|--apply] --mapping <absolute-path> --runtime-manifest <absolute-path> --script-sha256 <sha256> [--install-dir <absolute-path>]

Copies a verified adapter and private mapping into root-only libexec. The mapping names a
dedicated SSH identity, pinned known_hosts file, `node` label and `public_ip` label for each
of the eight exact runtime-manifest Edge IDs. The collector remains loopback-only on 203:
the dedicated remote public key must use a fixed forced-command protocol named
`juhe-tunnel-probe-read-v1`, returning only its read-only metrics text. This installer never
opens a Prometheus listener, accepts HTTP URLs or copies an SSH private key.
EOF
}
die() { printf '%s\n' "$*" >&2; exit 2; }
safe_path() { case "$1" in /) return 1 ;; /*) ;; *) return 1 ;; esac; case "$1" in *$'\n'*|*$'\r'*|*$'\t'*|*' '*|*'$'*|*'`'*|*'"'*|*"'"*|*'\\'*|*'|'*|*';'*|*'&'*|*'<'*|*'>') return 1 ;; esac; }
sha_ok() { printf '%s' "$1" | /usr/bin/grep -Eq '^[0-9A-Fa-f]{64}$'; }
sha256() { /usr/bin/shasum -a 256 "$1" | /usr/bin/awk '{print $1}'; }
edge_ok() { printf '%s' "$1" | /usr/bin/grep -Eq '^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$'; }
logical_ok() { printf '%s' "$1" | /usr/bin/grep -Eq '^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$'; }
label_ok() { printf '%s' "$1" | /usr/bin/grep -Eq '^[A-Za-z0-9][A-Za-z0-9._-]{2,127}$'; }
node_ok() { printf '%s' "$1" | /usr/bin/grep -Eq '^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$'; }
ipv4_ok() { printf '%s' "$1" | /usr/bin/awk -F. 'NF == 4 { for (i = 1; i <= 4; i++) if ($i !~ /^[0-9]+$/ || $i > 255) exit 1; exit 0 } { exit 1 }'; }
ssh_target_ok() { printf '%s' "$1" | /usr/bin/grep -Eq '^[A-Za-z0-9][A-Za-z0-9._-]{0,63}@[A-Za-z0-9][A-Za-z0-9.-]{0,252}$'; }
root_path_chain() {
  local path="$1" owner mode
  while [ "$path" != / ]; do
    [ ! -L "$path" ] && [ -e "$path" ] || return 1
    owner="$(/usr/bin/stat -f '%u' "$path")"; mode="$(/usr/bin/stat -f '%Lp' "$path")"
    [ "$owner" = 0 ] || return 1
    case "$mode" in ?[2367][0-7]|??[2367]) return 1 ;; esac
    path="$(/usr/bin/dirname "$path")"
  done
}
root_private_file() {
  local path="$1" mode uid
  [ ! -L "$path" ] && [ -f "$path" ] || return 1
  uid="$(/usr/bin/stat -f '%u' "$path")"; mode="$(/usr/bin/stat -f '%Lp' "$path")"
  [ "$uid" = 0 ] && { [ "$mode" = 400 ] || [ "$mode" = 600 ]; } && root_path_chain "$path"
}
validate_runtime_manifest() {
  local edge logical label config wrapper peer extra count=0
  while IFS=$'\t' read -r edge logical label config wrapper peer extra; do
    case "$edge" in ''|\#*) continue ;; esac
    [ -z "${extra:-}" ] || return 1
    edge_ok "$edge" && logical_ok "$logical" && label_ok "$label" && ipv4_ok "$peer" || return 1
    count=$((count + 1))
  done < "$RUNTIME_MANIFEST"
  [ "$count" -eq 8 ] || return 1
  /usr/bin/awk -F '\t' '!/^($|#)/ { if (NF != 6 || edge[$1]++) exit 1 } END { exit NR ? 0 : 1 }' "$RUNTIME_MANIFEST"
}
validate_mapping() {
  local edge target node public_ip known_hosts identity extra count=0
  while IFS=$'\t' read -r edge target node public_ip known_hosts identity extra; do
    case "$edge" in ''|\#*) continue ;; esac
    [ -z "${extra:-}" ] || return 1
    edge_ok "$edge" && ssh_target_ok "$target" && node_ok "$node" && ipv4_ok "$public_ip" || return 1
    safe_path "$known_hosts" && safe_path "$identity" || return 1
    root_private_file "$known_hosts" && root_private_file "$identity" || return 1
    count=$((count + 1))
  done < "$MAPPING"
  [ "$count" -eq 8 ] || return 1
  /usr/bin/awk -F '\t' '
    NR == FNR {
      if ($0 ~ /^($|#)/) next
      if (NF != 6 || manifest[$1]++) exit 1
      manifest_count++
      next
    }
    {
      if ($0 ~ /^($|#)/) next
      if (NF != 6 || !($1 in manifest) || seen[$1]++ || series[$3 SUBSEP $4]++) exit 1
      mapping_count++
    }
    END { exit manifest_count == 8 && mapping_count == 8 ? 0 : 1 }
  ' "$RUNTIME_MANIFEST" "$MAPPING"
}
while [ "$#" -gt 0 ]; do
  case "$1" in
    --dry-run) MODE=dry-run; shift ;;
    --apply) MODE=apply; shift ;;
    --mapping) MAPPING="${2:?missing mapping}"; shift 2 ;;
    --runtime-manifest) RUNTIME_MANIFEST="${2:?missing runtime manifest}"; shift 2 ;;
    --install-dir) INSTALL_DIR="${2:?missing install dir}"; shift 2 ;;
    --script-sha256) SCRIPT_SHA256="${2:?missing script SHA-256}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown option: $1" ;;
  esac
done
[ -n "$MAPPING" ] || die '--mapping is required'
[ -n "$RUNTIME_MANIFEST" ] || die '--runtime-manifest is required'
safe_path "$MAPPING" && safe_path "$RUNTIME_MANIFEST" && safe_path "$INSTALL_DIR" || die 'paths must be absolute, non-root and template-safe'
sha_ok "$SCRIPT_SHA256" || die '--script-sha256 must be SHA-256 hex'
root_private_file "$MAPPING" || die 'mapping must be root-only mode 0400 or 0600 and non-symlink'
[ ! -L "$RUNTIME_MANIFEST" ] && [ -f "$RUNTIME_MANIFEST" ] && root_path_chain "$RUNTIME_MANIFEST" || die 'runtime manifest must be root-only and non-symlink'
validate_runtime_manifest || die 'runtime manifest must contain exactly eight unique Edge IDs'
validate_mapping || die 'mapping must contain the exact eight manifest Edge IDs and unique node/public_ip series'
SCRIPT_PATH="${JUHE_AI_OPERATION_SCRIPT_PATH:-${BASH_SOURCE:-$0}}"
SCRIPT_DIR="$(cd "$(dirname "$SCRIPT_PATH")" && pwd)"
SOURCE="$SCRIPT_DIR/wireguard-203-tls-nonce-probe-adapter.sh"
TARGET="$INSTALL_DIR/wireguard-203-tls-nonce-probe-adapter.sh"
TARGET_MAPPING="$INSTALL_DIR/wireguard-203-tls-nonce-probe.map"
printf 'mode=%s adapter=%s mapping=validated-8-series\n' "$MODE" "$TARGET"
[ "$MODE" = apply ] || exit 0
[ "$(/usr/bin/id -u)" -eq 0 ] || die '--apply requires root'
[ ! -L "$SOURCE" ] && [ -f "$SOURCE" ] || die 'source adapter must be a regular file'
[ "$(sha256 "$SOURCE")" = "$(printf '%s' "$SCRIPT_SHA256" | /usr/bin/tr '[:upper:]' '[:lower:]')" ] || die 'source adapter hash mismatch'
root_path_chain "$(/usr/bin/dirname "$INSTALL_DIR")" || die 'install-dir parent must be root-only'
[ -e "$INSTALL_DIR" ] || /bin/mkdir -p "$INSTALL_DIR"
root_path_chain "$INSTALL_DIR" || die 'install-dir must be root-only'
adapter_tmp="$TARGET.tmp.$$"; mapping_tmp="$TARGET_MAPPING.tmp.$$"
/bin/cp "$SOURCE" "$adapter_tmp"; /bin/cp "$MAPPING" "$mapping_tmp"
/usr/sbin/chown root:wheel "$adapter_tmp" "$mapping_tmp"; /bin/chmod 700 "$adapter_tmp"; /bin/chmod 600 "$mapping_tmp"
[ "$(sha256 "$adapter_tmp")" = "$(printf '%s' "$SCRIPT_SHA256" | /usr/bin/tr '[:upper:]' '[:lower:]')" ] || { /bin/rm -f "$adapter_tmp" "$mapping_tmp"; die 'adapter changed while copying'; }
/bin/mv -f "$adapter_tmp" "$TARGET"; /bin/mv -f "$mapping_tmp" "$TARGET_MAPPING"
root_path_chain "$TARGET" && root_path_chain "$TARGET_MAPPING" || die 'installed probe adapter path is not root-only'
printf '203 SSH probe adapter installed: %s\n' "$TARGET"
