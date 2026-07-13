import assert from 'node:assert/strict'

import {
  beginLatestTurnEdit,
  isLatestEditableUserMessage,
  resolveChatReconciliationNotice,
  resolveChatSubmitFailure
} from '../../views/chat/chatTurnEditing'
import type { ChatMessage } from '../../types/domain/chat'

function message(overrides: Partial<ChatMessage> & Pick<ChatMessage, 'id' | 'turnId' | 'sequenceNo' | 'role'>): ChatMessage {
  return {
    conversationId: 'conv_1',
    status: 'completed',
    contentText: overrides.role === 'user' ? '原始 **Markdown**' : '回答',
    contentBlocks: overrides.role === 'user'
      ? [{ type: 'input_marker', inputType: 'input_text', order: 0 }]
      : [],
    model: 'mock-model',
    createdAt: '2026-07-13T00:00:00.000Z',
    expiresAt: '2026-07-20T00:00:00.000Z',
    ...overrides
  }
}

const olderUser = message({ id: 'user_old', turnId: 'turn_old', sequenceNo: 1, role: 'user' })
const olderAssistant = message({ id: 'assistant_old', turnId: 'turn_old', sequenceNo: 2, role: 'assistant' })
const latestUser = message({
  id: 'user_latest',
  turnId: 'turn_latest',
  sequenceNo: 3,
  role: 'user',
  clientMessageId: 'client_latest',
  contentBlocks: [
    { type: 'input_marker', inputType: 'input_text', order: 0 },
    { type: 'input_marker', inputType: 'input_text', order: 1 }
  ]
})
const latestAssistant = message({ id: 'assistant_latest', turnId: 'turn_latest', sequenceNo: 4, role: 'assistant' })
const messages = [olderUser, olderAssistant, latestUser, latestAssistant]

assert.equal(isLatestEditableUserMessage(messages, latestUser.id), true)
assert.equal(isLatestEditableUserMessage(messages, olderUser.id), false)
assert.deepEqual(beginLatestTurnEdit(messages, latestUser.id), {
  conversationId: 'conv_1',
  turnId: 'turn_latest',
  userMessageId: 'user_latest',
  assistantMessageId: 'assistant_latest',
  content: '原始 **Markdown**'
})

for (const invalid of [
  [latestUser],
  [latestAssistant, latestUser],
  [latestUser, message({ ...latestAssistant, turnId: 'turn_other' })],
  [latestUser, message({ ...latestAssistant, sequenceNo: 5 })],
  [message({ ...latestUser, status: 'streaming' }), latestAssistant],
  [latestUser, message({ ...latestAssistant, status: 'failed' })],
  [latestUser, message({ ...latestAssistant, status: 'canceled' })],
  [message({ ...latestUser, contentText: '   ' }), latestAssistant],
  [message({ ...latestUser, contentBlocks: [] }), latestAssistant],
  [message({ ...latestUser, contentBlocks: [{ type: 'input_marker', inputType: 'input_image', order: 0 }] }), latestAssistant],
  [message({ ...latestUser, contentBlocks: [{ type: 'input_marker', inputType: 'input_text', order: 1 }] }), latestAssistant],
  [message({ ...latestUser, contentBlocks: [
    { type: 'input_marker', inputType: 'input_text', order: 0 },
    { type: 'input_marker', inputType: 'input_text', order: 2 }
  ] }), latestAssistant],
  [message({ ...latestUser, contentBlocks: [
    { type: 'input_marker', inputType: 'input_text', order: 0, unexpected: true } as unknown as NonNullable<ChatMessage['contentBlocks']>[number]
  ] }), latestAssistant],
  [message({ ...latestUser, contentBlocks: [{ type: 'reasoning', text: '畸形用户块' }] }), latestAssistant]
] satisfies ChatMessage[][]) {
  assert.equal(isLatestEditableUserMessage(invalid, invalid.find((item) => item.role === 'user')?.id ?? latestUser.id), false)
}

assert.deepEqual(resolveChatSubmitFailure({ streamStarted: false, accepted: false, replaceConflict: false }), {
  restoreSubmittedDraft: true,
  clearEditing: false
}, '开始前且未接受时应恢复草稿并保留 replaceTurnId 以便重试')
assert.deepEqual(resolveChatSubmitFailure({ streamStarted: false, accepted: true, replaceConflict: false }), {
  restoreSubmittedDraft: false,
  clearEditing: true
}, '开始前断线但服务端已接受时不得恢复旧 prompt')
assert.deepEqual(resolveChatSubmitFailure({ streamStarted: true, accepted: true, replaceConflict: false }), {
  restoreSubmittedDraft: false,
  clearEditing: true
}, '开始后的错误不得恢复旧 prompt')
assert.deepEqual(resolveChatSubmitFailure({ streamStarted: false, accepted: false, replaceConflict: true }), {
  restoreSubmittedDraft: true,
  clearEditing: true
}, '替换冲突应保留提交草稿，但清除已经失效的 replaceTurnId')

assert.equal(resolveChatReconciliationNotice({ accepted: true, assistantStatus: 'completed', silent: false }), 'none', '仓库已完成时不得把真实成功误报为发送失败')
assert.equal(resolveChatReconciliationNotice({ accepted: true, assistantStatus: 'canceled', silent: true }), 'none', '用户主动停止并已取消时保持静默')
assert.equal(resolveChatReconciliationNotice({ accepted: true, assistantStatus: 'canceled', silent: false }), 'stopped')
assert.equal(resolveChatReconciliationNotice({ accepted: true, assistantStatus: 'failed', silent: false }), 'failed')
assert.equal(resolveChatReconciliationNotice({ accepted: true, assistantStatus: 'streaming', silent: false }), 'pending')
assert.equal(resolveChatReconciliationNotice({ accepted: false, silent: false }), 'transport_error')
assert.equal(resolveChatReconciliationNotice({ accepted: false, silent: true }), 'none')

console.log('AI 问答最近轮次编辑资格与失败对账回归通过')
