import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { ChatGenerationRunner, type ChatGenerationEvent } from '../../modules/chat/chat-generation-runner.js'
import { ChatGenerationRegistry } from '../../modules/chat/chat-generation-registry.js'
import { createChatSseSubscriber, startChatSseHeartbeat, writeChatSseEvent } from '../../modules/chat/chat-sse-subscriber.js'

const source = readFileSync('src/modules/chat/chat.routes.ts', 'utf8')
const dbServiceSource = readFileSync('src/db-service.ts', 'utf8')
assert.match(source, /chatGenerationRegistry/, 'chat 路由必须使用服务端 generation registry')
assert.match(source, /attachChatSseResponseErrorBoundary/, 'Chat SSE 必须在写入前安装下游响应错误边界')
assert.match(source, /event: 'chat_sse_downstream_response_error'/, 'Chat SSE 响应错误必须记录独立事件')
assert.match(source, /epipeSource: errorCode === 'EPIPE' \? 'chat_sse' : undefined/, 'Chat SSE EPIPE 必须记录固定来源')
assert.match(source, /streams\/:turnId/, '必须提供活动轮次重附着 SSE 路由')
assert.match(source, /registry\.start\(/, 'accept 成功后必须同步登记 runner')
assert.doesNotMatch(source, /res\.once\('close'[\s\S]{0,180}controller\.abort\(\)/, 'accept 后 response close 不得 abort 服务端 runner')
assert.match(source, /writeChatSseEvent\(res, 'message\.started'[\s\S]{0,300}registry\.subscribe\(identity, subscriber\)/, 'POST 必须在 started 背压后仍尝试订阅并发送初始 snapshot')
assert.match(source, /registry\.start\(runner\)[\s\S]{0,1000}preparationClaim\.controller\.signal\.aborted[\s\S]{0,120}runner\.abort\(\)/, 'accept 窗口取消后必须立即 abort 已登记 runner')
assert.match(source, /runner\.completion\.finally\(cleanup\)/, 'attach 必须在 runner completion 时兜底清理连接')
assert.equal((source.match(/startChatSseHeartbeat\(\{/gu) ?? []).length, 2, '初始 POST 与 attach 必须各复用一次共享 5 秒 heartbeat helper')
assert.doesNotMatch(source, /function startSubscriberHeartbeat|15_000/, 'chat 路由不得保留旧 15 秒私有 heartbeat')
assert.match(
  source,
  /reportUnexpectedError:\s*\(error, stage\)[\s\S]{0,220}errorLogFields\(error,\s*\{[\s\S]{0,320}traceId[\s\S]{0,120}conversationId:[\s\S]{0,120}turnId:[\s\S]{0,120}stage/u,
  'runner 原始异常必须只写服务端日志，并携带 traceId/conversationId/turnId/stage'
)
const shutdownSource = dbServiceSource.slice(dbServiceSource.indexOf('async function shutdownDbService'))
const stopListenIndex = shutdownSource.indexOf('httpEndpoint.server.close(')
const queueDrainIndex = shutdownSource.indexOf('rejectQueuedDbServiceRequestsForShutdown()')
const activeDrainIndex = shutdownSource.indexOf('waitForActiveDbServiceRequests(')
const runnerShutdownIndex = shutdownSource.indexOf('shutdownChatGenerationRegistry(')
const databaseCloseIndex = shutdownSource.indexOf('closeStorageDatabases()')
assert(stopListenIndex >= 0 && stopListenIndex < queueDrainIndex && queueDrainIndex < activeDrainIndex && activeDrainIndex < runnerShutdownIndex && runnerShutdownIndex < databaseCloseIndex, 'DB service shutdown 顺序必须为 stop listen -> queue/active drain -> runner -> DB close')
assert.match(shutdownSource, /while \(\(activeConcurrentRequestCount > 0 \|\| dbServiceRequestQueueDraining\)/, 'DB service shutdown 必须有界等待并发请求和已启动的串行 queue drain')

let releaseExecution!: () => void
const executionGate = new Promise<void>((resolve) => { releaseExecution = resolve })
let upstreamCalls = 0
const registry = new ChatGenerationRegistry()
const runner = new ChatGenerationRunner({
  identity: { ownerId: 'owner', conversationId: 'conversation', turnId: 'turn', assistantMessageId: 'assistant' },
  execute: async ({ publish }) => {
    upstreamCalls += 1
    publish('message.delta', { messageId: 'assistant', delta: 'first' }, { contentTextDelta: 'first' })
    publish('tool.started', { messageId: 'assistant', item: { id: 'tool-1', type: 'web_search_call' } }, {
      toolEvent: { id: 'tool-1', toolType: 'web_search_call', status: 'started', item: { query: 'test' } }
    })
    await executionGate
    publish('message.delta', { messageId: 'assistant', delta: 'second' }, { contentTextDelta: 'second' })
    return { status: 'completed', data: { messageId: 'assistant' } }
  }
})
assert.equal(registry.start(runner), true)
const detachedEvents: ChatGenerationEvent[] = []
registry.subscribe(runner.identity, { trySend: (event) => { detachedEvents.push(event); return event.type !== 'content_block.started' } })
await new Promise<void>((resolve) => setImmediate(resolve))
const attachedEvents: ChatGenerationEvent[] = []
assert.equal(registry.subscribe(runner.identity, { trySend: (event) => { attachedEvents.push(event) } }), true)
assert.equal(attachedEvents[0]?.type, 'message.snapshot')
assert.equal(attachedEvents[0]?.data.assistant.contentText, 'first')
assert.deepEqual(attachedEvents[0]?.data.assistant.toolEvents[0], {
  id: 'tool-1', type: 'web_search_call', status: 'started', item: { query: 'test' }
}, 'snapshot wire toolEvents 必须使用前端统一字段 type')
assert.equal(attachedEvents[0]?.eventVersion, 2, '重附着 snapshot 必须携带最新块事件版本')
assert.deepEqual(attachedEvents[0]?.data.assistant.contentBlocks.map((block: { type: string }) => block.type), ['output_text', 'tool_call'])
assert.equal(
  attachedEvents[0]?.data.assistant.contentBlocks.find((block: { callId?: string }) => block.callId === 'tool-1')?.toolType,
  'web_search_call',
  'contentBlocks 继续使用 toolType 契约并保持真实出现顺序'
)
releaseExecution()
await runner.completion
assert.equal(upstreamCalls, 1, '重新附着不得重复调用上游')
assert(attachedEvents.some((event) => event.type === 'content_block.started' && event.data.block?.text === 'second'))
assert.equal(attachedEvents.at(-1)?.type, 'message.completed')

let canceledUpstreamCalls = 0
const canceledRegistry = new ChatGenerationRegistry()
const canceledRunner = new ChatGenerationRunner({
  identity: { ownerId: 'owner', conversationId: 'accepting', turnId: 'accepted-turn', assistantMessageId: 'accepted-assistant' },
  execute: async ({ signal }) => {
    if (signal.aborted) return { status: 'canceled', data: { messageId: 'accepted-assistant' } }
    canceledUpstreamCalls += 1
    return { status: 'completed', data: { messageId: 'accepted-assistant' } }
  }
})
assert.equal(canceledRegistry.start(canceledRunner), true)
assert.equal(canceledRegistry.stop(canceledRunner.identity), true)
await canceledRunner.completion
assert.equal(canceledRunner.state, 'canceled')
assert.equal(canceledUpstreamCalls, 0, 'accept 窗口取消后不得继续调用上游')

const backpressureWrites: string[] = []
let backpressureDetached = 0
let backpressureEnded = 0
const backpressureSubscriber = createChatSseSubscriber({
  response: {
    destroyed: false,
    writableEnded: false,
    write: (chunk) => { backpressureWrites.push(chunk); return false },
    end: () => { backpressureEnded += 1 }
  },
  detach: () => { backpressureDetached += 1 }
})
let resolveBackpressureGate!: () => void
const backpressureGate = new Promise<void>((resolve) => { resolveBackpressureGate = resolve })
const backpressureRunner = new ChatGenerationRunner({
  identity: { ownerId: 'owner', conversationId: 'backpressure', turnId: 'turn', assistantMessageId: 'assistant' },
  execute: async ({ publish }) => {
    publish('message.delta', { delta: 'before' }, { contentTextDelta: 'before' })
    await backpressureGate
    publish('message.delta', { delta: 'after' }, { contentTextDelta: 'after' })
    return { status: 'completed', data: {} }
  }
})
const backpressureRegistry = new ChatGenerationRegistry()
assert.equal(backpressureRegistry.start(backpressureRunner), true)
await new Promise<void>((resolve) => setImmediate(resolve))
assert.equal(backpressureRegistry.subscribe(backpressureRunner.identity, backpressureSubscriber), false)
assert.equal(backpressureWrites.length, 1, 'write(false) 后同一 subscriber 写入次数必须有界')
assert.equal(backpressureDetached, 1)
assert.equal(backpressureEnded, 1)
const reattachedAfterBackpressure: ChatGenerationEvent[] = []
assert.equal(backpressureRegistry.subscribe(backpressureRunner.identity, { trySend: (event) => { reattachedAfterBackpressure.push(event) } }), true)
assert.equal(reattachedAfterBackpressure[0]?.data.assistant.contentText, 'before')
resolveBackpressureGate()
await backpressureRunner.completion
assert.equal(backpressureWrites.length, 1)

let throwingEndDetached = 0
const throwingEndSubscriber = createChatSseSubscriber({
  response: {
    destroyed: true,
    writableEnded: false,
    write: () => { throw new Error('destroyed response must not be written') },
    end: () => { throw new Error('socket already closed') }
  },
  detach: () => { throwingEndDetached += 1 }
})
assert.doesNotThrow(() => assert.equal(throwingEndSubscriber.trySend({ type: 'message.delta', eventVersion: 1, data: {} }), false), '连接 cleanup 失败不能向 runner 传播')
assert.equal(throwingEndDetached, 1)

const versionedWrites: string[] = []
const versionedSubscriber = createChatSseSubscriber({
  response: {
    destroyed: false,
    writableEnded: false,
    write: (chunk) => { versionedWrites.push(chunk); return true },
    end: () => {}
  },
  detach: () => {}
})
assert.equal(versionedSubscriber.trySend({
  type: 'message.delta',
  eventVersion: 7,
  data: { messageId: 'assistant', delta: 'versioned', eventVersion: 999 }
}), true)
assert.deepEqual(JSON.parse(versionedWrites[0]!.split('\ndata: ')[1]!.trim()), {
  messageId: 'assistant',
  delta: 'versioned',
  eventVersion: 7
}, 'SSE data 必须携带 runner eventVersion，且 runner 值覆盖 data 同名字段')

const initialBackpressureWrites: string[] = []
let initialBackpressureDetached = 0
let initialBackpressureEnded = 0
const initialBackpressureResponse = {
  destroyed: false,
  writableEnded: false,
  write: (chunk: string) => { initialBackpressureWrites.push(chunk); return false },
  end: () => { initialBackpressureEnded += 1 }
}
assert.equal(writeChatSseEvent(initialBackpressureResponse, 'message.started', { turnId: 'initial-turn' }), false)
let releaseInitialBackpressure!: () => void
const initialBackpressureGate = new Promise<void>((resolve) => { releaseInitialBackpressure = resolve })
const initialBackpressureRunner = new ChatGenerationRunner({
  identity: { ownerId: 'owner', conversationId: 'initial-backpressure', turnId: 'initial-turn', assistantMessageId: 'assistant' },
  execute: async ({ publish }) => {
    await initialBackpressureGate
    publish('message.delta', { delta: 'after-snapshot' }, { contentTextDelta: 'after-snapshot' })
    return { status: 'completed', data: {} }
  }
})
const initialBackpressureRegistry = new ChatGenerationRegistry()
assert.equal(initialBackpressureRegistry.start(initialBackpressureRunner), true)
let initialBackpressureSubscriber!: ReturnType<typeof createChatSseSubscriber>
initialBackpressureSubscriber = createChatSseSubscriber({
  response: initialBackpressureResponse,
  detach: () => {
    initialBackpressureDetached += 1
    initialBackpressureRegistry.unsubscribe(initialBackpressureRunner.identity, initialBackpressureSubscriber)
  }
})
assert.equal(initialBackpressureRegistry.subscribe(initialBackpressureRunner.identity, initialBackpressureSubscriber), false)
assert.match(initialBackpressureWrites[0] ?? '', /event: message\.started/)
assert.match(initialBackpressureWrites[1] ?? '', /event: message\.snapshot/)
assert.equal(initialBackpressureWrites.length, 2, 'started 与 snapshot 各尝试一次后必须停止写入')
assert.equal(initialBackpressureDetached, 1)
assert.equal(initialBackpressureEnded, 1)
releaseInitialBackpressure()
await initialBackpressureRunner.completion
assert.equal(initialBackpressureRunner.state, 'completed', '初始连接背压不得中止 runner')
assert.equal(initialBackpressureWrites.length, 2, 'detach 后不得继续写 delta 或 terminal')

interface FakeHeartbeatTimer {
  callback: () => void
  delayMs: number
  kind: 'interval' | 'timeout'
  active: boolean
  unrefCalls: number
  unref(): void
}

function fakeHeartbeatScheduler() {
  const intervals: FakeHeartbeatTimer[] = []
  const timeouts: FakeHeartbeatTimer[] = []
  const createTimer = (kind: FakeHeartbeatTimer['kind'], callback: () => void, delayMs: number): FakeHeartbeatTimer => ({
    callback,
    delayMs,
    kind,
    active: true,
    unrefCalls: 0,
    unref() { this.unrefCalls += 1 }
  })
  return {
    intervals,
    timeouts,
    fire(timer: FakeHeartbeatTimer): void {
      if (!timer.active) return
      if (timer.kind === 'timeout') timer.active = false
      timer.callback()
    },
    scheduler: {
      setInterval(callback: () => void, intervalMs: number): FakeHeartbeatTimer {
        const timer = createTimer('interval', callback, intervalMs)
        intervals.push(timer)
        return timer
      },
      clearInterval(timer: unknown): void {
        const heartbeatTimer = timer as FakeHeartbeatTimer
        heartbeatTimer.active = false
      },
      setTimeout(callback: () => void, timeoutMs: number): FakeHeartbeatTimer {
        const timer = createTimer('timeout', callback, timeoutMs)
        timeouts.push(timer)
        return timer
      },
      clearTimeout(timer: unknown): void {
        const drainTimer = timer as FakeHeartbeatTimer
        drainTimer.active = false
      }
    }
  }
}

function heartbeatResponse(write: (chunk: string) => boolean) {
  type HeartbeatEvent = 'close' | 'drain'
  const listeners = new Map<HeartbeatEvent, () => void>()
  const emit = (event: HeartbeatEvent): void => {
    const listener = listeners.get(event)
    if (!listener) return
    listeners.delete(event)
    listener()
  }
  return {
    response: {
      destroyed: false,
      writableEnded: false,
      write,
      end: () => {},
      once(event: HeartbeatEvent, listener: () => void) {
        listeners.set(event, listener)
        return this
      },
      off(event: HeartbeatEvent, listener: () => void) {
        if (listeners.get(event) === listener) listeners.delete(event)
        return this
      }
    },
    close: () => { emit('close') },
    drain: () => { emit('drain') },
    listenerCount: (event: HeartbeatEvent) => Number(listeners.has(event))
  }
}

{
  const writes: string[] = []
  let unwritableCalls = 0
  const fake = fakeHeartbeatScheduler()
  const target = heartbeatResponse((chunk) => { writes.push(chunk); return true })
  const stopHeartbeat = startChatSseHeartbeat({
    response: target.response,
    onUnwritable: () => { unwritableCalls += 1 },
    scheduler: fake.scheduler
  })
  assert.equal(fake.intervals.length, 1)
  assert.equal(fake.intervals[0]?.delayMs, 5_000, 'SSE heartbeat 默认周期必须是 5 秒')
  assert.equal(fake.intervals[0]?.unrefCalls, 1)
  assert.deepEqual(writes, [], '5 秒前不得提前写心跳')
  fake.fire(fake.intervals[0]!)
  assert.deepEqual(writes, [': heartbeat\n\n'])
  assert.equal(unwritableCalls, 0)
  stopHeartbeat()
  stopHeartbeat()
  assert.equal(fake.intervals[0]?.active, false, 'stop 必须幂等清理 timer')
  assert.equal(target.listenerCount('close'), 0)
}

{
  let writes = 0
  let unwritableCalls = 0
  const fake = fakeHeartbeatScheduler()
  const target = heartbeatResponse(() => {
    writes += 1
    return writes > 1
  })
  const stopHeartbeat = startChatSseHeartbeat({
    response: target.response,
    onUnwritable: () => { unwritableCalls += 1 },
    scheduler: fake.scheduler
  })
  fake.fire(fake.intervals[0]!)
  assert.equal(writes, 1)
  assert.equal(unwritableCalls, 0, 'write=false 仅表示背压，不得立即断连')
  assert.equal(fake.intervals[0]?.active, false, '背压期间必须暂停 heartbeat interval')
  assert.equal(target.listenerCount('drain'), 1)
  assert.equal(fake.timeouts[0]?.delayMs, 10_000, '默认 drain timeout 必须为两倍 heartbeat 周期')
  assert.equal(fake.timeouts[0]?.active, true)
  fake.fire(fake.intervals[0]!)
  assert.equal(writes, 1, '背压期间不得继续写心跳')

  target.drain()
  assert.equal(fake.timeouts[0]?.active, false, 'drain 后必须清 timeout')
  assert.equal(target.listenerCount('drain'), 0)
  assert.equal(fake.intervals.length, 2, 'drain 后必须恢复 heartbeat interval')
  assert.equal(fake.intervals[1]?.active, true)
  fake.fire(fake.intervals[1]!)
  assert.equal(writes, 2)
  assert.equal(unwritableCalls, 0)
  stopHeartbeat()
}

{
  let unwritableCalls = 0
  const fake = fakeHeartbeatScheduler()
  const target = heartbeatResponse(() => false)
  startChatSseHeartbeat({
    response: target.response,
    onUnwritable: () => { unwritableCalls += 1 },
    scheduler: fake.scheduler
  })
  fake.fire(fake.intervals[0]!)
  fake.fire(fake.timeouts[0]!)
  assert.equal(unwritableCalls, 1, 'drain timeout 后必须执行断连收口')
  assert.equal(target.listenerCount('close'), 0)
  assert.equal(target.listenerCount('drain'), 0)
  target.drain()
  assert.equal(fake.intervals.length, 1, 'timeout 后 drain 不得恢复 interval')
}

{
  let unwritableCalls = 0
  const fake = fakeHeartbeatScheduler()
  const target = heartbeatResponse(() => false)
  const stopHeartbeat = startChatSseHeartbeat({
    response: target.response,
    onUnwritable: () => { unwritableCalls += 1 },
    scheduler: fake.scheduler
  })
  fake.fire(fake.intervals[0]!)
  stopHeartbeat()
  assert.equal(fake.intervals[0]?.active, false)
  assert.equal(fake.timeouts[0]?.active, false, '手动 stop 必须清 drain timeout')
  assert.equal(target.listenerCount('close'), 0)
  assert.equal(target.listenerCount('drain'), 0, '手动 stop 必须移除 drain listener')
  target.drain()
  assert.equal(fake.intervals.length, 1)
  assert.equal(unwritableCalls, 0, '手动 stop 不得伪造断连回调')
}

for (const failureMode of ['throw', 'destroyed', 'writableEnded'] as const) {
  let writes = 0
  let unwritableCalls = 0
  const fake = fakeHeartbeatScheduler()
  const target = heartbeatResponse(() => {
    writes += 1
    if (failureMode === 'throw') throw new Error('socket closed')
    return true
  })
  if (failureMode !== 'throw') target.response[failureMode] = true
  startChatSseHeartbeat({
    response: target.response,
    onUnwritable: () => { unwritableCalls += 1 },
    scheduler: fake.scheduler
  })
  fake.fire(fake.intervals[0]!)
  assert.equal(writes, failureMode === 'throw' ? 1 : 0)
  assert.equal(unwritableCalls, 1, `${failureMode} 必须立即触发断连收口`)
  assert.equal(fake.intervals[0]?.active, false)
}

{
  let unwritableCalls = 0
  const fake = fakeHeartbeatScheduler()
  const target = heartbeatResponse(() => true)
  startChatSseHeartbeat({
    response: target.response,
    onUnwritable: () => { unwritableCalls += 1 },
    scheduler: fake.scheduler
  })
  target.close()
  fake.fire(fake.intervals[0]!)
  assert.equal(fake.intervals[0]?.active, false, 'response close 必须清理 timer')
  assert.equal(unwritableCalls, 1, 'response close 必须通知调用方取消 subscriber')
}
console.log('chat stream reattach regression contract passed')
