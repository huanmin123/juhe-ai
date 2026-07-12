import assert from 'node:assert/strict'

import { applyChatStreamEvent, parseChatSseBlock } from '../../views/chat/chatStream'
import type { ChatMessage } from '../../types/domain/chat'

const event = parseChatSseBlock('event: message.delta\ndata: {"messageId":"msg_2","delta":"你好"}')
assert.deepEqual(event, { type: 'message.delta', data: { messageId: 'msg_2', delta: '你好' } })

const messages: ChatMessage[] = [{
  id: 'msg_2', conversationId: 'conv_1', turnId: 'turn_1', sequenceNo: 2,
  role: 'assistant', status: 'streaming', contentText: '', model: 'mock-model',
  createdAt: '2026-07-12T00:00:00.000Z', expiresAt: '2026-07-19T00:00:00.000Z'
}]
applyChatStreamEvent(messages, event!)
assert.equal(messages[0].contentText, '你好')
const reasoning = parseChatSseBlock('event: reasoning.delta\ndata: {"messageId":"msg_2","delta":"先分析"}')
const tool = parseChatSseBlock('event: tool.started\ndata: {"messageId":"msg_2","item":{"id":"tool_1","type":"web_search_call"}}')
assert.ok(reasoning && tool)
applyChatStreamEvent(messages, reasoning)
applyChatStreamEvent(messages, tool)
assert.equal(messages[0].reasoningText, '先分析')
assert.equal(messages[0].toolEvents?.[0]?.type, 'web_search_call')

applyChatStreamEvent(messages, { type: 'message.completed', data: { messageId: 'msg_2', finishReason: 'stop' } })
assert.equal(messages[0].status, 'completed')
assert.equal(messages[0].finishReason, 'stop')

console.log('AI 问答前端流式状态回归通过')
