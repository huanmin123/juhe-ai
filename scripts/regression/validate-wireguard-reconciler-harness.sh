#!/usr/bin/env bash
# Executes a rewritten temporary copy of the real reconciler against deterministic command
# stubs. No production path, launchd domain, WireGuard interface or network is touched.
set -euo pipefail

REPO_ROOT="${1:?repository root is required}"
SOURCE="$REPO_ROOT/docs/deploy/macos/operations/wireguard-reconciler.sh"
ROOT="$(mktemp -d "${TMPDIR:-/tmp}/juhe-wg-reconciler.XXXXXX")"
FAKE="$ROOT/fake"
RUN="$ROOT/run"
STATE="$ROOT/state"
MANIFEST="$ROOT/manifest"
ACTION_LOG="$ROOT/actions.log"
PROBE_MAPPING="$ROOT/probe.map"
PROBE_ADAPTER="$ROOT/probe-adapter.sh"
PROBE_INSTALLER="$ROOT/probe-installer.sh"
MIGRATION_MANIFEST="$ROOT/migration.manifest"
MIGRATOR="$ROOT/migrator.sh"
OPERATIONS="$ROOT/operations"
INSTALLER="$OPERATIONS/install-wireguard-reconciler.sh"
INSTALLED_MANIFEST="$ROOT/root-libexec-installer/wireguard-reconciler.manifest"
trap 'rm -rf "$ROOT" >/dev/null 2>&1 || true' EXIT HUP INT TERM
mkdir -p "$FAKE" "$RUN" "$STATE" "$OPERATIONS"
: > "$ACTION_LOG"

cat > "$FAKE/stat" <<'EOF'
#!/usr/bin/env bash
case "$2" in
  %u) printf '0\n' ;;
  %Lp) case "${3:-}" in *known_hosts|*probe_identity|*.map|*/manifest|*.conf) printf '600\n' ;; *) printf '700\n' ;; esac ;;
  %m) date +%s ;;
  *) printf '0\n' ;;
esac
EOF
cat > "$FAKE/id" <<'EOF'
#!/usr/bin/env bash
printf '0\n'
EOF
cat > "$FAKE/chown" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
cat > "$FAKE/bash" <<'EOF'
#!/usr/bin/env bash
exec /usr/bin/env bash "$@"
EOF
cat > "$FAKE/route" <<'EOF'
#!/usr/bin/env bash
if [ "${3:-}" = default ]; then printf 'gateway: 10.0.0.1\ninterface: en0\n'; else printf 'interface: utun9\n'; fi
EOF
cat > "$FAKE/ifconfig" <<'EOF'
#!/usr/bin/env bash
printf 'inet 10.0.0.2 netmask 0xffffff00\n'
EOF
cat > "$FAKE/pmset" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
cat > "$FAKE/logger" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "${FAKE_EVENT_LOG:?}"
exit 0
EOF
cat > "$FAKE/shasum" <<'EOF'
#!/usr/bin/env bash
if command -v sha256sum >/dev/null 2>&1; then
  if [ "${1:-}" = -a ] && [ "${2:-}" = 256 ]; then shift 2; fi
  sha256sum "$@"
else
  /usr/bin/shasum "$@"
fi
EOF
cat > "$FAKE/wg" <<'EOF'
#!/usr/bin/env bash
edge="$(printf '%s' "${2:-}" | sed -n 's/[^0-9]*\([0-9][0-9]*\)$/\1/p')"
if [ "$1" = show ] && [ -n "${FAKE_WG_LOG:-}" ]; then printf 'wg show %s\n' "${2:-}" >> "$FAKE_WG_LOG"; fi
if [ "$1" = show ] && [ "$3" = latest-handshakes ]; then
  if { [ -n "${FAKE_FRESH_UTUN:-}" ] && [ "$2" = "$FAKE_FRESH_UTUN" ]; } || [ -f "$FAKE_RUN/killed.$edge" ]; then printf 'peer %s\n' "$(date +%s)"; else printf 'peer %s\n' "${FAKE_HANDSHAKE:-0}"; fi
  exit 0
fi
if [ "$1" = show ] && [ "$3" = transfer ]; then if [ -f "$FAKE_RUN/killed.$edge" ]; then printf 'peer 200 200\n'; else printf 'peer 100 100\n'; fi; exit 0; fi
if [ "$1" = show ] && { [ -f "$FAKE_RUN/killed.$edge" ] || [ -f "$FAKE_RUN/interface-gone.$edge" ]; }; then exit 1; fi
if [ "$1" = show ]; then exit 0; fi
exit 1
EOF
cat > "$FAKE/wg-quick" <<'EOF'
#!/usr/bin/env bash
command_name="${1:-}"
config_path="${2:-}"
edge="$(basename "$config_path" .conf)"
number="$(printf '%s' "$edge" | sed -n 's/.*\([0-9][0-9]*\)$/\1/p')"
actual_interface="utun${number:-0}"
printf 'wg-quick %s %s\n' "$command_name" "$config_path" >> "${FAKE_WG_LOG:?}"
case "$command_name" in
  down)
    rm -f "$FAKE_RUN/$edge.name"
    exit 0
    ;;
  up)
    if [ "${FAKE_WG_UP_FAIL:-0}" -ne 0 ]; then exit "${FAKE_WG_UP_FAIL}"; fi
    if [ "${FAKE_WG_READY_DELAY_SECONDS:-0}" -gt 0 ]; then sleep "$FAKE_WG_READY_DELAY_SECONDS"; fi
    if [ "${FAKE_WG_NEVER_READY:-0}" -eq 0 ]; then
      printf '%s\n' "$actual_interface" > "$FAKE_RUN/$edge.name"
      : > "$FAKE_RUN/$actual_interface.sock"
      printf 'wg-quick ready %s\n' "$actual_interface" >> "${FAKE_WG_LOG:?}"
    fi
    if [ "${FAKE_WG_UP_HOLD_SECONDS:-0}" -gt 0 ]; then sleep "$FAKE_WG_UP_HOLD_SECONDS"; fi
    exit 0
    ;;
  *) exit 64 ;;
esac
EOF
cat > "$FAKE/probe" <<'EOF'
#!/usr/bin/env bash
if [ "${6:-}" = verify ] || [ "${5:-}" = verify ]; then
  edge=''
  while [ "$#" -gt 0 ]; do
    if [ "$1" = --edge-id ]; then edge="${2:-}"; break; fi
    shift
  done
  if [ "${FAKE_VERIFY:-75}" -eq 0 ] && [ -n "$edge" ]; then touch "$FAKE_RUN/verified.$edge"; fi
  exit "${FAKE_VERIFY:-75}"
fi
case "${FAKE_PROBE:-1}" in 0|1) exit "${FAKE_PROBE}" ;; *) exit 75 ;; esac
EOF
cat > "$FAKE/ssh" <<'EOF'
#!/usr/bin/env bash
[ -n "${FAKE_METRICS:-}" ] && /bin/cat "$FAKE_METRICS"
EOF
cat > "$FAKE/plutil" <<'EOF'
#!/usr/bin/env bash
value_for() {
  local slot="$1" plist="$2"
  [ -f "$plist" ] || return 0
  /usr/bin/awk -F '\t' -v slot="$slot" '$1 == slot { print $2; exit }' "$plist"
}
replace_value() {
  local slot="$1" value="$2" plist="$3" temp
  temp="$plist.plutil.$$"
  if [ -f "$plist" ]; then
    /usr/bin/awk -F '\t' -v slot="$slot" '$1 != slot { print }' "$plist" > "$temp"
  else
    : > "$temp"
  fi
  printf '%s\t%s\n' "$slot" "$value" >> "$temp"
  /bin/mv "$temp" "$plist"
}
case "$1" in
  -lint) exit 0 ;;
  -extract)
    index="${2#ProgramArguments.}"
    value="$(value_for "$index" "${6:?missing plist path}")"
    if [ -z "$value" ]; then
      case "$index" in
        0) value="${FAKE_SOURCE_WRAPPER:?}" ;;
        1)
          case "${FAKE_SOURCE_CONFIG##*/}" in
            config1)
              edge="$(printf '%s' "${6:?missing plist path}" | sed -n 's/.*edge\([0-9][0-9]*\)\.plist/\1/p')"
              value="$(dirname "$FAKE_SOURCE_CONFIG")/config${edge:-1}"
              ;;
            *) value="${FAKE_SOURCE_CONFIG:?}" ;;
          esac
          ;;
      esac
    fi
    [ -z "$value" ] || printf '%s\n' "$value"
    exit 0 ;;
  -replace)
    index="${2#ProgramArguments.}"
    [ "${3:-}" = -string ] || exit 64
    replace_value "$index" "${4:?missing replacement value}" "${5:?missing plist path}"
    exit 0 ;;
  *) exit 0 ;;
esac
EOF
cat > "$FAKE/launchctl" <<'EOF'
#!/usr/bin/env bash
plist_value() {
  local slot="$1" plist="$2"
  [ -f "$plist" ] || return 0
  /usr/bin/awk -F '\t' -v slot="$slot" '$1 == slot { print $2; exit }' "$plist"
}
last="$*"
printf '%s %s\n' "$1" "$*" >> "$FAKE_ACTION_LOG"
case "$1" in
  print)
    edge="$(printf '%s' "$last" | sed -n 's/.*edge\([0-9][0-9]*\).*/\1/p')"; [ -n "$edge" ] || edge=1
    label="${last##*/}"
    plist="${FAKE_LAUNCHD_DIR:?}/$label.plist"
    [ -f "$plist" ] || exit 113
    job_wrapper="$(plist_value 0 "$plist")"
    job_config="$(plist_value 1 "$plist")"
    [ -n "$job_wrapper" ] || job_wrapper="${FAKE_SOURCE_WRAPPER:?}"
    if [ -z "$job_config" ]; then
      # Regular reconciler fixtures share one wrapper but bind every edge to its own
      # config. Installer fixtures have a persisted plist pair and do not use this
      # fallback.
      case "${FAKE_SOURCE_CONFIG##*/}" in
        config1) job_config="$(dirname "$FAKE_SOURCE_CONFIG")/config$edge" ;;
        *) job_config="${FAKE_SOURCE_CONFIG:?}" ;;
      esac
    fi
    job="ProgramArguments = ( $job_wrapper $job_config )"
    printf 'jobdump %s\n' "$job" >> "$FAKE_ACTION_LOG"
    printf '%s\n' "$job"
    exit 0
    ;;
  kill) edge="$(printf '%s' "$last" | sed -n 's/.*edge\([0-9][0-9]*\).*/\1/p')"; [ -n "$edge" ] || edge=1; rm -f "$FAKE_RUN/wg$edge.name" "$FAKE_RUN/wg-edge$edge.name"; touch "$FAKE_RUN/killed.$edge"; exit 0 ;;
  kickstart) edge="$(printf '%s' "$last" | sed -n 's/.*edge\([0-9][0-9]*\).*/\1/p')"; [ -n "$edge" ] || edge=1; if [ "${FAKE_LAUNCHD_ALLOW_ALL:-0}" -ne 1 ] && [ "$edge" -gt 1 ] && [ ! -f "$FAKE_RUN/verified.edge1" ]; then printf 'kickstart-before-canary-verify edge%s\n' "$edge" >> "$FAKE_ACTION_LOG"; exit 91; fi; if [ "${FAKE_KICKSTART_FAIL_EDGE:-}" = "$edge" ]; then exit 91; fi; [ "${FAKE_KICKSTART:-0}" -eq 0 ] && { printf 'utun%s\n' "$edge" > "$FAKE_RUN/wg$edge.name"; printf 'utun%s\n' "$edge" > "$FAKE_RUN/wg-edge$edge.name"; }; exit "${FAKE_KICKSTART:-0}" ;;
  bootstrap) bootstrap_label="${last##*/}"; bootstrap_label="${bootstrap_label%.plist}"; [ "${FAKE_BOOTSTRAP_FAIL_LABEL:-}" != "$bootstrap_label" ] || exit 91; exit 0 ;;
  *) exit 0 ;;
esac
EOF
chmod 700 "$FAKE"/*

sed \
  -e "s|/usr/bin/stat|$FAKE/stat|g" \
  -e "s|/usr/bin/id|$FAKE/id|g" \
  -e "s|/sbin/route|$FAKE/route|g" \
  -e "s|/sbin/ifconfig|$FAKE/ifconfig|g" \
  -e "s|/usr/bin/pmset|$FAKE/pmset|g" \
  -e "s|/usr/bin/shasum|$FAKE/shasum|g" \
  -e "s|/usr/bin/plutil|$FAKE/plutil|g" \
  -e "s|/usr/bin/logger|$FAKE/logger|g" \
  -e "s|/Library/LaunchDaemons|$ROOT/launchd|g" \
  -e "s|/bin/launchctl|$FAKE/launchctl|g" \
  -e "s|/var/run/wireguard|$RUN|g" \
  -e 's/^FRESH_CONFIRMATIONS=3$/FRESH_CONFIRMATIONS=1/' \
  "$SOURCE" > "$ROOT/reconciler.sh"
chmod 700 "$ROOT/reconciler.sh"

sed \
  -e "s|/usr/bin/stat|$FAKE/stat|g" \
  -e "s|/usr/bin/ssh|$FAKE/ssh|g" \
  "$REPO_ROOT/docs/deploy/macos/operations/wireguard-203-tls-nonce-probe-adapter.sh" > "$PROBE_ADAPTER"
chmod 700 "$PROBE_ADAPTER"
sed \
  -e "s|/usr/bin/stat|$FAKE/stat|g" \
  "$REPO_ROOT/docs/deploy/macos/operations/install-wireguard-203-tls-nonce-probe-adapter.sh" > "$PROBE_INSTALLER"
chmod 700 "$PROBE_INSTALLER"
mkdir -p "$ROOT/launchd"
sed \
  -e "s|/usr/bin/stat|$FAKE/stat|g" \
  -e "s|/usr/bin/id|$FAKE/id|g" \
  -e "s|/usr/bin/plutil|$FAKE/plutil|g" \
  -e "s|/usr/bin/shasum|$FAKE/shasum|g" \
  -e "s|/usr/sbin/chown|$FAKE/chown|g" \
  -e "s|/usr/local/bin/bash|$FAKE/bash|g" \
  -e "s|/usr/local/bin/wg-quick|$FAKE/wg-quick|g" \
  -e "s|/usr/local/bin/wg|$FAKE/wg|g" \
  -e "s|/usr/local/libexec/juhe-ai/wireguard-config|$ROOT/root-libexec/wireguard-config|g" \
  -e "s|/Library/LaunchDaemons|$ROOT/launchd|g" \
  -e "s|/bin/launchctl|$FAKE/launchctl|g" \
  -e "s|/var/run/wireguard|$RUN|g" \
  "$REPO_ROOT/docs/deploy/macos/operations/migrate-wireguard-root-wrappers.sh" > "$MIGRATOR"
chmod 700 "$MIGRATOR"
cp "$MIGRATOR" "$OPERATIONS/migrate-wireguard-root-wrappers.sh"
cp "$ROOT/reconciler.sh" "$OPERATIONS/wireguard-reconciler.sh"
sed \
  -e "s|/usr/bin/stat|$FAKE/stat|g" \
  -e "s|/usr/bin/id|$FAKE/id|g" \
  -e "s|/usr/bin/plutil|$FAKE/plutil|g" \
  -e "s|/usr/bin/shasum|$FAKE/shasum|g" \
  -e "s|/usr/sbin/chown|$FAKE/chown|g" \
  -e "s|/usr/local/bin/wg|$FAKE/wg|g" \
  -e "s|/usr/local/libexec/juhe-ai/wireguard-config|$ROOT/root-libexec/wireguard-config|g" \
  -e "s|/Library/LaunchDaemons|$ROOT/launchd|g" \
  -e "s|/bin/launchctl|$FAKE/launchctl|g" \
  "$REPO_ROOT/docs/deploy/macos/operations/install-wireguard-reconciler.sh" > "$INSTALLER"
chmod 700 "$OPERATIONS/migrate-wireguard-root-wrappers.sh" "$OPERATIONS/wireguard-reconciler.sh" "$INSTALLER"

make_manifest() {
  : > "$MANIFEST"; rm -f "$RUN"/killed.*
  local index
  for index in 1 2 3 4 5 6 7 8; do
    printf 'edge%s\twg%s\tcom.example.wg.edge%s\t%s/config%s\t%s/wrapper.sh\t10.0.%s.2\n' "$index" "$index" "$index" "$ROOT" "$index" "$ROOT" "$index" >> "$MANIFEST"
    printf 'PersistentKeepalive = 25\n' > "$ROOT/config$index"
    printf '#!/usr/bin/env bash\n' > "$ROOT/wrapper.sh"
    chmod 700 "$ROOT/wrapper.sh"
    printf 'utun%s\n' "$index" > "$RUN/wg$index.name"
    printf 'utun%s\n' "$index" > "$RUN/wg-edge$index.name"
  done
}

make_probe_mapping() {
  : > "$PROBE_MAPPING"
  : > "$ROOT/known_hosts"
  : > "$ROOT/probe_identity"
  local index
  for index in 1 2 3 4 5 6 7 8; do
    printf 'edge%s\tprobe@monitoring.internal\tnode%s\t198.51.100.%s\t%s/known_hosts\t%s/probe_identity\n' "$index" "$index" "$index" "$ROOT" "$ROOT" >> "$PROBE_MAPPING"
  done
}

write_probe_metrics() {
  local edge_status="$1" observed_at="$2" index status
  : > "$ROOT/probe.metrics"
  for index in 1 2 3 4 5 6 7 8; do
    status=1
    [ "$index" -eq 1 ] && status="$edge_status"
    # Label order is not part of the contract; node/public_ip identity is.
    if [ $((index % 2)) -eq 0 ]; then
      printf 'juhe_tunnel_probe_up{public_ip="198.51.100.%s",node="node%s"} %s\n' "$index" "$index" "$status" >> "$ROOT/probe.metrics"
      printf 'juhe_tunnel_probe_last_observed_timestamp_seconds{public_ip="198.51.100.%s",node="node%s"} %s\n' "$index" "$index" "$observed_at" >> "$ROOT/probe.metrics"
    else
      printf 'juhe_tunnel_probe_up{node="node%s",public_ip="198.51.100.%s"} %s\n' "$index" "$index" "$status" >> "$ROOT/probe.metrics"
      printf 'juhe_tunnel_probe_last_observed_timestamp_seconds{node="node%s",public_ip="198.51.100.%s"} %s\n' "$index" "$index" "$observed_at" >> "$ROOT/probe.metrics"
    fi
  done
}

expect_adapter_status() {
  local mapping="$1" expected="$2" mode="${3:-observe}" min_observed_at="${4:-}"
  set +e
  if [ "$mode" = verify ]; then
    FAKE_METRICS="$ROOT/probe.metrics" bash "$PROBE_ADAPTER" --edge-id edge1 --nonce probe-nonce-0001 --mode verify --min-observed-at "$min_observed_at" --mapping "$mapping" --runtime-manifest "$MANIFEST" >/dev/null 2>&1
  else
    FAKE_METRICS="$ROOT/probe.metrics" bash "$PROBE_ADAPTER" --edge-id edge1 --nonce probe-nonce-0001 --mode observe --mapping "$mapping" --runtime-manifest "$MANIFEST" >/dev/null 2>&1
  fi
  local status=$?
  set -e
  [ "$status" -eq "$expected" ] || { echo "probe adapter expected $expected, got $status" >&2; exit 1; }
}

expect_installer_reject() {
  local mapping="$1"
  set +e
  bash "$PROBE_INSTALLER" --dry-run --mapping "$mapping" --runtime-manifest "$MANIFEST" --script-sha256 0000000000000000000000000000000000000000000000000000000000000000 >/dev/null 2>&1
  local status=$?
  set -e
  [ "$status" -ne 0 ] || { echo "probe installer accepted invalid mapping: $mapping" >&2; exit 1; }
}

make_migration_manifest() {
  local config_hash wrapper_hash index
  config_hash="$(sha256_file "$ROOT/migration.conf")"
  wrapper_hash="$(sha256_file "$ROOT/migration-wrapper.sh")"
  : > "$MIGRATION_MANIFEST"
  for index in 1 2 3 4 5 6 7 8; do
    : > "$ROOT/launchd/com.example.wg.edge$index.plist"
    printf 'edge%s\twg-edge%s\tcom.example.wg.edge%s\t%s/migration.conf\t%s/migration-wrapper.sh\t10.0.%s.2\t%s\t%s\n' "$index" "$index" "$index" "$ROOT" "$ROOT" "$index" "$config_hash" "$wrapper_hash" >> "$MIGRATION_MANIFEST"
  done
}

sha256_file() {
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | /usr/bin/awk '{print $1}' | /usr/bin/tr -d '\r'
  else
    sha256sum "$1" | /usr/bin/awk '{print $1}' | /usr/bin/tr -d '\r'
  fi
}

expect_migrator_status() {
  local expected="$1"
  set +e
  FAKE_SOURCE_WRAPPER="$ROOT/migration-wrapper.sh" FAKE_SOURCE_CONFIG="$ROOT/migration.conf" FAKE_LAUNCHD_DIR="$ROOT/launchd" FAKE_ACTION_LOG="$ACTION_LOG" bash "$MIGRATOR" --dry-run --manifest "$MIGRATION_MANIFEST" --install-dir "$ROOT/root-libexec" >/dev/null 2>&1
  local status=$?
  set -e
  if [ "$expected" = accept ]; then
    [ "$status" -eq 0 ] || { echo 'migrator rejected fixed minimal wrapper unexpectedly' >&2; exit 1; }
  else
    [ "$status" -ne 0 ] || { echo "migrator accepted unsafe source: $expected" >&2; exit 1; }
  fi
}

apply_migrator() {
  FAKE_SOURCE_WRAPPER="$ROOT/migration-wrapper.sh" FAKE_SOURCE_CONFIG="$ROOT/migration.conf" FAKE_LAUNCHD_DIR="$ROOT/launchd" FAKE_ACTION_LOG="$ACTION_LOG" FAKE_RUN="$RUN" FAKE_LAUNCHD_ALLOW_ALL=1 FAKE_BOOTSTRAP_FAIL_LABEL="${FAKE_BOOTSTRAP_FAIL_LABEL:-}" \
    bash "$MIGRATOR" --apply --manifest "$MIGRATION_MANIFEST" --install-dir "$ROOT/root-libexec"
}

apply_installer() {
  local helper_hash migrator_hash
  helper_hash="$(sha256_file "$OPERATIONS/wireguard-reconciler.sh")"
  migrator_hash="$(sha256_file "$OPERATIONS/migrate-wireguard-root-wrappers.sh")"
  case "$helper_hash" in
    [0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]) ;;
    *) printf 'invalid harness helper SHA-256: length=%s value=%q\n' "${#helper_hash}" "$helper_hash" >&2; return 1 ;;
  esac
  FAKE_SOURCE_WRAPPER="$ROOT/migration-wrapper.sh" FAKE_SOURCE_CONFIG="$ROOT/migration.conf" FAKE_LAUNCHD_DIR="$ROOT/launchd" FAKE_ACTION_LOG="$ACTION_LOG" FAKE_RUN="$RUN" FAKE_LAUNCHD_ALLOW_ALL=1 \
    bash "$INSTALLER" --apply --manifest "$MIGRATION_MANIFEST" --install-dir "$ROOT/root-libexec-installer" --state-dir "$ROOT/installer-state" --wg-bin "$FAKE/wg" --probe-helper "$FAKE/probe" --script-sha256 "$helper_hash" --migrator-sha256 "$migrator_hash"
}

wait_for_log() {
  local pattern="$1" log="$2" attempts=0
  while [ "$attempts" -lt 40 ]; do
    /usr/bin/grep -Fq "$pattern" "$log" 2>/dev/null && return 0
    sleep 1
    attempts=$((attempts + 1))
  done
  echo "timed out waiting for log marker: $pattern" >&2
  return 1
}

expect_fixed_wrapper_lifecycle() {
  local wrapper="$ROOT/root-libexec/wireguard/edge1/run-wireguard.sh"
  local config="$ROOT/root-libexec/wireguard-config/wg-edge1.conf"
  local log="$ROOT/fixed-wrapper.log"
  local override_log="$ROOT/fixed-wrapper-override.log"
  local wrapper_pid wrapper_status
  : > "$log"
  : > "$override_log"
  cat > "$ROOT/override-wg-quick.sh" <<'EOF'
#!/usr/bin/env bash
printf 'unexpected WG_QUICK_BIN execution\n' >> "$FAKE_OVERRIDE_LOG"
exit 99
EOF
  cat > "$ROOT/override-wg.sh" <<'EOF'
#!/usr/bin/env bash
printf 'unexpected WG_BIN execution\n' >> "$FAKE_OVERRIDE_LOG"
exit 99
EOF
  chmod 700 "$ROOT/override-wg-quick.sh" "$ROOT/override-wg.sh"

  FAKE_RUN="$RUN" FAKE_WG_LOG="$log" FAKE_OVERRIDE_LOG="$override_log" FAKE_WG_READY_DELAY_SECONDS=1 FAKE_WG_UP_HOLD_SECONDS=5 WG_QUICK_BIN="$ROOT/override-wg-quick.sh" WG_BIN="$ROOT/override-wg.sh" \
    bash "$wrapper" wg-edge1 "$config" > "$ROOT/fixed-wrapper.out" 2>&1 &
  wrapper_pid=$!
  if ! wait_for_log "wg show utun1" "$log"; then
    cat "$ROOT/fixed-wrapper.out" >&2
    cat "$log" >&2
    cat "$wrapper" >&2
    if ! FAKE_RUN="$RUN" FAKE_WG_LOG="$log" "$FAKE/wg" show utun1 >&2; then
      echo 'fixed-wrapper fake wg direct readiness check failed' >&2
    fi
    cat "$log" >&2
    kill "$wrapper_pid" >/dev/null 2>&1 || true
    wait "$wrapper_pid" 2>/dev/null || true
    exit 1
  fi
  first_down_line="$(/usr/bin/grep -n -F "wg-quick down $config" "$log" | /usr/bin/sed -n '1s/:.*//p')"
  up_line="$(/usr/bin/grep -n -F "wg-quick up $config" "$log" | /usr/bin/sed -n '1s/:.*//p')"
  [ -n "$first_down_line" ] && [ -n "$up_line" ] && [ "$first_down_line" -lt "$up_line" ] || { echo 'fixed wrapper did not call wg-quick down before up' >&2; exit 1; }
  [ ! -s "$override_log" ] || { echo 'fixed wrapper honored a WG_* binary override' >&2; exit 1; }
  touch "$RUN/interface-gone.1"
  set +e
  wait "$wrapper_pid"
  wrapper_status=$?
  set -e
  [ "$wrapper_status" -ne 0 ] || { echo 'fixed wrapper treated an interface exit as healthy' >&2; exit 1; }
  [ "$(/usr/bin/grep -Fc "wg-quick down $config" "$log")" -ge 2 ] || { echo 'fixed wrapper did not clean up with wg-quick down after interface exit' >&2; exit 1; }
  [ ! -e "$RUN/wg-edge1.name" ] && [ ! -e "$RUN/utun1.sock" ] || { echo 'fixed wrapper did not remove name/socket state after interface exit' >&2; exit 1; }
}

expect_fixed_wrapper_failure_paths() {
  local wrapper="$ROOT/root-libexec/wireguard/edge1/run-wireguard.sh"
  local config="$ROOT/root-libexec/wireguard-config/wg-edge1.conf"
  local timeout_wrapper="$ROOT/timeout-wrapper.sh"
  local status
  rm -f "$RUN/interface-gone.1"
  : > "$ROOT/fixed-wrapper-failure.log"
  set +e
  FAKE_RUN="$RUN" FAKE_WG_LOG="$ROOT/fixed-wrapper-failure.log" FAKE_WG_UP_FAIL=23 "$wrapper" wg-edge1 "$config" >/dev/null 2>&1
  status=$?
  set -e
  [ "$status" -ne 0 ] || { echo 'fixed wrapper treated wg-quick up failure as ready' >&2; exit 1; }
  [ "$(/usr/bin/grep -Fc "wg-quick up $config" "$ROOT/fixed-wrapper-failure.log")" -eq 1 ] || { echo 'fixed wrapper retried an up failure without a new launchd action' >&2; exit 1; }

  cp "$wrapper" "$timeout_wrapper"
  sed -i.bak 's/ready_attempt" -le 30/ready_attempt" -le 2/' "$timeout_wrapper"; rm -f "$timeout_wrapper.bak"
  : > "$ROOT/fixed-wrapper-timeout.log"
  set +e
  FAKE_RUN="$RUN" FAKE_WG_LOG="$ROOT/fixed-wrapper-timeout.log" FAKE_WG_NEVER_READY=1 FAKE_WG_UP_HOLD_SECONDS=5 "$timeout_wrapper" wg-edge1 "$config" >/dev/null 2>&1
  status=$?
  set -e
  [ "$status" -ne 0 ] || { echo 'fixed wrapper accepted a ready timeout' >&2; exit 1; }
  [ "$(/usr/bin/grep -Fc "wg-quick up $config" "$ROOT/fixed-wrapper-timeout.log")" -eq 1 ] || { echo 'fixed wrapper continued after ready timeout' >&2; exit 1; }
}

run_once() {
  FAKE_ACTION_LOG="$ACTION_LOG" FAKE_EVENT_LOG="$ROOT/events.log" FAKE_RUN="$RUN" FAKE_ROOT="$ROOT" FAKE_LAUNCHD_DIR="$ROOT/launchd" FAKE_SOURCE_WRAPPER="$ROOT/wrapper.sh" FAKE_SOURCE_CONFIG="$ROOT/config1" FAKE_HANDSHAKE="$1" FAKE_PROBE="$2" FAKE_KICKSTART="$3" FAKE_FRESH_UTUN="${4:-}" FAKE_VERIFY="${5:-75}" FAKE_KICKSTART_FAIL_EDGE="${6:-}" \
    bash "$ROOT/reconciler.sh" --once --manifest "$MANIFEST" --state-dir "$STATE" --wg-bin "$FAKE/wg" --probe-helper "$FAKE/probe" --network-stable-seconds 1 --stale-seconds 10 --action-timeout-seconds 2 --global-cooldown-seconds 1 --per-edge-cooldown-seconds 1 --action-window-seconds 10
}

make_manifest
make_probe_mapping

# The adapter receives metrics only through a dedicated SSH forced-command protocol. Its
# strict mapping protects the root reconciler from missing, duplicate or mismatched series.
now_probe="$(date +%s)"
write_probe_metrics 1 "$now_probe"
expect_adapter_status "$PROBE_MAPPING" 0
expect_adapter_status "$PROBE_MAPPING" 0 verify "$now_probe"
write_probe_metrics 0 "$(date +%s)"
expect_adapter_status "$PROBE_MAPPING" 1
write_probe_metrics 1 "$(date +%s)"
bash "$PROBE_INSTALLER" --dry-run --mapping "$PROBE_MAPPING" --runtime-manifest "$MANIFEST" --script-sha256 0000000000000000000000000000000000000000000000000000000000000000 >/dev/null

cp "$PROBE_MAPPING" "$ROOT/probe.duplicate-edge.map"
sed -i.bak 's/^edge8\t/edge7\t/' "$ROOT/probe.duplicate-edge.map"; rm -f "$ROOT/probe.duplicate-edge.map.bak"
expect_adapter_status "$ROOT/probe.duplicate-edge.map" 75
expect_installer_reject "$ROOT/probe.duplicate-edge.map"

cp "$PROBE_MAPPING" "$ROOT/probe.wrong-edge-set.map"
sed -i.bak 's/^edge8\t/edge9\t/' "$ROOT/probe.wrong-edge-set.map"; rm -f "$ROOT/probe.wrong-edge-set.map.bak"
expect_adapter_status "$ROOT/probe.wrong-edge-set.map" 75
expect_installer_reject "$ROOT/probe.wrong-edge-set.map"

awk 'BEGIN { FS=OFS="\t" } $1 == "edge2" { $3="node1"; $4="198.51.100.1" } { print }' "$PROBE_MAPPING" > "$ROOT/probe.duplicate-series.map"
expect_adapter_status "$ROOT/probe.duplicate-series.map" 75
expect_installer_reject "$ROOT/probe.duplicate-series.map"

sed '$d' "$PROBE_MAPPING" > "$ROOT/probe.missing.map"
expect_adapter_status "$ROOT/probe.missing.map" 75
expect_installer_reject "$ROOT/probe.missing.map"

cp "$PROBE_MAPPING" "$ROOT/probe.extraneous.map"
printf 'edge9\tprobe@monitoring.internal\tnode9\t198.51.100.9\t%s/known_hosts\t%s/probe_identity\n' "$ROOT" "$ROOT" >> "$ROOT/probe.extraneous.map"
expect_adapter_status "$ROOT/probe.extraneous.map" 75
expect_installer_reject "$ROOT/probe.extraneous.map"

awk 'BEGIN { FS=OFS="\t" } $1 == "edge1" { $2="http://untrusted.invalid" } { print }' "$PROBE_MAPPING" > "$ROOT/probe.http.map"
expect_adapter_status "$ROOT/probe.http.map" 75
expect_installer_reject "$ROOT/probe.http.map"

# The migration accepts a real legacy wrapper from a service-user-writable parent as
# hash-bound input, but it must never execute or copy that source into a root job.
printf 'PersistentKeepalive = 25\n' > "$ROOT/migration.conf"
cat > "$ROOT/migration-wrapper.sh" <<'EOF'
#!/usr/bin/env bash
printf 'legacy source wrapper executed\n' >> "${FAKE_SOURCE_EXECUTED:?}"
"${WG_QUICK_BIN:-/tmp/attacker-wg-quick}" up "$2"
EOF
chmod 700 "$ROOT/migration-wrapper.sh"
make_migration_manifest
expect_migrator_status accept
rm -f "$ROOT/source-wrapper-executed"
FAKE_SOURCE_EXECUTED="$ROOT/source-wrapper-executed" apply_migrator
[ ! -e "$ROOT/source-wrapper-executed" ] || { echo 'migrator executed the legacy source wrapper' >&2; exit 1; }
[ -f "$ROOT/root-libexec/wireguard/edge1/run-wireguard.sh" ] || { echo 'migrator did not generate the target wrapper' >&2; exit 1; }
[ -f "$ROOT/root-libexec/wireguard-config/wg-edge1.conf" ] || { echo 'migrator did not install the fixed config path' >&2; exit 1; }
[ "$($FAKE/stat -f '%Lp' "$ROOT/root-libexec/wireguard-config")" = 700 ] || { echo 'migrator did not restrict canonical config directory to mode 700' >&2; exit 1; }
[ "$($FAKE/stat -f '%Lp' "$ROOT/root-libexec/wireguard-config/wg-edge1.conf")" = 600 ] || { echo 'migrator did not restrict canonical config file to mode 600' >&2; exit 1; }
[ ! -e "$ROOT/etc/wireguard" ] || { echo 'migrator wrote to the retired /usr/local/etc target' >&2; exit 1; }
[ "$(FAKE_SOURCE_WRAPPER="$ROOT/migration-wrapper.sh" FAKE_SOURCE_CONFIG="$ROOT/migration.conf" "$FAKE/plutil" -extract ProgramArguments.0 raw -o - "$ROOT/launchd/com.example.wg.edge1.plist")" = "$ROOT/root-libexec/wireguard/edge1/run-wireguard.sh" ] || { echo 'migrator plist wrapper replacement did not persist' >&2; exit 1; }
[ "$(FAKE_SOURCE_WRAPPER="$ROOT/migration-wrapper.sh" FAKE_SOURCE_CONFIG="$ROOT/migration.conf" "$FAKE/plutil" -extract ProgramArguments.1 raw -o - "$ROOT/launchd/com.example.wg.edge1.plist")" = "$ROOT/root-libexec/wireguard-config/wg-edge1.conf" ] || { echo 'migrator plist config replacement did not persist' >&2; exit 1; }
rm -f "$RUN"/killed.* "$RUN"/interface-gone.*
expect_fixed_wrapper_lifecycle
expect_fixed_wrapper_failure_paths

# The installer must record the same root wrapper/config pair that the migrated launchd
# job exposes. The real reconciler then accepts the pair from launchctl print before it
# can issue a recovery action.
rm -rf "$ROOT/root-libexec-installer" "$ROOT/installer-state" "$ROOT/root-libexec/wireguard-config"
: > "$ACTION_LOG"
make_migration_manifest
set +e
FAKE_BOOTSTRAP_FAIL_LABEL='com.juhe-ai.wireguard-reconciler' apply_installer
installer_failure_status=$?
set -e
[ "$installer_failure_status" -ne 0 ] || { echo 'installer accepted a reconciler bootstrap failure' >&2; exit 1; }
[ ! -e "$ROOT/root-libexec-installer/wireguard/edge1/run-wireguard.sh" ] || { echo 'installer bootstrap failure retained migrated wrapper artifacts' >&2; exit 1; }
[ ! -e "$ROOT/root-libexec/wireguard-config/wg-edge1.conf" ] || { echo 'installer bootstrap failure retained migrated config' >&2; exit 1; }
: > "$ACTION_LOG"
make_migration_manifest
apply_installer
[ -f "$INSTALLED_MANIFEST" ] || { echo 'installer did not write a runtime manifest' >&2; exit 1; }
installed_wrapper="$ROOT/root-libexec-installer/wireguard/edge1/run-wireguard.sh"
installed_config="$ROOT/root-libexec/wireguard-config/wg-edge1.conf"
[ "$installed_config" = "$ROOT/root-libexec/wireguard-config/wg-edge1.conf" ] || { echo 'custom installer root changed the canonical config path' >&2; exit 1; }
installed_manifest_line="$(/usr/bin/awk -F '\t' '$1 == "edge1" { print $4 "|" $5; exit }' "$INSTALLED_MANIFEST")"
[ "$installed_manifest_line" = "$installed_config|$installed_wrapper" ] || { echo 'installer runtime manifest does not bind migrated wrapper/config paths' >&2; exit 1; }
[ "$(FAKE_SOURCE_WRAPPER="$ROOT/migration-wrapper.sh" FAKE_SOURCE_CONFIG="$ROOT/migration.conf" "$FAKE/plutil" -extract ProgramArguments.0 raw -o - "$ROOT/launchd/com.example.wg.edge1.plist")" = "$installed_wrapper" ] || { echo 'installer migration plist wrapper replacement did not persist' >&2; exit 1; }
[ "$(FAKE_SOURCE_WRAPPER="$ROOT/migration-wrapper.sh" FAKE_SOURCE_CONFIG="$ROOT/migration.conf" "$FAKE/plutil" -extract ProgramArguments.1 raw -o - "$ROOT/launchd/com.example.wg.edge1.plist")" = "$installed_config" ] || { echo 'installer migration plist config replacement did not persist' >&2; exit 1; }
rm -rf "$STATE"; mkdir "$STATE"; : > "$ACTION_LOG"; make_manifest
FAKE_ACTION_LOG="$ACTION_LOG" FAKE_EVENT_LOG="$ROOT/events.log" FAKE_RUN="$RUN" FAKE_ROOT="$ROOT" FAKE_LAUNCHD_DIR="$ROOT/launchd" FAKE_SOURCE_WRAPPER="$ROOT/migration-wrapper.sh" FAKE_SOURCE_CONFIG="$ROOT/migration.conf" FAKE_HANDSHAKE=0 FAKE_PROBE=1 FAKE_KICKSTART=0 FAKE_VERIFY=0 \
  bash "$ROOT/root-libexec-installer/wireguard-reconciler.sh" --once --manifest "$INSTALLED_MANIFEST" --state-dir "$STATE" --wg-bin "$FAKE/wg" --probe-helper "$FAKE/probe" --network-stable-seconds 1 --stale-seconds 10 --action-timeout-seconds 1 --global-cooldown-seconds 1 --per-edge-cooldown-seconds 1 --action-window-seconds 10 || true
sleep 1
FAKE_ACTION_LOG="$ACTION_LOG" FAKE_EVENT_LOG="$ROOT/events.log" FAKE_RUN="$RUN" FAKE_ROOT="$ROOT" FAKE_LAUNCHD_DIR="$ROOT/launchd" FAKE_SOURCE_WRAPPER="$ROOT/migration-wrapper.sh" FAKE_SOURCE_CONFIG="$ROOT/migration.conf" FAKE_HANDSHAKE=0 FAKE_PROBE=1 FAKE_KICKSTART=0 FAKE_VERIFY=0 \
  bash "$ROOT/root-libexec-installer/wireguard-reconciler.sh" --once --manifest "$INSTALLED_MANIFEST" --state-dir "$STATE" --wg-bin "$FAKE/wg" --probe-helper "$FAKE/probe" --network-stable-seconds 1 --stale-seconds 10 --action-timeout-seconds 1 --global-cooldown-seconds 1 --per-edge-cooldown-seconds 1 --action-window-seconds 10 || true
sleep 1
FAKE_ACTION_LOG="$ACTION_LOG" FAKE_EVENT_LOG="$ROOT/events.log" FAKE_RUN="$RUN" FAKE_ROOT="$ROOT" FAKE_LAUNCHD_DIR="$ROOT/launchd" FAKE_SOURCE_WRAPPER="$ROOT/migration-wrapper.sh" FAKE_SOURCE_CONFIG="$ROOT/migration.conf" FAKE_HANDSHAKE=0 FAKE_PROBE=1 FAKE_KICKSTART=0 FAKE_VERIFY=0 \
  bash "$ROOT/root-libexec-installer/wireguard-reconciler.sh" --once --manifest "$INSTALLED_MANIFEST" --state-dir "$STATE" --wg-bin "$FAKE/wg" --probe-helper "$FAKE/probe" --network-stable-seconds 1 --stale-seconds 10 --action-timeout-seconds 1 --global-cooldown-seconds 1 --per-edge-cooldown-seconds 1 --action-window-seconds 10 || true
/usr/bin/grep -Fq "ProgramArguments = ( $installed_wrapper $installed_config )" "$ACTION_LOG" || { echo 'reconciler launchctl print did not expose the installer wrapper/config pair' >&2; exit 1; }
/usr/bin/grep -Fxq 'kickstart kickstart -k system/com.example.wg.edge1' "$ACTION_LOG" || { echo 'reconciler did not issue the exact canary kickstart argv after accepting the installer wrapper/config pair' >&2; exit 1; }

# Hooks remain a source-config rejection even though the source wrapper is intentionally
# treated as migration metadata rather than executable code.
printf 'PersistentKeepalive = 25\nPostUp = /bin/true\n' > "$ROOT/migration.conf"
make_migration_manifest
expect_migrator_status wireguard-hook
printf 'PersistentKeepalive = 25\n' > "$ROOT/migration.conf"
printf '#!/bin/sh\nsource /tmp/untrusted.sh\nexec /tmp/untrusted-helper "$1"\n' > "$ROOT/migration-wrapper.sh"
make_migration_manifest
expect_migrator_status accept

# One fresh edge means the real script must not invoke launchctl.
: > "$ACTION_LOG"
: > "$ROOT/events.log"
run_once 0 1 0 utun8 || true
run_once 0 1 0 utun8 || true
[ ! -s "$ACTION_LOG" ] || { echo 'partial stale invoked launchctl' >&2; exit 1; }
/usr/bin/grep -Fq 'gate=partial-or-unconfirmed-stale' "$ROOT/events.log" || { echo 'partial stale did not record the no-action gate' >&2; exit 1; }

# Unknown external evidence must suppress all action.
rm -rf "$STATE"; mkdir "$STATE"; : > "$ACTION_LOG"; make_manifest
run_once 0 75 0 || true
run_once 0 75 0 || true
[ ! -s "$ACTION_LOG" ] || { echo 'probe unknown invoked launchctl' >&2; exit 1; }

# A just-observed network state is deliberately not settled yet, even with every
# WireGuard edge stale and an externally failed probe.
rm -rf "$STATE"; mkdir "$STATE"; : > "$ACTION_LOG"; make_manifest
run_once 0 1 0 || true
[ ! -s "$ACTION_LOG" ] || { echo 'network settling invoked launchctl' >&2; exit 1; }

# A maintenance lock must suppress a fully stale batch.
rm -rf "$STATE"; mkdir "$STATE"; : > "$ACTION_LOG"; make_manifest; touch "$ROOT/maintenance.lock"
FAKE_ACTION_LOG="$ACTION_LOG" FAKE_EVENT_LOG="$ROOT/events.log" FAKE_RUN="$RUN" FAKE_ROOT="$ROOT" FAKE_LAUNCHD_DIR="$ROOT/launchd" FAKE_SOURCE_WRAPPER="$ROOT/wrapper.sh" FAKE_SOURCE_CONFIG="$ROOT/config1" FAKE_HANDSHAKE=0 FAKE_PROBE=1 FAKE_KICKSTART=0 \
  bash "$ROOT/reconciler.sh" --once --manifest "$MANIFEST" --state-dir "$STATE" --wg-bin "$FAKE/wg" --probe-helper "$FAKE/probe" --maintenance-lock "$ROOT/maintenance.lock" --network-stable-seconds 1 --stale-seconds 10 --action-timeout-seconds 1 || true
[ ! -s "$ACTION_LOG" ] || { echo 'maintenance lock invoked launchctl' >&2; exit 1; }
rm -f "$ROOT/maintenance.lock"

# The release lock is an independent gate and must have the same zero-action
# behavior as the maintenance lock.
rm -rf "$STATE"; mkdir "$STATE"; : > "$ACTION_LOG"; make_manifest; touch "$ROOT/release.lock"
FAKE_ACTION_LOG="$ACTION_LOG" FAKE_EVENT_LOG="$ROOT/events.log" FAKE_RUN="$RUN" FAKE_ROOT="$ROOT" FAKE_LAUNCHD_DIR="$ROOT/launchd" FAKE_SOURCE_WRAPPER="$ROOT/wrapper.sh" FAKE_SOURCE_CONFIG="$ROOT/config1" FAKE_HANDSHAKE=0 FAKE_PROBE=1 FAKE_KICKSTART=0 \
  bash "$ROOT/reconciler.sh" --once --manifest "$MANIFEST" --state-dir "$STATE" --wg-bin "$FAKE/wg" --probe-helper "$FAKE/probe" --release-lock "$ROOT/release.lock" --network-stable-seconds 1 --stale-seconds 10 --action-timeout-seconds 1 || true
[ ! -s "$ACTION_LOG" ] || { echo 'release lock invoked launchctl' >&2; exit 1; }
rm -f "$ROOT/release.lock"

# A stale observation from an old disabled/delayed run cannot become the first half of a
# new confirmation. The max-gap guard must leave this batch at zero recovery actions.
rm -rf "$STATE"; mkdir "$STATE"; : > "$ACTION_LOG"; make_manifest
run_once 0 1 0 || true
sleep 1
old_observed_at=$(( $(date +%s) - 1000 ))
for edge in 1 2 3 4 5 6 7 8; do printf '1\t1\t0\t%s\n' "$old_observed_at" > "$STATE/edge.edge$edge.state"; done
run_once 0 1 0 || true
[ ! -s "$ACTION_LOG" ] || { echo 'expired stale sample chain invoked launchctl' >&2; exit 1; }

# Two stale samples after network settling reach only the canary. Its forced kickstart
# failure must prevent every remaining edge from being touched.
rm -rf "$STATE"; mkdir "$STATE"; : > "$ACTION_LOG"; make_manifest
run_once 0 1 1 || true
sleep 1
run_once 0 1 1 || true
run_once 0 1 1 || true
grep -Fq 'com.example.wg.edge1' "$ACTION_LOG" || { echo 'canary failure did not reach launchctl' >&2; cat "$ROOT/events.log" >&2; cat "$ACTION_LOG" >&2; exit 1; }
if grep -Eq 'edge[2-8]' "$ACTION_LOG"; then echo 'canary failure touched a non-canary edge' >&2; exit 1; fi

# With post-action external evidence, the first action must be canary edge1 and only then
# can the real reconciler issue any action for another exact manifest edge. The fake
# launchctl itself rejects a non-canary kickstart until the fake verifier recorded edge1.
rm -rf "$STATE"; mkdir "$STATE"; : > "$ACTION_LOG"; make_manifest
run_once 0 1 0 '' 0 || true
sleep 1
run_once 0 1 0 '' 0 || true
run_once 0 1 0 '' 0 || true
first_edge="$(sed -n '1s/.*edge\([0-9][0-9]*\).*/\1/p' "$ACTION_LOG")"
[ "$first_edge" = 1 ] || { echo 'first recovery action was not canary edge1' >&2; exit 1; }
[ -f "$RUN/verified.edge1" ] || { echo 'canary did not complete independent verification' >&2; exit 1; }
canary_kickstart_line="$(grep -n 'kickstart .*edge1' "$ACTION_LOG" | sed -n '1s/:.*//p')"
first_other_line="$(grep -n -E 'edge[2-8]' "$ACTION_LOG" | sed -n '1s/:.*//p')"
[ -n "$canary_kickstart_line" ] && [ -n "$first_other_line" ] && [ "$canary_kickstart_line" -lt "$first_other_line" ] || { echo 'non-canary action occurred before canary kickstart' >&2; exit 1; }
if grep -Fq 'kickstart-before-canary-verify' "$ACTION_LOG"; then echo 'non-canary kickstart bypassed canary verification' >&2; exit 1; fi
for edge in 2 3 4 5 6 7 8; do
  grep -Eq "kickstart .*edge$edge" "$ACTION_LOG" || { echo "successful canary did not reconcile edge$edge" >&2; cat "$ROOT/events.log" >&2; cat "$ACTION_LOG" >&2; exit 1; }
done

# A post-canary failure is terminal. Edge2 fails at kickstart and the batch must leave
# edge3 through edge8 untouched instead of reporting an incorrect completed result.
rm -rf "$STATE"; mkdir "$STATE"; : > "$ACTION_LOG"; make_manifest
run_once 0 1 0 '' 0 2 || true
sleep 1
run_once 0 1 0 '' 0 2 || true
run_once 0 1 0 '' 0 2 || true
grep -Eq 'kickstart .*edge1' "$ACTION_LOG" || { echo 'non-canary failure test did not recover canary' >&2; exit 1; }
grep -Eq 'kickstart .*edge2' "$ACTION_LOG" || { echo 'non-canary failure did not reach edge2' >&2; exit 1; }
if grep -Eq 'kickstart .*edge[3-8]' "$ACTION_LOG"; then echo 'non-canary failure restarted a later edge' >&2; cat "$ACTION_LOG" >&2; exit 1; fi
grep -Fq 'result=non-canary-failed edge=edge2 remaining=skipped' "$ROOT/events.log" || { echo 'non-canary failure was not recorded as terminal' >&2; cat "$ROOT/events.log" >&2; exit 1; }
printf 'WireGuard reconciler fake-command harness passed\n'
