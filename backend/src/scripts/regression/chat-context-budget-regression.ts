import assert from 'node:assert/strict'

import {
  ChatContextBudgetError,
  estimateChatTokens,
  trimChatContextToBudget,
  validateFixedChatInputBudget
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
  imageTokenEstimate: 0
}
const trimmed = trimChatContextToBudget({ ...baseBudgetInput, maxInputTokens: 6_000 })
assert.deepEqual(trimmed, history.slice(2), '超出预算时必须从最旧完整轮次开始裁剪')
assert.equal(trimmed[0]?.role, 'user')
assert.equal(trimmed.at(-1)?.role, 'assistant')

const shortInstructions = trimChatContextToBudget({ ...baseBudgetInput, maxInputTokens: 9_000 })
const longInstructions = trimChatContextToBudget({ ...baseBudgetInput, instructions: '规则'.repeat(3_500), maxInputTokens: 9_000 })
assert(longInstructions.length < shortInstructions.length, '更长的 system instructions 必须减少可保留历史')

const mediumHistory = [
  { role: 'user' as const, content: '较早问题'.repeat(300) },
  { role: 'assistant' as const, content: '较早回答'.repeat(300) },
  { role: 'user' as const, content: '最近问题' },
  { role: 'assistant' as const, content: '最近回答' }
]
const withoutTools = trimChatContextToBudget({ ...baseBudgetInput, history: mediumHistory, maxInputTokens: 7_000 })
const withTools = trimChatContextToBudget({ ...baseBudgetInput, history: mediumHistory, toolsEnabled: true, maxInputTokens: 7_000 })
assert(withTools.length < withoutTools.length, '启用工具时必须扣除工具定义预留')

const withoutImages = trimChatContextToBudget({ ...baseBudgetInput, maxInputTokens: 9_000 })
const withImage = trimChatContextToBudget({ ...baseBudgetInput, imageTokenEstimate: 4_096, maxInputTokens: 9_000 })
assert(withImage.length < withoutImages.length, '每张图片必须扣除保守 token 预留')

assert.throws(
  () => trimChatContextToBudget({ ...baseBudgetInput, currentUserContent: '很长'.repeat(20_000), maxInputTokens: 16_000 }),
  (error) => error instanceof ChatContextBudgetError && error.code === 'chat_input_exceeds_context',
  '固定提示、当前输入和固定预留已经超限时必须明确拒绝'
)
assert.throws(
  () => validateFixedChatInputBudget({ ...baseBudgetInput, currentUserContent: '很长'.repeat(20_000), maxInputTokens: 16_000 }),
  (error) => error instanceof ChatContextBudgetError && error.code === 'chat_input_exceeds_context',
  '固定输入预检必须能在读取历史前独立拒绝超预算请求'
)

assert.deepEqual(trimChatContextToBudget({ ...baseBudgetInput }), history, '模型目录未返回窗口时不得伪造 16K 或其他客户端上限')

assert(estimateChatTokens('中文') >= 1, '本地 tokenizer 必须能计算中文 token')

console.log('AI 问答上下文预算回归通过')
