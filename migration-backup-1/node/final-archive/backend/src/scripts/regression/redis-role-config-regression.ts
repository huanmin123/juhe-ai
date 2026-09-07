import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const scriptDir = dirname(fileURLToPath(import.meta.url))
const repoRoot = resolve(scriptDir, '../../../..')
const composeSource = readFileSync(resolve(repoRoot, 'docker/compose.performance.yml'), 'utf8')
const envSource = readFileSync(resolve(repoRoot, 'docker/.env.performance.example'), 'utf8')

const cache = serviceBlock('redis-cache', 'redis-state')
const state = serviceBlock('redis-state', 'redis-queue')
const queue = serviceBlock('redis-queue', 'juhe-ai')

assert.match(cache, /--appendonly no/, 'redis-cache 必须关闭 AOF')
assert.match(cache, /--save ""/, 'redis-cache 必须关闭 RDB')
assert.match(cache, /--maxmemory-policy allkeys-lru/, 'redis-cache 必须允许 LRU 淘汰')
assert.match(cache, /JUHE_AI_REDIS_CACHE_PORT:-6379/, 'redis-cache 默认端口必须是 6379')

assert.match(state, /--appendonly no/, 'redis-state 必须关闭 AOF')
assert.match(state, /--save ""/, 'redis-state 必须关闭 RDB')
assert.match(state, /--maxmemory-policy noeviction/, 'redis-state 不允许淘汰运行态 key')
assert.match(state, /JUHE_AI_REDIS_STATE_PORT:-6380/, 'redis-state 默认端口必须是 6380')

assert.match(queue, /--appendonly yes/, 'redis-queue 必须开启 AOF')
assert.match(queue, /--appendfsync everysec/, 'redis-queue 必须使用 everysec fsync')
assert.match(queue, /--save ""/, 'redis-queue 必须关闭 RDB，避免重复 fork')
assert.match(queue, /--auto-aof-rewrite-min-size "\$\$\{REDIS_QUEUE_AOF_REWRITE_MIN_SIZE:-1gb\}"/, 'redis-queue 必须显式配置 1GB 初始 rewrite 门槛')
assert.match(queue, /--maxmemory-policy noeviction/, 'redis-queue 不允许淘汰未确认消息')
assert.match(queue, /JUHE_AI_REDIS_QUEUE_PORT:-6381/, 'redis-queue 默认端口必须是 6381')

assert.match(envSource, /^JUHE_AI_REDIS_CACHE_MAXMEMORY=768mb$/m, 'cache 示例容量必须是 768mb')
assert.match(envSource, /^JUHE_AI_REDIS_STATE_MAXMEMORY=2048mb$/m, 'state 首次切换保持 2GB 上限')
assert.match(envSource, /^JUHE_AI_REDIS_QUEUE_MAXMEMORY=2048mb$/m, 'queue 示例容量必须是 2GB')
assert.match(envSource, /^JUHE_AI_REDIS_QUEUE_AOF_REWRITE_MIN_SIZE=1gb$/m, 'queue 示例必须显式声明 AOF rewrite 初始门槛')

console.log('redis-role-config-regression passed')

function serviceBlock(name: string, nextName: string): string {
  const pattern = new RegExp(`^  ${escapeRegExp(name)}:\\r?\\n([\\s\\S]*?)(?=^  ${escapeRegExp(nextName)}:\\r?$)`, 'm')
  const match = composeSource.match(pattern)
  assert.ok(match, `compose 中缺少 ${name} service block`)
  return match[0]
}

function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
}
