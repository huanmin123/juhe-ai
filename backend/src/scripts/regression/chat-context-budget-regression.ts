import assert from 'node:assert/strict'

import {
  ChatContextBudgetError,
  estimateChatTokens,
  resolveEffectiveChatContextWindowTokens,
  trimChatContextToBudget
} from '../../modules/chat/chat-context-budget.js'

const history = [
  { role: 'user' as const, content: '旧问题'.repeat(900) },
  { role: 'assistant' as const, content: '旧回答'.repeat(900) },
  { role: 'user' as const, content: '新问题' },
  { role: 'assistant' as const, content: '新回答' }
]
const baseBudgetInput = {
  history,
  currentUserContent: '当前问题',
  instructions: '遵循用户要求',
  toolsEnabled: false,
  imageCount: 0
}
const trimmed = trimChatContextToBudget({ ...baseBudgetInput, contextWindowTokens: 13_000 })
assert.deepEqual(trimmed, history.slice(2), '超出预算时必须从最旧完整轮次开始裁剪')
assert.equal(trimmed[0]?.role, 'user')
assert.equal(trimmed.at(-1)?.role, 'assistant')

const shortInstructions = trimChatContextToBudget({ ...baseBudgetInput, contextWindowTokens: 20_000 })
const longInstructions = trimChatContextToBudget({ ...baseBudgetInput, instructions: '规则'.repeat(3_500), contextWindowTokens: 20_000 })
assert(longInstructions.length < shortInstructions.length, '更长的 system instructions 必须减少可保留历史')

const mediumHistory = [
  { role: 'user' as const, content: '较早问题'.repeat(300) },
  { role: 'assistant' as const, content: '较早回答'.repeat(300) },
  { role: 'user' as const, content: '最近问题' },
  { role: 'assistant' as const, content: '最近回答' }
]
const withoutTools = trimChatContextToBudget({ ...baseBudgetInput, history: mediumHistory, contextWindowTokens: 16_000 })
const withTools = trimChatContextToBudget({ ...baseBudgetInput, history: mediumHistory, toolsEnabled: true, contextWindowTokens: 16_000 })
assert(withTools.length < withoutTools.length, '启用工具时必须扣除工具定义预留')

const withoutImages = trimChatContextToBudget({ ...baseBudgetInput, contextWindowTokens: 20_000 })
const withImage = trimChatContextToBudget({ ...baseBudgetInput, imageCount: 1, contextWindowTokens: 20_000 })
assert(withImage.length < withoutImages.length, '每张图片必须扣除保守 token 预留')

assert.throws(
  () => trimChatContextToBudget({ ...baseBudgetInput, currentUserContent: '很长'.repeat(20_000), contextWindowTokens: 16_000 }),
  (error) => error instanceof ChatContextBudgetError && error.code === 'chat_input_exceeds_context',
  '固定提示、当前输入和固定预留已经超限时必须明确拒绝'
)

assert.equal(resolveEffectiveChatContextWindowTokens({ clientContextWindowTokens: 2_000_000, serverContextWindowTokens: 128_000 }), 128_000)
assert.equal(resolveEffectiveChatContextWindowTokens({ clientContextWindowTokens: 16_000, serverContextWindowTokens: 128_000 }), 16_000)
assert.equal(resolveEffectiveChatContextWindowTokens({ clientContextWindowTokens: 2_000_000 }), 16_000, '未知模型不得盲信客户端声明的 2M 窗口')
assert(estimateChatTokens('中文') >= 2, 'UTF-8 估算不能按英文字符数低估中文')

console.log('AI 问答上下文预算回归通过')
