import assert from 'node:assert/strict'

import { collectChatResponsesSse } from '../../modules/chat/chat-responses-sse.js'

async function* chunks(): AsyncGenerator<Uint8Array> {
  const text = 'event: response.output_item.added\ndata: {"type":"response.output_item.added","item":{"type":"web_search_call","id":"tool_1"}}\n\n'
    + 'event: response.function_call_arguments.delta\ndata: {"type":"response.function_call_arguments.delta","delta":"{\\"q\\":\\"天气\\"}"}\n\n'
    + 'event: response.output_item.done\ndata: {"type":"response.output_item.done","item":{"type":"web_search_call","status":"completed"}}\n\n'
    + 'event: response.output_text.delta\ndata: {"type":"response.output_text.delta","delta":"结果已返回"}\n\n'
    + 'event: response.completed\ndata: {"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":123,"output_tokens":17}}}\n\n'
  const bytes = new TextEncoder().encode(text)
  yield bytes.slice(0, 17); yield bytes.slice(17)
}

const events: string[] = []
const result = await collectChatResponsesSse(chunks(), (event) => events.push(event.type))
assert.deepEqual(events, ['tool_started', 'tool_updated', 'tool_completed', 'text_delta', 'completed'])
assert.equal(result.content, '结果已返回')
assert.equal(result.inputTokens, 123)
assert.equal(result.outputTokens, 17)

async function* truncatedChunks(): AsyncGenerator<Uint8Array> {
  yield new TextEncoder().encode('event: response.output_text.delta\ndata: {"type":"response.output_text.delta","delta":"部分回答"}\n\n')
}
await assert.rejects(
  () => collectChatResponsesSse(truncatedChunks(), () => undefined),
  /缺少 response\.completed/,
  'Responses 只有 delta 就断流时不得被持久化为 completed'
)

async function* oversizedReasoningChunks(): AsyncGenerator<Uint8Array> {
  const delta = '思'.repeat(100_000)
  yield new TextEncoder().encode(`event: response.reasoning_text.delta\ndata: ${JSON.stringify({ type: 'response.reasoning_text.delta', delta })}\n\nevent: response.completed\ndata: {"type":"response.completed","response":{"status":"completed"}}\n\n`)
}
await assert.rejects(
  () => collectChatResponsesSse(oversizedReasoningChunks(), () => undefined),
  /结构化过程超过|单个事件超过/,
  'reasoning/tool 辅助过程必须有独立累计和单事件字节上限'
)

async function* cumulativeReasoningChunks(): AsyncGenerator<Uint8Array> {
  for (let index = 0; index < 4; index += 1) {
    const delta = 'r'.repeat(50 * 1024)
    yield new TextEncoder().encode(`event: response.reasoning_text.delta\ndata: ${JSON.stringify({ type: 'response.reasoning_text.delta', delta })}\n\n`)
  }
  yield new TextEncoder().encode('event: response.completed\ndata: {"type":"response.completed","response":{"status":"completed"}}\n\n')
}
await assert.rejects(
  () => collectChatResponsesSse(cumulativeReasoningChunks(), () => undefined),
  /结构化过程超过/,
  '多个合法小事件累计后也不得突破结构化过程上限'
)

async function* unterminatedOversizedBlock(): AsyncGenerator<Uint8Array> {
  yield new TextEncoder().encode(`event: response.output_text.delta\ndata: ${'x'.repeat(80 * 1024)}`)
}
await assert.rejects(
  () => collectChatResponsesSse(unterminatedOversizedBlock(), () => undefined),
  /单个事件超过/,
  '没有 SSE 分隔符的 pending block 也必须有界'
)

async function* tooManyEvents(): AsyncGenerator<Uint8Array> {
  for (let index = 0; index < 2050; index += 1) {
    yield new TextEncoder().encode('event: response.reasoning_text.delta\ndata: {"type":"response.reasoning_text.delta","delta":""}\n\n')
  }
  yield new TextEncoder().encode('event: response.completed\ndata: {"type":"response.completed","response":{"status":"completed"}}\n\n')
}
await assert.rejects(
  () => collectChatResponsesSse(tooManyEvents(), () => undefined),
  /事件数量超过/,
  'Responses 事件总数必须有界且不得无界保留事件数组'
)

console.log('AI 问答 Responses SSE 工具事件回归通过')
