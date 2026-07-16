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
  assert.equal(healthy.events.at(-1)?.type, 'message.delta', '故障 subscriber 不得影响健康 subscriber')
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
  assert(Buffer.byteLength(JSON.stringify(assistant.contentBlocks), 'utf8') <= CHAT_GENERATION_REASONING_MAX_BYTES + CHAT_GENERATION_TOOL_JSON_MAX_BYTES + 1024)
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
  assert.equal(registry.subscribe(runner.identity, eventCollector().subscriber), false, '超时返回后仍必须拒绝订阅')
  const rejected = new ChatGenerationRunner({
    identity: identity('turn_after_timeout', 'conv_after_timeout'),
    execute: async () => ({ status: 'completed', data: {} })
  })
  assert.equal(registry.start(rejected), false, '超时返回后仍必须拒绝新 start')
}

await twoSubscribersShareOneExecution()
await subscriberFailuresAreIsolated()
await stopRequiresExactTurn()
await staleFinallyCannotDeleteReplacement()
await terminalFollowsFinalize()
await snapshotIsBounded()
await shutdownRejectsAndDrains()
await shutdownTimeoutBoundsNonCooperativeExecution()

console.log('AI 问答服务端生成 runner 回归通过')
