import { strict as assert } from 'node:assert'

import {
  AccountSideEffectQueue,
  type QueuedAccountSideEffect,
  type AccountErrorHandlingOperation
} from '../../modules/gateway/runtime/account-side-effect-queue.js'

const queue = new AccountSideEffectQueue()

assert.equal(queue.length, 0, '新队列初始长度应为 0')
assert.equal(queue.peek(), undefined, '空队列 peek 应返回 undefined')
assert.equal(queue.pop(), undefined, '空队列 pop 应返回 undefined')

queue.push(queuedAccountErrorHandling('late', 300, 1))
queue.push(queuedAccountErrorHandling('tie-newer', 200, 2))
queue.push(queuedAccountErrorHandling('early', 100, 3))
queue.push(queuedAccountErrorHandling('tie-older', 200, 1))

assert.equal(operationAccountId(queue.peek()), 'early', '队首应为 nextAttemptAtMs 最早的副作用')
assert.deepEqual(
  drainAccountIds(queue),
  ['early', 'tie-older', 'tie-newer', 'late'],
  '队列出队顺序应按 nextAttemptAtMs、enqueuedAtMs 排序'
)

const replaceQueue = new AccountSideEffectQueue()
replaceQueue.push(queuedAccountErrorHandling('first', 100, 1))
replaceQueue.push(queuedAccountErrorHandling('second', 200, 2))
replaceQueue.push(queuedAccountErrorHandling('third', 300, 3))

const thirdIndex = replaceQueue.findIndex((item) => operationAccountId(item) === 'third')
assert.notEqual(thirdIndex, -1, 'findIndex 应能在堆数组中找到目标项')
replaceQueue.replaceAt(thirdIndex, queuedAccountErrorHandling('third', 50, 4))
replaceQueue.replaceAt(-1, queuedAccountErrorHandling('ignored', 1, 1))
replaceQueue.replaceAt(999, queuedAccountErrorHandling('ignored', 1, 1))

assert.deepEqual(
  drainAccountIds(replaceQueue),
  ['third', 'first', 'second'],
  'replaceAt 后应重新建堆，并忽略越界替换'
)

const removeQueue = new AccountSideEffectQueue()
removeQueue.push(queuedAccountErrorHandling('keep-late', 400, 4))
removeQueue.push(queuedAccountErrorHandling('remove-early', 100, 1))
removeQueue.push(queuedAccountErrorHandling('keep-early', 200, 2))
removeQueue.push(queuedAccountErrorHandling('remove-late', 300, 3))

assert.equal(
  removeQueue.removeWhere((item) => operationAccountId(item).startsWith('remove-')),
  2,
  'removeWhere 应返回删除数量'
)
assert.equal(removeQueue.length, 2, 'removeWhere 后队列长度应减少')
assert.equal(removeQueue.removeWhere((item) => operationAccountId(item) === 'missing'), 0, '无匹配删除应返回 0')
assert.deepEqual(
  drainAccountIds(removeQueue),
  ['keep-early', 'keep-late'],
  'removeWhere 后剩余元素仍应保持堆序'
)

const clearQueue = new AccountSideEffectQueue()
clearQueue.push(queuedAccountErrorHandling('clear-a', 100, 1))
clearQueue.push(queuedAccountErrorHandling('clear-b', 200, 2))
clearQueue.clear()
assert.equal(clearQueue.length, 0, 'clear 应清空队列')
assert.equal(clearQueue.pop(), undefined, 'clear 后 pop 应返回 undefined')

console.log('网关账号副作用内存优先队列回归通过：push/pop、replaceAt、removeWhere 和 clear 均保持堆序语义')

function queuedAccountErrorHandling(accountId: string, nextAttemptAtMs: number, enqueuedAtMs: number): QueuedAccountSideEffect {
  return {
    operation: accountErrorHandlingOperation(accountId),
    attempts: 0,
    enqueuedAtMs,
    nextAttemptAtMs,
    expiresAtMs: nextAttemptAtMs + 60_000
  }
}

function accountErrorHandlingOperation(accountId: string): AccountErrorHandlingOperation {
  return {
    type: 'apply_account_error_handling',
    account: {
      id: accountId,
      providerCode: 'openai',
      providerProtocolProfileId: 'profile-openai',
      protocolCode: 'openai',
      protocolVersion: 'v1',
      systemAccountId: 'sys_side_effect_queue',
      accountOwnerSystemAccountId: 'sys_side_effect_queue',
      groupOwnerSystemAccountId: 'sys_side_effect_queue',
      accountAccessType: 'owner',
      groupAccessType: 'owner',
      name: accountId,
      type: 'api_key',
      status: 'active',
      concurrencyLimit: 1,
      priority: 0,
      superPriorityEnabled: false,
      fallbackEnabled: false,
      clientCompatibility: 'openai_standard',
      healthCheckEndpointMode: 'chat_json',
      baseUrl: 'https://api.openai.com/v1',
      apiKey: 'sk-side-effect-queue',
      streamFailureCount: 0,
      credentials: {}
    },
    input: {
      success: false,
      errorMessage: 'side effect queue structure regression'
    }
  }
}

function drainAccountIds(queueToDrain: AccountSideEffectQueue): string[] {
  const accountIds: string[] = []
  while (queueToDrain.length > 0) {
    accountIds.push(operationAccountId(queueToDrain.pop()))
  }
  return accountIds
}

function operationAccountId(item: QueuedAccountSideEffect | undefined): string {
  assert.ok(item, '队列项不能为空')
  assert.equal(item.operation.type, 'apply_account_error_handling', '测试队列项应为账号错误处理副作用')
  return item.operation.account.id
}
