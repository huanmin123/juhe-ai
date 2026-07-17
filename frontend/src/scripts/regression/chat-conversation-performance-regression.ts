import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import { ChatModelLoadCoordinator, ChatSingleFlightCoordinator, applyDeletedChatConversation } from '../../views/chat/chatConversationPerformance'

const flush = () => new Promise<void>((resolve) => queueMicrotask(resolve))

let calls = 0
let resolveLoad!: (value: string[]) => void
const firstLoad = new Promise<string[]>((resolve) => { resolveLoad = resolve })
const coordinator = new ChatModelLoadCoordinator<string>({
  load: async () => {
    calls += 1
    return firstLoad
  },
  retryDelayMilliseconds: 0
})
const request = { apiKeyId: 'key_a', conversationId: 'conv_a' }
const concurrentFirst = coordinator.load(request)
const concurrentSecond = coordinator.load({ ...request, conversationId: 'conv_b' })
assert.equal(calls, 1, '同一 API Key 的模型列表请求必须 single-flight 合并')
resolveLoad(['model_a'])
assert.deepEqual(await concurrentFirst, ['model_a'])
assert.deepEqual(await concurrentSecond, ['model_a'])
assert.deepEqual(await coordinator.load(request), ['model_a'], '已有模型结果必须按 API Key 复用')
assert.equal(calls, 1, '命中缓存时不能重复请求模型列表')

let now = 0
let ttlCalls = 0
const ttlCoordinator = new ChatModelLoadCoordinator<string>({
  load: async () => [`model_${++ttlCalls}`],
  now: () => now,
  cacheTtlMilliseconds: 30_000
})
const ttlRequest = { apiKeyId: 'key_ttl', conversationId: 'conv_ttl' }
assert.deepEqual(await ttlCoordinator.load(ttlRequest), ['model_1'])
now = 29_999
assert.deepEqual(await ttlCoordinator.load(ttlRequest), ['model_1'], 'TTL 内必须继续复用缓存')
now = 30_000
assert.deepEqual(await ttlCoordinator.load(ttlRequest), ['model_2'], '模型配置缓存到期后必须重新加载')
assert.equal(ttlCalls, 2, '过期模型缓存必须只触发一次新的加载')

let timeoutAttempts = 0
const retryCoordinator = new ChatModelLoadCoordinator<string>({
  load: async () => {
    timeoutAttempts += 1
    if (timeoutAttempts === 1) throw Object.assign(new Error('timeout'), { code: 'ECONNABORTED' })
    return ['model_retried']
  },
  retryDelayMilliseconds: 0
})
assert.deepEqual(await retryCoordinator.load({ apiKeyId: 'key_timeout', conversationId: 'conv_timeout' }), ['model_retried'], '首次超时必须有限重试后恢复可用')
assert.equal(timeoutAttempts, 2, '模型列表超时只允许一次补偿重试')

let aborted = false
const cancellationCoordinator = new ChatModelLoadCoordinator<string>({
  load: async (_request, signal) => new Promise<string[]>((_, reject) => {
    signal.addEventListener('abort', () => {
      aborted = true
      reject(Object.assign(new Error('aborted'), { name: 'AbortError' }))
    }, { once: true })
  }),
  retryDelayMilliseconds: 0
})
const cancelled = cancellationCoordinator.load({ apiKeyId: 'key_old', conversationId: 'conv_old' })
await flush()
cancellationCoordinator.cancel('key_old')
await assert.rejects(cancelled, /aborted/)
assert.equal(aborted, true, '切换到其他 API Key 时必须取消旧模型列表请求')

const conversations = [{ id: 'conv_a' }, { id: 'conv_b' }]
const deleted = applyDeletedChatConversation({ conversations, selectedConversationId: 'conv_a', deletedConversationId: 'conv_a' })
assert.deepEqual(deleted.conversations, [{ id: 'conv_b' }], '服务端删除成功后必须立刻从列表移除')
assert.equal(deleted.selectedConversationId, undefined, '删除当前会话时必须立即解除选择，不得等待本地缓存')
assert.equal(deleted.nextConversationId, 'conv_b', '下一会话加载应在 UI 已完成删除后异步触发')

let contextCalls = 0
let resolveContext!: (value: number) => void
const contextRequest = new Promise<number>((resolve) => { resolveContext = resolve })
const contextCoordinator = new ChatSingleFlightCoordinator<number>()
const contextFirst = contextCoordinator.load('conv_context', async () => {
  contextCalls += 1
  return contextRequest
})
const contextSecond = contextCoordinator.load('conv_context', async () => {
  contextCalls += 1
  return 99
})
assert.equal(contextCalls, 1, '同一会话并发刷新上下文状态时必须只发起一次请求')
resolveContext(42)
assert.equal(await contextFirst, 42)
assert.equal(await contextSecond, 42)

const chatViewSource = readFileSync('../frontend/src/views/chat/ChatView.vue', 'utf8')
assert.match(chatViewSource, /ChatSingleFlightCoordinator/, '上下文状态请求必须通过可测试的 single-flight 协调器去重')

console.log('AI 问答会话性能回归通过')
