import assert from 'node:assert/strict'

Object.assign(globalThis, {
  window: { location: { origin: 'http://127.0.0.1:5173' } }
})

const observedBodies: Array<Record<string, unknown>> = []
const observedUrls: string[] = []
let responseStatus = 409
let responseBody = ''
globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
  observedUrls.push(String(input))
  observedBodies.push(JSON.parse(String(init?.body ?? '{}')) as Record<string, unknown>)
  if (responseStatus === 200) return new Response(responseBody, { status: 200, headers: { 'content-type': 'text/event-stream' } })
  const payload = responseStatus === 401
    ? { code: 'auth_required', message: '请先登录' }
    : { code: 'chat_replace_conflict', message: '最近一轮已变化，请重新确认后再编辑' }
  return new Response(JSON.stringify(payload), {
    status: responseStatus,
    headers: { 'content-type': 'application/json' }
  })
}) as typeof fetch

const { attachChatStream, chatApi, ChatStreamHttpError, streamChatMessage } = await import('../../api/domains/chat')
const { http, setUnauthorizedHandler } = await import('../../api/http')
let thrown: unknown
try {
  await streamChatMessage({
    conversationId: 'conv_1',
    clientMessageId: 'client_replace',
    replaceTurnId: 'turn_old',
    content: '修正后的问题',
    contentBlocks: [{ type: 'input_text', text: '修正后的问题' }],
    model: 'mock-model',
    generationParameters: {
      temperature: 0.4,
      frequencyPenalty: 0.2,
      presencePenalty: -0.1,
      maxOutputTokens: 321,
      seed: 42
    },
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
assert.deepEqual(observedBodies[0]?.generationParameters, {
  temperature: 0.4,
  frequencyPenalty: 0.2,
  presencePenalty: -0.1,
  maxOutputTokens: 321,
  seed: 42
}, '流请求必须透传非空生成参数')

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

const encodedId = 'conv/with?reserved#chars'
responseStatus = 200
responseBody = ': heartbeat\n\nevent: message.delta\ndata: {"messageId":"assistant","delta":"x","eventVersion":1}\n\n'
let streamActivities = 0
let streamEvents = 0
await streamChatMessage({ conversationId: encodedId, clientMessageId: 'encoded', content: 'path', model: 'mock', onActivity: () => { streamActivities += 1 }, onEvent: () => { streamEvents += 1 } })
assert.equal(streamEvents, 1, 'heartbeat comment 不得进入 ChatStreamEvent')
assert.ok(streamActivities >= 2, '非空 reader chunk 与 heartbeat comment 都必须记录传输活动')
responseBody = ': heartbeat\n\n'
let attachActivities = 0
await attachChatStream({ conversationId: encodedId, turnId: 'turn/with?#', onActivity: () => { attachActivities += 1 }, onEvent: () => { throw new Error('heartbeat 不得产生事件') } })
assert.ok(attachActivities >= 2, '重附着流也必须记录 chunk 与 heartbeat 活动')
assert(observedUrls.at(-2)?.includes('/conversations/conv%2Fwith%3Freserved%23chars/stream'))
assert(observedUrls.at(-1)?.includes('/conversations/conv%2Fwith%3Freserved%23chars/streams/turn%2Fwith%3F%23'))

let malformedStreamCanceled = false
globalThis.fetch = (async () => new Response(new ReadableStream<Uint8Array>({
  start(controller) { controller.enqueue(new TextEncoder().encode('event: malformed\ndata: {}\n\n')) },
  cancel() { malformedStreamCanceled = true }
}), { status: 200, headers: { 'content-type': 'text/event-stream' } })) as typeof fetch
await assert.rejects(streamChatMessage({
  conversationId: 'conv_cancel', clientMessageId: 'client_cancel', content: 'cancel', model: 'mock', onEvent: () => undefined
}), /格式无效/)
assert.equal(malformedStreamCanceled, true, 'SSE 协议提前失败后必须取消仍未结束的响应体')

const axiosUrls: string[] = []
const previousAdapter = http.defaults.adapter
http.defaults.adapter = (async (config) => {
  axiosUrls.push(String(config.url))
  return { data: { data: config.method === 'post' ? { stopped: true } : [] }, status: 200, statusText: 'OK', headers: {}, config }
})
await chatApi.listMessages(encodedId, { afterSequenceNo: 1 })
await chatApi.getConversationSync(encodedId, 2)
await chatApi.stop(encodedId, { clientMessageId: 'client', turnId: 'turn/with?#' })
http.defaults.adapter = previousAdapter
assert.deepEqual(axiosUrls, [
  '/my-chat/conversations/conv%2Fwith%3Freserved%23chars/messages',
  '/my-chat/conversations/conv%2Fwith%3Freserved%23chars/sync',
  '/my-chat/conversations/conv%2Fwith%3Freserved%23chars/stop'
], 'chat API conversation 动态路径段必须统一编码')

console.log('AI 问答流式 HTTP 类型错误与替换字段回归通过')
