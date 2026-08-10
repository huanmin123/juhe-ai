#!/bin/sh
set -eu

if [ "${JUHE_AI_TABLE_MONITOR_STORE:-sqlite}" = "postgres" ]; then
  exec /usr/local/bin/juhe-ai-table-monitor "$@"
fi

stats_path="${JUHE_AI_STATS_DATABASE_PATH:?JUHE_AI_STATS_DATABASE_PATH is required}"
timeout_seconds="${JUHE_AI_TABLE_MONITOR_STARTUP_TIMEOUT_SECONDS:-90}"

case "$timeout_seconds" in
  ''|*[!0-9]*)
    echo "JUHE_AI_TABLE_MONITOR_STARTUP_TIMEOUT_SECONDS must be a non-negative integer" >&2
    exit 2
    ;;
esac

elapsed=0
until test -f "$stats_path"; do
  if [ "$elapsed" -ge "$timeout_seconds" ]; then
    echo "table monitor startup timed out waiting for stats SQLite source: $stats_path" >&2
    exit 1
  fi
  sleep 1
  elapsed=$((elapsed + 1))
done

exec /usr/local/bin/juhe-ai-table-monitor "$@"
