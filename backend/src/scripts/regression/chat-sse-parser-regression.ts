import assert from 'node:assert/strict'

import { collectOpenAIChatSse } from '../../modules/chat/chat-gateway-sse.js'

const encoder = new TextEncoder()
const payload = [
  'data: {"choices":[{"delta":{"content":"你"}}]}\n\n',
  ': heartbeat\n\n',
  'data: {"choices":[{"delta":{"content":"好"},"finish_reason":"stop"}]}\n\n',
  'data: {"choices":[],"usage":{"prompt_tokens":19,"completion_tokens":3}}\n\n',
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
assert.equal(result.inputTokens, 19)
assert.equal(result.outputTokens, 3)

await assert.rejects(
  collectOpenAIChatSse(async function* () {
    yield encoder.encode('data: {"choices":[{"delta":{"content":"未完成"}}]}\n\n')
  }(), 1024),
  /缺少 \[DONE\]/
)

await assert.rejects(
  collectOpenAIChatSse(async function* () {
    yield encoder.encode(`data: ${'x'.repeat(80 * 1024)}`)
  }(), 1024),
  /单个事件超过/,
  'Chat Completions 无分隔 pending block 必须有硬上限'
)

async function* manySmallEvents(): AsyncGenerator<Uint8Array> {
  for (let index = 0; index < 2050; index += 1) yield encoder.encode('data: {"choices":[{"delta":{"content":"x"}}]}\n\n')
  yield encoder.encode('data: [DONE]\n\n')
}

const longEventStream = await collectOpenAIChatSse(manySmallEvents(), 4096)
assert.equal(longEventStream.done, true, '默认事件预算必须允许超过 2048 个合法小事件完成')
assert.equal(longEventStream.content, 'x'.repeat(2050))

await assert.rejects(
  collectOpenAIChatSse(manySmallEvents(), 4096, undefined, 2_048),
  /事件数量超过 2048 上限/,
  'Chat Completions 显式低事件预算仍必须拒绝第 2049 个事件'
)

console.log('AI 问答 OpenAI SSE 解析回归通过')
