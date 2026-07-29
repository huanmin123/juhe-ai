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

usage() {
  cat <<'EOF'
Usage: performance-handover-controller.sh [--dry-run|--apply] --action <status|preflight|takeover|switchback|recover> --plan-dir <absolute path>
  [--route-header-name <HTTP token>] [--stability-seconds <60..600>]

The plan directory must contain a mode-0600 handover.conf with these non-secret
absolute paths and labels: route_file, main_fragment, temporary_fragment,
nginx_bin, nginx_main_config, ingress_health_url, access_log, main_label,
temporary_label. It must be owned by the deployment controller.
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
SWITCH_ATTEMPTED=0
ROLLBACK_PROVEN=0
SOURCE_LABEL=

assert_safe_value() {
  case "$2" in ''|*'\n'*|*'\r'*|*'$'*|*'`'*|*';'*|*'|'*|*'&'*) echo "unsafe $1" >&2; exit 2;; esac
}

read_config() {
  [ -f "$CONF" ] && [ ! -L "$CONF" ] || { echo "missing plan config: $CONF" >&2; exit 1; }
  if [ "$(uname -s)" = Darwin ]; then
    [ "$(stat -f '%Lp' "$CONF" 2>/dev/null || true)" = 600 ] || { echo 'plan config must be mode 0600' >&2; exit 1; }
  fi
  route_file= main_fragment= temporary_fragment= nginx_bin= nginx_main_config= ingress_health_url= access_log= main_label= temporary_label=
  while IFS='=' read -r key value; do
    [ -n "$key" ] || continue
    case "$key" in
      route_file|main_fragment|temporary_fragment|nginx_bin|nginx_main_config|ingress_health_url|access_log|main_label|temporary_label)
        assert_safe_value "$key" "$value"
        case "$key" in
          route_file) route_file="$value" ;;
          main_fragment) main_fragment="$value" ;;
          temporary_fragment) temporary_fragment="$value" ;;
          nginx_bin) nginx_bin="$value" ;;
          nginx_main_config) nginx_main_config="$value" ;;
          ingress_health_url) ingress_health_url="$value" ;;
          access_log) access_log="$value" ;;
          main_label) main_label="$value" ;;
          temporary_label) temporary_label="$value" ;;
        esac
        ;;
      *password*|*secret*|*token*|*key*|*url*postgres*|*redis*) echo "secret-like plan key is forbidden: $key" >&2; exit 2 ;;
      *) echo "unknown plan key: $key" >&2; exit 2 ;;
    esac
  done < "$CONF"
  for required in route_file main_fragment temporary_fragment nginx_bin nginx_main_config ingress_health_url access_log main_label temporary_label; do
    case "$required" in
      route_file) value="$route_file" ;;
      main_fragment) value="$main_fragment" ;;
      temporary_fragment) value="$temporary_fragment" ;;
      nginx_bin) value="$nginx_bin" ;;
      nginx_main_config) value="$nginx_main_config" ;;
      ingress_health_url) value="$ingress_health_url" ;;
      access_log) value="$access_log" ;;
      main_label) value="$main_label" ;;
      temporary_label) value="$temporary_label" ;;
    esac
    [ -n "$value" ] || { echo "missing plan key: $required" >&2; exit 1; }
  done
  for path in "$route_file" "$main_fragment" "$temporary_fragment" "$nginx_bin" "$nginx_main_config" "$access_log"; do
    case "$path" in /*) ;; *) echo "plan path must be absolute: $path" >&2; exit 2;; esac
  done
  [ -f "$main_fragment" ] && [ ! -L "$main_fragment" ] || { echo 'main route fragment must be a regular file' >&2; exit 1; }
  [ -f "$temporary_fragment" ] && [ ! -L "$temporary_fragment" ] || { echo 'temporary route fragment must be a regular file' >&2; exit 1; }
  [ -f "$nginx_main_config" ] && [ ! -L "$nginx_main_config" ] || { echo 'nginx main config must be a regular file' >&2; exit 1; }
  [ "$MODE" = dry-run ] || [ -x "$nginx_bin" ] || { echo 'nginx binary is not executable' >&2; exit 1; }
  case "$ingress_health_url" in http://*|https://*) ;; *) echo 'ingress health URL must use HTTP(S)' >&2; exit 2;; esac
  case "$main_label$temporary_label" in *[!A-Za-z0-9._~-]*|'') echo 'route labels contain unsupported characters' >&2; exit 2;; esac
  [ "$main_label" != "$temporary_label" ] || { echo 'route labels must differ' >&2; exit 2; }
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

label_for() { [ "$1" = main ] && printf '%s' "$main_label" || printf '%s' "$temporary_label"; }
fragment_for() { [ "$1" = main ] && printf '%s' "$main_fragment" || printf '%s' "$temporary_fragment"; }

verify_ingress_once() {
  target="$1" expected="$(label_for "$target")" headers="$(mktemp -t juhe-ai-handover.XXXXXX)"
  case "$ingress_health_url" in
    */__aisys__/health) api_health_url="${ingress_health_url%/__aisys__/health}/__aisys__/api/health" ;;
    *) api_health_url="${ingress_health_url%/}/__aisys__/api/health" ;;
  esac
  curl -fsS --max-time 8 -D "$headers" -o /dev/null "$ingress_health_url"
  grep -Eiq "^${ROUTE_HEADER_NAME}:[[:space:]]*${expected}[[:space:]]*$" "$headers"
  curl -fsS --max-time 8 "$api_health_url" >/dev/null
  rm -f -- "$headers"
}

verify_ingress_stable() {
  target="$1" prior_size="$(stat -f '%z' "$access_log" 2>/dev/null || printf 0)" attempts=$((STABILITY_SECONDS / 5))
  [ "$attempts" -ge 12 ] || attempts=12
  for _ in $(seq 1 "$attempts"); do verify_ingress_once "$target"; sleep 5; done
  current_size="$(stat -f '%z' "$access_log" 2>/dev/null || printf 0)"
  [ "$current_size" -gt "$prior_size" ] || { echo 'ingress access log did not advance during stability proof' >&2; return 1; }
}

nginx_test_reload() { "$nginx_bin" -t -c "$nginx_main_config" && "$nginx_bin" -s reload -c "$nginx_main_config"; }

restore_route() {
  [ -f "$ROLLBACK_FRAGMENT" ] || return 1
  stage="$route_file.handover-restore.$$"
  cp -p -- "$ROLLBACK_FRAGMENT" "$stage"
  mv -f -- "$stage" "$route_file"
  nginx_test_reload && verify_ingress_stable "$SOURCE_LABEL"
}

on_exit() {
  code="$?"
  set +e
  if [ "$code" -ne 0 ] && [ "$SWITCH_ATTEMPTED" = 1 ]; then
    echo "handover failed; restoring $SOURCE_LABEL route" >&2
    if restore_route; then ROLLBACK_PROVEN=1; write_journal rollback-proven "$ACTION" "$SOURCE_LABEL" "$(journal_value target)"; else write_journal rollback-unproven "$ACTION" "$SOURCE_LABEL" "$(journal_value target)"; echo 'ROLLBACK_UNPROVEN: retain both slots and investigate' >&2; fi
  fi
  exit "$code"
}

read_config
printf 'mode=%s action=%s plan=%s route=%s\n' "$MODE" "$ACTION" "$PLAN_DIR" "$route_file"
[ "$MODE" = apply ] || exit 0
command -v curl >/dev/null

case "$ACTION" in
  status)
    [ -f "$JOURNAL" ] && cat "$JOURNAL" || printf 'state=uninitialized\n'
    exit 0
    ;;
  preflight)
    [ ! -f "$JOURNAL" ] || { echo 'existing handover journal requires recover or status' >&2; exit 1; }
    verify_ingress_stable main
    write_journal preflight preflight main temporary
    printf 'PREFLIGHT_OK\n'
    exit 0
    ;;
  takeover) SOURCE_LABEL=main; TARGET_LABEL=temporary ;;
  switchback) SOURCE_LABEL=temporary; TARGET_LABEL=main ;;
  recover)
    [ -f "$JOURNAL" ] || { echo 'no handover journal to recover' >&2; exit 1; }
    SOURCE_LABEL="$(journal_value source)"; case "$SOURCE_LABEL" in main|temporary) ;; *) echo 'invalid recovery source' >&2; exit 1;; esac
    restore_route || { write_journal rollback-unproven recover "$SOURCE_LABEL" unknown; echo 'ROLLBACK_UNPROVEN: retain both slots and investigate' >&2; exit 70; }
    write_journal rollback-proven recover "$SOURCE_LABEL" "$(journal_value target)"
    printf 'RECOVERY_OK route=%s\n' "$SOURCE_LABEL"
    exit 0
    ;;
esac

[ ! -f "$JOURNAL" ] || { echo 'existing handover journal requires recover or status' >&2; exit 1; }
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
write_journal stable "$ACTION" "$SOURCE_LABEL" "$TARGET_LABEL"
SWITCH_ATTEMPTED=0
trap - EXIT
write_journal committed "$ACTION" "$SOURCE_LABEL" "$TARGET_LABEL"
printf 'HANDOVER_OK target=%s; both performance slots remain running\n' "$TARGET_LABEL"
