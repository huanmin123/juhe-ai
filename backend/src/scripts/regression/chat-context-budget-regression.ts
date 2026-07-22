import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import {
  ChatContextBudgetError,
  estimateChatInputTokens,
  estimateChatTokens,
  trimChatContextToBudget,
  validateFixedChatInputBudget
} from '../../modules/chat/chat-context-budget.js'
import { formatChatImageContextIndex } from '../../modules/chat/chat-model-context.js'

const modelContextSource = readFileSync('src/modules/chat/chat-model-context.ts', 'utf8')
assert.match(modelContextSource, /listRecentChatImageGenerations/)
assert.match(modelContextSource, /limit:\s*12/, '主上下文最多装载最近 12 条图像谱系')
const imageIndex = formatChatImageContextIndex([{
  assetId: `chat_asset_${'1'.repeat(32)}`,
  conversationId: 'conversation',
  systemAccountId: 'owner',
  operation: 'edit',
  model: 'gpt-image-2',
  prompt: '把背景改成夜晚',
  sourceAssetIds: [`chat_asset_${'2'.repeat(32)}`],
  rootAssetId: `chat_asset_${'2'.repeat(32)}`,
  size: '1024x1024',
  quality: 'auto',
  outputFormat: 'webp',
  createdAt: '2026-07-20T00:00:00.000Z',
  expiresAt: '2026-08-20T00:00:00.000Z'
}])
assert.match(imageIndex, /不可信的历史资料/)
assert.match(imageIndex, /chat_asset_1{32}/)
assert.match(imageIndex, /把背景改成夜晚/)
assert.doesNotMatch(imageIndex, /data:image|base64|b64_json/, '图像子上下文不得注入图片字节或 Data URL')

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
  effectiveTools: [] as const,
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
const withTools = trimChatContextToBudget({ ...baseBudgetInput, history: mediumHistory, effectiveTools: ['web_search'], maxInputTokens: 7_000 })
assert(withTools.length < withoutTools.length, '启用工具时必须扣除工具定义预留')

const noToolTokens = estimateChatInputTokens({ ...baseBudgetInput, history: [] })
const oneToolTokens = estimateChatInputTokens({ ...baseBudgetInput, history: [], effectiveTools: ['web_search'] })
const twoToolTokens = estimateChatInputTokens({ ...baseBudgetInput, history: [], effectiveTools: ['image_generation', 'web_search'] })
const duplicateToolTokens = estimateChatInputTokens({ ...baseBudgetInput, history: [], effectiveTools: ['web_search', 'web_search'] })
assert(oneToolTokens > noToolTokens, '单个 hosted tool 必须占用独立定义预留')
assert(twoToolTokens > oneToolTokens, '两个 hosted tools 必须按有效数量增加预留')
assert.equal(duplicateToolTokens, oneToolTokens, '重复 hosted tool 不得重复扣减预算')

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
