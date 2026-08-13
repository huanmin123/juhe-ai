import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const queueFiles = [
  '../../modules/gateway/usage/record-queue.service.ts',
  '../../modules/public-api-logs/public-api-log-queue.service.ts',
  '../../modules/record-maintenance/record-maintenance-queue.service.ts'
] as const

for (const relativePath of queueFiles) {
  const source = readFileSync(new URL(relativePath, import.meta.url), 'utf8')
  assert.doesNotMatch(source, /scheduleProcessFatalError/, `${relativePath} 的 Redis 瞬时失败不得升级为主进程 fatal`)
  assert.match(source, /redis_stream|Redis Stream|redis.*enqueue/i, `${relativePath} 必须保留 Redis Stream 运行路径`)
}

const invalidationSource = readFileSync(new URL('../../shared/gateway-cache-invalidation.ts', import.meta.url), 'utf8')
assert.doesNotMatch(invalidationSource, /scheduleProcessFatalError/, 'Redis runtime state 瞬时失败不得调度进程 fatal')
assert.match(invalidationSource, /gateway_cache_invalidation_runtime_state_publish_failed/, 'runtime state 失败必须记录受控错误事件')

console.log('redis-runtime-failure-boundary-regression passed')
