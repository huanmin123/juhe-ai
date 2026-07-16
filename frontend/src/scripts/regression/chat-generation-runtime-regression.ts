import assert from 'node:assert/strict'

import {
  ChatGenerationRuntime,
  type ChatGenerationRuntimeDependencies,
  type ChatGenerationRuntimeStartInput
} from '../../views/chat/chatGenerationRuntime'
import type { ChatMessage, ChatStreamEvent } from '../../types/domain/chat'

function deferred(): { promise: Promise<void>; resolve: () => void; reject: (error: unknown) => void } {
  let resolve!: () => void
  let reject!: (error: unknown) => void
  return { promise: new Promise<void>((next, fail) => { resolve = next; reject = fail }), resolve, reject }
}

function assistant(id = 'assistant'): ChatMessage {
  return {
    id, conversationId: 'conversation', turnId: 'turn', sequenceNo: 2, role: 'assistant', status: 'streaming',
    contentText: '', model: 'model', createdAt: '2026-07-16T00:00:00.000Z', expiresAt: '2026-07-23T00:00:00.000Z'
  }
}

function user(): ChatMessage {
  return { ...assistant('user'), sequenceNo: 1, role: 'user', status: 'completed', contentText: 'hello', clientMessageId: 'client' }
}

const posts: Array<{ signal: AbortSignal; onEvent: (event: ChatStreamEvent) => void }> = []
const attaches: Array<{ signal: AbortSignal; onEvent: (event: ChatStreamEvent) => void }> = []
const postGates: ReturnType<typeof deferred>[] = []
const attachGates: ReturnType<typeof deferred>[] = []
const stopCalls: Array<{ conversationId: string; target: { clientMessageId: string; turnId: string } }> = []
let failNextStop = false
const timers: Array<() => void> = []
const dependencies: ChatGenerationRuntimeDependencies = {
  streamMessage: async (input) => {
    const gate = deferred()
    postGates.push(gate)
    posts.push({ signal: input.signal, onEvent: input.onEvent })
    await gate.promise
  },
  attachStream: async (input) => {
    const gate = deferred()
    attachGates.push(gate)
    attaches.push({ signal: input.signal, onEvent: input.onEvent })
    await gate.promise
  },
  stop: async (conversationId, target) => {
    stopCalls.push({ conversationId, target })
    if (failNextStop) { failNextStop = false; throw new Error('stop network failure') }
    return { stopped: true }
  },
  schedule: (callback) => { timers.push(callback); return callback },
  cancelSchedule: (handle) => {
    const index = timers.indexOf(handle as () => void)
    if (index >= 0) timers.splice(index, 1)
  }
}
const runtime = new ChatGenerationRuntime(dependencies, { reconnectDelaysMs: [250, 500, 1000] })
const startInput: ChatGenerationRuntimeStartInput = {
  systemAccountId: 'account', conversationId: 'conversation', clientMessageId: 'client', content: 'hello', model: 'model'
}

const notifications: string[] = []
const unsubscribeOne = runtime.subscribe('account', 'conversation', (turn) => { notifications.push(`one:${turn?.status}:${turn?.eventVersion}`) })
const unsubscribeTwo = runtime.subscribe('account', 'conversation', (turn) => { notifications.push(`two:${turn?.status}:${turn?.eventVersion}`) })
const first = runtime.start(startInput)
const duplicate = runtime.start(startInput)
assert.equal(first, duplicate)
assert.equal(posts.length, 1, '同一会话多个 UI subscriber / 重复 start 只能发起一次 POST')
unsubscribeOne()
assert.equal(posts[0]!.signal.aborted, false, 'UI unsubscribe 不得 abort 网络')

posts[0]!.onEvent({ type: 'message.started', data: { turnId: 'turn', userMessage: user(), assistantMessage: assistant() } })
posts[0]!.onEvent({
  type: 'message.snapshot',
  data: { turnId: 'turn', assistant: { id: 'assistant', status: 'streaming', contentText: 'snap', reasoningText: 'reason', toolEvents: [], contentBlocks: [] }, eventVersion: 1 }
})
posts[0]!.onEvent({ type: 'message.delta', data: { messageId: 'assistant', delta: '-new', eventVersion: 2 } })
assert.equal(runtime.get('account', 'conversation')?.projection.contentText, 'snap-new')
posts[0]!.onEvent({ type: 'message.snapshot', data: { turnId: 'turn', assistant: { id: 'assistant', status: 'streaming', contentText: 'authoritative', reasoningText: '', toolEvents: [], contentBlocks: [] }, eventVersion: 3 } })
assert.equal(runtime.get('account', 'conversation')?.projection.contentText, 'authoritative', 'snapshot 必须替换已有 partial')
posts[0]!.onEvent({ type: 'message.delta', data: { messageId: 'assistant', delta: '-duplicate', eventVersion: 3 } })
posts[0]!.onEvent({ type: 'message.delta', data: { messageId: 'assistant', delta: '-old', eventVersion: 2 } })
assert.equal(runtime.get('account', 'conversation')?.projection.contentText, 'authoritative', '重复/低版本事件必须忽略')

postGates[0]!.reject(new Error('network disconnected'))
await Promise.resolve()
await Promise.resolve()
assert.equal(stopCalls.length, 0, 'accepted 后网络断开不得自动 stop')
assert.equal(timers.length, 1)
timers.shift()!()
await Promise.resolve()
assert.equal(attaches.length, 1, '断线重连必须使用 GET attach')
assert.equal(posts.length, 1, 'attach 不得重复 POST')
attachGates[0]!.reject(new Error('attach disconnected'))
await Promise.resolve(); await Promise.resolve()
timers.shift()!(); await Promise.resolve()
attachGates[1]!.reject(new Error('attach disconnected'))
await Promise.resolve(); await Promise.resolve()
timers.shift()!(); await Promise.resolve()
attachGates[2]!.reject(new Error('attach disconnected'))
await Promise.resolve(); await Promise.resolve()
assert.equal(timers.length, 0, '重附着次数必须有界')
assert.equal(runtime.get('account', 'conversation')?.status, 'running', '耗尽重连后保留 running 供外部 sync')
const unsubscribeRecovery = runtime.subscribe('account', 'conversation', () => undefined)
await Promise.resolve()
assert.equal(attaches.length, 4, '重连预算耗尽后重新订阅必须允许恢复 GET attach')
assert.equal(posts.length, 1)
unsubscribeRecovery()

failNextStop = true
await assert.rejects(runtime.stop('account', 'conversation'), /stop network failure/)
await runtime.stop('account', 'conversation')
await runtime.stop('account', 'conversation')
assert.deepEqual(stopCalls, [
  { conversationId: 'conversation', target: { clientMessageId: 'client', turnId: 'turn' } },
  { conversationId: 'conversation', target: { clientMessageId: 'client', turnId: 'turn' } }
], 'stop HTTP 失败后必须允许用户重试，成功后重复 stop 不得再次调用')

const replacement = runtime.start({ ...startInput, clientMessageId: 'client-new' })
posts[1]!.onEvent({ type: 'message.started', data: { turnId: 'turn-new', userMessage: { ...user(), clientMessageId: 'client-new', turnId: 'turn-new' }, assistantMessage: { ...assistant('assistant-new'), turnId: 'turn-new' } } })
await runtime.stop('account', 'conversation', { clientMessageId: 'client', turnId: 'turn' })
assert.equal(stopCalls.length, 2, '旧 turn stop identity 不得影响新 turn')
assert.equal(runtime.get('account', 'conversation'), replacement)

runtime.activateAccount('other-account')
assert.equal(posts[1]!.signal.aborted, true, '账号切换必须 abort 旧账号前端连接')
assert.equal(stopCalls.length, 2, '账号切换不得调用服务端 stop')
assert.equal(runtime.get('account', 'conversation'), undefined)

runtime.attach({ systemAccountId: 'other-account', conversationId: 'conversation', clientMessageId: 'client-attach', turnId: 'turn-attach', assistantMessageId: 'assistant-attach' })
assert.equal(posts.length, 2)
assert.equal(attaches.length, 6, 'stop 失败恢复和主动 attach 都必须使用 GET')
attaches[5]!.onEvent({ type: 'message.snapshot', data: { turnId: 'turn-attach', assistant: { id: 'assistant-attach', status: 'streaming', contentText: 'attached', reasoningText: '', toolEvents: [], contentBlocks: [] }, eventVersion: 4 } })
attaches[5]!.onEvent({ type: 'message.completed', data: { messageId: 'assistant-attach', finishReason: 'stop', eventVersion: 5 } })
assert.equal(runtime.get('other-account', 'conversation')?.status, 'completed')
assert.equal(attaches[5]!.signal.aborted, true, 'terminal 必须清理网络连接')
assert.equal(timers.length, 0)
assert(notifications.some((value) => value === 'two:running:3'), '保留的 UI subscriber 必须收到版本推进通知')
unsubscribeTwo()
runtime.close()

console.log('AI 问答应用级 generation runtime 回归通过')
