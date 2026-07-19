#!/usr/bin/env bash
set -euo pipefail

RELEASE=''
ENV_FILE=''
SCOPE=''
CACHE_LABEL=''
STATE_LABEL=''
QUEUE_LABEL=''
BASE_DIR=/Users/huanmin/juhe-ai-lite

usage() {
  cat <<'EOF'
Usage: verify-redis-role-isolation.sh --release <absolute-release> --env-file <absolute-env> \
  --scope <main|temporary> --cache-label <label> --state-label <label> --queue-label <label>

Read-only commands: PING, CONFIG GET, INFO persistence, INFO server, launchctl print and lsof.
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --release) RELEASE="${2:?missing release}"; shift 2 ;;
    --env-file) ENV_FILE="${2:?missing env file}"; shift 2 ;;
    --scope) SCOPE="${2:?missing scope}"; shift 2 ;;
    --cache-label) CACHE_LABEL="${2:?missing cache label}"; shift 2 ;;
    --state-label) STATE_LABEL="${2:?missing state label}"; shift 2 ;;
    --queue-label) QUEUE_LABEL="${2:?missing queue label}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown option: $1" >&2; usage >&2; exit 2 ;;
  esac
done

case "$SCOPE" in main|temporary) ;; *) echo 'scope must be main or temporary' >&2; exit 2;; esac
for path in "$RELEASE" "$ENV_FILE"; do case "$path" in /*) ;; *) echo "path must be absolute: $path" >&2; exit 2;; esac; done
[ -d "$RELEASE/backend" ] || { echo 'release backend directory is missing' >&2; exit 1; }
[ -r "$ENV_FILE" ] || { echo 'env file is not readable' >&2; exit 1; }

if [ "$SCOPE" = main ]; then
  EXPECTED_CACHE_LABEL=top.huanmin.juhe-ai-lite.redis-cache
  EXPECTED_STATE_LABEL=top.huanmin.juhe-ai-lite.redis-state
  EXPECTED_QUEUE_LABEL=top.huanmin.juhe-ai-lite.redis-queue
else
  EXPECTED_CACHE_LABEL=top.huanmin.juhe-ai-lite.redis.temporary.cache
  EXPECTED_STATE_LABEL=top.huanmin.juhe-ai-lite.redis.temporary.state
  EXPECTED_QUEUE_LABEL=top.huanmin.juhe-ai-lite.redis.temporary.queue
fi
[ "$CACHE_LABEL" = "$EXPECTED_CACHE_LABEL" ] || { echo "cache label must be $EXPECTED_CACHE_LABEL" >&2; exit 2; }
[ "$STATE_LABEL" = "$EXPECTED_STATE_LABEL" ] || { echo "state label must be $EXPECTED_STATE_LABEL" >&2; exit 2; }
[ "$QUEUE_LABEL" = "$EXPECTED_QUEUE_LABEL" ] || { echo "queue label must be $EXPECTED_QUEUE_LABEL" >&2; exit 2; }

if [ "$SCOPE" = main ]; then
  EXPECTED_PORTS='cache=6379,state=6380,queue=6381'
  EXPECTED_DIRS="cache=$BASE_DIR/shared/redis-cache,state=$BASE_DIR/redis/main/state/data,queue=$BASE_DIR/redis/main/queue/data"
  EXPECTED_LOGS="cache=$BASE_DIR/logs/redis-cache.log,state=$BASE_DIR/redis/main/state/logs/redis.log,queue=$BASE_DIR/redis/main/queue/logs/redis.log"
else
  EXPECTED_PORTS='cache=16379,state=16380,queue=16381'
  EXPECTED_DIRS="cache=$BASE_DIR/redis/temporary/cache/data,state=$BASE_DIR/redis/temporary/state/data,queue=$BASE_DIR/redis/temporary/queue/data"
  EXPECTED_LOGS="cache=$BASE_DIR/redis/temporary/cache/logs/redis.log,state=$BASE_DIR/redis/temporary/state/logs/redis.log,queue=$BASE_DIR/redis/temporary/queue/logs/redis.log"
fi

launchd_pid() {
  launchctl print "system/$1" 2>/dev/null | awk '/^[[:space:]]*pid = [0-9]+/ {print $3; exit}'
}

owner_binding() {
  local role="$1" label="$2" port="$3" job_pid port_pids
  launchctl print "system/$label" >/dev/null
  job_pid="$(launchd_pid "$label")"
  port_pids="$(lsof -tiTCP:"$port" -sTCP:LISTEN 2>/dev/null | sort -u)"
  [ -n "$job_pid" ] && [ "$port_pids" = "$job_pid" ] || {
    echo "Redis owner mismatch role=$role label=$label launchd=${job_pid:-missing} port=${port_pids:-free}" >&2
    return 1
  }
  printf '%s=%s\n' "$role" "$job_pid"
}

CACHE_PID="$(owner_binding cache "$CACHE_LABEL" "$(printf '%s' "$EXPECTED_PORTS" | sed -n 's/.*cache=\([0-9]*\).*/\1/p')")"
STATE_PID="$(owner_binding state "$STATE_LABEL" "$(printf '%s' "$EXPECTED_PORTS" | sed -n 's/.*state=\([0-9]*\).*/\1/p')")"
QUEUE_PID="$(owner_binding queue "$QUEUE_LABEL" "$(printf '%s' "$EXPECTED_PORTS" | sed -n 's/.*queue=\([0-9]*\).*/\1/p')")"

export JUHE_AI_REDIS_ROLE_ENV_FILE="$ENV_FILE"
export JUHE_AI_REDIS_ROLE_SCOPE="$SCOPE"
export JUHE_AI_EXPECTED_REDIS_PORTS="$EXPECTED_PORTS"
export JUHE_AI_EXPECTED_REDIS_PIDS="$CACHE_PID,$STATE_PID,$QUEUE_PID"
export JUHE_AI_EXPECTED_REDIS_DIRS="$EXPECTED_DIRS"
export JUHE_AI_EXPECTED_REDIS_LOGS="$EXPECTED_LOGS"
(
  cd "$RELEASE/backend"
  node --input-type=module <<'NODE'
import { readFileSync } from 'node:fs'
import { parse } from 'dotenv'
import { createClient } from 'redis'

const env = parse(readFileSync(process.env.JUHE_AI_REDIS_ROLE_ENV_FILE, 'utf8'))
const scope = process.env.JUHE_AI_REDIS_ROLE_SCOPE
const keyValueMap = (raw) => new Map(raw.split(',').map((entry) => {
  const index = entry.indexOf('=')
  return [entry.slice(0, index), entry.slice(index + 1)]
}))
const expectedPorts = keyValueMap(process.env.JUHE_AI_EXPECTED_REDIS_PORTS)
const expectedPids = keyValueMap(process.env.JUHE_AI_EXPECTED_REDIS_PIDS)
const expectedDirs = keyValueMap(process.env.JUHE_AI_EXPECTED_REDIS_DIRS)
const expectedLogs = keyValueMap(process.env.JUHE_AI_EXPECTED_REDIS_LOGS)
const roles = ['cache', 'state', 'queue']
const urls = [env.JUHE_AI_REDIS_CACHE_URL, env.JUHE_AI_REDIS_STATE_URL, env.JUHE_AI_REDIS_QUEUE_URL]
if (urls.some((url) => !url)) throw new Error('Redis role URL 配置不完整')
const parsedUrls = urls.map((value) => new URL(value))
const physicalEndpoints = parsedUrls.map((url) => `${url.hostname}:${url.port || '6379'}`)
if (new Set(physicalEndpoints).size !== roles.length) throw new Error('Redis cache/state/queue 未物理隔离')

const processIds = []
const results = []
for (let index = 0; index < roles.length; index += 1) {
  const role = roles[index]
  const url = parsedUrls[index]
  if (url.hostname !== '127.0.0.1' || (url.port || '6379') !== expectedPorts.get(role)) {
    throw new Error(`${scope} ${role} Redis endpoint 不符合固定角色端口`)
  }
  const client = createClient({ url: urls[index] })
  client.on('error', () => undefined)
  await client.connect()
  try {
    if (await client.ping() !== 'PONG') throw new Error(`${role} PING failed`)
    const config = await client.sendCommand(['CONFIG', 'GET', 'appendonly', 'appendfsync', 'save', 'maxmemory-policy', 'dir', 'logfile'])
    const map = config && typeof config === 'object' && !Array.isArray(config)
      ? new Map(Object.entries(config).map(([key, value]) => [key, String(value)]))
      : new Map(Array.from({ length: Math.floor(config.length / 2) }, (_, entryIndex) => [String(config[entryIndex * 2]), String(config[entryIndex * 2 + 1])]))
    const persistence = await client.info('persistence')
    const server = await client.info('server')
    const processId = Number(/^process_id:(\d+)/m.exec(server)?.[1])
    if (String(processId) !== expectedPids.get(role)) throw new Error(`${role} INFO PID 与 launchd/lsof owner 不一致`)
    if (map.get('dir') !== expectedDirs.get(role) || map.get('logfile') !== expectedLogs.get(role)) {
      throw new Error(`${role} Redis dir/logfile 不属于固定角色目录`)
    }
    processIds.push(processId)
    const expected = role === 'cache' ? { appendonly: 'no', policy: 'allkeys-lru' }
      : role === 'state' ? { appendonly: 'no', policy: 'noeviction' }
        : { appendonly: 'yes', policy: 'noeviction' }
    if (map.get('appendonly') !== expected.appendonly || map.get('save') !== '' || map.get('maxmemory-policy') !== expected.policy) {
      throw new Error(`${role} Redis 持久化或淘汰策略不符合角色契约`)
    }
    if (role === 'queue' && map.get('appendfsync') !== 'everysec') throw new Error('queue Redis appendfsync 必须为 everysec')
    const aofEnabled = /^aof_enabled:(\d+)/m.exec(persistence)?.[1]
    if (aofEnabled !== (role === 'queue' ? '1' : '0')) throw new Error(`${role} Redis AOF live 状态错误`)
    results.push({ role, endpoint: physicalEndpoints[index], processId, dir: map.get('dir'), logfile: map.get('logfile') })
  } finally {
    await client.quit()
  }
}
if (new Set(processIds).size !== roles.length) throw new Error('Redis cache/state/queue 共享同一 processId')
console.log(JSON.stringify({ scope, roles: results }))
NODE
)
unset JUHE_AI_REDIS_ROLE_ENV_FILE JUHE_AI_REDIS_ROLE_SCOPE JUHE_AI_EXPECTED_REDIS_PORTS \
  JUHE_AI_EXPECTED_REDIS_PIDS JUHE_AI_EXPECTED_REDIS_DIRS JUHE_AI_EXPECTED_REDIS_LOGS
printf 'REDIS_ROLE_ISOLATION_OK scope=%s\n' "$SCOPE"
