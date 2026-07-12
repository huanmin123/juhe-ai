import assert from 'node:assert/strict'

import { estimateChatTokens, trimChatContextToBudget } from '../../modules/chat/chat-context-budget.js'

const history = [
  { role: 'user' as const, content: '旧问题'.repeat(900) },
  { role: 'assistant' as const, content: '旧回答'.repeat(900) },
  { role: 'user' as const, content: '新问题' },
  { role: 'assistant' as const, content: '新回答' }
]
const trimmed = trimChatContextToBudget({ history, currentUserContent: '当前问题', contextWindowTokens: 13_000 })
assert.deepEqual(trimmed, history.slice(2), '超出预算时必须从最旧完整轮次开始裁剪')
assert.equal(trimmed[0]?.role, 'user')
assert.equal(trimmed.at(-1)?.role, 'assistant')

const noHistory = trimChatContextToBudget({ history, currentUserContent: '很长'.repeat(20_000) })
assert.deepEqual(noHistory, [], '当前问题耗尽未知模型的保守预算时仍应保留当前问题并移除全部历史')
assert(estimateChatTokens('中文') >= 2, 'UTF-8 估算不能按英文字符数低估中文')

console.log('AI 问答上下文预算回归通过')
