import assert from 'node:assert/strict'

import {
  beginLatestTurnEdit,
  isDefinitiveChatHttpRejection,
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
      ? [{ type: 'input_text', text: '原始 **Markdown**', order: 0 }]
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
    { type: 'input_text', text: '原始 ', order: 0 },
    { type: 'input_text', text: '**Markdown**', order: 1 }
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
  content: '原始 **Markdown**',
  contentBlocks: [
    { type: 'input_text', text: '原始 ' },
    { type: 'input_text', text: '**Markdown**' }
  ]
})
assert.equal(isLatestEditableUserMessage([latestUser, message({ ...latestAssistant, status: 'failed' })], latestUser.id), true, '最近失败轮次必须允许显式编辑并重新生成')
assert.equal(isLatestEditableUserMessage([latestUser, message({ ...latestAssistant, status: 'canceled' })], latestUser.id), true, '最近停止轮次必须允许显式编辑并重新生成')

const imageEdit = beginLatestTurnEdit([
  message({
    ...latestUser,
    contentText: '图片前\n[图片]\n图片后',
    contentBlocks: [
      { type: 'input_text', text: '图片前', order: 0 },
      { type: 'input_image', assetId: 'asset_1', order: 1 },
      { type: 'input_text', text: '图片后', order: 2 }
    ]
  }),
  latestAssistant
], latestUser.id)
assert.deepEqual(imageEdit?.contentBlocks, [
  { type: 'input_text', text: '图片前' },
  { type: 'input_image', assetId: 'asset_1' },
  { type: 'input_text', text: '图片后' }
], '最近一轮含图片时也必须恢复原始文字与图片顺序')

const fiveImageBlocks = Array.from({ length: 5 }, (_item, index) => ({ type: 'input_image' as const, assetId: `asset_${index}`, order: index }))
assert.equal(beginLatestTurnEdit([
  message({ ...latestUser, contentBlocks: fiveImageBlocks }),
  latestAssistant
], latestUser.id)?.contentBlocks.length, 5, '最近一轮 5 张图片必须允许恢复编辑')
assert.equal(beginLatestTurnEdit([
  message({ ...latestUser, contentBlocks: [...fiveImageBlocks, { type: 'input_image', assetId: 'asset_5', order: 5 }] }),
  latestAssistant
], latestUser.id), undefined, '编辑重发边界不得恢复伪造的第 6 张图片')

for (const invalid of [
  [latestUser],
  [latestAssistant, latestUser],
  [latestUser, message({ ...latestAssistant, turnId: 'turn_other' })],
  [latestUser, message({ ...latestAssistant, sequenceNo: 5 })],
  [message({ ...latestUser, status: 'streaming' }), latestAssistant],
  [message({ ...latestUser, contentBlocks: [] }), latestAssistant],
  [message({ ...latestUser, contentBlocks: [{ type: 'input_text', text: '原始 **Markdown**', order: 1 }] }), latestAssistant],
  [message({ ...latestUser, contentBlocks: [
    { type: 'input_text', text: '原始 ', order: 0 },
    { type: 'input_text', text: '**Markdown**', order: 2 }
  ] }), latestAssistant],
  [message({ ...latestUser, contentBlocks: [
    { type: 'input_text', text: '原始 **Markdown**', order: 0, unexpected: true } as unknown as NonNullable<ChatMessage['contentBlocks']>[number]
  ] }), latestAssistant],
  [message({ ...latestUser, contentBlocks: [
    { type: 'input_text', order: 0 } as unknown as NonNullable<ChatMessage['contentBlocks']>[number]
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
assert.deepEqual(resolveChatSubmitFailure({ streamStarted: false, accepted: false, confirmed: false, replaceConflict: false }), {
  restoreSubmittedDraft: false,
  clearEditing: false,
  pendingConfirmation: true
}, '权威读取全部失败时不得恢复旧 replaceTurnId 草稿，必须进入待确认门禁')

assert.equal(resolveChatReconciliationNotice({ accepted: true, assistantStatus: 'completed', silent: false }), 'none', '仓库已完成时不得把真实成功误报为发送失败')
assert.equal(resolveChatReconciliationNotice({ accepted: true, assistantStatus: 'canceled', silent: true }), 'none', '用户主动停止并已取消时保持静默')
assert.equal(resolveChatReconciliationNotice({ accepted: true, assistantStatus: 'canceled', silent: false }), 'stopped')
assert.equal(resolveChatReconciliationNotice({ accepted: true, assistantStatus: 'failed', silent: false }), 'failed')
assert.equal(resolveChatReconciliationNotice({ accepted: true, assistantStatus: 'streaming', silent: false }), 'pending')
assert.equal(resolveChatReconciliationNotice({ accepted: false, silent: false }), 'transport_error')
assert.equal(resolveChatReconciliationNotice({ accepted: false, silent: true }), 'none')
assert.equal(isDefinitiveChatHttpRejection({ status: 401, code: 'future_auth_code' }), true)
assert.equal(isDefinitiveChatHttpRejection({ status: 429, code: 'future_rate_limit_code' }), true)
assert.equal(isDefinitiveChatHttpRejection({ status: 409, code: 'chat_message_already_exists' }), false, '幂等已接受必须继续权威对账')
assert.equal(isDefinitiveChatHttpRejection({ status: 500, code: 'unknown_server_error' }), false, '5xx 可能发生在 accept 后，不能恢复旧 replaceTurnId')

console.log('AI 问答最近轮次编辑资格与失败对账回归通过')
