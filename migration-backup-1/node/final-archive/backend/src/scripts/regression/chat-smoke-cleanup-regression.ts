import assert from 'node:assert/strict'

import { runChatSmokeWithCleanup } from './chat-smoke-cleanup.js'

const successEvents: string[] = []
await runChatSmokeWithCleanup({
  run: async () => { successEvents.push('run') },
  cleanupSteps: [
    { name: 'cleanup-1', run: async () => { successEvents.push('cleanup-1') } },
    { name: 'cleanup-2', run: async () => { successEvents.push('cleanup-2') } }
  ],
  onSuccess: () => { successEvents.push('success') }
})
assert.deepEqual(successEvents, ['run', 'cleanup-1', 'cleanup-2', 'success'], '成功提示只能出现在全部清理完成后')

const runError = new Error('smoke 执行失败')
const cleanupError = new Error('临时数据库删除失败')
const aggregateEvents: string[] = []
await assert.rejects(
  runChatSmokeWithCleanup({
    run: async () => { aggregateEvents.push('run'); throw runError },
    cleanupSteps: [
      { name: 'cleanup-failed', run: async () => { aggregateEvents.push('cleanup-failed'); throw cleanupError } },
      { name: 'cleanup-after-failure', run: async () => { aggregateEvents.push('cleanup-after-failure') } }
    ],
    onSuccess: () => { aggregateEvents.push('success') }
  }),
  (error) => error instanceof AggregateError
    && error.message === 'AI 问答 smoke 执行失败，且清理失败'
    && error.errors.length === 2
    && error.errors[0] === runError
    && error.errors[1] === cleanupError
)
assert.deepEqual(aggregateEvents, ['run', 'cleanup-failed', 'cleanup-after-failure'], '清理失败后仍应继续后续清理且不得打印成功')

const cleanupOnlyError = new Error('Redis 连接关闭失败')
let cleanupOnlySuccess = false
await assert.rejects(
  runChatSmokeWithCleanup({
    run: async () => undefined,
    cleanupSteps: [{ name: 'redis-quit', run: async () => { throw cleanupOnlyError } }],
    onSuccess: () => { cleanupOnlySuccess = true }
  }),
  (error) => error === cleanupOnlyError,
  '单独清理失败必须传播原始错误并令进程非零退出'
)
assert.equal(cleanupOnlySuccess, false)

console.log('AI 问答 smoke 清理错误传播回归通过')
