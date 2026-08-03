#!/usr/bin/env bash
set -euo pipefail

PROXY_URL='socks5h://127.0.0.1:7890'
TARGET_HOST='api.openai.com'
TARGET_URL='https://api.openai.com/'
PROXY_PORT=7890
SERVICE_LABEL=''

while [ "$#" -gt 0 ]; do
  case "$1" in
    --proxy-url) PROXY_URL="${2:?missing proxy URL}"; shift 2 ;;
    --proxy-port) PROXY_PORT="${2:?missing proxy port}"; shift 2 ;;
    --target-host) TARGET_HOST="${2:?missing target host}"; shift 2 ;;
    --target-url) TARGET_URL="${2:?missing target URL}"; shift 2 ;;
    --service-label) SERVICE_LABEL="${2:?missing service label}"; shift 2 ;;
    -h|--help) echo 'Read-only proxy/DNS diagnostics. No configuration is changed.'; exit 0 ;;
    *) echo "Unknown option: $1" >&2; exit 2 ;;
  esac
done
case "$PROXY_PORT" in ''|*[!0-9]*) echo 'proxy port must be numeric' >&2; exit 2;; esac
case "$TARGET_HOST" in ''|*[!A-Za-z0-9._-]*) echo 'invalid target host' >&2; exit 2;; esac

echo '== system =='
date -u '+%Y-%m-%dT%H:%M:%SZ'
sw_vers 2>/dev/null || true
uname -a

echo '== listeners =='
lsof -nP -iTCP:"$PROXY_PORT" -sTCP:LISTEN 2>/dev/null || true
if [ -n "$SERVICE_LABEL" ]; then
  launchctl print "gui/$(id -u)/$SERVICE_LABEL" 2>/dev/null | sed -n '1,80p' || true
fi

echo '== resolver summary =='
scutil --dns 2>/dev/null | sed -n '1,220p' || true
echo '== target resolution =='
dscacheutil -q host -a name "$TARGET_HOST" 2>/dev/null || true
if command -v dig >/dev/null 2>&1; then dig +time=3 +tries=1 "$TARGET_HOST" A "$TARGET_HOST" AAAA || true; fi

echo '== connectivity (status only) =='
curl -sS -o /dev/null --max-time 10 -w 'direct_http=%{http_code} remote_ip=%{remote_ip} connect=%{time_connect} total=%{time_total}\n' "$TARGET_URL" || true
curl -sS -o /dev/null --max-time 15 --proxy "$PROXY_URL" -w 'proxy_http=%{http_code} remote_ip=%{remote_ip} connect=%{time_connect} total=%{time_total}\n' "$TARGET_URL" || true
echo 'diagnostics complete; proxy credentials and configuration content were not printed'
