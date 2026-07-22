import assert from 'node:assert/strict'

import { collectChatResponsesSse } from '../../modules/chat/chat-responses-sse.js'

async function* chunks(): AsyncGenerator<Uint8Array> {
  const text = 'event: response.reasoning_summary_text.delta\ndata: {"type":"response.reasoning_summary_text.delta","delta":"正在判断工具"}\n\n'
    + 'event: response.output_item.done\ndata: {"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"reasoning_1","summary":[]}}\n\n'
    + 'event: response.output_item.added\ndata: {"type":"response.output_item.added","item":{"type":"web_search_call","id":"tool_1"}}\n\n'
    + 'event: response.function_call_arguments.delta\ndata: {"type":"response.function_call_arguments.delta","delta":"{\\"q\\":\\"天气\\"}"}\n\n'
    + 'event: response.output_item.done\ndata: {"type":"response.output_item.done","item":{"type":"web_search_call","status":"completed"}}\n\n'
    + 'event: response.output_item.added\ndata: {"type":"response.output_item.added","item":{"type":"image_generation_call","id":"img_1","status":"started"}}\n\n'
    + 'event: response.image_generation_call.in_progress\ndata: {"type":"response.image_generation_call.in_progress","item":{"type":"image_generation_call","id":"img_1","status":"in_progress","partial_image":"not-base64-event"}}\n\n'
    + 'event: response.image_generation_call.completed\ndata: {"type":"response.image_generation_call.completed","item":{"type":"image_generation_call","id":"img_1","status":"completed","result":"iVBORw==","revised_prompt":"画一只猫"}}\n\n'
    + 'event: response.image_generation_call.completed\ndata: {"type":"response.image_generation_call.completed","item":{"type":"image_generation_call","id":"img_1","status":"completed","result":"重复结果"}}\n\n'
    + 'event: response.output_text.delta\ndata: {"type":"response.output_text.delta","delta":"结果已返回"}\n\n'
    + 'event: response.completed\ndata: {"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":123,"output_tokens":17}}}\n\n'
  const bytes = new TextEncoder().encode(text)
  yield bytes.slice(0, 17); yield bytes.slice(17)
}

const events: Array<{ type: string; item?: Record<string, unknown> }> = []
const result = await collectChatResponsesSse(chunks(), (event) => events.push(event.type === 'image_started' || event.type === 'image_updated' || event.type === 'image_completed' || event.type === 'image_failed' ? { type: event.type, item: event.item } : { type: event.type }))
assert.deepEqual(events.map((event) => event.type), ['reasoning_delta', 'reasoning_completed', 'tool_started', 'tool_updated', 'tool_completed', 'image_started', 'image_updated', 'image_completed', 'text_delta', 'completed'])
assert.equal(events.filter((event) => event.type === 'image_completed').length, 1, '同一 callId 只允许首个合法 final')
assert.equal(events.find((event) => event.type === 'image_started')?.item?.callId, 'img_1', '图片开始事件必须把 item.id 归一为稳定 callId')
assert.equal(events.find((event) => event.type === 'image_completed')?.item?.callId, 'img_1')
assert.equal(events.find((event) => event.type === 'image_completed')?.item?.result, undefined, '图像 Base64 不得进入普通 ChatResponsesEvent')
assert.equal(events.some((event) => JSON.stringify(event).includes('not-base64-event')), false, 'partial_image 负载不得进入普通事件')

let imageSinkCallId = ''
let imageSinkBytes = 0
async function* largeImageChunks(): AsyncGenerator<Uint8Array> {
  const result = 'A'.repeat(96 * 1024)
  const payload = `event: response.image_generation_call.completed\ndata: {"type":"response.image_generation_call.completed","item":{"type":"image_generation_call","id":"img_large","status":"completed","result":"${result}"}}\n\n`
  const bytes = new TextEncoder().encode(payload)
  for (let offset = 0; offset < bytes.length; offset += 97) yield bytes.slice(offset, offset + 97)
  yield new TextEncoder().encode('event: response.completed\ndata: {"type":"response.completed","response":{"status":"completed"}}\n\n')
}
const largeResult = await collectChatResponsesSse(largeImageChunks(), () => undefined, 192 * 1024, 65_536, async (input) => {
  imageSinkCallId = input.callId
  for await (const chunk of input.chunks) imageSinkBytes += chunk.length
})
assert.equal(largeResult.content, '')
assert.equal(imageSinkCallId, 'img_large')
assert.equal(imageSinkBytes, 96 * 1024)

async function* completedResponseWithNestedImageOutput(): AsyncGenerator<Uint8Array> {
  yield new TextEncoder().encode('event: response.image_generation_call.completed\ndata: {"type":"response.image_generation_call.completed","item":{"type":"image_generation_call","id":"img_nested","status":"completed","result":"iVBORw=="}}\n\n')
  yield new TextEncoder().encode('event: response.completed\ndata: {"type":"response.completed","response":{"status":"completed","output":[{"type":"image_generation_call","id":"img_nested","status":"completed","result":"iVBORw=="}],"usage":{"input_tokens":7,"output_tokens":3}}}\n\n')
}
const nestedTerminalEvents: string[] = []
let nestedImageSinkCalls = 0
const nestedTerminalResult = await collectChatResponsesSse(completedResponseWithNestedImageOutput(), (event) => nestedTerminalEvents.push(event.type), undefined, undefined, async (input) => {
  nestedImageSinkCalls += 1
  for await (const _chunk of input.chunks) { /* drain the bounded image stream */ }
})
assert.equal(nestedImageSinkCalls, 1, 'response.completed 内嵌 image_generation_call 不得重复落图')
assert.deepEqual(nestedTerminalEvents, ['image_completed', 'completed'], 'response.completed 必须保留为消息终态事件')
assert.deepEqual({ inputTokens: nestedTerminalResult.inputTokens, outputTokens: nestedTerminalResult.outputTokens }, { inputTokens: 7, outputTokens: 3 })

async function* itemIdImageResult(): AsyncGenerator<Uint8Array> {
  yield new TextEncoder().encode('event: response.image_generation_call.completed\ndata: {"type":"response.image_generation_call.completed","item_id":"img_item_id","status":"completed","result":"iVBORw=="}\n\n')
  yield new TextEncoder().encode('event: response.completed\ndata: {"type":"response.completed","response":{"status":"completed"}}\n\n')
}
let itemIdSinkCallId = ''
await collectChatResponsesSse(itemIdImageResult(), () => undefined, undefined, undefined, async (input) => {
  itemIdSinkCallId = input.callId
  for await (const _chunk of input.chunks) { /* drain */ }
})
assert.equal(itemIdSinkCallId, 'img_item_id', 'Responses bridge 的 item_id 必须作为图片 callId')

async function* terminalThenSocketAbort(): AsyncGenerator<Uint8Array> {
  yield new TextEncoder().encode('event: response.completed\ndata: {"type":"response.completed","response":{"status":"completed"}}\n\n')
  throw new Error('aborted after terminal')
}
const terminalThenAbort = await collectChatResponsesSse(terminalThenSocketAbort(), () => undefined)
assert.equal(terminalThenAbort.content, '', '已观察到 response.completed 后的连接中断不得把已完成轮次改为失败')

async function* largeTerminalWithNestedImageThenAbort(): AsyncGenerator<Uint8Array> {
  const imageResult = 'A'.repeat(96 * 1024)
  const payload = `event: response.completed\ndata: ${JSON.stringify({
    type: 'response.completed',
    response: {
      status: 'completed',
      output: [{ type: 'image_generation_call', id: 'img_terminal_large', status: 'completed', result: imageResult }],
      usage: { input_tokens: 19, output_tokens: 5 }
    }
  })}\n\n`
  const bytes = new TextEncoder().encode(payload)
  for (let offset = 0; offset < bytes.length; offset += 113) yield bytes.slice(offset, offset + 113)
  throw new Error('aborted after large terminal')
}
const largeTerminalEvents: string[] = []
let largeTerminalSinkCallId = ''
let largeTerminalSinkBytes = 0
const largeTerminalResult = await collectChatResponsesSse(
  largeTerminalWithNestedImageThenAbort(),
  (event) => largeTerminalEvents.push(event.type),
  undefined,
  undefined,
  async (input) => {
    largeTerminalSinkCallId = input.callId
    for await (const chunk of input.chunks) largeTerminalSinkBytes += chunk.length
  }
)
assert.equal(largeTerminalSinkCallId, 'img_terminal_large', '超大 response.completed 必须流式提取内嵌图片')
assert.equal(largeTerminalSinkBytes, 96 * 1024, '超大终态内嵌图片不得截断')
assert.deepEqual(largeTerminalEvents, ['image_completed', 'completed'], '超大终态必须先落图再结束消息')
assert.deepEqual(
  { inputTokens: largeTerminalResult.inputTokens, outputTokens: largeTerminalResult.outputTokens },
  { inputTokens: 19, outputTokens: 5 },
  '超大终态必须保留 usage'
)

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
    yield new TextEncoder().encode('event: response.output_text.delta\ndata: {"type":"response.output_text.delta","delta":"x"}\n\n')
  }
  yield new TextEncoder().encode('event: response.completed\ndata: {"type":"response.completed","response":{"status":"completed"}}\n\n')
}
const longEventStream = await collectChatResponsesSse(tooManyEvents(), () => undefined)
assert.equal(longEventStream.content, 'x'.repeat(2050), '默认事件预算必须允许超过 2048 个合法小 delta 完成')

await assert.rejects(
  () => collectChatResponsesSse(tooManyEvents(), () => undefined, 192 * 1024, 2_048),
  /事件数量超过 2048 上限/,
  'Responses 显式低事件预算仍必须拒绝第 2049 个事件'
)

async function* ignoredLifecycleEvents(): AsyncGenerator<Uint8Array> {
  for (let index = 0; index < 2049; index += 1) {
    yield new TextEncoder().encode('event: response.in_progress\ndata: {"type":"response.in_progress"}\n\n')
  }
  yield new TextEncoder().encode('event: response.completed\ndata: {"type":"response.completed","response":{"status":"completed"}}\n\n')
}
await assert.rejects(
  () => collectChatResponsesSse(ignoredLifecycleEvents(), () => undefined, 192 * 1024, 2_048),
  /事件数量超过 2048 上限/,
  'Responses 未投影的 lifecycle/unknown raw block 也必须消耗事件预算，不能绕过 CPU DoS 边界'
)

console.log('AI 问答 Responses SSE 工具事件回归通过')
