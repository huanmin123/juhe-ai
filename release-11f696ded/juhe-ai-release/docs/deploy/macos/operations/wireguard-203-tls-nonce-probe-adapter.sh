#!/usr/bin/env bash
# Root-only reader for the independent 203 TLS/nonce probe collector.
# 0=known healthy, 1=known failed, 75=unknown. It never exposes Prometheus over HTTP.
set -euo pipefail
umask 077

MAPPING='/usr/local/libexec/juhe-ai/wireguard-203-tls-nonce-probe.map'
RUNTIME_MANIFEST='/usr/local/libexec/juhe-ai/wireguard-reconciler.manifest'
SSH_BIN='/usr/bin/ssh'
EDGE_ID=''
NONCE=''
MODE=''
MIN_OBSERVED_AT=''
SUCCESS_METRIC='juhe_tunnel_probe_up'
TIMESTAMP_METRIC='juhe_tunnel_probe_last_observed_timestamp_seconds'
REMOTE_PROTOCOL='juhe-tunnel-probe-read-v1'
HOST_KEY_ALIAS='juhe-wg-probe-203'

usage() {
  cat <<'EOF'
Usage: wireguard-203-tls-nonce-probe-adapter.sh --edge-id <id> --nonce <nonce> --mode <observe|verify> [--min-observed-at <epoch>] [--mapping <absolute-path>] [--runtime-manifest <absolute-path>] [--ssh-bin <absolute-path>]

Private root-owned mapping format (tab-separated, no header):
  edge-id<TAB>ssh-user-at-host<TAB>node-label<TAB>public-ip-label<TAB>known-hosts-path<TAB>private-key-path

The adapter invokes only the fixed SSH protocol command `juhe-tunnel-probe-read-v1` with
BatchMode, a pinned HostKeyAlias and a dedicated root-only identity. Its remote public key
must be forced to return the collector's read-only Prometheus text. The remote collector
continues to listen only on loopback; this script never accepts HTTP/HTTPS URLs, remote
commands, caller-provided selectors or nonce challenge arguments.

Each requested series must contain exactly `node` and `public_ip` labels and exactly one
sample for `juhe_tunnel_probe_up` and
`juhe_tunnel_probe_last_observed_timestamp_seconds`. observe returns 0 only for a fresh
success and 1 only for a fresh known failure. Malformed, duplicate, stale or unavailable
records return 75. verify additionally requires a fresh successful sample timestamp not
earlier than the recovery action start time. The nonce is local audit correlation only; it
is deliberately not sent to the collector.
EOF
}

safe_path() { case "$1" in /) return 1 ;; /*) ;; *) return 1 ;; esac; case "$1" in *$'\n'*|*$'\r'*|*$'\t'*|*' '*|*'$'*|*'`'*|*'"'*|*"'"*|*'\\'*|*'|'*|*';'*|*'&'*|*'<'*|*'>') return 1 ;; esac; }
edge_ok() { printf '%s' "$1" | /usr/bin/grep -Eq '^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$'; }
node_ok() { printf '%s' "$1" | /usr/bin/grep -Eq '^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$'; }
ipv4_ok() { printf '%s' "$1" | /usr/bin/awk -F. 'NF == 4 { for (i = 1; i <= 4; i++) if ($i !~ /^[0-9]+$/ || $i > 255) exit 1; exit 0 } { exit 1 }'; }
ssh_target_ok() { printf '%s' "$1" | /usr/bin/grep -Eq '^[A-Za-z0-9][A-Za-z0-9._-]{0,63}@[A-Za-z0-9][A-Za-z0-9.-]{0,252}$'; }
nonce_ok() { printf '%s' "$1" | /usr/bin/grep -Eq '^[A-Za-z0-9][A-Za-z0-9._-]{7,127}$'; }
root_path_chain() { local path="$1" owner mode; while [ "$path" != / ]; do [ ! -L "$path" ] && [ -e "$path" ] || return 1; owner="$(/usr/bin/stat -f '%u' "$path")"; mode="$(/usr/bin/stat -f '%Lp' "$path")"; [ "$owner" = 0 ] || return 1; case "$mode" in ?[2367][0-7]|??[2367]) return 1 ;; esac; path="$(/usr/bin/dirname "$path")"; done; }
root_private_file() {
  local path="$1" mode uid
  [ ! -L "$path" ] && [ -f "$path" ] || return 1
  uid="$(/usr/bin/stat -f '%u' "$path")"; mode="$(/usr/bin/stat -f '%Lp' "$path")"
  [ "$uid" = 0 ] && { [ "$mode" = 400 ] || [ "$mode" = 600 ]; } && root_path_chain "$path"
}

while [ "$#" -gt 0 ]; do case "$1" in --edge-id) EDGE_ID="${2:?}"; shift 2;; --nonce) NONCE="${2:?}"; shift 2;; --mode) MODE="${2:?}"; shift 2;; --min-observed-at) MIN_OBSERVED_AT="${2:?}"; shift 2;; --mapping) MAPPING="${2:?}"; shift 2;; --runtime-manifest) RUNTIME_MANIFEST="${2:?}"; shift 2;; --ssh-bin) SSH_BIN="${2:?}"; shift 2;; -h|--help) usage; exit 0;; *) exit 75;; esac; done
edge_ok "$EDGE_ID" && nonce_ok "$NONCE" || exit 75
case "$MODE" in observe|verify) ;; *) exit 75 ;; esac
[ "$MODE" != verify ] || printf '%s' "$MIN_OBSERVED_AT" | /usr/bin/grep -Eq '^[0-9]+$' || exit 75
safe_path "$MAPPING" && safe_path "$RUNTIME_MANIFEST" && safe_path "$SSH_BIN" || exit 75
root_private_file "$MAPPING" || exit 75
root_private_file "$RUNTIME_MANIFEST" || exit 75
[ ! -L "$SSH_BIN" ] && [ -x "$SSH_BIN" ] && root_path_chain "$SSH_BIN" || exit 75

runtime_count=0
while IFS=$'\t' read -r runtime_edge runtime_logical runtime_label runtime_config runtime_wrapper runtime_peer runtime_extra; do
  case "$runtime_edge" in ''|\#*) continue ;; esac
  [ -z "${runtime_extra:-}" ] || exit 75
  edge_ok "$runtime_edge" || exit 75
  runtime_count=$((runtime_count + 1))
done < "$RUNTIME_MANIFEST"
[ "$runtime_count" -eq 8 ] || exit 75
/usr/bin/awk -F '\t' '!/^($|#)/ { if (NF != 6 || runtime[$1]++) exit 1; count++ } END { exit count == 8 ? 0 : 1 }' "$RUNTIME_MANIFEST" || exit 75

ssh_target=''; node_label=''; public_ip=''; known_hosts=''; identity=''; mapping_count=0 selected_count=0
while IFS=$'\t' read -r edge mapped_target mapped_node mapped_public mapped_known_hosts mapped_identity extra; do
  case "$edge" in ''|\#*) continue ;; esac
  [ -z "${extra:-}" ] || exit 75
  edge_ok "$edge" && ssh_target_ok "$mapped_target" && node_ok "$mapped_node" && ipv4_ok "$mapped_public" || exit 75
  safe_path "$mapped_known_hosts" && safe_path "$mapped_identity" || exit 75
  root_private_file "$mapped_known_hosts" && root_private_file "$mapped_identity" || exit 75
  mapping_count=$((mapping_count + 1))
  if [ "$edge" = "$EDGE_ID" ]; then
    selected_count=$((selected_count + 1))
    ssh_target="$mapped_target"; node_label="$mapped_node"; public_ip="$mapped_public"; known_hosts="$mapped_known_hosts"; identity="$mapped_identity"
  fi
done < "$MAPPING"
[ "$mapping_count" -eq 8 ] && [ "$selected_count" -eq 1 ] || exit 75
/usr/bin/awk -F '\t' '
  NR == FNR {
    if ($0 ~ /^($|#)/) next
    if (NF != 6 || manifest[$1]++) exit 1
    manifest_count++
    next
  }
  {
    if ($0 ~ /^($|#)/) next
    if (NF != 6 || !($1 in manifest) || mapped[$1]++ || series[$3 SUBSEP $4]++) exit 1
    mapping_count++
  }
  END { exit manifest_count == 8 && mapping_count == 8 ? 0 : 1 }
' "$RUNTIME_MANIFEST" "$MAPPING" || exit 75

# -F /dev/null and the disabled proxy options prevent user/global SSH configuration from
# changing the transport. HostKeyAlias means the private known_hosts file pins one expected key.
set +e
metrics="$("$SSH_BIN" -n -T -F /dev/null \
  -o BatchMode=yes -o IdentitiesOnly=yes -o PasswordAuthentication=no \
  -o KbdInteractiveAuthentication=no -o ChallengeResponseAuthentication=no \
  -o StrictHostKeyChecking=yes -o "HostKeyAlias=$HOST_KEY_ALIAS" \
  -o "UserKnownHostsFile=$known_hosts" -o GlobalKnownHostsFile=/dev/null \
  -o ProxyCommand=none -o ProxyJump=none -o ClearAllForwardings=yes \
  -o ConnectTimeout=2 -o ConnectionAttempts=1 -o ServerAliveInterval=2 \
  -o ServerAliveCountMax=2 -i "$identity" "$ssh_target" "$REMOTE_PROTOCOL" 2>/dev/null)"
ssh_status=$?
set -e
[ "$ssh_status" -eq 0 ] || exit 75
# A forced read-only protocol returns a small textfile collector payload. Bound a broken remote
# command before parsing it; this is not an endpoint or configuration dump.
[ "$(printf '%s' "$metrics" | /usr/bin/wc -c | /usr/bin/tr -d '[:space:]')" -le 262144 ] || exit 75

metric_values() {
  local metric="$1" value_pattern="$2"
  printf '%s\n' "$metrics" | /usr/bin/awk -v metric="$metric" -v node="$node_label" -v public_ip="$public_ip" -v value_pattern="$value_pattern" '
    function trim(value) { sub(/^[[:space:]]+/, "", value); sub(/[[:space:]]+$/, "", value); return value }
    function expected_series(labels, count, part, pieces, item_index, node_count, public_count) {
      count = split(labels, pieces, ",")
      if (count != 2) return 0
      node_count = 0; public_count = 0
      for (item_index = 1; item_index <= count; item_index++) {
        part = trim(pieces[item_index])
        if (part == "node=\"" node "\"") node_count++
        else if (part == "public_ip=\"" public_ip "\"") public_count++
        else return 0
      }
      return node_count == 1 && public_count == 1
    }
    index($0, metric "{") == 1 {
      rest = substr($0, length(metric) + 2)
      brace_end = index(rest, "}")
      if (brace_end == 0) next
      labels = substr(rest, 1, brace_end - 1)
      value = trim(substr(rest, brace_end + 1))
      if (expected_series(labels) && value ~ value_pattern) print value
    }
  '
}

success_values="$(metric_values "$SUCCESS_METRIC" '^(0|1)$')"
timestamp_values="$(metric_values "$TIMESTAMP_METRIC" '^[0-9]+([.][0-9]+)?$')"
[ "$(printf '%s\n' "$success_values" | /usr/bin/awk 'NF{count++} END{print count+0}')" -eq 1 ] || exit 75
[ "$(printf '%s\n' "$timestamp_values" | /usr/bin/awk 'NF{count++} END{print count+0}')" -eq 1 ] || exit 75
now="$(/bin/date +%s)"; timestamp="$(printf '%s\n' "$timestamp_values" | /usr/bin/awk '{printf "%d", $1}')"
[ "$timestamp" -le "$now" ] && [ $((now - timestamp)) -le 45 ] || exit 75
if [ "$MODE" = verify ]; then
  [ "$success_values" = 1 ] && [ "$timestamp" -ge "$MIN_OBSERVED_AT" ] && exit 0
  exit 75
fi
case "$success_values" in 1) exit 0 ;; 0) exit 1 ;; *) exit 75 ;; esac
