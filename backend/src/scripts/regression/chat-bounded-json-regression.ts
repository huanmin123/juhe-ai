import assert from 'node:assert/strict'

import { readChatJsonResponse } from '../../modules/chat/chat-bounded-json.js'

const payload = { data: Array.from({ length: 3000 }, (_item, index) => ({ id: `model-${index}` })) }
const parsed = await readChatJsonResponse(new Response(JSON.stringify(payload)), 512 * 1024)
assert.equal((parsed as typeof payload).data.length, 3000, '合法的大模型列表不能被先截断再误判为无效 JSON')

let canceled = false
const oversized = new Response(new ReadableStream<Uint8Array>({
  start(controller) { controller.enqueue(new TextEncoder().encode('x'.repeat(70 * 1024))) },
  cancel() { canceled = true }
}))
await assert.rejects(() => readChatJsonResponse(oversized, 64 * 1024), /超过 64 KiB 上限/)
assert.equal(canceled, true, '超限上游响应必须主动取消 reader')

console.log('AI 问答上游 JSON 流式限长读取回归通过')
