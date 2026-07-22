import assert from 'node:assert/strict'

import {
  CHAT_GENERATION_REASONING_MAX_BYTES,
  CHAT_GENERATION_TEXT_MAX_BYTES,
  CHAT_GENERATION_TOOL_JSON_MAX_BYTES,
  ChatGenerationRunner,
  type ChatGenerationEvent,
  type ChatGenerationSubscriber
} from '../../modules/chat/chat-generation-runner.js'
import { ChatGenerationRegistry } from '../../modules/chat/chat-generation-registry.js'

const tick = () => new Promise<void>((resolve) => setImmediate(resolve))
const deferred = <T>() => {
  let resolve!: (value: T | PromiseLike<T>) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((onResolve, onReject) => { resolve = onResolve; reject = onReject })
  return { promise, resolve, reject }
}
const identity = (turnId: string, conversationId = 'conv_1') => ({
  ownerId: 'owner_1', conversationId, turnId, assistantMessageId: `assistant_${turnId}`
})
const eventCollector = () => {
  const events: ChatGenerationEvent[] = []
  const subscriber: ChatGenerationSubscriber = { trySend: (event) => { events.push(event); return true } }
  return { events, subscriber }
}

async function twoSubscribersShareOneExecution(): Promise<void> {
  const execution = deferred<{ status: 'completed'; data: { finishReason: string } }>()
  let executeCount = 0
  const runner = new ChatGenerationRunner({
    identity: identity('turn_shared'),
    execute: async () => { executeCount += 1; return execution.promise }
  })
  const registry = new ChatGenerationRegistry()
  assert.equal(registry.start(runner), true)
  assert.equal(runner.state, 'running', 'start 必须同步进入 running')
  const first = eventCollector()
  const second = eventCollector()
  assert.equal(registry.subscribe(runner.identity, first.subscriber), true)
  assert.equal(registry.subscribe(runner.identity, second.subscriber), true)
  assert.equal(first.events[0]?.type, 'message.snapshot', '订阅的首事件必须是完整快照')
  assert.equal(first.events[0]?.eventVersion, 0)
  await tick()
  assert.equal(executeCount, 1, '多个订阅者只能共享一次 execute')
  runner.publish('message.delta', { delta: 'A' }, { contentTextDelta: 'A' })
  runner.publish('message.delta', { delta: 'B' }, { contentTextDelta: 'B' })
  assert.deepEqual(first.events.map((event) => event.eventVersion), [0, 1, 2], '事件版本必须单调递增')
  const late = eventCollector()
  assert.equal(registry.subscribe(runner.identity, late.subscriber), true)
  assert.deepEqual(late.events.map((event) => event.eventVersion), [2], '新订阅只接收当前快照，不重放低版本事件')
  assert.equal(late.events[0]?.type, 'message.snapshot')
  assert.equal(late.events[0]?.data.assistant.contentText, 'AB')
  execution.resolve({ status: 'completed', data: { finishReason: 'stop' } })
  await runner.completion
  assert.equal(first.events.at(-1)?.type, 'message.completed')
}

async function orderedContentBlockLifecycleAndSnapshot(): Promise<void> {
  const execution = deferred<{ status: 'completed'; data: { messageId: string } }>()
  const runner = new ChatGenerationRunner({ identity: identity('turn_blocks'), execute: () => execution.promise })
  const collector = eventCollector()
  runner.start()
  runner.subscribe(collector.subscriber)

  runner.publish('message.delta', { delta: '正文 A' }, { contentTextDelta: '正文 A' })
  runner.publish('reasoning.delta', { delta: '思考 1' }, { reasoningTextDelta: '思考 1' })
  runner.publish('reasoning.delta', { delta: '思考 2' }, { reasoningTextDelta: '思考 2' })
  runner.publish('reasoning.completed', {}, { reasoningCompleted: true })
  runner.publish('tool.started', { item: { id: 'search-1' } }, {
    toolEvent: { id: 'search-1', toolType: 'web_search', status: 'started', item: { query: '时间线' } }
  })
  runner.publish('tool.updated', { item: { id: 'search-1' } }, {
    toolEvent: { id: 'search-1', toolType: 'web_search', status: 'updated', item: { phase: 'searching' } }
  })
  runner.publish('tool.completed', { item: { id: 'search-1' } }, {
    toolEvent: { id: 'search-1', toolType: 'web_search', status: 'completed', item: { result: 'done' } }
  })
  runner.publish('message.delta', { delta: '正文 B' }, { contentTextDelta: '正文 B' })

  assert.deepEqual(
    collector.events.map((event) => event.type),
    [
      'message.snapshot',
      'content_block.started',
      'content_block.started',
      'content_block.delta',
      'content_block.completed',
      'content_block.started',
      'content_block.updated',
      'content_block.completed',
      'content_block.started'
    ],
    'runner 必须把旧 transport 投影收敛为块级有序事件'
  )
  assert.deepEqual(collector.events.map((event) => event.eventVersion), [0, 1, 2, 3, 4, 5, 6, 7, 8])
  assert.equal(collector.events[3]?.data.delta, '思考 2')
  assert.equal(collector.events[4]?.data.block.status, 'completed', '收到 reasoning 完成事件后必须立即终态化思考块')
  assert.equal(collector.events[5]?.data.block.order, 3)
  assert.equal(collector.events[6]?.data.blockId, collector.events[5]?.data.block.blockId, '工具更新必须命中原块')
  assert.equal(collector.events[7]?.data.block.status, 'completed')

  const late = eventCollector()
  assert.equal(runner.subscribe(late.subscriber), true)
  assert.equal(late.events[0]?.eventVersion, 8)
  const snapshot = late.events[0]?.data.assistant
  assert.deepEqual(snapshot.contentBlocks.map((block: { type: string }) => block.type), [
    'output_text', 'reasoning', 'tool_call', 'output_text'
  ])
  assert.deepEqual(snapshot.contentBlocks.map((block: { order: number }) => block.order), [1, 2, 3, 4])
  assert.equal(snapshot.contentText, '正文 A正文 B')
  assert.equal(snapshot.contentBlocks[1].text, '思考 1思考 2')
  assert.equal(snapshot.contentBlocks[2].callId, 'search-1')

  execution.resolve({ status: 'completed', data: { messageId: runner.identity.assistantMessageId } })
  await runner.completion
  assert.equal(collector.events.at(-1)?.type, 'message.completed')
  assert.equal(collector.events.filter((event) => event.type === 'content_block.completed').length, 2, '已完成的 reasoning 不得在消息终态重复完成')
}

async function terminalizesActiveBlocksOnFailureAndCancellation(): Promise<void> {
  for (const status of ['failed', 'canceled'] as const) {
    const runner = new ChatGenerationRunner({
      identity: identity(`turn_${status}`),
      execute: async ({ publish }) => {
        publish('reasoning.delta', { delta: status }, { reasoningTextDelta: status })
        publish('tool.started', { item: { id: `${status}-tool` } }, {
          toolEvent: { id: `${status}-tool`, toolType: 'web_search', status: 'started' }
        })
        return { status, data: { messageId: `assistant_turn_${status}` } }
      }
    })
    const collector = eventCollector()
    runner.start()
    runner.subscribe(collector.subscriber)
    await runner.completion

    const terminalBlockEvents = collector.events.filter((event) => event.type === 'content_block.updated')
    assert.equal(terminalBlockEvents.length, 2, `${status} 必须终态化 reasoning 和 tool`)
    assert(terminalBlockEvents.every((event) => event.data.patch.status === status))
    assert.equal(collector.events.at(-1)?.type, `message.${status}`)
    const terminalSnapshot = eventCollector()
    assert.equal(runner.subscribe(terminalSnapshot.subscriber), true)
    assert(terminalSnapshot.events[0]?.data.assistant.contentBlocks.every((block: { status?: string }) => block.status === status))
  }
}

async function subscriberFailuresAreIsolated(): Promise<void> {
  const execution = deferred<{ status: 'completed'; data: Record<string, never> }>()
  const runner = new ChatGenerationRunner({ identity: identity('turn_subscribers'), execute: () => execution.promise })
  const registry = new ChatGenerationRegistry()
  registry.start(runner)
  let throwingDeliveries = 0
  let backpressuredDeliveries = 0
  const throwing: ChatGenerationSubscriber = { trySend: () => {
    throwingDeliveries += 1
    if (throwingDeliveries > 1) throw new Error('closed')
    return true
  } }
  const backpressured: ChatGenerationSubscriber = { trySend: () => {
    backpressuredDeliveries += 1
    return backpressuredDeliveries === 1
  } }
  const healthy = eventCollector()
  assert.equal(registry.subscribe(runner.identity, throwing), true, 'throw subscriber 必须先成功收到 snapshot')
  assert.equal(registry.subscribe(runner.identity, backpressured), true, '背压 subscriber 必须先成功收到 snapshot')
  assert.equal(registry.subscribe(runner.identity, healthy.subscriber), true)
  runner.publish('message.delta', { delta: 'still-delivered' }, { contentTextDelta: 'still-delivered' })
  assert.equal(throwingDeliveries, 2, 'live publish 抛错后只移除故障 subscriber')
  assert.equal(backpressuredDeliveries, 2, 'live publish 返回 false 后只移除背压 subscriber')
  assert.equal(healthy.events.at(-1)?.type, 'content_block.started', '故障 subscriber 不得影响健康 subscriber')
  assert.equal(registry.unsubscribe(runner.identity, healthy.subscriber), true)
  assert.equal(runner.signal.aborted, false, 'unsubscribe 不得中断生成')
  await tick()
  execution.resolve({ status: 'completed', data: {} })
  await runner.completion
  assert.equal(runner.state, 'completed', '订阅者归零后 execute 仍必须完成')
}

async function stopRequiresExactTurn(): Promise<void> {
  const runner = new ChatGenerationRunner({
    identity: identity('turn_current'),
    execute: ({ signal }) => new Promise((resolve) => signal.addEventListener('abort', () => resolve({ status: 'canceled', data: {} }), { once: true }))
  })
  const registry = new ChatGenerationRegistry()
  registry.start(runner)
  await tick()
  assert.equal(registry.stop(identity('turn_old')), false, '旧 turn 停止请求必须无效')
  assert.equal(runner.signal.aborted, false)
  assert.equal(registry.stop(runner.identity), true, '完全匹配的停止请求必须 abort')
  await runner.completion
  assert.equal(runner.state, 'canceled')
  assert.equal(registry.stop(runner.identity), false, '终态 runner 不得重复停止')
}

async function staleFinallyCannotDeleteReplacement(): Promise<void> {
  const oldExecution = deferred<{ status: 'completed'; data: Record<string, never> }>()
  const oldRunner = new ChatGenerationRunner({ identity: identity('turn_old', 'conv_replace'), execute: () => oldExecution.promise })
  const registry = new ChatGenerationRegistry()
  registry.start(oldRunner)
  assert.equal(registry.delete(oldRunner.identity), true)
  const newExecution = deferred<{ status: 'completed'; data: Record<string, never> }>()
  const newRunner = new ChatGenerationRunner({ identity: identity('turn_new', 'conv_replace'), execute: () => newExecution.promise })
  assert.equal(registry.start(newRunner), true)
  oldExecution.resolve({ status: 'completed', data: {} })
  await oldRunner.completion
  assert.equal(registry.get(newRunner.identity), newRunner, '旧 runner finally 不得删除新 runner')
  newExecution.resolve({ status: 'completed', data: {} })
  await newRunner.completion
}

async function terminalFollowsFinalize(): Promise<void> {
  const finalize = deferred<void>()
  const order: string[] = []
  const runner = new ChatGenerationRunner({
    identity: identity('turn_finalize'),
    execute: async () => { order.push('execute'); await finalize.promise; order.push('finalized'); return { status: 'completed', data: {} } }
  })
  const registry = new ChatGenerationRegistry()
  const collector = eventCollector()
  registry.start(runner)
  registry.subscribe(runner.identity, { trySend: (event) => { if (event.type !== 'message.snapshot') order.push(event.type); collector.subscriber.trySend(event); return true } })
  await tick()
  assert.deepEqual(order, ['execute'], 'finalize 完成前不得发送 completed')
  finalize.resolve()
  await runner.completion
  assert.deepEqual(order, ['execute', 'finalized', 'message.completed'])
  assert.equal(registry.get(runner.identity), undefined, 'terminal publish 后 registry 才删除 runner')
}

async function snapshotIsBounded(): Promise<void> {
  const execution = deferred<{ status: 'completed'; data: Record<string, never> }>()
  const runner = new ChatGenerationRunner({ identity: identity('turn_bounded'), execute: () => execution.promise })
  const registry = new ChatGenerationRegistry()
  registry.start(runner)
  runner.publish('message.delta', { delta: 'x' }, { contentTextDelta: '\u4e2d'.repeat(CHAT_GENERATION_TEXT_MAX_BYTES) })
  runner.publish('reasoning.delta', { delta: 'x' }, { reasoningTextDelta: '\u601d'.repeat(CHAT_GENERATION_REASONING_MAX_BYTES) })
  for (let index = 0; index < 100; index += 1) {
    runner.publish('tool.updated', { index }, { toolEvent: { id: `tool_${index}`, toolType: 'web_search', status: 'updated', item: { payload: 'x'.repeat(8_192) } } })
  }
  const collector = eventCollector()
  registry.subscribe(runner.identity, collector.subscriber)
  const assistant = collector.events[0]!.data.assistant
  assert(Buffer.byteLength(assistant.contentText, 'utf8') <= CHAT_GENERATION_TEXT_MAX_BYTES)
  assert(Buffer.byteLength(assistant.reasoningText, 'utf8') <= CHAT_GENERATION_REASONING_MAX_BYTES)
  assert(Buffer.byteLength(JSON.stringify(assistant.toolEvents), 'utf8') <= CHAT_GENERATION_TOOL_JSON_MAX_BYTES)
  assert(Buffer.byteLength(JSON.stringify(assistant.contentBlocks), 'utf8') <= CHAT_GENERATION_TEXT_MAX_BYTES + CHAT_GENERATION_REASONING_MAX_BYTES + CHAT_GENERATION_TOOL_JSON_MAX_BYTES + 1024)
  execution.resolve({ status: 'completed', data: {} })
  await runner.completion
}

async function shutdownRejectsAndDrains(): Promise<void> {
  const runner = new ChatGenerationRunner({
    identity: identity('turn_shutdown'),
    execute: ({ signal }) => new Promise((resolve) => signal.addEventListener('abort', () => resolve({ status: 'canceled', data: {} }), { once: true }))
  })
  const registry = new ChatGenerationRegistry()
  registry.start(runner)
  await tick()
  await registry.shutdown({ timeoutMs: 1_000 })
  assert.equal(runner.signal.aborted, true)
  assert.equal(runner.state, 'canceled')
  assert.equal(registry.subscribe(runner.identity, eventCollector().subscriber), false, 'shutdown 后必须拒绝订阅')
  const rejected = new ChatGenerationRunner({ identity: identity('turn_after_shutdown'), execute: async () => ({ status: 'completed', data: {} }) })
  assert.equal(registry.start(rejected), false, 'shutdown 后必须拒绝新 start')
}

async function shutdownTimeoutBoundsNonCooperativeExecution(): Promise<void> {
  let executeStarted = false
  const runner = new ChatGenerationRunner({
    identity: identity('turn_shutdown_timeout', 'conv_shutdown_timeout'),
    execute: async () => {
      executeStarted = true
      return new Promise(() => {})
    }
  })
  const registry = new ChatGenerationRegistry()
  assert.equal(registry.start(runner), true)
  await tick()
  assert.equal(executeStarted, true)
  let completionSettled = false
  void runner.completion.then(() => { completionSettled = true })
  let outerTimeout: ReturnType<typeof setTimeout> | undefined
  try {
    await Promise.race([
      registry.shutdown({ timeoutMs: 10 }),
      new Promise<never>((_resolve, reject) => {
        outerTimeout = setTimeout(() => reject(new Error('shutdown 超出 500ms 外层上限')), 500)
      })
    ])
  } finally {
    if (outerTimeout) clearTimeout(outerTimeout)
  }
  assert.equal(runner.signal.aborted, true, 'shutdown 必须 abort 不合作 runner')
  await tick()
  assert.equal(completionSettled, false, '超时返回不要求不合作 execute 的 completion 伪造 settle')
  assert.equal(registry.get(runner.identity), undefined, '超时返回必须释放 registry 对不合作 runner 的强引用')
  assert.equal(registry.subscribe(runner.identity, eventCollector().subscriber), false, '超时返回后仍必须拒绝订阅')
  const rejected = new ChatGenerationRunner({
    identity: identity('turn_after_timeout', 'conv_after_timeout'),
    execute: async () => ({ status: 'completed', data: {} })
  })
  assert.equal(registry.start(rejected), false, '超时返回后仍必须拒绝新 start')
}

async function cleanupFailureDoesNotRejectDetachedRun(): Promise<void> {
  const unhandled: unknown[] = []
  const reported: unknown[] = []
  const onUnhandled = (reason: unknown) => { unhandled.push(reason) }
  process.on('unhandledRejection', onUnhandled)
  try {
    const runner = new ChatGenerationRunner({
      identity: identity('turn_cleanup_failure', 'conv_cleanup_failure'),
      execute: async () => ({ status: 'completed', data: {} }),
      reportCleanupError: (error) => { reported.push(error) }
    })
    assert.equal(runner.start(() => { throw new Error('cleanup failed') }), true)
    await runner.completion
    await tick()
    assert.equal(runner.state, 'completed', 'cleanup 失败不得改变 runner 终态')
    assert.equal((reported[0] as Error)?.message, 'cleanup failed', 'cleanup 异常应交给可选 reporter 有界处理')
    assert.deepEqual(unhandled, [], 'detached run 不得因 cleanup 失败产生 unhandled rejection')
  } finally {
    process.off('unhandledRejection', onUnhandled)
  }
}

async function unexpectedFailureWaitsForAuthoritativeFinalizer(): Promise<void> {
  const finalization = deferred<void>()
  const rawSecret = ['sk', 'runner-secret-that-must-not-leak'].join('-')
  const finalizerErrors: Array<{ code: string; message: string }> = []
  const reportedErrors: Array<{ stage: string; message: string }> = []
  const order: string[] = []
  const runner = new ChatGenerationRunner({
    identity: identity('turn_unexpected_failure', 'conv_unexpected_failure'),
    execute: async () => {
      order.push('execute')
      throw new Error(`unexpected upstream failure: ${rawSecret}`)
    },
    onUnexpectedError: async (error) => {
      finalizerErrors.push(error)
      order.push('finalizer.started')
      await finalization.promise
      order.push('finalizer.completed')
    },
    reportUnexpectedError: (error, stage) => {
      reportedErrors.push({ stage, message: error instanceof Error ? error.message : String(error) })
    }
  })
  const collector = eventCollector()
  assert.equal(runner.start(), true)
  assert.equal(runner.subscribe(collector.subscriber), true)
  await tick()
  assert.equal(runner.state, 'running', '权威 DB finalizer 完成前 runner 必须保持 running')
  assert.equal(collector.events.some((event) => event.type === 'message.failed'), false, 'DB finalizer 完成前不得发布失败终态')
  assert.deepEqual(order, ['execute', 'finalizer.started'])
  assert.deepEqual(reportedErrors, [{ stage: 'execute', message: `unexpected upstream failure: ${rawSecret}` }], 'execute cause 必须单独报告一次')
  assert.deepEqual(finalizerErrors, [{
    code: 'internal_generation_failed',
    message: '生成任务异常结束，请重新发送'
  }], 'unexpected finalizer 只接收公开安全错误')
  assert.doesNotMatch(JSON.stringify(finalizerErrors), /sk-runner-secret/u)

  finalization.resolve()
  await runner.completion
  assert.equal(runner.state, 'failed')
  assert.equal(runner.authoritativeTerminal, true, 'DB finalizer 成功后 runner 才能声明权威终态')
  assert.deepEqual(order, ['execute', 'finalizer.started', 'finalizer.completed'])
  assert.equal(finalizerErrors.length, 1, 'unexpected finalizer 只能调用一次')
  assert.deepEqual(collector.events.at(-1), {
    type: 'message.failed',
    eventVersion: 1,
    data: {
      messageId: runner.identity.assistantMessageId,
      code: 'internal_generation_failed',
      message: '生成任务异常结束，请重新发送'
    }
  })
}

async function unexpectedFinalizerFailureDoesNotPublishFalseTerminal(): Promise<void> {
  let finalizerCalls = 0
  const reportedErrors: Array<{ stage: string; message: string }> = []
  const runner = new ChatGenerationRunner({
    identity: identity('turn_unexpected_finalizer_failure', 'conv_unexpected_finalizer_failure'),
    execute: async () => { throw new Error('execute failed') },
    onUnexpectedError: async () => {
      finalizerCalls += 1
      throw new Error('DB finalizer failed')
    },
    reportUnexpectedError: (error, stage) => {
      reportedErrors.push({ stage, message: error instanceof Error ? error.message : String(error) })
    }
  })
  const collector = eventCollector()
  runner.start()
  runner.subscribe(collector.subscriber)
  await runner.completion
  assert.equal(finalizerCalls, 1)
  assert.equal(runner.state, 'failed', 'finalizer 自身异常也必须令 runner 内存状态收敛 failed')
  assert.equal(runner.authoritativeTerminal, false, 'DB finalizer 失败时不得缓存伪权威 terminal 快照')
  assert.equal(collector.events.some((event) => event.type === 'message.failed'), false, 'DB 未权威收口时不得伪造 message.failed')
  assert.deepEqual(reportedErrors, [
    { stage: 'execute', message: 'execute failed' },
    { stage: 'finalizer', message: 'DB finalizer failed' }
  ], 'execute 与 DB finalizer cause 必须各报告一次')
}

async function unexpectedReporterFailuresAreBounded(): Promise<void> {
  const unhandled: unknown[] = []
  const reporterStages: string[] = []
  const onUnhandled = (reason: unknown) => { unhandled.push(reason) }
  process.on('unhandledRejection', onUnhandled)
  try {
    const runner = new ChatGenerationRunner({
      identity: identity('turn_reporter_failure', 'conv_reporter_failure'),
      execute: async () => { throw new Error('execute cause') },
      onUnexpectedError: async () => { throw new Error('finalizer cause') },
      reportUnexpectedError: (_error, stage) => {
        reporterStages.push(stage)
        if (stage === 'execute') throw new Error('sync reporter failed')
        return Promise.reject(new Error('async reporter failed'))
      }
    })
    runner.start()
    await runner.completion
    await tick()
    assert.equal(runner.state, 'failed')
    assert.deepEqual(reporterStages, ['execute', 'finalizer'])
    assert.deepEqual(unhandled, [], 'reporter 自身异常不得产生 unhandled rejection')
  } finally {
    process.off('unhandledRejection', onUnhandled)
  }
}

await twoSubscribersShareOneExecution()
await orderedContentBlockLifecycleAndSnapshot()
await terminalizesActiveBlocksOnFailureAndCancellation()
await subscriberFailuresAreIsolated()
await stopRequiresExactTurn()
await staleFinallyCannotDeleteReplacement()
await terminalFollowsFinalize()
await snapshotIsBounded()
await shutdownRejectsAndDrains()
await cleanupFailureDoesNotRejectDetachedRun()
await shutdownTimeoutBoundsNonCooperativeExecution()
await unexpectedFailureWaitsForAuthoritativeFinalizer()
await unexpectedFinalizerFailureDoesNotPublishFalseTerminal()
await unexpectedReporterFailuresAreBounded()

console.log('AI 问答服务端生成 runner 回归通过')
