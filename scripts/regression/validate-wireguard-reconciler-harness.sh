#!/usr/bin/env bash
# Executes a rewritten temporary copy of the real reconciler against deterministic command
# stubs. No production path, launchd domain, WireGuard interface or network is touched.
set -euo pipefail

REPO_ROOT="${1:?repository root is required}"
SOURCE="$REPO_ROOT/docs/deploy/macos/operations/wireguard-reconciler.sh"
# macOS login sessions commonly expose TMPDIR below /var, which is a symlink to
# /private/var. Keep Darwin fixtures under the physical private temp root.
HARNESS_TMP_BASE="${JUHE_WG_HARNESS_TMPDIR:-${TMPDIR:-/tmp}}"
if [ "$(uname -s)" = Darwin ] && [ -d /private/tmp ]; then
  HARNESS_TMP_BASE=/private/tmp
fi
ROOT="$(mktemp -d "$HARNESS_TMP_BASE/juhe-wg-reconciler.XXXXXX")"
FAKE="$ROOT/fake"
RUN="$ROOT/run"
STATE="$ROOT/state"
MANIFEST="$ROOT/manifest"
ACTION_LOG="$ROOT/actions.log"
MIGRATION_MANIFEST="$ROOT/migration.manifest"
REUSE_MANIFEST="$ROOT/reuse.manifest"
MIGRATOR="$ROOT/migrator.sh"
OPERATIONS="$ROOT/operations"
INSTALLER="$OPERATIONS/install-wireguard-reconciler.sh"
INSTALLED_MANIFEST="$ROOT/root-libexec-installer/wireguard-reconciler.manifest"
INSTALLED_STATE_DIR="$ROOT/root-libexec-installer/wireguard-reconciler-state"
cleanup_harness() {
  if [ "${JUHE_WG_HARNESS_KEEP_ROOT:-0}" = 1 ]; then
    printf 'WireGuard harness root retained for diagnostics: %s\n' "$ROOT" >&2
  else
    rm -rf "$ROOT" >/dev/null 2>&1 || true
  fi
}
trap cleanup_harness EXIT HUP INT TERM
mkdir -p "$FAKE" "$RUN" "$STATE" "$OPERATIONS"
mkdir -p "$ROOT/launchd-loaded"
: > "$ACTION_LOG"

cat > "$FAKE/stat" <<'EOF'
#!/usr/bin/env bash
case "$2" in
  %u) printf '0\n' ;;
  %Su:%Sg) printf 'root:wheel\n' ;;
  %Lp) case "${3:-}" in */wireguard-bin/wg|*/wireguard-bin/wg-quick|*/fake/wg|*/fake/wg-quick) printf '755\n' ;; *known_hosts|*probe_identity|*.map|*/manifest|*.conf) printf '600\n' ;; *) printf '700\n' ;; esac ;;
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
ready_for_interface() {
  local iface="$1" number
  if [ "${FAKE_WG_AMBIGUOUS_PEER:-0}" -ne 0 ] && [ "$iface" = utun2 ] && [ -f "$FAKE_RUN/wg-edge1.ready" ]; then
    return 0
  fi
  number="$(printf '%s' "$iface" | sed -n 's/^utun\([0-9][0-9]*\)$/\1/p')"
  [ -n "$number" ] && [ -f "$FAKE_RUN/wg-edge$number.ready" ]
}
if [ "$1" = show ] && [ "${2:-}" = interfaces ]; then
  for ready in "$FAKE_RUN"/wg-edge*.ready; do
    [ -f "$ready" ] || continue
    edge_name="$(basename "$ready" .ready)"
    number="$(printf '%s' "$edge_name" | sed -n 's/^wg-edge\([0-9][0-9]*\)$/\1/p')"
    [ -n "$number" ] && printf 'utun%s\n' "$number"
  done
  if [ "${FAKE_WG_AMBIGUOUS_PEER:-0}" -ne 0 ] && [ -f "$FAKE_RUN/wg-edge1.ready" ]; then printf 'utun2\n'; fi
  exit 0
fi
if [ "$1" = show ] && [ "${3:-}" = peers ]; then
  [ -f "$FAKE_RUN/interface-gone.$edge" ] && exit 1
  ready_for_interface "${2:-}" || exit 1
  printf 'fake-peer-key\n'
  exit 0
fi
if [ "$1" = show ] && [ "$3" = latest-handshakes ]; then
  if { [ -n "${FAKE_FRESH_UTUN:-}" ] && [ "$2" = "$FAKE_FRESH_UTUN" ]; } || [ -f "$FAKE_RUN/killed.$edge" ]; then printf 'peer %s\n' "$(date +%s)"; else printf 'peer %s\n' "${FAKE_HANDSHAKE:-0}"; fi
  exit 0
fi
if [ "$1" = show ] && [ "$3" = transfer ]; then if [ -f "$FAKE_RUN/killed.$edge" ]; then printf 'peer 200 200\n'; else printf 'peer 100 100\n'; fi; exit 0; fi
if [ "$1" = show ] && { [ -f "$FAKE_RUN/killed.$edge" ] || [ -f "$FAKE_RUN/interface-gone.$edge" ]; }; then exit 1; fi
if [ "$1" = show ]; then ready_for_interface "${2:-}" || exit 1; exit 0; fi
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
    rm -f "$FAKE_RUN/$edge.ready"
    exit 0
    ;;
  up)
    if [ "${FAKE_WG_UP_FAIL:-0}" -ne 0 ]; then exit "${FAKE_WG_UP_FAIL}"; fi
    if [ "${FAKE_WG_READY_DELAY_SECONDS:-0}" -gt 0 ]; then sleep "$FAKE_WG_READY_DELAY_SECONDS"; fi
    if [ "${FAKE_WG_NEVER_READY:-0}" -eq 0 ]; then
      printf '%s\n' "$actual_interface" > "$FAKE_RUN/$edge.name"
      : > "$FAKE_RUN/$edge.ready"
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
has_value() {
  local slot="$1" plist="$2"
  [ -f "$plist" ] || return 1
  # ProgramArguments is a contiguous plist array. Treat any fake index beyond
  # arg3 as presence of the fourth array entry so a sparse text fixture cannot
  # hide an extra argument from the production existence check.
  if [ "$slot" = 4 ]; then
    /usr/bin/awk -F '\t' '$1 ~ /^[0-9]+$/ && $1 >= 4 { found=1; exit } END { exit !found }' "$plist"
    return $?
  fi
  /usr/bin/awk -F '\t' -v slot="$slot" '$1 == slot { found=1; exit } END { exit !found }' "$plist"
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
  '-lint') exit 0 ;;
  '-extract')
    key="${2#ProgramArguments.}"
    case "$2" in
      ProgramArguments.*) index="$key" ;;
      EnvironmentVariables.PATH) index=PATH ;;
      *) exit 64 ;;
    esac
    plist="${6:?missing plist path}"
    value="$(value_for "$index" "$plist")"
    if ! has_value "$index" "$plist"; then
      case "$index" in
        0) value='/usr/local/bin/bash' ;;
        1) value="${FAKE_SOURCE_WRAPPER:?}" ;;
        2) edge="$(printf '%s' "${6:?missing plist path}" | sed -n 's/.*edge\([0-9][0-9]*\)\.plist/\1/p')"; value="wg-edge${edge:-1}" ;;
        3)
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
    if has_value "$index" "$plist"; then
      exit 0
    fi
    case "$2" in
      ProgramArguments.*)
        case "$index" in
          0|1|2|3) exit 0 ;;
          *) exit 1 ;;
        esac
        ;;
      *) exit 1 ;;
    esac
    ;;
  '-replace')
    index="${2#ProgramArguments.}"
    [ "${3:-}" = -string ] || exit 64
    replace_value "$index" "${4:?missing replacement value}" "${5:?missing plist path}"
    exit 0 ;;
  *) exit 0 ;;
esac
EOF
cat > "$FAKE/PlistBuddy" <<'EOF'
#!/usr/bin/env bash
set -eu

[ "${1:-}" = -c ] || exit 64
command="${2:?missing command}"
plist="${3:?missing plist path}"

replace_value() {
  local slot="$1" value="$2" temp
  temp="$plist.plistbuddy.$$"
  /usr/bin/awk -F '\t' -v slot="$slot" '$1 != slot { print }' "$plist" > "$temp"
  printf '%s\t%s\n' "$slot" "$value" >> "$temp"
  /bin/mv "$temp" "$plist"
}

case "$command" in
  'Print :EnvironmentVariables') /usr/bin/awk -F '\t' '$1 == "PATH" { found=1; exit } END { exit !found }' "$plist" ;;
  'Add :EnvironmentVariables dict') ;;
  'Delete :EnvironmentVariables:PATH')
    temp="$plist.plistbuddy.$$"
    /usr/bin/awk -F '\t' '$1 != "PATH" { print }' "$plist" > "$temp"
    /bin/mv "$temp" "$plist"
    ;;
  'Delete :ProgramArguments')
    temp="$plist.plistbuddy.$$"
    /usr/bin/awk -F '\t' '$1 !~ /^[0-9]+$/ { print }' "$plist" > "$temp"
    /bin/mv "$temp" "$plist"
    ;;
  'Add :ProgramArguments array') ;;
  'Add :ProgramArguments:'*)
    suffix="${command#Add :ProgramArguments:}"
    slot="${suffix%% *}"
    value="${suffix#* string }"
    [ "$value" != "$suffix" ] || exit 64
    replace_value "$slot" "$value"
    ;;
  'Add :EnvironmentVariables:PATH string '*)
    value="${command#Add :EnvironmentVariables:PATH string }"
    replace_value PATH "$value"
    ;;
  *) exit 64 ;;
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
    [ -f "$plist" ] && [ -f "${FAKE_LAUNCHD_LOADED_DIR:?}/$label" ] || exit 113
    job_wrapper="$(plist_value 1 "$plist")"
    job_config="$(plist_value 3 "$plist")"
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
    job="ProgramArguments = ( /usr/local/bin/bash $job_wrapper wg-edge$edge $job_config )"
    printf 'jobdump %s\n' "$job" >> "$FAKE_ACTION_LOG"
    printf '%s\n' "$job"
    exit 0
    ;;
  kill) edge="$(printf '%s' "$last" | sed -n 's/.*edge\([0-9][0-9]*\).*/\1/p')"; [ -n "$edge" ] || edge=1; rm -f "$FAKE_RUN/wg$edge.name" "$FAKE_RUN/wg-edge$edge.name"; touch "$FAKE_RUN/killed.$edge"; exit 0 ;;
  bootout) bootout_label="${last##*/}"; bootout_label="${bootout_label%.plist}"; rm -f "${FAKE_LAUNCHD_LOADED_DIR:?}/$bootout_label"; exit 0 ;;
  kickstart) edge="$(printf '%s' "$last" | sed -n 's/.*edge\([0-9][0-9]*\).*/\1/p')"; [ -n "$edge" ] || edge=1; if [ "${FAKE_LAUNCHD_ALLOW_ALL:-0}" -ne 1 ] && [ "$edge" -gt 1 ] && [ ! -f "$FAKE_RUN/verified.edge1" ]; then printf 'kickstart-before-canary-verify edge%s\n' "$edge" >> "$FAKE_ACTION_LOG"; exit 91; fi; if [ "${FAKE_KICKSTART_FAIL_EDGE:-}" = "$edge" ]; then exit 91; fi; [ "${FAKE_KICKSTART:-0}" -eq 0 ] && { printf 'utun%s\n' "$edge" > "$FAKE_RUN/wg$edge.name"; printf 'utun%s\n' "$edge" > "$FAKE_RUN/wg-edge$edge.name"; }; exit "${FAKE_KICKSTART:-0}" ;;
  bootstrap) bootstrap_label="${last##*/}"; bootstrap_label="${bootstrap_label%.plist}"; failure_marker="$FAKE_RUN/bootstrap-failed.$bootstrap_label"; if [ "${FAKE_BOOTSTRAP_FAIL_LABEL:-}" = "$bootstrap_label" ] && [ ! -e "$failure_marker" ]; then : > "$failure_marker"; exit 91; fi; : > "${FAKE_LAUNCHD_LOADED_DIR:?}/$bootstrap_label"; exit 0 ;;
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
  -e "s|/usr/libexec/PlistBuddy|$FAKE/PlistBuddy|g" \
  -e "s|/usr/bin/logger|$FAKE/logger|g" \
  -e "s|/Library/LaunchDaemons|$ROOT/launchd|g" \
  -e "s|/bin/launchctl|$FAKE/launchctl|g" \
  -e "s|/var/run/wireguard|$RUN|g" \
  -e 's/^FRESH_CONFIRMATIONS=3$/FRESH_CONFIRMATIONS=1/' \
  "$SOURCE" > "$ROOT/reconciler.sh"
chmod 700 "$ROOT/reconciler.sh"

mkdir -p "$ROOT/launchd"
sed \
  -e "s|/usr/bin/stat|$FAKE/stat|g" \
  -e "s|/usr/bin/id|$FAKE/id|g" \
  -e "s|/usr/bin/plutil|$FAKE/plutil|g" \
  -e "s|/usr/libexec/PlistBuddy|$FAKE/PlistBuddy|g" \
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
  -e "s|/usr/libexec/PlistBuddy|$FAKE/PlistBuddy|g" \
  -e "s|/usr/bin/shasum|$FAKE/shasum|g" \
  -e "s|/usr/sbin/chown|$FAKE/chown|g" \
  -e "s|/usr/local/bin/bash|$FAKE/bash|g" \
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
    : > "$ROOT/launchd-loaded/com.example.wg.edge$index"
  done
}

make_migration_manifest() {
  local config_hash wrapper_hash index
  config_hash="$(sha256_file "$ROOT/migration.conf")"
  wrapper_hash="$(sha256_file "$ROOT/migration-wrapper.sh")"
  : > "$MIGRATION_MANIFEST"
  for index in 1 2 3 4 5 6 7 8; do
    printf '0\t%s/bash\n1\t%s/migration-wrapper.sh\n2\twg-edge%s\n3\t%s/migration.conf\n' "$FAKE" "$ROOT" "$index" "$ROOT" > "$ROOT/launchd/com.example.wg.edge$index.plist"
    : > "$ROOT/launchd-loaded/com.example.wg.edge$index"
    printf 'edge%s\twg-edge%s\tcom.example.wg.edge%s\t%s/migration.conf\t%s/migration-wrapper.sh\t10.0.%s.2\t%s\t%s\n' "$index" "$index" "$index" "$ROOT" "$ROOT" "$index" "$config_hash" "$wrapper_hash" >> "$MIGRATION_MANIFEST"
  done
}

make_reuse_manifest() {
  local config wrapper config_hash wrapper_hash index
  : > "$REUSE_MANIFEST"
  for index in 1 2 3 4 5 6 7 8; do
    config="$ROOT/root-libexec/wireguard-config/wg-edge$index.conf"
    wrapper="$ROOT/root-libexec-installer/wireguard/edge$index/run-wireguard.sh"
    config_hash="$(sha256_file "$config")"
    wrapper_hash="$(sha256_file "$wrapper")"
    printf 'edge%s\twg-edge%s\tcom.example.wg.edge%s\t%s\t%s\t10.0.%s.2\t%s\t%s\n' "$index" "$index" "$index" "$config" "$wrapper" "$index" "$config_hash" "$wrapper_hash" >> "$REUSE_MANIFEST"
  done
}

prepare_default_wireguard_bins() {
  mkdir -p "$ROOT/root-libexec/wireguard-bin"
  cp "$FAKE/wg" "$ROOT/root-libexec/wireguard-bin/wg"
  cp "$FAKE/wg-quick" "$ROOT/root-libexec/wireguard-bin/wg-quick"
  chmod 755 "$ROOT/root-libexec/wireguard-bin/wg" "$ROOT/root-libexec/wireguard-bin/wg-quick"
}

restore_installed_plists() {
  local index
  for index in 1 2 3 4 5 6 7 8; do
    printf '0\t%s/bash\n1\t%s/wireguard/edge%s/run-wireguard.sh\n2\twg-edge%s\n3\t%s/wg-edge%s.conf\n' "$FAKE" "$ROOT/root-libexec-installer" "$index" "$index" "$ROOT/root-libexec/wireguard-config" "$index" > "$ROOT/launchd/com.example.wg.edge$index.plist"
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
  FAKE_SOURCE_WRAPPER="$ROOT/migration-wrapper.sh" FAKE_SOURCE_CONFIG="$ROOT/migration.conf" FAKE_LAUNCHD_DIR="$ROOT/launchd" FAKE_LAUNCHD_LOADED_DIR="$ROOT/launchd-loaded" FAKE_ACTION_LOG="$ACTION_LOG" bash "$MIGRATOR" --dry-run --manifest "$MIGRATION_MANIFEST" --install-dir "$ROOT/root-libexec" >"$ROOT/migrator-status.out" 2>"$ROOT/migrator-status.err"
  local status=$?
  set -e
  if [ "$expected" = accept ]; then
    [ "$status" -eq 0 ] || { cat "$ROOT/migrator-status.err" >&2; echo 'migrator rejected fixed minimal wrapper unexpectedly' >&2; exit 1; }
  else
    if [ "$status" -eq 0 ]; then
      echo "migrator accepted unsafe source: $expected" >&2
      exit 1
    fi
    printf 'migrator rejected unsafe source=%s status=%s\n' "$expected" "$status" >&2
    cat "$ROOT/migrator-status.err" >&2
  fi
}

apply_migrator() {
  FAKE_SOURCE_WRAPPER="$ROOT/migration-wrapper.sh" FAKE_SOURCE_CONFIG="$ROOT/migration.conf" FAKE_LAUNCHD_DIR="$ROOT/launchd" FAKE_LAUNCHD_LOADED_DIR="$ROOT/launchd-loaded" FAKE_ACTION_LOG="$ACTION_LOG" FAKE_RUN="$RUN" FAKE_LAUNCHD_ALLOW_ALL=1 FAKE_BOOTSTRAP_FAIL_LABEL="${FAKE_BOOTSTRAP_FAIL_LABEL:-}" \
    bash "$MIGRATOR" --apply --manifest "$MIGRATION_MANIFEST" --install-dir "$ROOT/root-libexec"
}

apply_installer() {
  local manifest="${1:-$MIGRATION_MANIFEST}" reuse_existing="${2:-0}" helper_hash migrator_hash runtime_path
  helper_hash="$(sha256_file "$OPERATIONS/wireguard-reconciler.sh")"
  migrator_hash="$(sha256_file "$OPERATIONS/migrate-wireguard-root-wrappers.sh")"
  runtime_path="$FAKE:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
  case "$helper_hash" in
    [0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]) ;;
    *) printf 'invalid harness helper SHA-256: length=%s value=%q\n' "${#helper_hash}" "$helper_hash" >&2; return 1 ;;
  esac
  set -- --apply --manifest "$manifest" --install-dir "$ROOT/root-libexec-installer" --wg-bin "$FAKE/wg" --wg-quick-bin "$FAKE/wg-quick" --runtime-path "$runtime_path" --probe-helper "$FAKE/probe" --script-sha256 "$helper_hash" --migrator-sha256 "$migrator_hash"
  if [ "$reuse_existing" = 1 ]; then set -- "$@" --reuse-existing-root-wrappers; fi
  FAKE_SOURCE_WRAPPER="$ROOT/migration-wrapper.sh" FAKE_SOURCE_CONFIG="$ROOT/migration.conf" FAKE_LAUNCHD_DIR="$ROOT/launchd" FAKE_LAUNCHD_LOADED_DIR="$ROOT/launchd-loaded" FAKE_ACTION_LOG="$ACTION_LOG" FAKE_RUN="$RUN" FAKE_LAUNCHD_ALLOW_ALL=1 \
    bash "$INSTALLER" "$@"
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
  local peer_failure_wrapper="$ROOT/peer-failure-wrapper.sh"
  local ambiguous_peer_wrapper="$ROOT/ambiguous-peer-wrapper.sh"
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

  # A peer query failure (including an interface that disappeared) is unknown, not
  # evidence of readiness. The wrapper must fail closed without selecting a stale
  # textual mapping.
  cp "$wrapper" "$peer_failure_wrapper"
  sed -i.bak 's/ready_attempt" -le 30/ready_attempt" -le 2/' "$peer_failure_wrapper"; rm -f "$peer_failure_wrapper.bak"
  touch "$RUN/interface-gone.1"
  set +e
  FAKE_RUN="$RUN" FAKE_WG_NEVER_READY=1 FAKE_WG_UP_HOLD_SECONDS=5 "$peer_failure_wrapper" wg-edge1 "$config" >/dev/null 2>&1
  status=$?
  set -e
  rm -f "$RUN/interface-gone.1"
  [ "$status" -ne 0 ] || { echo 'fixed wrapper treated a failed peer query as a ready interface' >&2; exit 1; }

  # Reusing a utun number is expected, but two live utun devices that expose
  # the same peer key cannot be attributed safely. The wrapper must fail closed.
  cp "$wrapper" "$ambiguous_peer_wrapper"
  sed -i.bak 's/ready_attempt" -le 30/ready_attempt" -le 2/' "$ambiguous_peer_wrapper"; rm -f "$ambiguous_peer_wrapper.bak"
  set +e
  FAKE_RUN="$RUN" FAKE_WG_AMBIGUOUS_PEER=1 FAKE_WG_UP_HOLD_SECONDS=5 "$ambiguous_peer_wrapper" wg-edge1 "$config" >/dev/null 2>&1
  status=$?
  set -e
  [ "$status" -ne 0 ] || { echo 'fixed wrapper accepted an ambiguous peer public key' >&2; exit 1; }
}

run_once() (
  local handshake="$1" probe="$2" kickstart="$3" fresh_utun="${4:-}" verify="${5:-75}" kickstart_fail_edge="${6:-}" lock_kind="${7:-}" lock_path="${8:-}"
  # Recovery fixtures run against the installer-owned wrapper/config pair. The
  # ordinary MANIFEST remains dedicated to the pre-install adapter checks above.
  set -- --once --manifest "$INSTALLED_MANIFEST" --state-dir "$STATE" --wg-bin "$FAKE/wg" --probe-helper "$FAKE/probe" --network-stable-seconds 1 --stale-seconds 10 --action-timeout-seconds 2 --global-cooldown-seconds 1 --per-edge-cooldown-seconds 1 --action-window-seconds 10
  case "$lock_kind" in
    '') ;;
    maintenance) set -- "$@" --maintenance-lock "$lock_path" ;;
    release) set -- "$@" --release-lock "$lock_path" ;;
    *) echo "run_once: unsupported lock kind: $lock_kind" >&2; return 2 ;;
  esac
  FAKE_ACTION_LOG="$ACTION_LOG" FAKE_EVENT_LOG="$ROOT/events.log" FAKE_RUN="$RUN" FAKE_ROOT="$ROOT" FAKE_LAUNCHD_DIR="$ROOT/launchd" FAKE_LAUNCHD_LOADED_DIR="$ROOT/launchd-loaded" FAKE_SOURCE_WRAPPER="$ROOT/wrapper.sh" FAKE_SOURCE_CONFIG="$ROOT/config1" FAKE_HANDSHAKE="$handshake" FAKE_PROBE="$probe" FAKE_KICKSTART="$kickstart" FAKE_FRESH_UTUN="$fresh_utun" FAKE_VERIFY="$verify" FAKE_KICKSTART_FAIL_EDGE="$kickstart_fail_edge" \
    bash "$ROOT/reconciler.sh" "$@" &
  local pid=$! elapsed=0
  while kill -0 "$pid" 2>/dev/null; do
    if [ "$elapsed" -ge 45 ]; then
      set +e
      kill "$pid" 2>/dev/null
      wait "$pid" 2>/dev/null
      set -e
      echo 'reconciler --once exceeded 45-second harness bound' >&2
      return 124
    fi
    sleep 1
    elapsed=$((elapsed + 1))
  done
  set +e
  wait "$pid"
  status=$?
  set -e
  exit "$status"
)

installer_once() (
  FAKE_ACTION_LOG="$ACTION_LOG" FAKE_EVENT_LOG="$ROOT/events.log" FAKE_RUN="$RUN" FAKE_ROOT="$ROOT" FAKE_LAUNCHD_DIR="$ROOT/launchd" FAKE_LAUNCHD_LOADED_DIR="$ROOT/launchd-loaded" FAKE_SOURCE_WRAPPER="$ROOT/migration-wrapper.sh" FAKE_SOURCE_CONFIG="$ROOT/migration.conf" FAKE_HANDSHAKE=0 FAKE_PROBE=1 FAKE_KICKSTART=0 FAKE_VERIFY=0 \
    bash "$ROOT/root-libexec-installer/wireguard-reconciler.sh" --once --manifest "$INSTALLED_MANIFEST" --state-dir "$STATE" --wg-bin "$FAKE/wg" --probe-helper "$FAKE/probe" --network-stable-seconds 1 --stale-seconds 10 --action-timeout-seconds 1 --global-cooldown-seconds 1 --per-edge-cooldown-seconds 1 --action-window-seconds 10 &
  local pid=$! elapsed=0
  while kill -0 "$pid" 2>/dev/null; do
    if [ "$elapsed" -ge 45 ]; then
      set +e
      kill "$pid" 2>/dev/null
      wait "$pid" 2>/dev/null
      set -e
      echo 'installer reconciler --once exceeded 45-second harness bound' >&2
      return 124
    fi
    sleep 1
    elapsed=$((elapsed + 1))
  done
  set +e
  wait "$pid"
  status=$?
  set -e
  exit "$status"
)

run_once_expect_status() {
  local stage="$1" expected="$2" status
  shift 2
  set +e
  run_once "$@"
  status=$?
  set -e
  RUN_ONCE_STATUS="$status"
  printf '%s-run-status=%s\n' "$stage" "$status" >&2
  case ",$expected," in
    *,"$status",*) return 0 ;;
  esac
  echo "stage=$stage unexpected-status=$status expected=$expected" >&2
  echo 'action log:' >&2
  cat "$ACTION_LOG" >&2 2>/dev/null
  echo 'event log:' >&2
  cat "$ROOT/events.log" >&2 2>/dev/null
  return 1
}

# The migration accepts a real legacy wrapper from a service-user-writable parent as
# hash-bound input, but it must never execute or copy that source into a root job.
printf '[Peer]\nPublicKey = fake-peer-key\nPersistentKeepalive = 25\n' > "$ROOT/migration.conf"
cat > "$ROOT/migration-wrapper.sh" <<'EOF'
#!/usr/bin/env bash
printf 'legacy source wrapper executed\n' >> "${FAKE_SOURCE_EXECUTED:?}"
"${WG_QUICK_BIN:-/tmp/attacker-wg-quick}" up "$2"
EOF
chmod 700 "$ROOT/migration-wrapper.sh"
make_migration_manifest
expect_migrator_status accept
# The source LaunchDaemon contract is exactly four arguments. An extra argument
# beyond arg3 must reject the migration rather than being ignored.
printf '4\tunexpected-extra-argument\n' >> "$ROOT/launchd/com.example.wg.edge1.plist"
expect_migrator_status strict-four-argument-contract
make_migration_manifest
printf '4\t\n' >> "$ROOT/launchd/com.example.wg.edge1.plist"
expect_migrator_status strict-four-argument-contract-empty
make_migration_manifest
printf '16\tunexpected-sparse-extra-argument\n' >> "$ROOT/launchd/com.example.wg.edge1.plist"
expect_migrator_status strict-four-argument-contract-late
make_migration_manifest
prepare_default_wireguard_bins
rm -f "$ROOT/source-wrapper-executed"
FAKE_SOURCE_EXECUTED="$ROOT/source-wrapper-executed" apply_migrator
[ ! -e "$ROOT/source-wrapper-executed" ] || { echo 'migrator executed the legacy source wrapper' >&2; exit 1; }
[ -f "$ROOT/root-libexec/wireguard/edge1/run-wireguard.sh" ] || { echo 'migrator did not generate the target wrapper' >&2; exit 1; }
[ -f "$ROOT/root-libexec/wireguard-config/wg-edge1.conf" ] || { echo 'migrator did not install the fixed config path' >&2; exit 1; }
[ "$($FAKE/stat -f '%Lp' "$ROOT/root-libexec/wireguard-config")" = 700 ] || { echo 'migrator did not restrict canonical config directory to mode 700' >&2; exit 1; }
[ "$($FAKE/stat -f '%Lp' "$ROOT/root-libexec/wireguard-config/wg-edge1.conf")" = 600 ] || { echo 'migrator did not restrict canonical config file to mode 600' >&2; exit 1; }
[ ! -e "$ROOT/etc/wireguard" ] || { echo 'migrator wrote to the retired /usr/local/etc target' >&2; exit 1; }
[ "$(FAKE_SOURCE_WRAPPER="$ROOT/migration-wrapper.sh" FAKE_SOURCE_CONFIG="$ROOT/migration.conf" "$FAKE/plutil" -extract EnvironmentVariables.PATH raw -o - "$ROOT/launchd/com.example.wg.edge1.plist")" = "$ROOT/root-libexec/wireguard-bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin" ] || { echo 'migrator plist runtime PATH did not persist' >&2; exit 1; }
/usr/bin/grep -Fxq "wg_quick='$ROOT/root-libexec/wireguard-bin/wg-quick'" "$ROOT/root-libexec/wireguard/edge1/run-wireguard.sh" || { echo 'migrator wrapper did not render the default fixed wg-quick path' >&2; exit 1; }
/usr/bin/grep -Fxq "wg_bin='$ROOT/root-libexec/wireguard-bin/wg'" "$ROOT/root-libexec/wireguard/edge1/run-wireguard.sh" || { echo 'migrator wrapper did not render the default fixed wg path' >&2; exit 1; }
[ "$(FAKE_SOURCE_WRAPPER="$ROOT/migration-wrapper.sh" FAKE_SOURCE_CONFIG="$ROOT/migration.conf" "$FAKE/plutil" -extract ProgramArguments.0 raw -o - "$ROOT/launchd/com.example.wg.edge1.plist")" = "$FAKE/bash" ] || { echo 'migrator plist bash argument did not persist' >&2; exit 1; }
[ "$(FAKE_SOURCE_WRAPPER="$ROOT/migration-wrapper.sh" FAKE_SOURCE_CONFIG="$ROOT/migration.conf" "$FAKE/plutil" -extract ProgramArguments.1 raw -o - "$ROOT/launchd/com.example.wg.edge1.plist")" = "$ROOT/root-libexec/wireguard/edge1/run-wireguard.sh" ] || { echo 'migrator plist wrapper replacement did not persist' >&2; exit 1; }
[ "$(FAKE_SOURCE_WRAPPER="$ROOT/migration-wrapper.sh" FAKE_SOURCE_CONFIG="$ROOT/migration.conf" "$FAKE/plutil" -extract ProgramArguments.2 raw -o - "$ROOT/launchd/com.example.wg.edge1.plist")" = wg-edge1 ] || { echo 'migrator plist logical argument did not persist' >&2; exit 1; }
[ "$(FAKE_SOURCE_WRAPPER="$ROOT/migration-wrapper.sh" FAKE_SOURCE_CONFIG="$ROOT/migration.conf" "$FAKE/plutil" -extract ProgramArguments.3 raw -o - "$ROOT/launchd/com.example.wg.edge1.plist")" = "$ROOT/root-libexec/wireguard-config/wg-edge1.conf" ] || { echo 'migrator plist config replacement did not persist' >&2; exit 1; }
rm -f "$RUN"/killed.* "$RUN"/interface-gone.*
if [ "${JUHE_WG_HARNESS_TRANSACTION_ONLY:-0}" != 1 ]; then
  expect_fixed_wrapper_lifecycle
  expect_fixed_wrapper_failure_paths
fi

# The installer must record the same root wrapper/config pair that the migrated launchd
# job exposes. The real reconciler then accepts the pair from launchctl print before it
# can issue a recovery action.
rm -rf "$ROOT/root-libexec-installer" "$ROOT/installer-state" "$ROOT/root-libexec/wireguard-config"
: > "$ACTION_LOG"
make_migration_manifest
mkdir -p "$ROOT/installer-failure-baseline"
for installer_edge in 1 2 3 4 5 6 7 8; do
  cp "$ROOT/launchd/com.example.wg.edge$installer_edge.plist" "$ROOT/installer-failure-baseline/edge$installer_edge.plist"
  [ -f "$ROOT/launchd-loaded/com.example.wg.edge$installer_edge" ] || { echo "installer failure baseline edge$installer_edge was not loaded" >&2; exit 1; }
done
set +e
rm -f "$RUN/bootstrap-failed.com.juhe-ai.wireguard-reconciler"
FAKE_BOOTSTRAP_FAIL_LABEL='com.juhe-ai.wireguard-reconciler' apply_installer
installer_failure_status=$?
set -e
[ "$installer_failure_status" -ne 0 ] || { echo 'installer accepted a reconciler bootstrap failure' >&2; exit 1; }
[ ! -e "$ROOT/root-libexec-installer/wireguard/edge1/run-wireguard.sh" ] || { echo 'installer bootstrap failure retained migrated wrapper artifacts' >&2; exit 1; }
[ ! -e "$ROOT/root-libexec/wireguard-config/wg-edge1.conf" ] || { echo 'installer bootstrap failure retained migrated config' >&2; exit 1; }
for installer_edge in 1 2 3 4 5 6 7 8; do
  cmp -s "$ROOT/installer-failure-baseline/edge$installer_edge.plist" "$ROOT/launchd/com.example.wg.edge$installer_edge.plist" || { echo "installer bootstrap failure did not restore edge$installer_edge plist bytes" >&2; exit 1; }
  [ -f "$ROOT/launchd-loaded/com.example.wg.edge$installer_edge" ] || { echo "installer bootstrap failure did not restore edge$installer_edge loaded state" >&2; exit 1; }
done
: > "$ACTION_LOG"
make_migration_manifest
apply_installer
[ -f "$INSTALLED_MANIFEST" ] || { echo 'installer did not write a runtime manifest' >&2; exit 1; }
[ -d "$INSTALLED_STATE_DIR" ] || { echo 'installer did not create the derived root-only state directory' >&2; exit 1; }
installed_wrapper="$ROOT/root-libexec-installer/wireguard/edge1/run-wireguard.sh"
installed_config="$ROOT/root-libexec/wireguard-config/wg-edge1.conf"
[ "$installed_config" = "$ROOT/root-libexec/wireguard-config/wg-edge1.conf" ] || { echo 'custom installer root changed the canonical config path' >&2; exit 1; }
installed_manifest_line="$(/usr/bin/awk -F '\t' '$1 == "edge1" { print $4 "|" $5; exit }' "$INSTALLED_MANIFEST")"
[ "$installed_manifest_line" = "$installed_config|$installed_wrapper" ] || { echo 'installer runtime manifest does not bind migrated wrapper/config paths' >&2; exit 1; }
[ "$(FAKE_SOURCE_WRAPPER="$ROOT/migration-wrapper.sh" FAKE_SOURCE_CONFIG="$ROOT/migration.conf" "$FAKE/plutil" -extract ProgramArguments.0 raw -o - "$ROOT/launchd/com.example.wg.edge1.plist")" = "$FAKE/bash" ] || { echo 'installer migration plist bash argument did not persist' >&2; exit 1; }
[ "$(FAKE_SOURCE_WRAPPER="$ROOT/migration-wrapper.sh" FAKE_SOURCE_CONFIG="$ROOT/migration.conf" "$FAKE/plutil" -extract ProgramArguments.1 raw -o - "$ROOT/launchd/com.example.wg.edge1.plist")" = "$installed_wrapper" ] || { echo 'installer migration plist wrapper replacement did not persist' >&2; exit 1; }
[ "$(FAKE_SOURCE_WRAPPER="$ROOT/migration-wrapper.sh" FAKE_SOURCE_CONFIG="$ROOT/migration.conf" "$FAKE/plutil" -extract ProgramArguments.2 raw -o - "$ROOT/launchd/com.example.wg.edge1.plist")" = wg-edge1 ] || { echo 'installer migration plist logical argument did not persist' >&2; exit 1; }
[ "$(FAKE_SOURCE_WRAPPER="$ROOT/migration-wrapper.sh" FAKE_SOURCE_CONFIG="$ROOT/migration.conf" "$FAKE/plutil" -extract ProgramArguments.3 raw -o - "$ROOT/launchd/com.example.wg.edge1.plist")" = "$installed_config" ] || { echo 'installer migration plist config replacement did not persist' >&2; exit 1; }
[ "$(FAKE_SOURCE_WRAPPER="$ROOT/migration-wrapper.sh" FAKE_SOURCE_CONFIG="$ROOT/migration.conf" "$FAKE/plutil" -extract EnvironmentVariables.PATH raw -o - "$ROOT/launchd/com.example.wg.edge1.plist")" = "$FAKE:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin" ] || { echo 'installer did not pass runtime PATH to the migrator' >&2; exit 1; }
/usr/bin/grep -Fxq "wg_quick='$FAKE/wg-quick'" "$installed_wrapper" || { echo 'installer did not pass the fixed wg-quick path to the migrator' >&2; exit 1; }
/usr/bin/grep -Fxq "wg_bin='$FAKE/wg'" "$installed_wrapper" || { echo 'installer did not pass the fixed wg path to the migrator' >&2; exit 1; }
/usr/bin/grep -Fq "<string>--state-dir</string><string>$INSTALLED_STATE_DIR</string>" "$ROOT/launchd/com.juhe-ai.wireguard-reconciler.plist" || { echo 'installer did not pass its derived state directory to the helper plist' >&2; exit 1; }
/usr/bin/grep -Fq "<key>PATH</key><string>$FAKE:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>" "$ROOT/launchd/com.juhe-ai.wireguard-reconciler.plist" || { echo 'installer plist runtime PATH did not persist' >&2; exit 1; }
make_reuse_manifest
mkdir -p "$ROOT/reuse-failure-baseline"
for installer_edge in 1 2 3 4 5 6 7 8; do
  cp "$ROOT/launchd/com.example.wg.edge$installer_edge.plist" "$ROOT/reuse-failure-baseline/edge$installer_edge.plist"
  [ -f "$ROOT/launchd-loaded/com.example.wg.edge$installer_edge" ] || { echo "reuse baseline edge$installer_edge was not loaded" >&2; exit 1; }
done
: > "$ACTION_LOG"
set +e
rm -f "$RUN/bootstrap-failed.com.juhe-ai.wireguard-reconciler"
FAKE_BOOTSTRAP_FAIL_LABEL='com.juhe-ai.wireguard-reconciler' apply_installer "$REUSE_MANIFEST" 1
reuse_failure_status=$?
set -e
[ "$reuse_failure_status" -ne 0 ] || { echo 'reuse installer accepted a reconciler bootstrap failure' >&2; exit 1; }
for installer_edge in 1 2 3 4 5 6 7 8; do
  cmp -s "$ROOT/reuse-failure-baseline/edge$installer_edge.plist" "$ROOT/launchd/com.example.wg.edge$installer_edge.plist" || { echo "reuse installer changed edge$installer_edge plist" >&2; exit 1; }
  [ -f "$ROOT/launchd-loaded/com.example.wg.edge$installer_edge" ] || { echo "reuse installer changed edge$installer_edge loaded state" >&2; exit 1; }
done
if /usr/bin/grep -Fq 'com.example.wg.edge' "$ACTION_LOG"; then
  echo 'reuse installer touched an Edge job' >&2
  cat "$ACTION_LOG" >&2
  exit 1
fi
if [ "${JUHE_WG_HARNESS_TRANSACTION_ONLY:-0}" = 1 ]; then
  printf 'WireGuard installer transaction harness passed\n'
  exit 0
fi
rm -rf "$STATE"; mkdir "$STATE"; : > "$ACTION_LOG"; make_manifest
for installer_attempt in 1 2 3; do
  set +e
  installer_once
  installer_status=$?
  set -e
  printf 'installer-reconciler-attempt=%s status=%s\n' "$installer_attempt" "$installer_status" >&2
  [ "$installer_status" -eq 0 ] || { echo "installer reconciler attempt $installer_attempt failed" >&2; exit 1; }
  [ "$installer_attempt" -eq 3 ] || sleep 1
done
/usr/bin/grep -Fq "ProgramArguments = ( /usr/local/bin/bash $installed_wrapper wg-edge1 $installed_config )" "$ACTION_LOG" || { echo 'reconciler launchctl print did not expose the installer four-argument contract' >&2; exit 1; }
/usr/bin/grep -Fxq 'kickstart kickstart -k system/com.example.wg.edge1' "$ACTION_LOG" || { echo 'reconciler did not issue the exact canary kickstart argv after accepting the installer wrapper/config pair' >&2; exit 1; }

# Hooks remain a source-config rejection even though the source wrapper is intentionally
# treated as migration metadata rather than executable code.
printf 'PersistentKeepalive = 25\nPostUp = /bin/true\n' > "$ROOT/migration.conf"
make_migration_manifest
expect_migrator_status wireguard-hook
printf 'PersistentKeepalive = 25\n' > "$ROOT/migration.conf"
printf '#!/bin/sh\nsource /tmp/untrusted.sh\nexec /tmp/untrusted-helper "$1"\n' > "$ROOT/migration-wrapper.sh"
make_migration_manifest
echo 'stage=legacy-wrapper-metadata-accept' >&2
expect_migrator_status accept
# The legacy-source metadata negatives above replace the fake source plists.
# Restore the pair installed by apply_installer before exercising reconciliation.
restore_installed_plists

# One fresh edge means the real script must not invoke launchctl.
: > "$ACTION_LOG"
: > "$ROOT/events.log"
echo 'stage=partial-stale' >&2
run_once_expect_status partial-stale-first 0 0 1 0 utun8
partial_stale_first_status="$RUN_ONCE_STATUS"
run_once_expect_status partial-stale-second 0 0 1 0 utun8
partial_stale_second_status="$RUN_ONCE_STATUS"
printf 'partial-stale-run-statuses=%s,%s\n' "$partial_stale_first_status" "$partial_stale_second_status" >&2
if [ -s "$ACTION_LOG" ]; then
  echo 'partial stale invoked launchctl' >&2
  cat "$ACTION_LOG" >&2
  cat "$ROOT/events.log" >&2
  exit 1
fi
if ! /usr/bin/grep -Fq 'gate=partial-or-unconfirmed-stale' "$ROOT/events.log"; then
  echo 'partial stale did not record the no-action gate' >&2
  cat "$ROOT/events.log" >&2
  exit 1
fi

# Unknown external evidence must suppress all action.
echo 'stage=probe-unknown' >&2
rm -rf "$STATE"; mkdir "$STATE"; : > "$ACTION_LOG"; : > "$ROOT/events.log"; make_manifest
run_once_expect_status probe-unknown-first 0 0 75 0
run_once_expect_status probe-unknown-second 0 0 75 0
[ ! -s "$ACTION_LOG" ] || { echo 'probe unknown invoked launchctl' >&2; exit 1; }
/usr/bin/grep -Fq 'probe=unknown action=none' "$ROOT/events.log" || { echo 'probe unknown did not record the no-action event' >&2; cat "$ROOT/events.log" >&2; exit 1; }

# A just-observed network state is deliberately not settled yet, even with every
# WireGuard edge stale and an externally failed probe.
echo 'stage=network-settling' >&2
rm -rf "$STATE"; mkdir "$STATE"; : > "$ACTION_LOG"; : > "$ROOT/events.log"; make_manifest
run_once_expect_status network-settling 0 0 1 0
[ ! -s "$ACTION_LOG" ] || { echo 'network settling invoked launchctl' >&2; exit 1; }
/usr/bin/grep -Fq 'gate=network-settling' "$ROOT/events.log" || { echo 'network settling did not record the no-action gate' >&2; cat "$ROOT/events.log" >&2; exit 1; }

# A maintenance lock must suppress a fully stale batch.
rm -rf "$STATE"; mkdir "$STATE"; : > "$ACTION_LOG"; : > "$ROOT/events.log"; make_manifest; touch "$ROOT/maintenance.lock"
run_once_expect_status maintenance-lock 0 0 1 0 '' 75 '' maintenance "$ROOT/maintenance.lock"
[ ! -s "$ACTION_LOG" ] || { echo 'maintenance lock invoked launchctl' >&2; exit 1; }
/usr/bin/grep -Fq 'gate=maintenance-or-release-lock action=none' "$ROOT/events.log" || { echo 'maintenance lock did not record the no-action gate' >&2; cat "$ROOT/events.log" >&2; exit 1; }
rm -f "$ROOT/maintenance.lock"

# The release lock is an independent gate and must have the same zero-action
# behavior as the maintenance lock.
rm -rf "$STATE"; mkdir "$STATE"; : > "$ACTION_LOG"; : > "$ROOT/events.log"; make_manifest; touch "$ROOT/release.lock"
run_once_expect_status release-lock 0 0 1 0 '' 75 '' release "$ROOT/release.lock"
[ ! -s "$ACTION_LOG" ] || { echo 'release lock invoked launchctl' >&2; exit 1; }
/usr/bin/grep -Fq 'gate=maintenance-or-release-lock action=none' "$ROOT/events.log" || { echo 'release lock did not record the no-action gate' >&2; cat "$ROOT/events.log" >&2; exit 1; }
rm -f "$ROOT/release.lock"

# A stale observation from an old disabled/delayed run cannot become the first half of a
# new confirmation. The max-gap guard must leave this batch at zero recovery actions.
rm -rf "$STATE"; mkdir "$STATE"; : > "$ACTION_LOG"; make_manifest
run_once_expect_status expired-stale-first 0 0 1 0
sleep 1
old_observed_at=$(( $(date +%s) - 1000 ))
for edge in 1 2 3 4 5 6 7 8; do printf '1\t1\t0\t%s\n' "$old_observed_at" > "$STATE/edge.edge$edge.state"; done
run_once_expect_status expired-stale-second 0 0 1 0
[ ! -s "$ACTION_LOG" ] || { echo 'expired stale sample chain invoked launchctl' >&2; exit 1; }

# Two stale samples after network settling reach only the canary. Its forced kickstart
# failure must prevent every remaining edge from being touched.
rm -rf "$STATE"; mkdir "$STATE"; : > "$ACTION_LOG"; make_manifest
run_once_expect_status canary-failure-first 0,1 0 1 1
sleep 1
run_once_expect_status canary-failure-second 0,1 0 1 1
run_once_expect_status canary-failure-third 0,1 0 1 1
grep -Fq 'com.example.wg.edge1' "$ACTION_LOG" || { echo 'canary failure did not reach launchctl' >&2; cat "$ROOT/events.log" >&2; cat "$ACTION_LOG" >&2; exit 1; }
if grep -Eq 'edge[2-8]' "$ACTION_LOG"; then echo 'canary failure touched a non-canary edge' >&2; exit 1; fi

# With post-action external evidence, the first action must be canary edge1 and only then
# can the real reconciler issue any action for another exact manifest edge. The fake
# launchctl itself rejects a non-canary kickstart until the fake verifier recorded edge1.
rm -rf "$STATE"; mkdir "$STATE"; : > "$ACTION_LOG"; make_manifest
run_once_expect_status canary-success-first 0 0 1 0 '' 0
sleep 1
run_once_expect_status canary-success-second 0 0 1 0 '' 0
run_once_expect_status canary-success-third 0 0 1 0 '' 0
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
run_once_expect_status non-canary-failure-first 0,1 0 1 0 '' 0 2
sleep 1
run_once_expect_status non-canary-failure-second 0,1 0 1 0 '' 0 2
run_once_expect_status non-canary-failure-third 0,1 0 1 0 '' 0 2
grep -Eq 'kickstart .*edge1' "$ACTION_LOG" || { echo 'non-canary failure test did not recover canary' >&2; exit 1; }
grep -Eq 'kickstart .*edge2' "$ACTION_LOG" || { echo 'non-canary failure did not reach edge2' >&2; exit 1; }
if grep -Eq 'kickstart .*edge[3-8]' "$ACTION_LOG"; then echo 'non-canary failure restarted a later edge' >&2; cat "$ACTION_LOG" >&2; exit 1; fi
grep -Fq 'result=non-canary-failed edge=edge2 remaining=skipped' "$ROOT/events.log" || { echo 'non-canary failure was not recorded as terminal' >&2; cat "$ROOT/events.log" >&2; exit 1; }
printf 'WireGuard reconciler fake-command harness passed\n'
