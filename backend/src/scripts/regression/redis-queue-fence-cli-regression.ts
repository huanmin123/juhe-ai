import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const source = readFileSync(fileURLToPath(new URL('../operations/manage-redis-queue-fence.ts', import.meta.url)), 'utf8')
assert.match(source, /acquireRedisQueueFence\(/, 'fence CLI 必须复用原子 acquire')
assert.match(source, /releaseRedisQueueFence\(/, 'fence CLI 必须复用 compare-and-delete release')
assert.match(source, /releaseRedisQueueFenceIdempotently\(/, 'fence CLI 必须为崩溃恢复提供 absent-or-owned 幂等释放')
assert.match(source, /release-idempotent/, 'fence CLI 必须显式区分恢复释放 action')
assert.match(source, /JUHE_AI_QUEUE_FENCE_TOKEN/, 'fence CLI 必须从环境读取 token')
assert.doesNotMatch(source, /console\.(?:log|error)\([^\n]*fenceToken/, 'fence CLI 不得输出 token')
console.log('redis queue fence CLI regression passed')
