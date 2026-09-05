import { strict as assert } from 'node:assert'

import {
  AccountSideEffectEpochRegistry,
  AccountSideEffectQueue,
  type AccountSideEffectEpoch,
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

const indexedQueue = new AccountSideEffectQueue()
indexedQueue.push(queuedAccountErrorHandling('indexed-a', 300, 1))
indexedQueue.push(queuedAccountErrorHandling('indexed-b', 100, 2))
indexedQueue.push(queuedAccountErrorHandling('indexed-c', 200, 3))
assert.equal(indexedQueue.hasRuntimeKey('indexed-b'), true, 'runtime 索引应能直接判断待执行项存在')
assert.notEqual(indexedQueue.findIndexByRuntimeKey('indexed-c'), -1, 'runtime 索引应返回当前堆位置')
assert.equal(indexedQueue.findIndexByRuntimeKey('missing'), -1, 'runtime 索引对未知 key 应返回 -1')
assert.equal(indexedQueue.hasFailures, true, '失败计数索引应反映队列内失败项')
assert.deepEqual(
  indexedQueue.removeRuntimeKey('indexed-b').map((item) => operationAccountId(item)),
  ['indexed-b'],
  '按 runtime 删除应只移除目标项'
)
assert.equal(indexedQueue.hasRuntimeKey('indexed-b'), false, '按 runtime 删除后索引必须同步清理')
assert.deepEqual(drainAccountIds(indexedQueue), ['indexed-c', 'indexed-a'], '索引删除后仍应保持堆序')
assert.equal(indexedQueue.hasFailures, false, '所有失败项出队后失败计数必须归零')

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

const priorityQueue = new AccountSideEffectQueue()
priorityQueue.push(queuedAccountErrorHandling('newer-failure', 100, 20))
priorityQueue.push(queuedAccountErrorHandling('oldest-failure', 300, 10))
priorityQueue.push(queuedAccountErrorHandling('other', 200, 5))
const evicted = priorityQueue.removeOldestWhere((item) => operationAccountId(item).endsWith('-failure'))
assert.equal(operationAccountId(evicted), 'oldest-failure', '成功优先入队应淘汰符合条件的最早失败')
assert.deepEqual(
  drainAccountIds(priorityQueue),
  ['newer-failure', 'other'],
  'removeOldestWhere 后剩余元素仍应保持堆序'
)

const failureAgeQueue = new AccountSideEffectQueue()
failureAgeQueue.push(queuedSuccessfulAccountHandling('older-success', 50, 1))
failureAgeQueue.push(queuedAccountErrorHandling('newer-failure-index', 100, 20))
failureAgeQueue.push(queuedAccountErrorHandling('oldest-failure-index', 300, 10))
assert.equal(
  operationAccountId(failureAgeQueue.removeOldestFailure()),
  'oldest-failure-index',
  '失败年龄索引应忽略更老的成功项并直接淘汰最早失败'
)
assert.equal(
  operationAccountId(failureAgeQueue.removeOldestFailure()),
  'newer-failure-index',
  '连续失败淘汰后年龄索引必须保持有效'
)
assert.equal(failureAgeQueue.removeOldestFailure(), undefined, '没有失败项时年龄索引应返回 undefined')
assert.deepEqual(drainAccountIds(failureAgeQueue), ['older-success'], '失败年龄索引不得删除成功 watermark')

const clearQueue = new AccountSideEffectQueue()
clearQueue.push(queuedAccountErrorHandling('clear-a', 100, 1))
clearQueue.push(queuedAccountErrorHandling('clear-b', 200, 2))
clearQueue.clear()
assert.equal(clearQueue.length, 0, 'clear 应清空队列')
assert.equal(clearQueue.pop(), undefined, 'clear 后 pop 应返回 undefined')

const epochs = new AccountSideEffectEpochRegistry()
const failure = epochs.observe('account-a', {
  observedAt: '2026-07-24T01:00:00.000Z',
  success: false
})
assert.equal(failure.accepted, true, '首个失败观测应成为当前 epoch')
assert.equal(epochs.isCurrent(failure.epoch), true, '首个失败 epoch 应为当前值')

const success = epochs.observe('account-a', {
  observedAt: '2026-07-24T01:00:01.000Z',
  success: true
})
assert.equal(success.accepted, true, '较新成功必须推进 epoch')
assert.equal(epochs.isCurrent(failure.epoch), false, '较新成功必须使已出队失败过期')
assert.equal(epochs.isCurrent(success.epoch), true, '较新成功 epoch 应为当前值')

const lateFailure = epochs.observe('account-a', {
  observedAt: '2026-07-24T01:00:00.500Z',
  success: false
})
assert.equal(lateFailure.accepted, false, '迟到失败不得覆盖较新成功')
assert.equal(epochs.isCurrent(lateFailure.epoch), false, '迟到失败 epoch 不得成为当前值')

const equalTimestampFailure = epochs.observe('account-a', {
  observedAt: success.epoch.observedAt,
  success: false
})
assert.equal(equalTimestampFailure.accepted, false, '同毫秒失败不得覆盖成功')

const revisionEpochs = new AccountSideEffectEpochRegistry()
const currentRevision = revisionEpochs.observe('account-revision', {
  observedAt: '2026-07-24T01:00:01.000Z',
  success: true,
  dispatchRevision: 2
})
const staleRevision = revisionEpochs.observe('account-revision', {
  observedAt: '2026-07-24T01:01:00.000Z',
  success: false,
  dispatchRevision: 1
})
assert.equal(staleRevision.accepted, false, '旧 dispatch revision 即使观测时间更晚也不得覆盖新 revision')
assert.equal(revisionEpochs.isCurrent(currentRevision.epoch), true, '旧 revision 不得推进当前 epoch')
const newerRevision = revisionEpochs.observe('account-revision', {
  observedAt: '2026-07-24T01:00:00.000Z',
  success: false,
  dispatchRevision: 3
})
assert.equal(newerRevision.accepted, true, '新 dispatch revision 应开启新 epoch，不受旧 revision 时间戳阻塞')
assert.equal(revisionEpochs.isCurrent(currentRevision.epoch), false, '新 revision 必须淘汰旧 revision epoch')

const independentAccount = epochs.observe('account-c', {
  observedAt: '2026-07-24T00:59:00.000Z',
  success: false
})
assert.equal(independentAccount.accepted, true, '不同账户必须使用独立 epoch')
assert.equal(epochs.isCurrent(independentAccount.epoch), true, '不同账户不得被其他账户成功过期')

const boundedEpochs = new AccountSideEffectEpochRegistry(2)
const oldestEpoch = boundedEpochs.observe('oldest', { observedAt: '2026-07-24T01:00:00.000Z', success: false }).epoch
boundedEpochs.observe('middle', { observedAt: '2026-07-24T01:00:01.000Z', success: false })
boundedEpochs.observe('newest', { observedAt: '2026-07-24T01:00:02.000Z', success: true })
assert.equal(boundedEpochs.isCurrent(oldestEpoch), false, 'epoch registry 超过容量时应淘汰最早账户以限制内存')

const retainedEpochs = new AccountSideEffectEpochRegistry(2)
const queuedEpoch = retainedEpochs.observe('queued-runtime', {
  observedAt: '2026-07-24T02:00:00.000Z',
  success: false,
  retain: true
}).epoch
const inFlightEpoch = retainedEpochs.observe('in-flight-runtime', {
  observedAt: '2026-07-24T02:00:01.000Z',
  success: true,
  retain: true
}).epoch
for (let index = 0; index < 10; index += 1) {
  retainedEpochs.observe(`transient-${index}`, {
    observedAt: new Date(Date.parse('2026-07-24T02:01:00.000Z') + index).toISOString(),
    success: false
  })
}
assert.equal(retainedEpochs.isCurrent(queuedEpoch), true, '仍在队列中的 epoch 不得因 LRU 容量被逐出')
assert.equal(retainedEpochs.isCurrent(inFlightEpoch), true, '正在执行的 epoch 不得因 LRU 容量被逐出')

retainedEpochs.release(queuedEpoch)
for (let index = 0; index < 10; index += 1) {
  retainedEpochs.observe(`post-queued-release-pressure-${index}`, {
    observedAt: new Date(Date.parse('2026-07-24T02:01:30.000Z') + index).toISOString(),
    success: false
  })
}
assert.equal(retainedEpochs.isCurrent(queuedEpoch), false, '队列项终结并释放后应在后续容量压力下恢复可淘汰')
assert.equal(retainedEpochs.isCurrent(inFlightEpoch), true, '释放其他 runtime 不得误淘汰仍在执行的 epoch')
retainedEpochs.release(queuedEpoch)
assert.equal(retainedEpochs.isCurrent(inFlightEpoch), true, '重复释放必须幂等，不能破坏其他活跃 epoch')
retainedEpochs.release(inFlightEpoch)
for (let index = 0; index < 10; index += 1) {
  retainedEpochs.observe(`post-release-${index}`, {
    observedAt: new Date(Date.parse('2026-07-24T02:02:00.000Z') + index).toISOString(),
    success: false
  })
}
assert.equal(retainedEpochs.isCurrent(inFlightEpoch), false, '执行终结释放后 epoch 应恢复普通容量淘汰语义')

const overlappingEpochs = new AccountSideEffectEpochRegistry(1)
const overlappingInFlight = overlappingEpochs.observe('overlapping-runtime', {
  observedAt: '2026-07-24T03:00:00.000Z',
  success: false,
  retain: true
}).epoch
const overlappingQueued = overlappingEpochs.observe('overlapping-runtime', {
  observedAt: '2026-07-24T03:00:01.000Z',
  success: true,
  retain: true
}).epoch
assert.equal(overlappingEpochs.isCurrent(overlappingInFlight), false, '同 runtime 新观测必须立即使旧执行 epoch 过期')
overlappingEpochs.release(overlappingInFlight)
overlappingEpochs.observe('capacity-pressure', {
  observedAt: '2026-07-24T03:00:02.000Z',
  success: false
})
assert.equal(overlappingEpochs.isCurrent(overlappingQueued), true, '释放旧执行引用不得误释放同 runtime 新队列引用')
overlappingEpochs.release(overlappingQueued)
overlappingEpochs.observe('post-overlap-release', {
  observedAt: '2026-07-24T03:00:03.000Z',
  success: false
})
assert.equal(overlappingEpochs.isCurrent(overlappingQueued), false, '同 runtime 所有活跃引用释放后才允许容量淘汰')

const randomizedQueue = new AccountSideEffectQueue()
const randomizedModel = new Map<string, QueuedAccountSideEffect>()
let randomizedSeed = 0x5eed1234
let randomizedSequence = 0
for (let step = 0; step < 2000; step += 1) {
  const action = nextRandomizedValue() % 6
  const currentItems = [...randomizedModel.values()]
  if (currentItems.length === 0 || action <= 1) {
    const runtimeKey = `randomized-${randomizedSequence}`
    const item = randomizedAccountSideEffect(runtimeKey, (nextRandomizedValue() & 1) === 0)
    randomizedQueue.push(item)
    randomizedModel.set(runtimeKey, item)
  } else if (action === 2) {
    const target = currentItems[nextRandomizedValue() % currentItems.length]
    assert.deepEqual(randomizedQueue.removeRuntimeKey(target.epoch.runtimeKey), [target], '随机按 runtime 删除应命中模型项')
    randomizedModel.delete(target.epoch.runtimeKey)
  } else if (action === 3) {
    const expected = expectedQueueHead(currentItems)
    assert.ok(expected, '非空随机模型必须存在堆顶')
    assert.equal(randomizedQueue.pop(), expected, '随机 pop 必须与 nextAttempt/enqueued 模型一致')
    randomizedModel.delete(expected.epoch.runtimeKey)
  } else if (action === 4) {
    const expected = expectedOldestFailure(currentItems)
    assert.equal(randomizedQueue.removeOldestFailure(), expected, '随机最早失败淘汰必须与年龄模型一致')
    if (expected) randomizedModel.delete(expected.epoch.runtimeKey)
  } else {
    const target = currentItems[nextRandomizedValue() % currentItems.length]
    const replacement = randomizedAccountSideEffect(target.epoch.runtimeKey, (nextRandomizedValue() & 1) === 0)
    const index = randomizedQueue.findIndexByRuntimeKey(target.epoch.runtimeKey)
    assert.equal(randomizedQueue.replaceAt(index, replacement), target, '随机替换应返回原队列项')
    randomizedModel.set(target.epoch.runtimeKey, replacement)
  }
  const expectedHead = expectedQueueHead([...randomizedModel.values()])
  assert.equal(randomizedQueue.length, randomizedModel.size, '随机操作后队列长度必须与模型一致')
  assert.equal(randomizedQueue.peek(), expectedHead, '随机操作后堆顶必须与模型一致')
  assert.equal(
    randomizedQueue.hasFailures,
    [...randomizedModel.values()].some((item) => !item.operation.input.success),
    '随机操作后失败计数索引必须与模型一致'
  )
}

console.log('网关账号副作用内存队列回归通过：堆序、runtime 索引与 queued/in-flight epoch 保留语义均正常')

function queuedAccountErrorHandling(accountId: string, nextAttemptAtMs: number, enqueuedAtMs: number): QueuedAccountSideEffect {
  return {
    operation: accountErrorHandlingOperation(accountId),
    epoch: accountSideEffectEpoch(accountId, enqueuedAtMs),
    attempts: 0,
    enqueuedAtMs,
    nextAttemptAtMs,
    expiresAtMs: nextAttemptAtMs + 60_000
  }
}

function queuedSuccessfulAccountHandling(
  accountId: string,
  nextAttemptAtMs: number,
  enqueuedAtMs: number
): QueuedAccountSideEffect {
  const item = queuedAccountErrorHandling(accountId, nextAttemptAtMs, enqueuedAtMs)
  item.operation.input.success = true
  item.epoch.success = true
  return item
}

function randomizedAccountSideEffect(runtimeKey: string, success: boolean): QueuedAccountSideEffect {
  randomizedSequence += 1
  const nextAttemptAtMs = (nextRandomizedValue() % 100_000) * 10_000 + randomizedSequence
  const enqueuedAtMs = (nextRandomizedValue() % 100_000) * 10_000 + randomizedSequence
  return success
    ? queuedSuccessfulAccountHandling(runtimeKey, nextAttemptAtMs, enqueuedAtMs)
    : queuedAccountErrorHandling(runtimeKey, nextAttemptAtMs, enqueuedAtMs)
}

function expectedQueueHead(items: QueuedAccountSideEffect[]): QueuedAccountSideEffect | undefined {
  return [...items].sort((left, right) => (
    left.nextAttemptAtMs - right.nextAttemptAtMs || left.enqueuedAtMs - right.enqueuedAtMs
  ))[0]
}

function expectedOldestFailure(items: QueuedAccountSideEffect[]): QueuedAccountSideEffect | undefined {
  return items
    .filter((item) => !item.operation.input.success)
    .sort((left, right) => (
      left.enqueuedAtMs - right.enqueuedAtMs || left.nextAttemptAtMs - right.nextAttemptAtMs
    ))[0]
}

function nextRandomizedValue(): number {
  randomizedSeed = (Math.imul(randomizedSeed, 1_664_525) + 1_013_904_223) >>> 0
  return randomizedSeed
}

function accountSideEffectEpoch(runtimeKey: string, sequence: number): AccountSideEffectEpoch {
  return {
    runtimeKey,
    sequence,
    observedAt: new Date(sequence).toISOString(),
    success: false
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
