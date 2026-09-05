import assert from 'node:assert/strict'

import { collectOpenAIChatSse } from '../../modules/chat/chat-gateway-sse.js'
import { collectChatResponsesSse } from '../../modules/chat/chat-responses-sse.js'

const encoder = new TextEncoder()
async function* chunks(value: string, widths = [3, 7, 11, 5]): AsyncGenerator<Uint8Array> {
  const bytes = encoder.encode(value)
  let offset = 0
  let index = 0
  while (offset < bytes.byteLength) {
    const width = widths[index++ % widths.length]!
    yield bytes.slice(offset, Math.min(bytes.byteLength, offset + width))
    offset += width
  }
}

const chatEvents = [
  { choices: [{ delta: { role: 'assistant', content: null, tool_calls: [{ index: 0, id: 'call-chat-1', type: 'function', function: { name: 'diagnostic_echo', arguments: '{"te' } }] }, finish_reason: null }] },
  { choices: [{ delta: { tool_calls: [{ index: 0, function: { arguments: 'xt":"chat"}' } }] }, finish_reason: null }] },
  { choices: [{ delta: {}, finish_reason: 'tool_calls' }], usage: { prompt_tokens: 12, completion_tokens: 5 } }
].map((payload) => `data: ${JSON.stringify(payload)}\n\n`).join('') + 'data: [DONE]\n\n'

const chatResult = await collectOpenAIChatSse(chunks(chatEvents), 192 * 1024)
assert.equal(chatResult.finishReason, 'tool_calls')
assert.deepEqual(chatResult.toolCalls, [{
  callId: 'call-chat-1',
  toolName: 'diagnostic_echo',
  argumentsJson: '{"text":"chat"}',
  sourceOrder: 0
}])
assert.deepEqual(chatResult.continuationItems, [{
  role: 'assistant',
  content: null,
  tool_calls: [{ id: 'call-chat-1', type: 'function', function: { name: 'diagnostic_echo', arguments: '{"text":"chat"}' } }]
}])

const repeatedChatFields = [
  { choices: [{ delta: { tool_calls: [{ index: 0, id: 'call-repeat-1', function: { name: 'diagnostic_echo', arguments: '{"text":' } }] } }] },
  { choices: [{ delta: { tool_calls: [{ index: 0, id: 'call-repeat-1', function: { name: 'diagnostic_echo', arguments: '"repeat"}' } }] }, finish_reason: 'tool_calls' }] }
].map((payload) => `data: ${JSON.stringify(payload)}\n\n`).join('') + 'data: [DONE]\n\n'
const repeatedChatResult = await collectOpenAIChatSse(chunks(repeatedChatFields), 192 * 1024)
assert.equal(repeatedChatResult.toolCalls[0]?.callId, 'call-repeat-1', '上游重复发送完整 call id 时不得拼接出重复 ID')
assert.equal(repeatedChatResult.toolCalls[0]?.toolName, 'diagnostic_echo', '上游重复发送完整工具名时不得拼接出重复名称')

const reasoningItem = { type: 'reasoning', id: 'reasoning-responses-1', summary: [{ type: 'summary_text', text: '需要调用工具' }] }
const functionItem = { type: 'function_call', id: 'fc-responses-1', call_id: 'call-responses-1', name: 'diagnostic_echo', arguments: '{"text":"responses"}', status: 'completed' }
const responsesEvents = [
  { type: 'response.output_item.added', output_index: 0, item: reasoningItem },
  { type: 'response.output_item.done', output_index: 0, item: reasoningItem },
  { type: 'response.output_item.added', output_index: 1, item: { ...functionItem, arguments: '' } },
  { type: 'response.function_call_arguments.delta', output_index: 1, item_id: 'fc-responses-1', delta: '{"text":' },
  { type: 'response.function_call_arguments.delta', output_index: 1, item_id: 'fc-responses-1', delta: '"responses"}' },
  { type: 'response.output_item.done', output_index: 1, item: functionItem },
  { type: 'response.completed', response: { id: 'resp-1', output: [reasoningItem, functionItem], usage: { input_tokens: 15, output_tokens: 7 } } }
].map((payload) => `event: ${payload.type}\ndata: ${JSON.stringify(payload)}\n\n`).join('')

const responsesResult = await collectChatResponsesSse(chunks(responsesEvents), () => undefined)
assert.deepEqual(responsesResult.toolCalls, [{
  callId: 'call-responses-1',
  toolName: 'diagnostic_echo',
  argumentsJson: '{"text":"responses"}',
  sourceOrder: 1
}])
assert.deepEqual(responsesResult.continuationItems, [reasoningItem, functionItem], 'Responses reasoning 与 function_call 必须按 output 顺序保留')

const fallbackResponsesEvents = [
  { type: 'response.output_item.done', output_index: 0, item: reasoningItem },
  { type: 'response.output_item.done', output_index: 1, item: functionItem },
  { type: 'response.completed', response: { id: 'resp-output-omitted', usage: { input_tokens: 9, output_tokens: 4 } } }
].map((payload) => `event: ${payload.type}\ndata: ${JSON.stringify(payload)}\n\n`).join('')
const fallbackResponsesResult = await collectChatResponsesSse(chunks(fallbackResponsesEvents), () => undefined)
assert.deepEqual(fallbackResponsesResult.toolCalls, [{
  callId: 'call-responses-1',
  toolName: 'diagnostic_echo',
  argumentsJson: '{"text":"responses"}',
  sourceOrder: 1
}], 'response.completed 省略 output 时必须从 output_item.done 恢复工具调用')
assert.deepEqual(
  fallbackResponsesResult.continuationItems,
  [reasoningItem, functionItem],
  'response.completed 省略 output 时仍必须保留 reasoning/function_call 顺序'
)

const compatibleFunctionItem = {
  type: 'function_call',
  call_id: 'call-compatible-1',
  name: 'diagnostic_echo',
  arguments: '{"text":"compatible"}'
}
const compatibleResponsesEvents = [
  { type: 'response.output_item.done', output_index: 0, item: compatibleFunctionItem },
  { type: 'response.completed', response: { id: 'resp-compatible', output: [compatibleFunctionItem] } }
].map((payload) => `event: ${payload.type}\ndata: ${JSON.stringify(payload)}\n\n`).join('')
const compatibleResponsesResult = await collectChatResponsesSse(chunks(compatibleResponsesEvents), () => undefined)
assert.deepEqual(compatibleResponsesResult.continuationItems, [{
  ...compatibleFunctionItem,
  id: 'fc_compatible-1',
  status: 'completed'
}], '兼容上游省略 function_call id/status 时必须补齐可回传的 Responses 项')

const oversizedArgument = 'a'.repeat(70 * 1024)
const oversizedChat = `data: ${JSON.stringify({ choices: [{ delta: { tool_calls: [{ index: 0, id: 'too-large', type: 'function', function: { name: 'diagnostic_echo', arguments: oversizedArgument } }] }, finish_reason: null }] })}\n\ndata: [DONE]\n\n`
await assert.rejects(
  collectOpenAIChatSse(chunks(oversizedChat, [64 * 1024]), 192 * 1024),
  /工具参数|64 KiB|单个事件/,
  'Chat function arguments 必须受单调用或事件字节上限保护'
)

console.log('AI 问答内部工具 SSE 归一化回归通过')
