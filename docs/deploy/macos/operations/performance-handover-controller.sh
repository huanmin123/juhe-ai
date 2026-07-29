#!/usr/bin/env bash
set -euo pipefail

# Switches one outer Nginx route fragment between two already-verified
# performance slots. Candidate preparation, backups and service lifecycle stay
# outside this controller so a route failure never destroys either fallback.

MODE=dry-run
ACTION=status
PLAN_DIR=
ROUTE_HEADER_NAME=X-Juhe-Active-Upstream
STABILITY_SECONDS=60
LOCK_DIR=
LOCK_HELD=0

usage() {
  cat <<'EOF'
Usage: performance-handover-controller.sh [--dry-run|--apply] --action <status|preflight|takeover|switchback|recover> --plan-dir <absolute path>
  [--route-header-name <HTTP token>] [--stability-seconds <60..600>]

The plan directory must contain a mode-0600 handover.conf with these non-secret
absolute paths and labels: route_file, main_fragment, temporary_fragment,
nginx_bin, node_bin, nginx_main_config, ingress_health_url, access_log,
main_label, temporary_label, main_instance_id, temporary_instance_id,
main_topology_identity and temporary_topology_identity. It must be owned by
the deployment controller and no other
user may write its directory or referenced route files.

Each slot also supplies its internal control health URL and three comma-separated
gateway health URLs. These loopback URLs are verified before an outer route is
changed, so the control plane cannot mask a failed gateway pool.
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --dry-run) MODE=dry-run; shift ;;
    --apply) MODE=apply; shift ;;
    --action) ACTION="${2:-}"; shift 2 ;;
    --plan-dir) PLAN_DIR="${2:-}"; shift 2 ;;
    --route-header-name) ROUTE_HEADER_NAME="${2:-}"; shift 2 ;;
    --stability-seconds) STABILITY_SECONDS="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown option: $1" >&2; usage >&2; exit 2 ;;
  esac
done

case "$ACTION" in status|preflight|takeover|switchback|recover) ;; *) echo 'invalid action' >&2; exit 2;; esac
case "$PLAN_DIR" in /*) ;; *) echo '--plan-dir must be absolute' >&2; exit 2;; esac
case "$ROUTE_HEADER_NAME" in ''|*[!A-Za-z0-9_-]*) echo 'invalid route header name' >&2; exit 2;; esac
case "$STABILITY_SECONDS" in ''|*[!0-9]*) echo 'stability seconds must be an integer' >&2; exit 2;; esac
[ "$STABILITY_SECONDS" -ge 60 ] && [ "$STABILITY_SECONDS" -le 600 ] || { echo 'stability seconds must be 60..600' >&2; exit 2; }

CONF="$PLAN_DIR/handover.conf"
JOURNAL="$PLAN_DIR/handover.journal"
ROLLBACK_FRAGMENT="$PLAN_DIR/route-before-switch.conf"
LOCK_DIR="$PLAN_DIR/handover.lock"
SWITCH_ATTEMPTED=0
ROLLBACK_PROVEN=0
SOURCE_LABEL=

assert_safe_value() {
  case "$2" in ''|*'\n'*|*'\r'*|*'$'*|*'`'*|*';'*|*'|'*|*'&'*) echo "unsafe $1" >&2; exit 2;; esac
}

assert_private_path() {
  path="$1" description="$2" metadata= owner= mode= group_mode= other_mode=
  [ -e "$path" ] && [ ! -L "$path" ] || { echo "$description must be a regular non-link path" >&2; exit 1; }
  metadata="$(stat -f '%u:%Lp' "$path" 2>/dev/null || true)"
  owner="${metadata%%:*}" mode="${metadata#*:}"
  [ "$owner" = "$(id -u)" ] || [ "$owner" = 0 ] || { echo "$description must be owned by the deployment controller or root" >&2; exit 1; }
  case "$mode" in [0-7][0-7][0-7]) ;; *) echo "unable to verify $description mode" >&2; exit 1;; esac
  group_mode="${mode#?}"; group_mode="${group_mode%?}"; other_mode="${mode#??}"
  case "$group_mode" in 2|3|6|7) echo "$description must not be group writable" >&2; exit 1;; esac
  case "$other_mode" in 2|3|6|7) echo "$description must not be world writable" >&2; exit 1;; esac
}

assert_private_ancestry() {
  path="$1" description="$2"
  while :; do
    assert_private_path "$path" "$description"
    [ "$path" = / ] && return 0
    path="$(dirname "$path")"
  done
}

assert_gateway_health_urls() {
  urls="$1" description="$2" count=0 old_ifs="$IFS"
  case "$urls" in *,,*|,*|*,) echo "$description must contain exactly three comma-separated loopback URLs" >&2; exit 2;; esac
  IFS=,
  set -- $urls
  IFS="$old_ifs"
  [ "$#" -eq 3 ] || { echo "$description must contain exactly three loopback URLs" >&2; exit 2; }
  for url in "$@"; do
    case "$url" in http://127.0.0.1:*|http://localhost:*|https://127.0.0.1:*|https://localhost:*) ;; *) echo "$description must use loopback HTTP(S) URLs" >&2; exit 2;; esac
    count=$((count + 1))
  done
}

acquire_lock() {
  if mkdir "$LOCK_DIR" 2>/dev/null; then
    LOCK_HELD=1
    umask 077
    printf 'pid=%s\nstarted_at=%s\n' "$$" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$LOCK_DIR/owner"
  else
    echo "handover lock already exists: $LOCK_DIR" >&2
    exit 75
  fi
}

release_lock() {
  [ "$LOCK_HELD" = 1 ] || return 0
  rm -f -- "$LOCK_DIR/owner"
  rmdir "$LOCK_DIR" 2>/dev/null || true
  LOCK_HELD=0
}

read_config() {
  [ -f "$CONF" ] && [ ! -L "$CONF" ] || { echo "missing plan config: $CONF" >&2; exit 1; }
  if [ "$(uname -s)" = Darwin ]; then
    [ "$(stat -f '%Lp' "$CONF" 2>/dev/null || true)" = 600 ] || { echo 'plan config must be mode 0600' >&2; exit 1; }
  fi
  route_file= main_fragment= temporary_fragment= nginx_bin= node_bin= nginx_main_config= ingress_health_url= access_log= main_label= temporary_label= main_instance_id= temporary_instance_id= main_topology_identity= temporary_topology_identity= main_control_health_url= temporary_control_health_url= main_gateway_health_urls= temporary_gateway_health_urls= seen_keys='|'
  while IFS='=' read -r key value; do
    [ -n "$key" ] || continue
    case "$key" in
      route_file|main_fragment|temporary_fragment|nginx_bin|node_bin|nginx_main_config|ingress_health_url|access_log|main_label|temporary_label|main_instance_id|temporary_instance_id|main_topology_identity|temporary_topology_identity|main_control_health_url|temporary_control_health_url|main_gateway_health_urls|temporary_gateway_health_urls)
        case "$seen_keys" in *"|$key|"*) echo "duplicate plan key: $key" >&2; exit 2;; esac
        seen_keys="${seen_keys}${key}|"
        assert_safe_value "$key" "$value"
        case "$key" in
          route_file) route_file="$value" ;;
          main_fragment) main_fragment="$value" ;;
          temporary_fragment) temporary_fragment="$value" ;;
          nginx_bin) nginx_bin="$value" ;;
          node_bin) node_bin="$value" ;;
          nginx_main_config) nginx_main_config="$value" ;;
          ingress_health_url) ingress_health_url="$value" ;;
          access_log) access_log="$value" ;;
          main_label) main_label="$value" ;;
          temporary_label) temporary_label="$value" ;;
          main_instance_id) main_instance_id="$value" ;;
          temporary_instance_id) temporary_instance_id="$value" ;;
          main_topology_identity) main_topology_identity="$value" ;;
          temporary_topology_identity) temporary_topology_identity="$value" ;;
          main_control_health_url) main_control_health_url="$value" ;;
          temporary_control_health_url) temporary_control_health_url="$value" ;;
          main_gateway_health_urls) main_gateway_health_urls="$value" ;;
          temporary_gateway_health_urls) temporary_gateway_health_urls="$value" ;;
        esac
        ;;
      *password*|*secret*|*token*|*key*|*url*postgres*|*redis*) echo "secret-like plan key is forbidden: $key" >&2; exit 2 ;;
      *) echo "unknown plan key: $key" >&2; exit 2 ;;
    esac
  done < "$CONF"
  for required in route_file main_fragment temporary_fragment nginx_bin node_bin nginx_main_config ingress_health_url access_log main_label temporary_label main_instance_id temporary_instance_id main_topology_identity temporary_topology_identity main_control_health_url temporary_control_health_url main_gateway_health_urls temporary_gateway_health_urls; do
    case "$required" in
      route_file) value="$route_file" ;;
      main_fragment) value="$main_fragment" ;;
      temporary_fragment) value="$temporary_fragment" ;;
      nginx_bin) value="$nginx_bin" ;;
      node_bin) value="$node_bin" ;;
      nginx_main_config) value="$nginx_main_config" ;;
      ingress_health_url) value="$ingress_health_url" ;;
      access_log) value="$access_log" ;;
      main_label) value="$main_label" ;;
      temporary_label) value="$temporary_label" ;;
      main_instance_id) value="$main_instance_id" ;;
      temporary_instance_id) value="$temporary_instance_id" ;;
      main_topology_identity) value="$main_topology_identity" ;;
      temporary_topology_identity) value="$temporary_topology_identity" ;;
      main_control_health_url) value="$main_control_health_url" ;;
      temporary_control_health_url) value="$temporary_control_health_url" ;;
      main_gateway_health_urls) value="$main_gateway_health_urls" ;;
      temporary_gateway_health_urls) value="$temporary_gateway_health_urls" ;;
    esac
    [ -n "$value" ] || { echo "missing plan key: $required" >&2; exit 1; }
  done
  for path in "$route_file" "$main_fragment" "$temporary_fragment" "$nginx_bin" "$node_bin" "$nginx_main_config" "$access_log"; do
    case "$path" in /*) ;; *) echo "plan path must be absolute: $path" >&2; exit 2;; esac
  done
  [ -f "$main_fragment" ] && [ ! -L "$main_fragment" ] || { echo 'main route fragment must be a regular file' >&2; exit 1; }
  [ -f "$temporary_fragment" ] && [ ! -L "$temporary_fragment" ] || { echo 'temporary route fragment must be a regular file' >&2; exit 1; }
  [ -f "$nginx_main_config" ] && [ ! -L "$nginx_main_config" ] || { echo 'nginx main config must be a regular file' >&2; exit 1; }
  [ "$MODE" = dry-run ] || [ -x "$nginx_bin" ] || { echo 'nginx binary is not executable' >&2; exit 1; }
  [ "$MODE" = dry-run ] || [ -x "$node_bin" ] || { echo 'node binary is not executable' >&2; exit 1; }
  case "$ingress_health_url" in http://*|https://*) ;; *) echo 'ingress health URL must use HTTP(S)' >&2; exit 2;; esac
  case "$main_control_health_url" in http://127.0.0.1:*|http://localhost:*|https://127.0.0.1:*|https://localhost:*) ;; *) echo 'main control health URL must be loopback HTTP(S)' >&2; exit 2;; esac
  case "$temporary_control_health_url" in http://127.0.0.1:*|http://localhost:*|https://127.0.0.1:*|https://localhost:*) ;; *) echo 'temporary control health URL must be loopback HTTP(S)' >&2; exit 2;; esac
  assert_gateway_health_urls "$main_gateway_health_urls" 'main gateway health URLs'
  assert_gateway_health_urls "$temporary_gateway_health_urls" 'temporary gateway health URLs'
  case "$main_label$temporary_label" in *[!A-Za-z0-9_-]*|'') echo 'route labels contain unsupported characters' >&2; exit 2;; esac
  [ "$main_label" != "$temporary_label" ] || { echo 'route labels must differ' >&2; exit 2; }
  case "$main_instance_id$temporary_instance_id$main_topology_identity$temporary_topology_identity" in *[!A-Za-z0-9._-]*|'') echo 'topology identities contain unsupported characters' >&2; exit 2;; esac
  if [ "$MODE" = apply ]; then
    assert_private_ancestry "$PLAN_DIR" 'plan directory'
    assert_private_ancestry "$CONF" 'plan config'
    assert_private_ancestry "$route_file" 'active route fragment'
    assert_private_ancestry "$main_fragment" 'main route fragment'
    assert_private_ancestry "$temporary_fragment" 'temporary route fragment'
    assert_private_ancestry "$nginx_main_config" 'nginx main config'
    assert_private_ancestry "$access_log" 'access log'
  fi
}

write_journal() {
  state="$1" action="$2" source="$3" target="$4"
  tmp="$JOURNAL.tmp.$$"
  umask 077
  {
    printf 'format_version=1\nstate=%s\naction=%s\nsource=%s\ntarget=%s\nupdated_at=%s\n' "$state" "$action" "$source" "$target" "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  } > "$tmp"
  chmod 600 "$tmp"
  mv -f -- "$tmp" "$JOURNAL"
}

journal_value() { awk -F= -v key="$1" '$1==key {print substr($0,index($0,"=")+1); exit}' "$JOURNAL"; }

assert_journal_route() {
  source="$1" target="$2"
  case "$source:$target" in main:temporary|temporary:main) ;; *) echo 'invalid journal route' >&2; exit 1;; esac
}

require_preflight() {
  expected_source="$1" expected_target="$2"
  [ -f "$JOURNAL" ] || { echo 'takeover requires a matching preflight journal' >&2; exit 1; }
  state="$(journal_value state)" source="$(journal_value source)" target="$(journal_value target)"
  [ "$state" = preflight ] && [ "$source" = "$expected_source" ] && [ "$target" = "$expected_target" ] || {
    echo "matching preflight journal required for $expected_source to $expected_target" >&2
    exit 1
  }
}

label_for() { [ "$1" = main ] && printf '%s' "$main_label" || printf '%s' "$temporary_label"; }
fragment_for() { [ "$1" = main ] && printf '%s' "$main_fragment" || printf '%s' "$temporary_fragment"; }
instance_for() { [ "$1" = main ] && printf '%s' "$main_instance_id" || printf '%s' "$temporary_instance_id"; }
topology_identity_for() { [ "$1" = main ] && printf '%s' "$main_topology_identity" || printf '%s' "$temporary_topology_identity"; }
control_health_url_for() { [ "$1" = main ] && printf '%s' "$main_control_health_url" || printf '%s' "$temporary_control_health_url"; }
gateway_health_urls_for() { [ "$1" = main ] && printf '%s' "$main_gateway_health_urls" || printf '%s' "$temporary_gateway_health_urls"; }

header_value_matches() {
  header="$1" expected="$2" headers="$3"
  awk -v header="$header" -v expected="$expected" '
    {
      line=$0; sub(/\r$/, "", line)
      separator=index(line, ":"); if (!separator) next
      name=substr(line, 1, separator - 1); value=substr(line, separator + 1)
      gsub(/^[[:space:]]+|[[:space:]]+$/, "", value)
      if (tolower(name) == tolower(header)) { count++; if (value != expected) conflict=1 }
    }
    END { exit count == 1 && !conflict ? 0 : 1 }
  ' "$headers"
}

health_identity() {
  expected_role="$1" expected_instance="$2" require_workers="$3" health_body="$4"
  "$node_bin" -e '
    const fs = require("fs")
    const health = JSON.parse(fs.readFileSync(0, "utf8"))
    const fail = () => process.exit(1)
    const validPid = (value) => Number.isSafeInteger(value) && value > 1
    const expectedRole = process.argv[1]
    const expectedInstance = process.argv[2]
    const requireWorkers = process.argv[3] === "true"
    if (health.status !== "ok" || health.runtimeMode !== "performance" || health.nodeRole !== expectedRole || (expectedInstance && health.instanceId !== expectedInstance) || typeof health.instanceId !== "string" || !health.instanceId || !validPid(health.processPid) || !validPid(health.dbServicePid) || !Array.isArray(health.workerProcesses) || !health.workerTopologyReady) fail()
    if (requireWorkers && health.workerProcesses.length === 0) fail()
    const workers = health.workerProcesses.map((worker) => {
      if (!worker || typeof worker.role !== "string" || !worker.role || !Number.isSafeInteger(worker.replicaIndex) || worker.replicaIndex < 0 || !validPid(worker.pid) || worker.ready !== true) fail()
      return `${worker.role}:${worker.replicaIndex}:${worker.pid}`
    }).sort()
    process.stdout.write([health.nodeRole, health.instanceId, health.processPid, health.dbServicePid, ...workers].join("|"))
  ' "$expected_role" "$expected_instance" "$require_workers" < "$health_body"
}

verify_slot_once() {
  target="$1" expected_instance="$(instance_for "$target")" expected_topology_identity="$(topology_identity_for "$target")" control_url="$(control_health_url_for "$target")" gateway_urls="$(gateway_health_urls_for "$target")" old_ifs="$IFS" control_identity= gateway_identity= gateway_observed= gateway_body= seen_gateway_instances='|'
  OBSERVED_SLOT_IDENTITY="$(
    control_headers="$(mktemp -t juhe-ai-handover-control.XXXXXX)" control_body="$(mktemp -t juhe-ai-handover-control-health.XXXXXX)"
    trap 'rm -f -- "$control_headers" "$control_body" "$gateway_body"' EXIT
    curl -fsS --max-time 8 -D "$control_headers" -o "$control_body" "$control_url"
    header_value_matches X-Juhe-Topology-Install "$expected_topology_identity" "$control_headers"
    control_identity="$(health_identity control "$expected_instance" true "$control_body")"
    IFS=,
    set -- $gateway_urls
    IFS="$old_ifs"
    gateway_observed="$control_identity"
    for gateway_url in "$@"; do
      gateway_body="$(mktemp -t juhe-ai-handover-gateway-health.XXXXXX)"
      curl -fsS --max-time 8 -o "$gateway_body" "$gateway_url"
      gateway_identity="$(health_identity gateway '' false "$gateway_body")"
      gateway_instance="${gateway_identity#gateway|}"; gateway_instance="${gateway_instance%%|*}"
      case "$seen_gateway_instances" in *"|$gateway_instance|"*) echo 'duplicate gateway instance in health proof' >&2; exit 1;; esac
      seen_gateway_instances="${seen_gateway_instances}${gateway_instance}|"
      gateway_observed="$gateway_observed|$gateway_identity"
      rm -f -- "$gateway_body"; gateway_body=
    done
    printf '%s' "$gateway_observed"
  )"
}

verify_slot_stable() {
  target="$1" slot_identity= attempts=$((STABILITY_SECONDS / 5))
  [ "$attempts" -ge 12 ] || attempts=12
  for _ in $(seq 1 "$attempts"); do
    verify_slot_once "$target"
    if [ -z "$slot_identity" ]; then slot_identity="$OBSERVED_SLOT_IDENTITY"; else [ "$slot_identity" = "$OBSERVED_SLOT_IDENTITY" ] || { echo 'slot topology identity changed during stability proof' >&2; return 1; }; fi
    sleep 5
  done
}

verify_ingress_once() {
  target="$1" expected_label="$(label_for "$target")" expected_instance="$(instance_for "$target")" expected_topology_identity="$(topology_identity_for "$target")"
  case "$ingress_health_url" in
    */__aisys__/health) api_health_url="${ingress_health_url%/__aisys__/health}/__aisys__/api/health" ;;
    *) api_health_url="${ingress_health_url%/}/__aisys__/api/health" ;;
  esac
  OBSERVED_TOPOLOGY_IDENTITY="$(
    headers="$(mktemp -t juhe-ai-handover.XXXXXX)" health_body="$(mktemp -t juhe-ai-handover-health.XXXXXX)"
    trap 'rm -f -- "$headers" "$health_body"' EXIT
    curl -fsS --max-time 8 -D "$headers" -o "$health_body" "$ingress_health_url"
    header_value_matches "$ROUTE_HEADER_NAME" "$expected_label" "$headers"
    header_value_matches X-Juhe-Topology-Install "$expected_topology_identity" "$headers"
    health_identity control "$expected_instance" true "$health_body"
    curl -fsS --max-time 8 "$api_health_url" >/dev/null
  )"
}

verify_ingress_stable() {
  target="$1" prior_size="$(stat -f '%z' "$access_log" 2>/dev/null || printf 0)" attempts=$((STABILITY_SECONDS / 5)) topology_identity=
  [ "$attempts" -ge 12 ] || attempts=12
  for _ in $(seq 1 "$attempts"); do
    verify_ingress_once "$target"
    if [ -z "$topology_identity" ]; then topology_identity="$OBSERVED_TOPOLOGY_IDENTITY"; else [ "$topology_identity" = "$OBSERVED_TOPOLOGY_IDENTITY" ] || { echo 'ingress topology identity changed during stability proof' >&2; return 1; }; fi
    sleep 5
  done
  current_size="$(stat -f '%z' "$access_log" 2>/dev/null || printf 0)"
  [ "$current_size" -gt "$prior_size" ] || { echo 'ingress access log did not advance during stability proof' >&2; return 1; }
}

nginx_test_reload() { "$nginx_bin" -t -c "$nginx_main_config" && "$nginx_bin" -s reload -c "$nginx_main_config"; }

restore_route() {
  [ -f "$ROLLBACK_FRAGMENT" ] || return 1
  stage="$route_file.handover-restore.$$"
  cp -p -- "$ROLLBACK_FRAGMENT" "$stage"
  mv -f -- "$stage" "$route_file"
  nginx_test_reload && verify_ingress_stable "$SOURCE_LABEL" && verify_slot_stable "$SOURCE_LABEL"
}

on_exit() {
  code="$?"
  set +e
  if [ "$code" -ne 0 ] && [ "$SWITCH_ATTEMPTED" = 1 ]; then
    echo "handover failed; restoring $SOURCE_LABEL route" >&2
    if restore_route; then ROLLBACK_PROVEN=1; write_journal rollback-proven "$ACTION" "$SOURCE_LABEL" "$(journal_value target)"; else write_journal rollback-unproven "$ACTION" "$SOURCE_LABEL" "$(journal_value target)"; echo 'ROLLBACK_UNPROVEN: retain both slots and investigate' >&2; fi
  fi
  release_lock
  exit "$code"
}

read_config
printf 'mode=%s action=%s plan=%s route=%s\n' "$MODE" "$ACTION" "$PLAN_DIR" "$route_file"
[ "$MODE" = apply ] || exit 0
command -v curl >/dev/null

case "$ACTION" in
  status)
    [ -f "$JOURNAL" ] && cat "$JOURNAL" || printf 'state=uninitialized\n'
    [ -d "$LOCK_DIR" ] && printf 'lock=present\n' || printf 'lock=absent\n'
    exit 0
    ;;
esac

acquire_lock
trap on_exit EXIT

case "$ACTION" in
  preflight)
    if [ -f "$JOURNAL" ]; then
      previous_state="$(journal_value state)" previous_action="$(journal_value action)"
      previous_source="$(journal_value source)" previous_target="$(journal_value target)"
      assert_journal_route "$previous_source" "$previous_target"
      case "$previous_state:$previous_action" in
        committed:takeover) SOURCE_LABEL=temporary; TARGET_LABEL=main ;;
        committed:switchback) SOURCE_LABEL=main; TARGET_LABEL=temporary ;;
        preflight-cancelled:recover) SOURCE_LABEL="$previous_source"; TARGET_LABEL="$previous_target" ;;
        rollback-proven:*) SOURCE_LABEL="$previous_source"; TARGET_LABEL="$previous_target" ;;
        *) echo 'existing handover journal requires recover or status' >&2; exit 1 ;;
      esac
    else
      SOURCE_LABEL=main
      TARGET_LABEL=temporary
    fi
    verify_slot_stable "$SOURCE_LABEL"
    verify_slot_stable "$TARGET_LABEL"
    verify_ingress_stable "$SOURCE_LABEL"
    write_journal preflight preflight "$SOURCE_LABEL" "$TARGET_LABEL"
    printf 'PREFLIGHT_OK source=%s target=%s\n' "$SOURCE_LABEL" "$TARGET_LABEL"
    exit 0
    ;;
  takeover)
    SOURCE_LABEL=main; TARGET_LABEL=temporary
    require_preflight "$SOURCE_LABEL" "$TARGET_LABEL"
    ;;
  switchback)
    SOURCE_LABEL=temporary; TARGET_LABEL=main
    require_preflight "$SOURCE_LABEL" "$TARGET_LABEL"
    ;;
  recover)
    [ -f "$JOURNAL" ] || { echo 'no handover journal to recover' >&2; exit 1; }
    SOURCE_LABEL="$(journal_value source)" TARGET_LABEL="$(journal_value target)" state="$(journal_value state)"; assert_journal_route "$SOURCE_LABEL" "$TARGET_LABEL"
    case "$state" in
      preflight)
        verify_slot_stable "$SOURCE_LABEL"
        verify_ingress_stable "$SOURCE_LABEL"
        write_journal preflight-cancelled recover "$SOURCE_LABEL" "$TARGET_LABEL"
        printf 'RECOVERY_OK route=%s; no route change had been staged\n' "$SOURCE_LABEL"
        exit 0
        ;;
      rollback-armed|route-staged|reload-requested|stable|rollback-unproven) ;;
      preflight-cancelled|rollback-proven|committed) echo "recover is not valid from stable state: $state" >&2; exit 1 ;;
      *) echo "recover is not valid from journal state: $state" >&2; exit 1 ;;
    esac
    restore_route || { write_journal rollback-unproven recover "$SOURCE_LABEL" "$TARGET_LABEL"; echo 'ROLLBACK_UNPROVEN: retain both slots and investigate' >&2; exit 70; }
    write_journal rollback-proven recover "$SOURCE_LABEL" "$(journal_value target)"
    printf 'RECOVERY_OK route=%s\n' "$SOURCE_LABEL"
    exit 0
    ;;
esac

verify_slot_stable "$SOURCE_LABEL"
verify_slot_stable "$TARGET_LABEL"
verify_ingress_stable "$SOURCE_LABEL"
cp -p -- "$route_file" "$ROLLBACK_FRAGMENT"
chmod 600 "$ROLLBACK_FRAGMENT"
write_journal rollback-armed "$ACTION" "$SOURCE_LABEL" "$TARGET_LABEL"
trap on_exit EXIT
SWITCH_ATTEMPTED=1
stage="$route_file.handover-stage.$$"
cp -p -- "$(fragment_for "$TARGET_LABEL")" "$stage"
mv -f -- "$stage" "$route_file"
write_journal route-staged "$ACTION" "$SOURCE_LABEL" "$TARGET_LABEL"
nginx_test_reload
write_journal reload-requested "$ACTION" "$SOURCE_LABEL" "$TARGET_LABEL"
verify_ingress_stable "$TARGET_LABEL"
verify_slot_stable "$TARGET_LABEL"
write_journal stable "$ACTION" "$SOURCE_LABEL" "$TARGET_LABEL"
SWITCH_ATTEMPTED=0
write_journal committed "$ACTION" "$SOURCE_LABEL" "$TARGET_LABEL"
release_lock
trap - EXIT
printf 'HANDOVER_OK target=%s; both performance slots remain running\n' "$TARGET_LABEL"
