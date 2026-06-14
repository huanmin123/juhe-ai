import { strict as assert } from 'node:assert'

import {
  AccountSideEffectQueue,
  type QueuedAccountSideEffect,
  type StreamFailureOperation
} from '../../modules/gateway/runtime/account-side-effect-queue.js'

const queue = new AccountSideEffectQueue()

assert.equal(queue.length, 0, '新队列初始长度应为 0')
assert.equal(queue.peek(), undefined, '空队列 peek 应返回 undefined')
assert.equal(queue.pop(), undefined, '空队列 pop 应返回 undefined')

queue.push(queuedStreamFailure('late', 300, 1))
queue.push(queuedStreamFailure('tie-newer', 200, 2))
queue.push(queuedStreamFailure('early', 100, 3))
queue.push(queuedStreamFailure('tie-older', 200, 1))

assert.equal(streamAccountId(queue.peek()), 'early', '队首应为 nextAttemptAtMs 最早的副作用')
assert.deepEqual(
  drainAccountIds(queue),
  ['early', 'tie-older', 'tie-newer', 'late'],
  '队列出队顺序应按 nextAttemptAtMs、enqueuedAtMs 排序'
)

const replaceQueue = new AccountSideEffectQueue()
replaceQueue.push(queuedStreamFailure('first', 100, 1))
replaceQueue.push(queuedStreamFailure('second', 200, 2))
replaceQueue.push(queuedStreamFailure('third', 300, 3))

const thirdIndex = replaceQueue.findIndex((item) => streamAccountId(item) === 'third')
assert.notEqual(thirdIndex, -1, 'findIndex 应能在堆数组中找到目标项')
replaceQueue.replaceAt(thirdIndex, queuedStreamFailure('third', 50, 4))
replaceQueue.replaceAt(-1, queuedStreamFailure('ignored', 1, 1))
replaceQueue.replaceAt(999, queuedStreamFailure('ignored', 1, 1))

assert.deepEqual(
  drainAccountIds(replaceQueue),
  ['third', 'first', 'second'],
  'replaceAt 后应重新建堆，并忽略越界替换'
)

const removeQueue = new AccountSideEffectQueue()
removeQueue.push(queuedStreamFailure('keep-late', 400, 4))
removeQueue.push(queuedStreamFailure('remove-early', 100, 1))
removeQueue.push(queuedStreamFailure('keep-early', 200, 2))
removeQueue.push(queuedStreamFailure('remove-late', 300, 3))

assert.equal(
  removeQueue.removeWhere((item) => streamAccountId(item).startsWith('remove-')),
  2,
  'removeWhere 应返回删除数量'
)
assert.equal(removeQueue.length, 2, 'removeWhere 后队列长度应减少')
assert.equal(removeQueue.removeWhere((item) => streamAccountId(item) === 'missing'), 0, '无匹配删除应返回 0')
assert.deepEqual(
  drainAccountIds(removeQueue),
  ['keep-early', 'keep-late'],
  'removeWhere 后剩余元素仍应保持堆序'
)

const clearQueue = new AccountSideEffectQueue()
clearQueue.push(queuedStreamFailure('clear-a', 100, 1))
clearQueue.push(queuedStreamFailure('clear-b', 200, 2))
clearQueue.clear()
assert.equal(clearQueue.length, 0, 'clear 应清空队列')
assert.equal(clearQueue.pop(), undefined, 'clear 后 pop 应返回 undefined')

console.log('网关账号副作用内存优先队列回归通过：push/pop、replaceAt、removeWhere 和 clear 均保持堆序语义')

function queuedStreamFailure(accountId: string, nextAttemptAtMs: number, enqueuedAtMs: number): QueuedAccountSideEffect {
  return {
    operation: streamFailureOperation(accountId),
    attempts: 0,
    enqueuedAtMs,
    nextAttemptAtMs,
    expiresAtMs: nextAttemptAtMs + 60_000
  }
}

function streamFailureOperation(accountId: string): StreamFailureOperation {
  return {
    type: 'record_account_stream_failure',
    input: {
      accountId,
      thresholdCount: 3,
      thresholdWindowMinutes: 1,
      action: 'cooldown',
      reason: 'side effect queue structure regression'
    }
  }
}

function drainAccountIds(queueToDrain: AccountSideEffectQueue): string[] {
  const accountIds: string[] = []
  while (queueToDrain.length > 0) {
    accountIds.push(streamAccountId(queueToDrain.pop()))
  }
  return accountIds
}

function streamAccountId(item: QueuedAccountSideEffect | undefined): string {
  assert.ok(item, '队列项不能为空')
  assert.equal(item.operation.type, 'record_account_stream_failure', '测试队列项应为流失败计数副作用')
  return item.operation.input.accountId
}
