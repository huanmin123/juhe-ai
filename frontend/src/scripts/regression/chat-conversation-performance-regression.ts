import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import * as performanceModule from '../../views/chat/chatConversationPerformance'

const { ChatModelLoadCoordinator, ChatSingleFlightCoordinator, applyDeletedChatConversation } = performanceModule
const ChatModelCapabilitiesLoadCoordinator = (performanceModule as unknown as { ChatModelCapabilitiesLoadCoordinator?: new <T>(input: { load: (request: { conversationId: string; modelId: string }, signal: AbortSignal) => Promise<T> }) => {
  load: (request: { conversationId: string; modelId: string }) => Promise<T>
  cancel: () => void
} }).ChatModelCapabilitiesLoadCoordinator
assert.equal(typeof ChatModelCapabilitiesLoadCoordinator, 'function', '必须提供可测试的单模型能力请求与取消协调器')
if (!ChatModelCapabilitiesLoadCoordinator) throw new Error('ChatModelCapabilitiesLoadCoordinator 未实现')

let capabilityCalls = 0
let resolveFirstCapability!: (value: string) => void
let firstCapabilityAborted = false
const capabilityCoordinator = new ChatModelCapabilitiesLoadCoordinator<string>({
  load: ({ modelId }, signal) => {
    capabilityCalls += 1
    if (modelId === 'model-a') {
      signal.addEventListener('abort', () => { firstCapabilityAborted = true }, { once: true })
      return new Promise<string>((resolve) => { resolveFirstCapability = resolve })
    }
    if (modelId === 'model-error') return Promise.reject(new Error('capability failed'))
    return Promise.resolve(`capability:${modelId}`)
  }
})
const firstCapability = capabilityCoordinator.load({ conversationId: 'conv-1', modelId: 'model-a' })
const duplicateCapability = capabilityCoordinator.load({ conversationId: 'conv-1', modelId: 'model-a' })
assert.equal(capabilityCalls, 1, '同一会话同一模型并发能力请求必须去重')
const switchedCapability = capabilityCoordinator.load({ conversationId: 'conv-1', modelId: 'model-b' })
assert.equal(firstCapabilityAborted, true, '切换模型必须取消旧能力请求')
assert.equal(await switchedCapability, 'capability:model-b')
resolveFirstCapability('capability:model-a')
const canceledCapabilities = await Promise.allSettled([firstCapability, duplicateCapability])
assert.equal(canceledCapabilities.every((result) => result.status === 'rejected'), true, '被取消的旧模型能力响应即使晚到也不得回填')
assert.equal(await capabilityCoordinator.load({ conversationId: 'conv-1', modelId: 'model-b' }), 'capability:model-b')
assert.equal(capabilityCalls, 3, '已完成能力读取后的下一次动作必须重新请求')
await assert.rejects(() => capabilityCoordinator.load({ conversationId: 'conv-1', modelId: 'model-error' }), /capability failed/)
await assert.rejects(() => capabilityCoordinator.load({ conversationId: 'conv-1', modelId: 'model-error' }), /capability failed/)
assert.equal(capabilityCalls, 5, '能力错误后用户重试时必须重新请求')

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
assert.deepEqual(await coordinator.load(request), ['model_a'], '上一批模型请求完成后必须重新读取')
assert.equal(calls, 2, '已完成的模型列表结果不能跨动作复用')

let freshCalls = 0
const freshCoordinator = new ChatModelLoadCoordinator<string>({
  load: async () => [`model_${++freshCalls}`]
})
const freshRequest = { apiKeyId: 'key_fresh', conversationId: 'conv_fresh' }
assert.deepEqual(await freshCoordinator.load(freshRequest), ['model_1'])
assert.deepEqual(await freshCoordinator.load(freshRequest), ['model_2'], '再次展开模型列表必须重新加载当前事实')
assert.equal(freshCalls, 2, '模型列表不得保留 TTL 结果缓存')

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
assert.doesNotMatch(chatViewSource, /listApiKeys|newApiKeyId|选择自己的 API Key/, '新建对话不得再加载或要求用户选择 API Key')
assert.match(chatViewSource, /chatApi\.createConversation\(\)/, '新建对话必须由后端自动绑定默认 GPT API Key')
assert.match(chatViewSource, /selectionEpochAtStart[\s\S]{0,500}conversationLoadEpoch === selectionEpochAtStart/, '创建请求返回时不得覆盖用户等待期间的新会话选择')
assert.match(chatViewSource, /@models-open=/, '模型下拉展开必须显式触发按需刷新')
assert.match(chatViewSource, /normalizeChatModelControls/, '同模型能力刷新后必须规范化思考和服务选项')

console.log('AI 问答会话性能回归通过')
