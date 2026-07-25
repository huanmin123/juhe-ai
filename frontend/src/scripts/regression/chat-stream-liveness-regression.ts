import assert from 'node:assert/strict'

import { ChatStreamHttpError } from '../../api/domains/chat'
import { ChatGenerationRuntime, type ChatGenerationRuntimeDependencies } from '../../views/chat/chatGenerationRuntime'
import type { ChatMessage, ChatStreamEvent, ChatSubmissionStatus } from '../../types/domain/chat'

interface Scheduled { callback: () => void; dueAt: number; canceled: boolean }

function assistant(id = 'assistant', turnId = 'turn'): ChatMessage {
  return {
    id, conversationId: 'conversation', turnId, sequenceNo: 2, role: 'assistant', status: 'streaming',
    contentText: '', contentBlocks: [], model: 'model', createdAt: '2026-07-18T00:00:00.000Z', expiresAt: '2026-07-25T00:00:00.000Z'
  }
}

function user(turnId = 'turn'): ChatMessage {
  return { ...assistant('user', turnId), sequenceNo: 1, role: 'user', status: 'completed', contentText: 'hello', clientMessageId: 'client' }
}

function accepted(runnerState: 'running' | 'missing' | 'terminal', assistantStatus: 'streaming' | 'completed' = 'streaming', eventVersion?: number, traceId?: string): ChatSubmissionStatus {
  return {
    state: 'accepted', turnId: 'turn', assistantMessageId: 'assistant', assistantStatus,
    runnerState, serverTime: '2026-07-18T00:00:10.000Z',
    ...(eventVersion === undefined ? {} : { eventVersion }),
    ...(traceId ? { traceId } : {})
  }
}

function createHarness(initialStatus: ChatSubmissionStatus, watchdogMaxChecks?: number) {
  let now = 0
  let submissionStatus = initialStatus
  let statusCalls = 0
  const watchdogs: Scheduled[] = []
  const posts: Array<{ signal: AbortSignal; onActivity: () => void; onEvent: (event: ChatStreamEvent) => void }> = []
  const attaches: Array<{ turnId: string; signal: AbortSignal; onActivity: () => void; onEvent: (event: ChatStreamEvent) => void }> = []
  const reconciliations: string[] = []
  let attachFailure: unknown
  let submissionFailure: unknown
  const dependencies: ChatGenerationRuntimeDependencies = {
    streamMessage: async (input) => {
      posts.push({ signal: input.signal, onActivity: input.onActivity, onEvent: input.onEvent })
      await new Promise<void>(() => undefined)
    },
    attachStream: async (input) => {
      attaches.push({ turnId: input.turnId, signal: input.signal, onActivity: input.onActivity, onEvent: input.onEvent })
      if (attachFailure) throw attachFailure
      await new Promise<void>(() => undefined)
    },
    stop: async () => ({ stopped: true }),
    schedule: (callback) => callback,
    cancelSchedule: () => undefined,
    getSubmissionStatus: async () => {
      statusCalls += 1
      if (submissionFailure) throw submissionFailure
      return submissionStatus
    },
    scheduleWatchdog: (callback, delayMs) => {
      const item = { callback, dueAt: now + delayMs, canceled: false }
      watchdogs.push(item)
      return item
    },
    cancelWatchdog: (handle) => { (handle as Scheduled).canceled = true },
    onReconcileRequired: (turn) => { reconciliations.push(turn.reconciliationReason ?? '') }
  }
  const runtime = new ChatGenerationRuntime(dependencies, { now: () => now, staleAfterMs: 10_000, watchdogMaxChecks })
  runtime.activateAccount('account')

  async function advance(milliseconds: number): Promise<void> {
    now += milliseconds
    for (;;) {
      const due = watchdogs.find((item) => !item.canceled && item.dueAt <= now)
      if (!due) break
      due.canceled = true
      due.callback()
      await Promise.resolve()
      await Promise.resolve()
    }
  }

  function activeWatchdogs(): number {
    return watchdogs.filter((item) => !item.canceled).length
  }

  return {
    runtime, posts, attaches, reconciliations,
    statusCalls: () => statusCalls,
    setStatus: (value: ChatSubmissionStatus) => { submissionStatus = value },
    setAttachFailure: (value: unknown) => { attachFailure = value },
    setSubmissionFailure: (value: unknown) => { submissionFailure = value },
    advance, activeWatchdogs
  }
}

const preparing = createHarness({ state: 'preparing', phase: 'preparing', serverTime: '2026-07-18T00:00:00.000Z' })
preparing.runtime.start({ systemAccountId: 'account', conversationId: 'conversation', clientMessageId: 'client', content: 'hello', model: 'model' })
await preparing.advance(9_999)
assert.equal(preparing.statusCalls(), 0, '9.999 秒静默不能查询状态')
await preparing.advance(1)
assert.equal(preparing.statusCalls(), 1, '10 秒静默必须只启动一次状态确认')
assert.equal(preparing.runtime.get('account', 'conversation')?.livenessState, 'checking')
assert.equal(preparing.runtime.get('account', 'conversation')?.status, 'preparing', 'preparing 确认不得伪造失败')

const notFound = createHarness({ state: 'not_found', serverTime: '2026-07-18T00:00:00.000Z' })
notFound.runtime.start({ systemAccountId: 'account', conversationId: 'conversation', clientMessageId: 'client', content: 'hello', model: 'model' })
await notFound.advance(10_000)
await notFound.advance(10_000)
assert.equal(notFound.runtime.get('account', 'conversation')?.status, 'preparing', 'not_found 必须经过连续次数和 grace，不能单次恢复草稿')
await notFound.advance(10_000)
assert.equal(notFound.runtime.get('account', 'conversation')?.status, 'failed', '连续三次 not_found 必须结束未接受请求，不能永久轮询')
assert.equal(notFound.activeWatchdogs(), 0)

const unavailable = createHarness({ state: 'preparing', phase: 'preparing', serverTime: '2026-07-18T00:00:00.000Z' })
unavailable.setSubmissionFailure(new Error('status unavailable'))
unavailable.runtime.start({ systemAccountId: 'account', conversationId: 'conversation', clientMessageId: 'client', content: 'hello', model: 'model' })
for (let index = 0; index < 5; index += 1) await unavailable.advance(10_000)
assert.equal(unavailable.statusCalls(), 5, '状态接口异常必须有明确自动查询上限')
assert.deepEqual(unavailable.reconciliations, ['http_error'])
assert.equal(unavailable.activeWatchdogs(), 0, '状态查询达到上限后必须停止自动轮询')

const heartbeat = createHarness({ state: 'preparing', phase: 'preparing', serverTime: '2026-07-18T00:00:00.000Z' })
heartbeat.runtime.start({ systemAccountId: 'account', conversationId: 'conversation', clientMessageId: 'client', content: 'hello', model: 'model' })
for (let index = 0; index < 6; index += 1) {
  await heartbeat.advance(5_000)
  heartbeat.posts[0]!.onActivity()
}
assert.equal(heartbeat.statusCalls(), 0, '5 秒 heartbeat 持续时不得误判停滞')

const running = createHarness(accepted('running'))
running.runtime.start({ systemAccountId: 'account', conversationId: 'conversation', clientMessageId: 'client', content: 'hello', model: 'model' })
running.posts[0]!.onEvent({ type: 'message.started', data: { turnId: 'turn', userMessage: user(), assistantMessage: assistant() } })
await running.advance(10_000)
assert.equal(running.statusCalls(), 1)
assert.equal(running.posts[0]!.signal.aborted, true, 'runner running 时必须中断旧 reader')
assert.equal(running.attaches.length, 1)
assert.equal(running.attaches[0]!.turnId, 'turn', 'runner running 必须重附着同一 turn')
assert.equal(running.runtime.get('account', 'conversation')?.livenessState, 'reconnecting')

const snapshotRecovery = createHarness(accepted('running', 'streaming', 5))
snapshotRecovery.runtime.start({ systemAccountId: 'account', conversationId: 'conversation', clientMessageId: 'client', content: 'hello', model: 'model' })
snapshotRecovery.posts[0]!.onEvent({ type: 'message.started', data: { turnId: 'turn', userMessage: user(), assistantMessage: assistant() } })
snapshotRecovery.posts[0]!.onEvent({ type: 'message.delta', data: { messageId: 'assistant', delta: '旧片段', eventVersion: 2 } })
await snapshotRecovery.advance(10_000)
snapshotRecovery.attaches[0]!.onEvent({
  type: 'message.snapshot',
  data: { turnId: 'turn', assistant: { ...assistant(), contentText: '权威完整快照' }, eventVersion: 5 }
})
assert.equal(snapshotRecovery.runtime.get('account', 'conversation')?.eventVersion, 5, '本地版本只能在应用同版本权威快照后推进')
assert.equal(snapshotRecovery.runtime.get('account', 'conversation')?.projection.contentText, '权威完整快照', '重附着快照必须补齐静默期间遗漏内容')

const sameVersionSnapshot = createHarness(accepted('running', 'streaming', 5))
sameVersionSnapshot.runtime.start({ systemAccountId: 'account', conversationId: 'conversation', clientMessageId: 'client', content: 'hello', model: 'model' })
sameVersionSnapshot.posts[0]!.onEvent({ type: 'message.started', data: { turnId: 'turn', userMessage: user(), assistantMessage: assistant() } })
sameVersionSnapshot.posts[0]!.onEvent({ type: 'message.snapshot', data: { turnId: 'turn', assistant: { ...assistant(), contentText: '本地不完整快照' }, eventVersion: 5 } })
sameVersionSnapshot.posts[0]!.onEvent({ type: 'message.snapshot', data: { turnId: 'turn', assistant: { ...assistant(), contentText: '同版本权威快照' }, eventVersion: 5 } })
assert.equal(sameVersionSnapshot.runtime.get('account', 'conversation')?.projection.contentText, '同版本权威快照', '同 eventVersion 的重附着权威快照必须允许替换本地不完整投影')

const boundedPreparing = createHarness({ state: 'preparing', phase: 'preparing', serverTime: '2026-07-18T00:00:00.000Z' }, 2)
boundedPreparing.runtime.start({ systemAccountId: 'account', conversationId: 'conversation', clientMessageId: 'client', content: 'hello', model: 'model' })
await boundedPreparing.advance(10_000)
await boundedPreparing.advance(10_000)
assert.equal(boundedPreparing.statusCalls(), 2, '持续 preparing 只能执行配置数量的自动状态查询')
assert.equal(boundedPreparing.runtime.get('account', 'conversation')?.status, 'failed', '持续 preparing 达到上限后必须以明确的客户端确认超时结束，允许重新发送')
assert.equal(boundedPreparing.activeWatchdogs(), 0)

const boundedRunning = createHarness(accepted('running'), 2)
boundedRunning.runtime.start({ systemAccountId: 'account', conversationId: 'conversation', clientMessageId: 'client', content: 'hello', model: 'model' })
boundedRunning.posts[0]!.onEvent({ type: 'message.started', data: { turnId: 'turn', userMessage: user(), assistantMessage: assistant() } })
await boundedRunning.advance(10_000)
await boundedRunning.advance(10_000)
assert.equal(boundedRunning.statusCalls(), 2, '持续 running 也必须有自动查询总上限')
assert.deepEqual(boundedRunning.reconciliations, ['http_error'], '已接受的长任务达到自动查询上限后只触发一次权威同步，不伪造服务端终态')
assert.equal(boundedRunning.activeWatchdogs(), 0)
assert.equal(boundedRunning.attaches[0]?.signal.aborted, true, '自动探活耗尽后必须释放停滞的重附着 SSE reader')
boundedRunning.runtime.attach({
  systemAccountId: 'account', conversationId: 'conversation', clientMessageId: 'client', turnId: 'turn', assistantMessageId: 'assistant',
  eventVersion: boundedRunning.runtime.get('account', 'conversation')?.eventVersion,
  projection: assistant()
})
await Promise.resolve()
assert.equal(boundedRunning.attaches.length, 1, '最终权威同步只有同版本 streaming 快照时必须保持耗尽粘性，不能重新附着并重启无限探活周期')
assert.equal(boundedRunning.activeWatchdogs(), 0)
boundedRunning.runtime.attach({
  systemAccountId: 'account', conversationId: 'conversation', clientMessageId: 'client', turnId: 'turn', assistantMessageId: 'assistant',
  eventVersion: 1,
  projection: { ...assistant(), contentText: '真实新进展' }
})
await Promise.resolve()
assert.equal(boundedRunning.attaches.length, 2, '只有更高事件版本的权威进展才允许开启新的有界探活周期')
assert.equal(boundedRunning.activeWatchdogs(), 1)

const terminal = createHarness(accepted('terminal', 'completed', undefined, 'trace_terminal_status'))
terminal.runtime.start({ systemAccountId: 'account', conversationId: 'conversation', clientMessageId: 'client', content: 'hello', model: 'model' })
terminal.posts[0]!.onEvent({ type: 'message.started', data: { turnId: 'turn', userMessage: user(), assistantMessage: assistant() } })
await terminal.advance(10_000)
assert.deepEqual(terminal.reconciliations, ['runner_terminal'])
assert.equal(terminal.runtime.get('account', 'conversation')?.status, 'running', '状态查询只触发权威同步，不直接伪造 terminal 消息')
assert.equal(terminal.runtime.get('account', 'conversation')?.projection.traceId, 'trace_terminal_status', '状态查询返回的 Trace ID 必须进入当前投影，供失败定位信息即时展示')

const missing = createHarness(accepted('missing'))
missing.setAttachFailure(new ChatStreamHttpError(409, 'chat_stream_runner_missing', '生成任务已中断'))
missing.runtime.start({ systemAccountId: 'account', conversationId: 'conversation', clientMessageId: 'client', content: 'hello', model: 'model' })
missing.posts[0]!.onEvent({ type: 'message.started', data: { turnId: 'turn', userMessage: user(), assistantMessage: assistant() } })
await missing.advance(10_000)
await Promise.resolve()
await Promise.resolve()
assert.equal(missing.attaches.length, 1, 'runner missing 必须走 attach 的权威失败收口')
assert.deepEqual(missing.reconciliations, ['runner_missing'])

const cleanup = createHarness(accepted('running'))
cleanup.runtime.start({ systemAccountId: 'account', conversationId: 'conversation', clientMessageId: 'client', content: 'hello', model: 'model' })
cleanup.posts[0]!.onEvent({ type: 'message.started', data: { turnId: 'turn', userMessage: user(), assistantMessage: assistant() } })
assert.equal(cleanup.activeWatchdogs(), 1)
cleanup.posts[0]!.onEvent({ type: 'message.completed', data: { messageId: 'assistant', eventVersion: 1 } })
assert.equal(cleanup.activeWatchdogs(), 0, 'terminal 必须清理 watchdog')

const forgotten = createHarness(accepted('running'))
forgotten.runtime.start({ systemAccountId: 'account', conversationId: 'conversation', clientMessageId: 'client', content: 'hello', model: 'model' })
forgotten.posts[0]!.onEvent({ type: 'message.started', data: { turnId: 'turn', userMessage: user(), assistantMessage: assistant() } })
assert.equal(forgotten.runtime.forget('account', 'conversation', 'turn'), true)
assert.equal(forgotten.activeWatchdogs(), 0, 'forget 必须清理 watchdog')

const stopped = createHarness(accepted('running'))
stopped.runtime.start({ systemAccountId: 'account', conversationId: 'conversation', clientMessageId: 'client', content: 'hello', model: 'model' })
stopped.posts[0]!.onEvent({ type: 'message.started', data: { turnId: 'turn', userMessage: user(), assistantMessage: assistant() } })
await stopped.runtime.stop('account', 'conversation', { clientMessageId: 'client', turnId: 'turn' })
assert.equal(stopped.activeWatchdogs(), 0, 'stop 必须清理 watchdog')
stopped.runtime.close()
assert.equal(stopped.activeWatchdogs(), 0, 'close 不得遗留 watchdog')

console.log('AI 问答 10 秒传输探活与权威状态确认回归通过')
