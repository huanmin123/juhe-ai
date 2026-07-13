import assert from 'node:assert/strict'

Object.assign(globalThis, {
  window: { location: { origin: 'http://127.0.0.1:5173' } }
})

const observedBodies: Array<Record<string, unknown>> = []
let responseStatus = 409
globalThis.fetch = (async (_input: RequestInfo | URL, init?: RequestInit) => {
  observedBodies.push(JSON.parse(String(init?.body ?? '{}')) as Record<string, unknown>)
  const payload = responseStatus === 401
    ? { code: 'auth_required', message: '请先登录' }
    : { code: 'chat_replace_conflict', message: '最近一轮已变化，请重新确认后再编辑' }
  return new Response(JSON.stringify(payload), {
    status: responseStatus,
    headers: { 'content-type': 'application/json' }
  })
}) as typeof fetch

const { ChatStreamHttpError, streamChatMessage } = await import('../../api/domains/chat')
const { setUnauthorizedHandler } = await import('../../api/http')
let thrown: unknown
try {
  await streamChatMessage({
    conversationId: 'conv_1',
    clientMessageId: 'client_replace',
    replaceTurnId: 'turn_old',
    content: '修正后的问题',
    contentBlocks: [{ type: 'input_text', text: '修正后的问题' }],
    model: 'mock-model',
    signal: new AbortController().signal,
    onEvent: () => undefined
  })
} catch (error) {
  thrown = error
}

assert(thrown instanceof ChatStreamHttpError)
assert.deepEqual({ status: thrown.status, code: thrown.code, message: thrown.message }, {
  status: 409,
  code: 'chat_replace_conflict',
  message: '最近一轮已变化，请重新确认后再编辑'
})
assert.equal(observedBodies[0]?.replaceTurnId, 'turn_old', '流请求必须透传 replaceTurnId')

let unauthorizedNotified = 0
setUnauthorizedHandler(() => { unauthorizedNotified += 1 })
responseStatus = 401
await assert.rejects(
  streamChatMessage({
    conversationId: 'conv_1',
    clientMessageId: 'client_unauthorized',
    content: '登录态回归',
    model: 'mock-model',
    signal: new AbortController().signal,
    onEvent: () => undefined
  }),
  (error) => error instanceof ChatStreamHttpError && error.status === 401 && error.code === 'auth_required'
)
assert.equal(unauthorizedNotified, 1, 'typed stream error 必须继续触发现有 401 登录跳转处理')

console.log('AI 问答流式 HTTP 类型错误与替换字段回归通过')
