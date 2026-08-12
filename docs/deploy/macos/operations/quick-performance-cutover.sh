#!/usr/bin/env bash
set -euo pipefail

# Routine release only: candidate is already running. Check it once, show its
# startup log, switch one route file, then check the public entry once.
MODE=dry-run
ACTIVE_ROUTE=
CANDIDATE_ROUTE=
NGINX_CONFIG=
NGINX_BIN=nginx
CANDIDATE_CONTROL=
CANDIDATE_API=
CANDIDATE_GATEWAY=
GO_HEALTH=
PUBLIC_CONTROL=
PUBLIC_API=
PUBLIC_GATEWAY=
STARTUP_LOG=
BACKUP=
SWITCHED=0

usage() {
  cat <<'EOF'
Usage: quick-performance-cutover.sh [--dry-run|--apply] [options]
  --active-route-file PATH --candidate-route-file PATH --nginx-main-config PATH
  --candidate-control-url URL --candidate-api-health-url URL --candidate-gateway-url URL
  --go-health-url URL
  --ingress-control-url URL --ingress-api-health-url URL --ingress-gateway-url URL
  --startup-log PATH [--nginx-bin PATH]

Apply checks the running candidate once, prints the last 80 startup-log lines,
replaces one route fragment, reloads Nginx, then checks public control/API/
gateway once. A failed public check restores the previous route automatically.
EOF
}

die() { printf 'ERROR %s\n' "$*" >&2; exit 2; }

while [ "$#" -gt 0 ]; do
  case "$1" in
    --dry-run) MODE=dry-run; shift ;;
    --apply) MODE=apply; shift ;;
    --active-route-file) ACTIVE_ROUTE="${2:-}"; shift 2 ;;
    --candidate-route-file) CANDIDATE_ROUTE="${2:-}"; shift 2 ;;
    --nginx-main-config) NGINX_CONFIG="${2:-}"; shift 2 ;;
    --nginx-bin) NGINX_BIN="${2:-}"; shift 2 ;;
    --candidate-control-url) CANDIDATE_CONTROL="${2:-}"; shift 2 ;;
    --candidate-api-health-url) CANDIDATE_API="${2:-}"; shift 2 ;;
    --candidate-gateway-url) CANDIDATE_GATEWAY="${2:-}"; shift 2 ;;
    --go-health-url) GO_HEALTH="${2:-}"; shift 2 ;;
    --ingress-control-url) PUBLIC_CONTROL="${2:-}"; shift 2 ;;
    --ingress-api-health-url) PUBLIC_API="${2:-}"; shift 2 ;;
    --ingress-gateway-url) PUBLIC_GATEWAY="${2:-}"; shift 2 ;;
    --startup-log) STARTUP_LOG="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown option: $1" ;;
  esac
done

case "$MODE" in dry-run|apply) ;; *) die 'mode must be dry-run or apply' ;; esac
for value in "$ACTIVE_ROUTE" "$CANDIDATE_ROUTE" "$NGINX_CONFIG" "$CANDIDATE_CONTROL" "$CANDIDATE_API" "$CANDIDATE_GATEWAY" "$GO_HEALTH" "$PUBLIC_CONTROL" "$PUBLIC_API" "$PUBLIC_GATEWAY" "$STARTUP_LOG"; do
  [ -n "$value" ] || die 'missing required option'
done
[ -f "$ACTIVE_ROUTE" ] || die "active route is missing: $ACTIVE_ROUTE"
[ -f "$CANDIDATE_ROUTE" ] || die "candidate route is missing: $CANDIDATE_ROUTE"
[ -f "$NGINX_CONFIG" ] || die "Nginx config is missing: $NGINX_CONFIG"
[ -f "$STARTUP_LOG" ] || die "startup log is missing: $STARTUP_LOG"
[ "$ACTIVE_ROUTE" != "$CANDIDATE_ROUTE" ] || die 'active and candidate route files must differ'

probe_ok() { curl -fsS --max-time 5 -o /dev/null "$1"; }
probe_gateway() {
  status="$(curl -sS --max-time 8 -o /dev/null -w '%{http_code}' "$1")" || return 1
  [ "$status" = 401 ]
}
reload_nginx() { "$NGINX_BIN" -t -c "$NGINX_CONFIG" && "$NGINX_BIN" -s reload -c "$NGINX_CONFIG"; }
restore_route() {
  [ -n "$BACKUP" ] && [ -f "$BACKUP" ] || return 1
  cp -p -- "$BACKUP" "$ACTIVE_ROUTE.quick-restore.$$"
  mv -f -- "$ACTIVE_ROUTE.quick-restore.$$" "$ACTIVE_ROUTE"
  reload_nginx
}
on_exit() {
  code="$?"
  trap - EXIT INT TERM
  set +e
  if [ "$code" -ne 0 ] && [ "$SWITCHED" = 1 ]; then
    printf 'quick cutover failed; restoring the original route\n' >&2
    restore_route || printf 'ROLLBACK_UNPROVEN: original route could not be restored automatically\n' >&2
  fi
  [ -z "$BACKUP" ] || rm -f -- "$BACKUP"
  exit "$code"
}

printf 'mode=%s active_route=%s candidate_route=%s\n' "$MODE" "$ACTIVE_ROUTE" "$CANDIDATE_ROUTE"
[ "$MODE" = apply ] || exit 0
command -v curl >/dev/null || die 'curl is required'
NGINX_BIN="$(command -v "$NGINX_BIN" 2>/dev/null || true)"
[ -n "$NGINX_BIN" ] || die 'nginx is not executable'

probe_ok "$CANDIDATE_CONTROL"
probe_ok "$CANDIDATE_API"
probe_gateway "$CANDIDATE_GATEWAY"
probe_ok "$GO_HEALTH"
tail -n 80 "$STARTUP_LOG"

BACKUP="$ACTIVE_ROUTE.quick-backup.$$"
cp -p -- "$ACTIVE_ROUTE" "$BACKUP"
cp -p -- "$CANDIDATE_ROUTE" "$ACTIVE_ROUTE.quick-next.$$"
trap on_exit EXIT INT TERM
SWITCHED=1
mv -f -- "$ACTIVE_ROUTE.quick-next.$$" "$ACTIVE_ROUTE"
reload_nginx
probe_ok "$PUBLIC_CONTROL"
probe_ok "$PUBLIC_API"
probe_gateway "$PUBLIC_GATEWAY"
SWITCHED=0
trap - EXIT INT TERM
rm -f -- "$BACKUP"
BACKUP=
printf 'QUICK_CUTOVER_OK route=%s; previous slot remains available for rollback\n' "$ACTIVE_ROUTE"
