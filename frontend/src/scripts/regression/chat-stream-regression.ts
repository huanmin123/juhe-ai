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

const oldTurn: ChatMessage[] = [
  { id: 'old_user', conversationId: 'conv_1', turnId: 'old_turn', sequenceNo: 1, role: 'user', status: 'completed', contentText: '旧问题', contentBlocks: [{ type: 'input_text', text: '旧问题', order: 0 }], model: 'mock-model', createdAt: '2026-07-12T00:00:00.000Z', expiresAt: '2026-07-19T00:00:00.000Z' },
  { id: 'old_assistant', conversationId: 'conv_1', turnId: 'old_turn', sequenceNo: 2, role: 'assistant', status: 'completed', contentText: '旧回答', model: 'mock-model', createdAt: '2026-07-12T00:00:01.000Z', expiresAt: '2026-07-19T00:00:01.000Z' }
]
const replacementStarted = {
  type: 'message.started' as const,
  data: {
    turnId: 'new_turn',
    userMessage: { ...oldTurn[0]!, id: 'new_user', turnId: 'new_turn', clientMessageId: 'client_replace', contentText: '新问题' },
    assistantMessage: { ...oldTurn[1]!, id: 'new_assistant', turnId: 'new_turn', status: 'streaming' as const, contentText: '' }
  }
}
applyChatStreamEvent(oldTurn, replacementStarted, { replaceTurnId: 'old_turn' })
applyChatStreamEvent(oldTurn, replacementStarted, { replaceTurnId: 'old_turn' })
assert.deepEqual(oldTurn.map((item) => [item.id, item.sequenceNo]), [['new_user', 1], ['new_assistant', 2]], 'message.started 必须一次性移除旧轮次并用新消息原位替换，重复事件不得重复插入')

console.log('AI 问答前端流式状态回归通过')
