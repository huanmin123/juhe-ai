import assert from 'node:assert/strict'

import {
  sanitizeChatContentBlocksForPersistence,
  terminalizeChatContentBlocksForPersistence
} from '../../modules/chat/chat-content-blocks.js'

const small = [{ type: 'reasoning', text: '简短过程' }]
assert.equal(sanitizeChatContentBlocksForPersistence(small), small, '有界结构块应原样持久化')
assert.deepEqual(sanitizeChatContentBlocksForPersistence([{ type: 'reasoning', text: 'x'.repeat(200 * 1024) }]), [], '超限结构块必须降级为空，确保失败终态可落库')
const circular: Record<string, unknown> = {}
circular.self = circular
assert.deepEqual(sanitizeChatContentBlocksForPersistence([circular]), [], '不可序列化结构块也必须降级为空')

const terminalized = terminalizeChatContentBlocksForPersistence([
  { type: 'reasoning', blockId: 'reasoning-active', order: 1, text: '思考', status: 'started' },
  { type: 'tool_call', blockId: 'tool-updated', order: 2, callId: 'search', toolType: 'web_search', status: 'updated' },
  { type: 'reasoning', blockId: 'reasoning-completed', order: 3, text: '完成', status: 'completed' },
  { type: 'output_image', blockId: 'image-active', order: 4, assetId: 'asset', status: 'started' },
  { type: 'output_text', blockId: 'text', order: 5, text: '正文' }
], 'completed')
assert.deepEqual(terminalized.map((block) => 'status' in block ? block.status : undefined), [
  'completed', 'completed', 'completed', 'completed', undefined
], '消息完成前必须把 reasoning、tool 和 image 活动块一并收敛为 completed')

const failed = terminalizeChatContentBlocksForPersistence([
  { type: 'reasoning', status: 'started' },
  { type: 'tool_call', status: 'updated' },
  { type: 'tool_call', status: 'completed' }
], 'failed')
assert.deepEqual(failed.map((block) => block.status), ['failed', 'failed', 'completed'], '失败只能改写仍活动的过程块')

console.log('AI 问答持久化结构块有界降级回归通过')
