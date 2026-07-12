import assert from 'node:assert/strict'

import { collectChatResponsesSse } from '../../modules/chat/chat-responses-sse.js'

async function* chunks(): AsyncGenerator<Uint8Array> {
  const text = 'event: response.output_item.added\ndata: {"type":"response.output_item.added","item":{"type":"web_search_call","id":"tool_1"}}\n\n'
    + 'event: response.function_call_arguments.delta\ndata: {"type":"response.function_call_arguments.delta","delta":"{\\"q\\":\\"天气\\"}"}\n\n'
    + 'event: response.output_item.done\ndata: {"type":"response.output_item.done","item":{"type":"web_search_call","status":"completed"}}\n\n'
    + 'event: response.output_text.delta\ndata: {"type":"response.output_text.delta","delta":"结果已返回"}\n\n'
    + 'event: response.completed\ndata: {"type":"response.completed","response":{"status":"completed"}}\n\n'
  const bytes = new TextEncoder().encode(text)
  yield bytes.slice(0, 17); yield bytes.slice(17)
}

const events: string[] = []
const result = await collectChatResponsesSse(chunks(), (event) => events.push(event.type))
assert.deepEqual(events, ['tool_started', 'tool_updated', 'tool_completed', 'text_delta', 'completed'])
assert.equal(result.content, '结果已返回')

console.log('AI 问答 Responses SSE 工具事件回归通过')
