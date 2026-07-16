import assert from 'node:assert/strict'

import { applyChatStreamEvent, parseChatSseBlock } from '../../views/chat/chatStream'
import type { ChatMessage } from '../../types/domain/chat'

const event = parseChatSseBlock('event: message.delta\ndata: {"messageId":"msg_2","delta":"你好","eventVersion":1}')
assert.deepEqual(event, { type: 'message.delta', data: { messageId: 'msg_2', delta: '你好', eventVersion: 1 } })
assert.equal(parseChatSseBlock('event: message.delta\ndata: {"messageId":"msg_2","eventVersion":2}'), undefined, 'delta 缺少文本必须拒绝')
assert.equal(parseChatSseBlock('event: message.delta\ndata: {"messageId":"msg_2","delta":"x","eventVersion":1.5}'), undefined, 'eventVersion 必须是安全整数')
assert.equal(parseChatSseBlock('event: message.started\ndata: {"turnId":"turn_1"}'), undefined, 'started 缺少 user/assistant 必须拒绝')
assert.equal(parseChatSseBlock('event: message.snapshot\ndata: {"turnId":"turn_1","assistant":{"id":"msg_2"},"eventVersion":2}'), undefined, 'snapshot 助手投影不完整必须拒绝')
assert.equal(parseChatSseBlock('event: tool.started\ndata: {"messageId":"msg_2","item":null,"eventVersion":2}'), undefined, 'tool item 必须是对象')
assert.equal(parseChatSseBlock('event: message.failed\ndata: {"messageId":"msg_2","eventVersion":2}'), undefined, 'failed 必须包含 code/message')

const messages: ChatMessage[] = [{
  id: 'msg_2', conversationId: 'conv_1', turnId: 'turn_1', sequenceNo: 2,
  role: 'assistant', status: 'streaming', contentText: '', model: 'mock-model',
  createdAt: '2026-07-12T00:00:00.000Z', expiresAt: '2026-07-19T00:00:00.000Z'
}]
applyChatStreamEvent(messages, event!)
assert.equal(messages[0].contentText, '你好')
const reasoning = parseChatSseBlock('event: reasoning.delta\ndata: {"messageId":"msg_2","delta":"先分析","eventVersion":2}')
const tool = parseChatSseBlock('event: tool.started\ndata: {"messageId":"msg_2","item":{"id":"tool_1","type":"web_search_call"},"eventVersion":3}')
assert.ok(reasoning && tool)
applyChatStreamEvent(messages, reasoning)
applyChatStreamEvent(messages, tool)
assert.equal(messages[0].reasoningText, '先分析')
assert.equal(messages[0].toolEvents?.[0]?.type, 'web_search_call')

const snapshot = parseChatSseBlock('event: message.snapshot\ndata: {"turnId":"turn_1","assistant":{"id":"msg_2","status":"streaming","contentText":"权威快照","reasoningText":"完整推理","toolEvents":[],"contentBlocks":[]},"eventVersion":4}')
assert.ok(snapshot)
applyChatStreamEvent(messages, snapshot)
assert.equal(messages[0].contentText, '权威快照', 'snapshot 必须替换而非追加当前 assistant 投影')
assert.equal(messages[0].reasoningText, '完整推理')
const toolSnapshot = parseChatSseBlock('event: message.snapshot\ndata: {"turnId":"turn_1","assistant":{"id":"msg_2","status":"streaming","contentText":"权威快照","reasoningText":"","toolEvents":[{"id":"tool_2","type":"web_search_call","status":"started","item":{"query":"test"}}],"contentBlocks":[{"type":"tool_call","id":"tool_2","toolType":"web_search_call","status":"started","item":{"query":"test"}}]},"eventVersion":5}')
assert.ok(toolSnapshot, '真实 snapshot 必须接受 toolEvents.type 与 contentBlocks.toolType')
applyChatStreamEvent(messages, toolSnapshot)
assert.equal(messages[0].toolEvents?.[0]?.type, 'web_search_call')

applyChatStreamEvent(messages, { type: 'message.canceled', data: { messageId: 'msg_2', eventVersion: 6 } })
assert.equal(messages[0].status, 'canceled')
applyChatStreamEvent(messages, { type: 'message.completed', data: { messageId: 'msg_2', finishReason: 'stop', eventVersion: 7 } })
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
