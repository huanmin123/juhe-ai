#!/usr/bin/env bash
set -euo pipefail

RELEASE=''
ENV_FILE=''
SCOPE=''
CACHE_LABEL=''
STATE_LABEL=''
QUEUE_LABEL=''

usage() {
  cat <<'EOF'
Usage: verify-redis-role-isolation.sh --release <absolute-release> --env-file <absolute-env> \
  --scope <main|temporary> --cache-label <label> --state-label <label> --queue-label <label>

Read-only commands: PING, CONFIG GET, INFO persistence, INFO server, launchctl print.
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
for label in "$CACHE_LABEL" "$STATE_LABEL" "$QUEUE_LABEL"; do
  case "$label" in ''|.*|*[!A-Za-z0-9._-]*) echo "invalid launchd label: $label" >&2; exit 2;; esac
done
[ -d "$RELEASE/backend" ] || { echo 'release backend directory is missing' >&2; exit 1; }
[ -r "$ENV_FILE" ] || { echo 'env file is not readable' >&2; exit 1; }

for label in "$CACHE_LABEL" "$STATE_LABEL" "$QUEUE_LABEL"; do
  launchctl print "system/$label" >/dev/null
done

export JUHE_AI_REDIS_ROLE_ENV_FILE="$ENV_FILE"
export JUHE_AI_REDIS_ROLE_SCOPE="$SCOPE"
(
  cd "$RELEASE/backend"
  node --input-type=module <<'NODE'
import { readFileSync } from 'node:fs'
import { parse } from 'dotenv'
import { createClient } from 'redis'

const env = parse(readFileSync(process.env.JUHE_AI_REDIS_ROLE_ENV_FILE, 'utf8'))
const scope = process.env.JUHE_AI_REDIS_ROLE_SCOPE
const expectedPorts = scope === 'main'
  ? { cache: '6379', state: '6380', queue: '6381' }
  : { cache: '16379', state: '16380', queue: '16381' }
const roles = ['cache', 'state', 'queue']
const urls = [
  env.JUHE_AI_REDIS_CACHE_URL,
  env.JUHE_AI_REDIS_STATE_URL,
  env.JUHE_AI_REDIS_QUEUE_URL
]
if (urls.some((url) => !url)) throw new Error('Redis role URL 配置不完整')

const parsedUrls = urls.map((value) => new URL(value))
const physicalEndpoints = parsedUrls.map((url) => `${url.protocol}//${url.hostname}:${url.port || '6379'}`)
if (new Set(physicalEndpoints).size !== roles.length) throw new Error('Redis cache/state/queue 未物理隔离')
for (let index = 0; index < roles.length; index += 1) {
  const role = roles[index]
  const url = parsedUrls[index]
  if (url.hostname !== '127.0.0.1' || (url.port || '6379') !== expectedPorts[role]) {
    throw new Error(`${scope} ${role} Redis 必须使用 127.0.0.1:${expectedPorts[role]}`)
  }
}

const processIds = []
const results = []
for (let index = 0; index < roles.length; index += 1) {
  const role = roles[index]
  const client = createClient({ url: urls[index] })
  client.on('error', () => undefined)
  await client.connect()
  try {
    if (await client.ping() !== 'PONG') throw new Error(`${role} PING failed`)
    const config = await client.sendCommand(['CONFIG', 'GET',
      'appendonly', 'appendfsync', 'save', 'maxmemory-policy', 'dir', 'logfile'])
    const map = new Map()
    for (let i = 0; i + 1 < config.length; i += 2) map.set(String(config[i]), String(config[i + 1]))
    const persistence = await client.info('persistence')
    const server = await client.info('server')
    const processId = Number(/^process_id:(\d+)/m.exec(server)?.[1])
    if (!Number.isInteger(processId) || processId <= 1) throw new Error(`${role} Redis process_id 无效`)
    processIds.push(processId)
    const expected = role === 'cache'
      ? { appendonly: 'no', policy: 'allkeys-lru' }
      : role === 'state'
        ? { appendonly: 'no', policy: 'noeviction' }
        : { appendonly: 'yes', policy: 'noeviction' }
    if (map.get('appendonly') !== expected.appendonly
      || map.get('save') !== ''
      || map.get('maxmemory-policy') !== expected.policy) {
      throw new Error(`${role} Redis 持久化或淘汰策略不符合角色契约`)
    }
    if (role === 'queue' && map.get('appendfsync') !== 'everysec') {
      throw new Error('queue Redis appendfsync 必须为 everysec')
    }
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
unset JUHE_AI_REDIS_ROLE_ENV_FILE JUHE_AI_REDIS_ROLE_SCOPE
printf 'REDIS_ROLE_ISOLATION_OK scope=%s\n' "$SCOPE"
