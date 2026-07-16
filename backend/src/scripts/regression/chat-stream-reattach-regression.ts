import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { ChatGenerationRunner, type ChatGenerationEvent } from '../../modules/chat/chat-generation-runner.js'
import { ChatGenerationRegistry } from '../../modules/chat/chat-generation-registry.js'
import { createChatSseSubscriber, writeChatSseEvent } from '../../modules/chat/chat-sse-subscriber.js'

const source = readFileSync('src/modules/chat/chat.routes.ts', 'utf8')
const dbServiceSource = readFileSync('src/db-service.ts', 'utf8')
assert.match(source, /chatGenerationRegistry/, 'chat 路由必须使用服务端 generation registry')
assert.match(source, /streams\/:turnId/, '必须提供活动轮次重附着 SSE 路由')
assert.match(source, /registry\.start\(/, 'accept 成功后必须同步登记 runner')
assert.doesNotMatch(source, /res\.once\('close'[\s\S]{0,180}controller\.abort\(\)/, 'accept 后 response close 不得 abort 服务端 runner')
assert.match(source, /writeChatSseEvent\(res, 'message\.started'[\s\S]{0,300}registry\.subscribe\(identity, subscriber\)/, 'POST 必须在 started 背压后仍尝试订阅并发送初始 snapshot')
assert.match(source, /registry\.start\(runner\)[\s\S]{0,1000}preparationClaim\.controller\.signal\.aborted[\s\S]{0,120}runner\.abort\(\)/, 'accept 窗口取消后必须立即 abort 已登记 runner')
assert.match(source, /runner\.completion\.finally\(cleanup\)/, 'attach 必须在 runner completion 时兜底清理连接')
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
    await executionGate
    publish('message.delta', { messageId: 'assistant', delta: 'second' }, { contentTextDelta: 'second' })
    return { status: 'completed', data: { messageId: 'assistant' } }
  }
})
assert.equal(registry.start(runner), true)
const detachedEvents: ChatGenerationEvent[] = []
registry.subscribe(runner.identity, { trySend: (event) => { detachedEvents.push(event); return event.type !== 'message.delta' } })
await new Promise<void>((resolve) => setImmediate(resolve))
const attachedEvents: ChatGenerationEvent[] = []
assert.equal(registry.subscribe(runner.identity, { trySend: (event) => { attachedEvents.push(event) } }), true)
assert.equal(attachedEvents[0]?.type, 'message.snapshot')
assert.equal(attachedEvents[0]?.data.assistant.contentText, 'first')
releaseExecution()
await runner.completion
assert.equal(upstreamCalls, 1, '重新附着不得重复调用上游')
assert(attachedEvents.some((event) => event.type === 'message.delta' && event.data.delta === 'second'))
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
console.log('chat stream reattach regression contract passed')
