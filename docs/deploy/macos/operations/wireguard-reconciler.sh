#!/usr/bin/env bash
# Root-only, single-pass WireGuard reconciler for macOS launchd.
# It intentionally has no HTTP, Caddy, Node, database, DNS, or generic process actions.
set -euo pipefail

MODE=once
MANIFEST=''
STATE_DIR='/var/db/juhe-ai/wireguard-reconciler'
MAINTENANCE_LOCK=''
WG_BIN='/usr/local/bin/wg'
PROBE_HELPER=''
RELEASE_LOCK=''
STALE_SECONDS=120
STALE_CONFIRMATIONS=2
STALE_SAMPLE_MAX_GAP_SECONDS=180
NETWORK_STABLE_SECONDS=30
SLEEP_GRACE_SECONDS=180
GLOBAL_COOLDOWN_SECONDS=1800
PER_EDGE_COOLDOWN_SECONDS=1800
ACTION_WINDOW_SECONDS=3600
MAX_GLOBAL_ACTIONS=1
LEASE_STALE_SECONDS=180
ACTION_TIMEOUT_SECONDS=45
FRESH_CONFIRMATIONS=3
BATCH_ID="wg-$$"

usage() {
  cat <<'EOF'
Usage: wireguard-reconciler.sh --once --manifest <absolute-path> [options]

The root-owned manifest is tab-separated with no header:
  edge-id<TAB>logical-interface<TAB>launchd-label<TAB>wg-config<TAB>wrapper<TAB>peer-tunnel-ip

Options:
  --once                         perform one bounded reconciliation pass (required)
  --manifest <absolute-path>     root-owned, non-symlink manifest
  --state-dir <absolute-path>    root-owned state directory
  --maintenance-lock <path>      when present, record and do not take recovery action
  --wg-bin <absolute-path>       wg executable; default /usr/local/bin/wg
  --probe-helper <absolute-path> root-owned adapter for the independent 203 TLS nonce probe
  --release-lock <path>          root-owned deployment lock; when present, do not take recovery action
  --stale-seconds <seconds>      minimum latest-handshake age; default 120
  --stale-confirmations <count>  consecutive stale samples; default 2
  --stale-sample-max-gap-seconds <seconds> maximum age between stale samples; default 180
  --network-stable-seconds <s>   unchanged default route interval; default 30
  --sleep-grace-seconds <s>      do not act shortly after a macOS sleep/wake record; default 180
  --global-cooldown-seconds <s>  minimum interval between recovery batches; default 1800
  --per-edge-cooldown-seconds <s> minimum interval before an edge can be restarted again; default 1800
  --action-window-seconds <s>    global action budget window; default 3600
  --max-global-actions <count>   maximum recovery batches in the action window; default 1
  --action-timeout-seconds <s>   maximum wait for cleanup or fresh handshake; default 45
EOF
}

die() { printf '%s\n' "$*" >&2; exit 2; }
event() {
  # Keep operational evidence local and avoid emitting config contents or credentials.
  local message="$1"
  /usr/bin/logger -t juhe-ai-wireguard-reconciler -- "$message" 2>/dev/null || true
  printf '%s\t%s\n' "$(/bin/date '+%Y-%m-%dT%H:%M:%S%z')" "$message" >> "$STATE_DIR/events.log"
  if [ "$(/usr/bin/wc -l < "$STATE_DIR/events.log")" -gt 800 ]; then
    /usr/bin/tail -n 600 "$STATE_DIR/events.log" > "$STATE_DIR/events.log.tmp.$$"
    /bin/mv -f "$STATE_DIR/events.log.tmp.$$" "$STATE_DIR/events.log"
  fi
}

is_positive_integer() { case "$1" in ''|*[!0-9]*) return 1 ;; *) [ "$1" -gt 0 ] ;; esac; }
is_absolute_safe_path() {
  case "$1" in
    /) return 1 ;;
    /*) ;;
    *) return 1 ;;
  esac
  case "$1" in *$'\n'*|*'\r'*|*'\t'*|*' '*|*'$'*|*'`'*|*'"'*|*"'"*|*'\\'*|*'|'*|*';'*|*'&'*|*'<'*|*'>'*) return 1 ;; esac
  return 0
}
is_edge_id() { printf '%s' "$1" | /usr/bin/grep -Eq '^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$'; }
is_logical_interface() { printf '%s' "$1" | /usr/bin/grep -Eq '^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$'; }
is_launchd_label() { printf '%s' "$1" | /usr/bin/grep -Eq '^[A-Za-z0-9][A-Za-z0-9._-]{2,127}$'; }
is_ipv4() {
  printf '%s' "$1" | /usr/bin/awk -F. 'NF == 4 { for (i = 1; i <= 4; i++) if ($i !~ /^[0-9]+$/ || $i > 255) exit 1; exit 0 } { exit 1 }'
}
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

while [ "$#" -gt 0 ]; do
  case "$1" in
    --once) MODE=once; shift ;;
    --manifest) MANIFEST="${2:?missing --manifest value}"; shift 2 ;;
    --state-dir) STATE_DIR="${2:?missing --state-dir value}"; shift 2 ;;
    --maintenance-lock) MAINTENANCE_LOCK="${2:?missing --maintenance-lock value}"; shift 2 ;;
    --wg-bin) WG_BIN="${2:?missing --wg-bin value}"; shift 2 ;;
    --probe-helper) PROBE_HELPER="${2:?missing --probe-helper value}"; shift 2 ;;
    --release-lock) RELEASE_LOCK="${2:?missing --release-lock value}"; shift 2 ;;
    --stale-seconds) STALE_SECONDS="${2:?missing --stale-seconds value}"; shift 2 ;;
    --stale-confirmations) STALE_CONFIRMATIONS="${2:?missing --stale-confirmations value}"; shift 2 ;;
    --stale-sample-max-gap-seconds) STALE_SAMPLE_MAX_GAP_SECONDS="${2:?missing --stale-sample-max-gap-seconds value}"; shift 2 ;;
    --network-stable-seconds) NETWORK_STABLE_SECONDS="${2:?missing --network-stable-seconds value}"; shift 2 ;;
    --sleep-grace-seconds) SLEEP_GRACE_SECONDS="${2:?missing --sleep-grace-seconds value}"; shift 2 ;;
    --global-cooldown-seconds) GLOBAL_COOLDOWN_SECONDS="${2:?missing --global-cooldown-seconds value}"; shift 2 ;;
    --per-edge-cooldown-seconds) PER_EDGE_COOLDOWN_SECONDS="${2:?missing --per-edge-cooldown-seconds value}"; shift 2 ;;
    --action-window-seconds) ACTION_WINDOW_SECONDS="${2:?missing --action-window-seconds value}"; shift 2 ;;
    --max-global-actions) MAX_GLOBAL_ACTIONS="${2:?missing --max-global-actions value}"; shift 2 ;;
    --lease-stale-seconds) LEASE_STALE_SECONDS="${2:?missing --lease-stale-seconds value}"; shift 2 ;;
    --action-timeout-seconds) ACTION_TIMEOUT_SECONDS="${2:?missing --action-timeout-seconds value}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown option: $1" ;;
  esac
done

[ -n "$MANIFEST" ] || die '--manifest is required'
is_absolute_safe_path "$MANIFEST" || die 'manifest path must be an absolute safe path'
is_absolute_safe_path "$STATE_DIR" || die 'state-dir must be an absolute safe path'
[ -z "$MAINTENANCE_LOCK" ] || is_absolute_safe_path "$MAINTENANCE_LOCK" || die 'maintenance-lock must be an absolute safe path'
is_absolute_safe_path "$WG_BIN" || die 'wg-bin must be an absolute safe path'
[ -n "$PROBE_HELPER" ] || die '--probe-helper is required; unknown external probe state must not trigger recovery'
is_absolute_safe_path "$PROBE_HELPER" || die 'probe-helper must be an absolute safe path'
[ -z "$RELEASE_LOCK" ] || is_absolute_safe_path "$RELEASE_LOCK" || die 'release-lock must be an absolute safe path'
for value in "$STALE_SECONDS" "$STALE_CONFIRMATIONS" "$STALE_SAMPLE_MAX_GAP_SECONDS" "$NETWORK_STABLE_SECONDS" "$SLEEP_GRACE_SECONDS" "$GLOBAL_COOLDOWN_SECONDS" "$PER_EDGE_COOLDOWN_SECONDS" "$ACTION_WINDOW_SECONDS" "$MAX_GLOBAL_ACTIONS" "$LEASE_STALE_SECONDS" "$ACTION_TIMEOUT_SECONDS" "$FRESH_CONFIRMATIONS"; do
  is_positive_integer "$value" || die 'time and count options must be positive integers'
done
[ "$(/usr/bin/id -u)" -eq 0 ] || die 'the WireGuard reconciler must run as root'
[ ! -L "$MANIFEST" ] || die 'manifest must not be a symlink'
[ -f "$MANIFEST" ] || die "manifest is not a regular file: $MANIFEST"
[ -e "$(/usr/bin/dirname "$MANIFEST")" ] || die 'manifest parent is missing'
root_path_chain "$MANIFEST" || die 'manifest and every parent must be root-owned and non-writable'
[ ! -L "$WG_BIN" ] && [ -x "$WG_BIN" ] || die "wg binary must be a non-symlink executable: $WG_BIN"
root_path_chain "$WG_BIN" || die 'wg binary and every parent must be root-owned and non-writable'
[ ! -L "$PROBE_HELPER" ] && [ -x "$PROBE_HELPER" ] || die 'probe-helper must be a non-symlink executable'
[ "$(/usr/bin/stat -f '%u' "$PROBE_HELPER")" = 0 ] || die 'probe-helper must be owned by root'
root_path_chain "$PROBE_HELPER" || die 'probe-helper and every parent must be root-owned and non-writable'
case "$(/usr/bin/stat -f '%Lp' "$PROBE_HELPER")" in ?[2367][0-7]|??[2367]) die 'probe-helper must not be writable by group or others' ;; esac

manifest_mode="$(/usr/bin/stat -f '%Lp' "$MANIFEST")"
manifest_uid="$(/usr/bin/stat -f '%u' "$MANIFEST")"
[ "$manifest_uid" = 0 ] || die 'manifest must be owned by root'
case "$manifest_mode" in
  ???) ;;
  *) die 'manifest mode could not be read' ;;
esac
case "${manifest_mode#?}" in *[2367]*) die 'manifest must not be writable by group or others' ;; esac

/bin/mkdir -p "$STATE_DIR"
/bin/chmod 700 "$STATE_DIR"
state_uid="$(/usr/bin/stat -f '%u' "$STATE_DIR")"
[ "$state_uid" = 0 ] || die 'state-dir must be owned by root'
[ ! -L "$STATE_DIR" ] || die 'state-dir must not be a symlink'
root_path_chain "$STATE_DIR" || die 'state-dir and every parent must be root-owned and non-writable'

LEASE_DIR="$STATE_DIR/lease"
if ! /bin/mkdir "$LEASE_DIR" 2>/dev/null; then
  lease_pid=''
  [ -f "$LEASE_DIR/pid" ] && lease_pid="$(/bin/cat "$LEASE_DIR/pid" 2>/dev/null || true)"
  lease_mtime="$(/usr/bin/stat -f '%m' "$LEASE_DIR" 2>/dev/null || printf '0')"
  if is_positive_integer "$lease_pid" && /bin/kill -0 "$lease_pid" 2>/dev/null; then exit 0; fi
  if is_positive_integer "$lease_mtime" && [ $(( $(/bin/date +%s) - lease_mtime )) -gt "$LEASE_STALE_SECONDS" ]; then
    /bin/rm -f "$LEASE_DIR/pid" && /bin/rmdir "$LEASE_DIR" 2>/dev/null || exit 0
    /bin/mkdir "$LEASE_DIR" 2>/dev/null || exit 0
  else
    exit 0
  fi
fi
printf '%s\n' "$$" > "$LEASE_DIR/pid"
cleanup() { /bin/rm -f "$LEASE_DIR/pid"; /bin/rmdir "$LEASE_DIR" 2>/dev/null || true; }
trap cleanup EXIT HUP INT TERM

if { [ -n "$MAINTENANCE_LOCK" ] && [ -e "$MAINTENANCE_LOCK" ]; } || { [ -n "$RELEASE_LOCK" ] && [ -e "$RELEASE_LOCK" ]; }; then
  event "batch=$BATCH_ID gate=maintenance-or-release-lock action=none"
  exit 0
fi

NETWORK_FILE="$STATE_DIR/network.state"
network_snapshot() {
  local route interface addresses
  route="$(/sbin/route -n get default 2>/dev/null || true)"
  interface="$(printf '%s\n' "$route" | /usr/bin/awk '/interface:/{print $2; exit}')"
  [ -n "$interface" ] || return 1
  addresses="$(/sbin/ifconfig "$interface" 2>/dev/null | /usr/bin/awk '/^[[:space:]]*inet /{print $2}' | /usr/bin/tr '\n' ',')"
  [ -n "$addresses" ] || return 1
  printf '%s|%s|%s\n' "$interface" "$(printf '%s\n' "$route" | /usr/bin/awk '/gateway:/{print $2; exit}')" "$addresses"
}

now="$(/bin/date +%s)"
BATCH_ID="wg-$now-$$"
latest_power_event_epoch() {
  # pmset's first two fields are a stable local timestamp in supported macOS releases.
  # A parse failure is deliberately unknown rather than a recovery signal.
  local line stamp
  line="$(/usr/bin/pmset -g log 2>/dev/null | /usr/bin/grep -E ' Sleep | Wake | DarkWake ' | /usr/bin/tail -n 1 || true)"
  [ -n "$line" ] || return 1
  stamp="$(printf '%s\n' "$line" | /usr/bin/awk '{print $1 " " $2 " " $3}')"
  /bin/date -j -f '%Y-%m-%d %H:%M:%S %z' "$stamp" +%s 2>/dev/null
}
power_epoch="$(latest_power_event_epoch || true)"
if is_positive_integer "$power_epoch" && [ "$power_epoch" -le "$now" ] && [ $((now - power_epoch)) -lt "$SLEEP_GRACE_SECONDS" ]; then
  event "batch=$BATCH_ID gate=sleep-wake-grace action=none"
  exit 0
fi
snapshot="$(network_snapshot || true)"
[ -n "$snapshot" ] || { event "batch=$BATCH_ID gate=network-unknown action=none"; exit 0; }
route_snapshot_id="$(printf '%s' "$snapshot" | /usr/bin/shasum -a 256 | /usr/bin/awk '{print substr($1, 1, 16)}')"
previous_snapshot=''
previous_since=0
if [ -f "$NETWORK_FILE" ]; then
  IFS=$'\t' read -r previous_snapshot previous_since < "$NETWORK_FILE" || true
fi
if [ "$snapshot" = "$previous_snapshot" ] && is_positive_integer "$previous_since"; then
  network_since="$previous_since"
else
  network_since="$now"
fi
printf '%s\t%s\n' "$snapshot" "$network_since" > "$NETWORK_FILE.tmp.$$"
/bin/mv -f "$NETWORK_FILE.tmp.$$" "$NETWORK_FILE"
if [ $((now - network_since)) -lt "$NETWORK_STABLE_SECONDS" ]; then
  event "batch=$BATCH_ID gate=network-settling route=$route_snapshot_id action=none"
  exit 0
fi

read_mapping() {
  local logical="$1" mapping_file actual
  mapping_file="/var/run/wireguard/$logical.name"
  [ ! -L "$mapping_file" ] || return 1
  [ -f "$mapping_file" ] || return 1
  actual="$(/bin/cat "$mapping_file" 2>/dev/null | /usr/bin/tr -d '[:space:]')"
  is_logical_interface "$actual" || return 1
  printf '%s\n' "$actual"
}

handshake_epoch() {
  "$WG_BIN" show "$1" latest-handshakes 2>/dev/null | /usr/bin/awk 'NR == 1 && $2 ~ /^[0-9]+$/ { print $2; exit }'
}

transfer_total() {
  "$WG_BIN" show "$1" transfer 2>/dev/null | /usr/bin/awk 'NR == 1 && $2 ~ /^[0-9]+$/ && $3 ~ /^[0-9]+$/ { print $2 + $3; exit }'
}

edge_state_path() { printf '%s/edge.%s.state\n' "$STATE_DIR" "$1"; }
mark_edge_sample() {
  local edge="$1" stale="$2" handshake="$3" observed_at="$4" file old_count=0 old_stale=0 old_observed_at=0 count=0
  file="$(edge_state_path "$edge")"
  if [ -f "$file" ]; then IFS=$'\t' read -r old_stale old_count _ old_observed_at < "$file" || true; fi
  is_positive_integer "$old_count" || old_count=0
  is_positive_integer "$old_observed_at" || old_observed_at=0
  if [ "$stale" = 1 ]; then
    # A historical stale sample cannot be paired with a much later one after the
    # reconciler was delayed or disabled. Break the confirmation chain instead.
    if [ "$old_stale" = 1 ] && [ "$observed_at" -ge "$old_observed_at" ] && [ $((observed_at - old_observed_at)) -le "$STALE_SAMPLE_MAX_GAP_SECONDS" ]; then
      count=$((old_count + 1))
    else
      count=1
    fi
  fi
  printf '%s\t%s\t%s\t%s\n' "$stale" "$count" "$handshake" "$observed_at" > "$file.tmp.$$"
  /bin/mv -f "$file.tmp.$$" "$file"
  printf '%s\n' "$count"
}

validate_manifest_and_collect() {
  local line edge logical label config wrapper peer extra line_no=0 count=0
  while IFS=$'\t' read -r edge logical label config wrapper peer extra; do
    line_no=$((line_no + 1))
    case "$edge" in ''|\#*) continue ;; esac
    [ -z "${extra:-}" ] || die "manifest line $line_no has too many fields"
    is_edge_id "$edge" || die "manifest line $line_no has invalid edge id"
    is_logical_interface "$logical" || die "manifest line $line_no has invalid logical interface"
    is_launchd_label "$label" || die "manifest line $line_no has unapproved launchd label"
    is_absolute_safe_path "$config" || die "manifest line $line_no has invalid config path"
    is_absolute_safe_path "$wrapper" || die "manifest line $line_no has invalid wrapper path"
    is_ipv4 "$peer" || die "manifest line $line_no has invalid peer tunnel IPv4"
    [ ! -L "$config" ] && [ -f "$config" ] || die "manifest line $line_no config is not a regular file"
    [ ! -L "$wrapper" ] && [ -x "$wrapper" ] || die "manifest line $line_no wrapper is not executable"
    [ "$(/usr/bin/stat -f '%u' "$config")" = 0 ] || die "manifest line $line_no config must be owned by root"
    [ "$(/usr/bin/stat -f '%u' "$wrapper")" = 0 ] || die "manifest line $line_no wrapper must be owned by root"
    root_path_chain "$config" || die "manifest line $line_no config parent must be root-only"
    root_path_chain "$wrapper" || die "manifest line $line_no wrapper parent must be root-only"
    case "$(/usr/bin/stat -f '%Lp' "$config")" in ???) ;; *) die "manifest line $line_no config mode unreadable" ;; esac
    case "$(/usr/bin/stat -f '%Lp' "$config")" in ?[2367][0-7]|??[2367]) die "manifest line $line_no config is writable by group or others" ;; esac
    /usr/bin/grep -Eq '^[[:space:]]*PersistentKeepalive[[:space:]]*=[[:space:]]*25[[:space:]]*$' "$config" || die "manifest line $line_no does not require PersistentKeepalive = 25"
    count=$((count + 1))
  done < "$MANIFEST"
  [ "$count" -eq 8 ] || die 'runtime manifest must contain exactly eight edges'
  /usr/bin/awk -F '\t' '!/^($|#)/ { if (edge[$1]++ || logical[$2]++ || label[$3]++) exit 1 }' "$MANIFEST" || die 'runtime manifest contains duplicate edge, logical interface, or launchd label'
}

validate_manifest_and_collect

probe_observation() {
  # Adapter contract: 0=fresh/healthy, 1=known edge failure, 75=unknown transport or collector state.
  # Its stdout/stderr are intentionally not persisted because they can contain endpoint metadata.
  local edge="$1" nonce="$2" status
  set +e
  "$PROBE_HELPER" --edge-id "$edge" --nonce "$nonce" --mode observe >/dev/null 2>&1
  status=$?
  set -e
  case "$status" in 0|1) printf '%s\n' "$status"; return 0 ;; *) return 1 ;; esac
}

probe_verify() {
  local edge="$1" nonce="$2" min_observed_at="$3"
  "$PROBE_HELPER" --edge-id "$edge" --nonce "$nonce" --mode verify --min-observed-at "$min_observed_at" >/dev/null 2>&1
}

wait_for_probe_verify() {
  local edge="$1" nonce="$2" min_observed_at="$3" deadline
  deadline=$(( $(/bin/date +%s) + ACTION_TIMEOUT_SECONDS ))
  while [ "$(/bin/date +%s)" -le "$deadline" ]; do
    if probe_verify "$edge" "$nonce" "$min_observed_at"; then return 0; fi
    /bin/sleep 5
  done
  return 1
}

all_stale=1
eligible_stale=1
edge_count=0
while IFS=$'\t' read -r edge logical label config wrapper peer extra; do
  case "$edge" in ''|\#*) continue ;; esac
  edge_count=$((edge_count + 1))
  actual="$(read_mapping "$logical" || true)"
  handshake=0
  if [ -n "$actual" ]; then handshake="$(handshake_epoch "$actual" || true)"; fi
  transfer=0
  if [ -n "$actual" ]; then transfer="$(transfer_total "$actual" || true)"; fi
  is_positive_integer "$handshake" || handshake=0
  stale=1
  if [ "$handshake" -gt 0 ] && [ $((now - handshake)) -lt "$STALE_SECONDS" ]; then stale=0; fi
  sample_count="$(mark_edge_sample "$edge" "$stale" "$handshake" "$now")"
  if [ "$stale" -ne 1 ]; then all_stale=0; eligible_stale=0; fi
  if [ "$sample_count" -lt "$STALE_CONFIRMATIONS" ]; then eligible_stale=0; fi
  probe_nonce="wg-observe-$edge-$now"
  probe_status="$(probe_observation "$edge" "$probe_nonce" || true)"
  if [ -z "$probe_status" ]; then
    event "batch=$BATCH_ID edge=$edge handshake_age=$((now - handshake)) transfer=$transfer utun=${actual:-missing} route=$route_snapshot_id probe=unknown action=none"
    exit 0
  fi
  if [ "$probe_status" -eq 0 ]; then
    event "batch=$BATCH_ID edge=$edge handshake_age=$((now - handshake)) transfer=$transfer utun=${actual:-missing} route=$route_snapshot_id probe=healthy-disagrees action=none"
    exit 0
  fi
done < "$MANIFEST"

if [ "$all_stale" -ne 1 ] || [ "$eligible_stale" -ne 1 ]; then
  event "batch=$BATCH_ID gate=partial-or-unconfirmed-stale edges=$edge_count action=none"
  exit 0
fi

LAST_ACTION_FILE="$STATE_DIR/last-action.epoch"
last_action=0
if [ -f "$LAST_ACTION_FILE" ]; then last_action="$(/bin/cat "$LAST_ACTION_FILE" 2>/dev/null || true)"; fi
is_positive_integer "$last_action" || last_action=0
if [ $((now - last_action)) -lt "$GLOBAL_COOLDOWN_SECONDS" ]; then
  event "batch=$BATCH_ID gate=global-cooldown action=none"
  exit 0
fi

ACTION_HISTORY="$STATE_DIR/action-history.epoch"
[ -f "$ACTION_HISTORY" ] || : > "$ACTION_HISTORY"
window_actions="$(/usr/bin/awk -v cutoff=$((now - ACTION_WINDOW_SECONDS)) '$1 ~ /^[0-9]+$/ && $1 >= cutoff { count++ } END { print count + 0 }' "$ACTION_HISTORY")"
if [ "$window_actions" -ge "$MAX_GLOBAL_ACTIONS" ]; then
  event "batch=$BATCH_ID gate=global-window-budget action=none"
  exit 0
fi

wait_for_cleanup() {
  local logical="$1" old_actual="$2" peer="$3" deadline route_text
  deadline=$(( $(/bin/date +%s) + ACTION_TIMEOUT_SECONDS ))
  while [ "$(/bin/date +%s)" -le "$deadline" ]; do
    route_text="$(/sbin/route -n get "$peer" 2>/dev/null || true)"
    if [ ! -e "/var/run/wireguard/$logical.name" ] \
      && ! "$WG_BIN" show "$old_actual" >/dev/null 2>&1 \
      && ! printf '%s\n' "$route_text" | /usr/bin/grep -Fq "interface: $old_actual"; then
      return 0
    fi
    /bin/sleep 1
  done
  return 1
}

wait_for_fresh_handshake() {
  local logical="$1" before_transfer="$2" consecutive=0 saw_transfer=0 actual handshake transfer deadline
  deadline=$(( $(/bin/date +%s) + ACTION_TIMEOUT_SECONDS ))
  while [ "$(/bin/date +%s)" -le "$deadline" ]; do
    actual="$(read_mapping "$logical" || true)"
    handshake=0
    if [ -n "$actual" ]; then handshake="$(handshake_epoch "$actual" || true)"; fi
    transfer=0
    if [ -n "$actual" ]; then transfer="$(transfer_total "$actual" || true)"; fi
    is_positive_integer "$handshake" || handshake=0
    is_positive_integer "$transfer" || transfer=0
    if [ "$transfer" -gt "$before_transfer" ]; then saw_transfer=1; fi
    # Cleanup has already proved that the old mapping and interface disappeared. macOS can
    # legally reuse the same utun number, so require a newly observed mapping plus fresh
    # handshakes and traffic rather than a different textual utun name.
    if [ -n "$actual" ] && [ "$handshake" -gt 0 ] && [ $(( $(/bin/date +%s) - handshake )) -lt "$STALE_SECONDS" ]; then
      consecutive=$((consecutive + 1))
      [ "$consecutive" -ge "$FRESH_CONFIRMATIONS" ] && [ "$saw_transfer" -eq 1 ] && return 0
    else
      consecutive=0
    fi
    /bin/sleep 5
  done
  return 1
}

run_bounded() {
  local child deadline status
  "$@" >/dev/null 2>&1 &
  child=$!
  deadline=$(( $(/bin/date +%s) + ACTION_TIMEOUT_SECONDS ))
  while /bin/kill -0 "$child" 2>/dev/null; do
    if [ "$(/bin/date +%s)" -gt "$deadline" ]; then
      /bin/kill -TERM "$child" 2>/dev/null || true
      /bin/sleep 1
      /bin/kill -KILL "$child" 2>/dev/null || true
      wait "$child" 2>/dev/null || true
      return 124
    fi
    /bin/sleep 1
  done
  set +e
  wait "$child"
  status=$?
  set -e
  return "$status"
}

restart_edge() {
  local edge="$1" logical="$2" label="$3" config="$4" wrapper="$5" peer="$6" old_actual job_dump before_transfer probe_nonce action_started action_id edge_last=0 edge_last_file plist index argument wrapper_count config_count
  edge_last_file="$STATE_DIR/edge.$edge.last-action.epoch"
  [ -f "$edge_last_file" ] && edge_last="$(/bin/cat "$edge_last_file" 2>/dev/null || true)"
  is_positive_integer "$edge_last" || edge_last=0
  if [ $((now - edge_last)) -lt "$PER_EDGE_COOLDOWN_SECONDS" ]; then
    event "batch=$BATCH_ID edge=$edge result=skipped reason=edge-cooldown"
    return 1
  fi
  job_dump="$(/bin/launchctl print "system/$label" 2>/dev/null || true)"
  [ -n "$job_dump" ] || { event "batch=$BATCH_ID edge=$edge result=skipped reason=launchd-job-missing"; return 1; }
  plist="/Library/LaunchDaemons/$label.plist"
  [ ! -L "$plist" ] && [ -f "$plist" ] && root_path_chain "$plist" || { event "batch=$BATCH_ID edge=$edge result=skipped reason=plist-unsafe"; return 1; }
  wrapper_count=0
  config_count=0
  index=0
  while [ "$index" -le 15 ]; do
    argument="$(/usr/bin/plutil -extract "ProgramArguments.$index" raw -o - "$plist" 2>/dev/null || true)"
    [ "$argument" = "$wrapper" ] && wrapper_count=$((wrapper_count + 1))
    [ "$argument" = "$config" ] && config_count=$((config_count + 1))
    index=$((index + 1))
  done
  [ "$wrapper_count" -eq 1 ] && [ "$config_count" -eq 1 ] || { event "batch=$BATCH_ID edge=$edge result=skipped reason=plist-pair-mismatch"; return 1; }
  old_actual="$(read_mapping "$logical" || true)"
  [ -n "$old_actual" ] || { event "batch=$BATCH_ID edge=$edge result=skipped reason=mapping-missing"; return 1; }
  before_transfer="$(transfer_total "$old_actual" || true)"
  is_positive_integer "$before_transfer" || before_transfer=0
  action_started="$(/bin/date +%s)"
  action_id="$BATCH_ID-$edge-$action_started"
  event "batch=$BATCH_ID edge=$edge action_id=$action_id phase=terminate-for-cleanup"
  run_bounded /bin/launchctl kill SIGTERM "system/$label" || { event "batch=$BATCH_ID edge=$edge action_id=$action_id result=failed phase=terminate-or-timeout"; return 1; }
  if ! wait_for_cleanup "$logical" "$old_actual" "$peer"; then
    event "batch=$BATCH_ID edge=$edge action_id=$action_id result=failed phase=cleanup-timeout"
    return 1
  fi
  run_bounded /bin/launchctl kickstart -k "system/$label" || { event "batch=$BATCH_ID edge=$edge action_id=$action_id result=failed phase=kickstart-or-timeout"; return 1; }
  if ! wait_for_fresh_handshake "$logical" "$before_transfer"; then
    event "batch=$BATCH_ID edge=$edge action_id=$action_id result=failed phase=new-mapping-handshake-or-transfer-timeout"
    return 1
  fi
  probe_nonce="wg-verify-$edge-$now-$$"
  if ! wait_for_probe_verify "$edge" "$probe_nonce" "$action_started"; then
    event "batch=$BATCH_ID edge=$edge action_id=$action_id result=failed phase=independent-probe"
    return 1
  fi
  printf '%s\n' "$now" > "$edge_last_file.tmp.$$"
  /bin/mv -f "$edge_last_file.tmp.$$" "$edge_last_file"
  event "batch=$BATCH_ID edge=$edge action_id=$action_id result=recovered"
  return 0
}

# All edges are confirmed stale. Prove one canary first; a shared WAN failure must not
# turn into eight blind restarts. Only then reconcile the remaining exact allowlist.
canary_done=0
batch_started=0
while IFS=$'\t' read -r edge logical label config wrapper peer extra; do
  case "$edge" in ''|\#*) continue ;; esac
  if [ "$canary_done" -eq 0 ]; then
    printf '%s\n' "$now" > "$LAST_ACTION_FILE.tmp.$$"
    /bin/mv -f "$LAST_ACTION_FILE.tmp.$$" "$LAST_ACTION_FILE"
    printf '%s\n' "$now" >> "$ACTION_HISTORY"
    if [ "$(/usr/bin/wc -l < "$ACTION_HISTORY")" -gt 200 ]; then
      /usr/bin/tail -n 100 "$ACTION_HISTORY" > "$ACTION_HISTORY.tmp.$$"
      /bin/mv -f "$ACTION_HISTORY.tmp.$$" "$ACTION_HISTORY"
    fi
    batch_started=1
    if ! restart_edge "$edge" "$logical" "$label" "$config" "$wrapper" "$peer"; then
      event "batch=$BATCH_ID result=canary-failed edge=$edge"
      exit 1
    fi
    canary_done=1
  else
    if ! restart_edge "$edge" "$logical" "$label" "$config" "$wrapper" "$peer"; then
      event "batch=$BATCH_ID result=non-canary-failed edge=$edge remaining=skipped"
      exit 1
    fi
  fi
done < "$MANIFEST"
[ "$batch_started" -eq 1 ] && event "batch=$BATCH_ID result=completed"
