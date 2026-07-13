import assert from 'node:assert/strict'

import { deleteActiveChatStreamIfMatches } from '../../modules/chat/chat-active-streams.js'

const previous = { turnId: 'turn_a', controller: new AbortController() }
const current = { turnId: 'turn_b', controller: new AbortController() }
const streams = new Map([['conv_1', current]])

assert.equal(deleteActiveChatStreamIfMatches(streams, 'conv_1', previous.turnId), false, '旧轮次 finally 不得删除同会话的新流')
assert.equal(streams.get('conv_1'), current)
assert.equal(deleteActiveChatStreamIfMatches(streams, 'conv_1', current.turnId), true, '当前轮次 finally 应清理自己的流')
assert.equal(streams.has('conv_1'), false)

console.log('AI 问答活动流条件清理回归通过')
