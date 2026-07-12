import assert from 'node:assert/strict'

import { collectOpenAIChatSse } from '../../modules/chat/chat-gateway-sse.js'

const encoder = new TextEncoder()
const payload = [
  'data: {"choices":[{"delta":{"content":"你"}}]}\n\n',
  ': heartbeat\n\n',
  'data: {"choices":[{"delta":{"content":"好"},"finish_reason":"stop"}]}\n\n',
  'data: [DONE]\n\n'
].join('')
const bytes = encoder.encode(payload)
const chunks = [bytes.slice(0, 17), bytes.slice(17, 52), bytes.slice(52, 91), bytes.slice(91)]

const result = await collectOpenAIChatSse(async function* () {
  for (const chunk of chunks) yield chunk
}(), 1024)

assert.equal(result.content, '你好')
assert.equal(result.finishReason, 'stop')
assert.equal(result.done, true)

await assert.rejects(
  collectOpenAIChatSse(async function* () {
    yield encoder.encode('data: {"choices":[{"delta":{"content":"未完成"}}]}\n\n')
  }(), 1024),
  /缺少 \[DONE\]/
)

console.log('AI 问答 OpenAI SSE 解析回归通过')
