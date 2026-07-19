import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { runRedisEnqueueWithBoundedRetry } from '../../shared/redis-enqueue-retry.js'

let attempts = 0
await runRedisEnqueueWithBoundedRetry(async () => {
  attempts += 1
  if (attempts < 3) throw new Error('ECONNRESET')
}, { delaysMs: [0, 0] })
assert.equal(attempts, 3, '瞬时错误最多三次内恢复时应成功')

attempts = 0
await assert.rejects(
  runRedisEnqueueWithBoundedRetry(async () => {
    attempts += 1
    throw new Error('command timeout')
  }, { delaysMs: [0, 0] }),
  /command timeout/
)
assert.equal(attempts, 3, '耗尽后必须抛出原始失败且总尝试次数为三次')

attempts = 0
await assert.rejects(
  runRedisEnqueueWithBoundedRetry(async () => {
    attempts += 1
    throw new Error('QUEUE_QUIESCED')
  }, { delaysMs: [0, 0] }),
  /QUEUE_QUIESCED/
)
assert.equal(attempts, 1, '发布 fence 拒绝是确定性结果，不得重试')

for (const relativePath of [
  '../../modules/gateway/usage/record-queue.service.ts',
  '../../modules/audit-logs/audit-log-queue.service.ts'
]) {
  const source = readFileSync(fileURLToPath(new URL(relativePath, import.meta.url)), 'utf8')
  assert.match(source, /runRedisEnqueueWithBoundedRetry\(/, `${relativePath} 必须使用共享有界重试`)
}

console.log('redis enqueue retry regression passed')
