import assert from 'node:assert/strict'

import { classifyChatGenerationError } from '../../modules/chat/chat-generation-error.js'
import { ChatGenerationRegistry } from '../../modules/chat/chat-generation-registry.js'
import { ChatGenerationRunner } from '../../modules/chat/chat-generation-runner.js'

const rawSecret = ['sk', 'test-secret-that-must-never-be-returned'].join('-')
const rawUrl = 'https://upstream.example.test/v1/responses'

const cases = [
  {
    error: Object.assign(new Error(`POST ${rawUrl} failed: ${rawSecret}`), { code: 'upstream_http_error' }),
    expectedCode: 'upstream_http_error',
    expectedMessage: '模型服务请求失败，请稍后重试'
  },
  {
    error: Object.assign(new Error(`socket failed: ${rawSecret}`), { code: 'ECONNRESET' }),
    expectedCode: 'upstream_stream_failed',
    expectedMessage: '模型响应中断，请重新发送'
  },
  {
    error: Object.assign(new Error(`image failed: ${rawUrl}`), { code: 'image_generation_failed' }),
    expectedCode: 'image_generation_failed',
    expectedMessage: '图片生成失败，请重新发送'
  },
  {
    error: Object.assign(new Error(`stream missing: ${rawSecret}`), { code: 'stream_interrupted' }),
    expectedCode: 'stream_interrupted',
    expectedMessage: '生成连接已中断，请重新发送'
  },
  {
    error: new Error(`unexpected\n    at ${rawUrl}\nAuthorization: Bearer ${rawSecret}`),
    expectedCode: 'internal_generation_failed',
    expectedMessage: '生成任务异常结束，请重新发送'
  }
] as const

for (const item of cases) {
  const result = classifyChatGenerationError(item.error)
  assert.equal(result.code, item.expectedCode)
  assert.equal(result.message, item.expectedMessage)
  assert.doesNotMatch(result.message, /https?:\/\//u, '公开错误文案不得包含 URL')
  assert.doesNotMatch(result.message, /sk-[A-Za-z0-9_-]+/u, '公开错误文案不得包含 API Key')
  assert.doesNotMatch(result.message, /Authorization|Bearer|\n\s*at\s/u, '公开错误文案不得包含 header 或堆栈')
}

assert.deepEqual(classifyChatGenerationError(null), {
  code: 'internal_generation_failed',
  message: '生成任务异常结束，请重新发送'
}, '非 Error 异常必须使用未知错误安全兜底')
assert.deepEqual(classifyChatGenerationError(new Error(`raw image failure ${rawSecret}`), 'image_generation_failed'), {
  code: 'image_generation_failed',
  message: '图片生成失败，请重新发送'
}, '调用链应使用结构化 fallback 分类，不得解析 raw message')
assert.deepEqual(classifyChatGenerationError(new Error(`HTTP raw body ${rawSecret}`), 'upstream_http_error'), {
  code: 'upstream_http_error',
  message: '模型服务请求失败，请稍后重试'
})
assert.deepEqual(classifyChatGenerationError(Object.assign(new Error('image socket closed'), { code: 'ECONNRESET' }), 'image_generation_failed'), {
  code: 'image_generation_failed',
  message: '图片生成失败，请重新发送'
}, '显式 image phase 必须覆盖底层网络错误码')
assert.deepEqual(classifyChatGenerationError(Object.assign(new Error('internal status'), { status: 503 }), 'internal_generation_failed'), {
  code: 'internal_generation_failed',
  message: '生成任务异常结束，请重新发送'
}, '显式 internal phase 必须覆盖底层 HTTP 状态')
assert.deepEqual(classifyChatGenerationError(Object.assign(new Error('inferred status'), { statusCode: 502 })), {
  code: 'upstream_http_error',
  message: '模型服务请求失败，请稍后重试'
}, '仅省略 phase 时才允许依据 HTTP 状态推断')

let resolveCompletion!: (value: { status: 'completed'; data: { messageId: string } }) => void
const completion = new Promise<{ status: 'completed'; data: { messageId: string } }>((resolve) => {
  resolveCompletion = resolve
})
let now = '2026-07-18T08:00:00.000Z'
const runner = new ChatGenerationRunner({
  identity: {
    ownerId: 'owner_status',
    conversationId: 'conversation_status',
    turnId: 'turn_status',
    assistantMessageId: 'assistant_status'
  },
  execute: () => completion,
  now: () => now
})
const registry = new ChatGenerationRegistry({ terminalSnapshotLimit: 2 })
assert.equal(registry.start(runner), true)
assert.deepEqual(registry.snapshot(runner.identity), {
  state: 'running',
  eventVersion: 0,
  lastSemanticActivityAt: '2026-07-18T08:00:00.000Z',
  assistantMessageId: 'assistant_status'
})
assert.deepEqual(registry.snapshot({ ...runner.identity, turnId: 'turn_missing' }), { state: 'missing' })

now = '2026-07-18T08:00:05.000Z'
runner.publish('message.delta', { messageId: 'assistant_status', delta: 'hello' }, { contentTextDelta: 'hello' })
assert.deepEqual(runner.statusSnapshot(), {
  state: 'running',
  eventVersion: 1,
  lastSemanticActivityAt: '2026-07-18T08:00:05.000Z',
  assistantMessageId: 'assistant_status'
})

now = '2026-07-18T08:00:09.000Z'
resolveCompletion({ status: 'completed', data: { messageId: 'assistant_status' } })
await runner.completion
assert.equal(registry.get(runner.identity), undefined, '完成后 active runner 必须释放')
assert.deepEqual(registry.snapshot(runner.identity), {
  state: 'terminal',
  eventVersion: 2,
  lastSemanticActivityAt: '2026-07-18T08:00:09.000Z',
  assistantMessageId: 'assistant_status'
}, '完成后 registry 必须保留有界 terminal 快照供状态查询确权')

console.log('AI 问答生成状态与安全错误回归通过')
