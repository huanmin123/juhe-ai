import assert from 'node:assert/strict'

import { sanitizeChatContentBlocksForPersistence } from '../../modules/chat/chat-content-blocks.js'

const small = [{ type: 'reasoning', text: '简短过程' }]
assert.equal(sanitizeChatContentBlocksForPersistence(small), small, '有界结构块应原样持久化')
assert.deepEqual(sanitizeChatContentBlocksForPersistence([{ type: 'reasoning', text: 'x'.repeat(200 * 1024) }]), [], '超限结构块必须降级为空，确保失败终态可落库')
const circular: Record<string, unknown> = {}
circular.self = circular
assert.deepEqual(sanitizeChatContentBlocksForPersistence([circular]), [], '不可序列化结构块也必须降级为空')

console.log('AI 问答持久化结构块有界降级回归通过')
