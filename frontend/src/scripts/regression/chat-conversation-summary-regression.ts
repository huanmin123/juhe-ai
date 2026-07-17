import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import { createChatConversationSummaryRefresher, mergeChatConversationSummary } from '../../views/chat/chatConversationSummary'
import type { ChatConversation } from '../../types/domain/chat'

function conversation(title: string, activeTurnId?: string): ChatConversation {
  return { id: 'conv_1', systemAccountId: 'sys_1', apiKeyNameSnapshot: 'Key', title, isPinned: true, activeTurnId, userTurnCount: 49, userTurnLimit: 50, lastMessageAt: '2026-07-13T00:00:00.000Z', createdAt: '2026-07-13T00:00:00.000Z', updatedAt: '2026-07-13T00:00:00.000Z' }
}
function deferred<T>(): { promise: Promise<T>; resolve: (value: T) => void } {
  let resolve!: (value: T) => void
  return { promise: new Promise<T>((next) => { resolve = next }), resolve }
}

const startedResponse = deferred<ChatConversation>()
const completedResponse = deferred<ChatConversation>()
let loadCount = 0
const applied: ChatConversation[] = []
const refresh = createChatConversationSummaryRefresher({
  load: async () => (++loadCount === 1 ? startedResponse.promise : completedResponse.promise),
  apply: (item) => applied.push(item)
})
const startedRefresh = refresh('conv_1')
const completedRefresh = refresh('conv_1')
completedResponse.resolve(conversation('修正后的首轮标题'))
await completedRefresh
startedResponse.resolve(conversation('旧标题', 'old_turn'))
await startedRefresh
assert.deepEqual(applied.map((item) => [item.title, item.isPinned, item.activeTurnId]), [['修正后的首轮标题', true, undefined]], '较早的 message.started 摘要响应不得覆盖完成后的新标题、置顶和空闲状态')

const merged = mergeChatConversationSummary(conversation('本地旧标题'), { ...conversation('后端新标题'), isPinned: false, userTurnCount: 50 })
assert.equal(merged.title, '后端新标题')
assert.equal(merged.isPinned, true, '标题刷新不属于置顶写入，不能用可能较早的 GET 响应覆盖本地置顶排序')
assert.deepEqual([merged.userTurnCount, merged.userTurnLimit], [50, 50], '摘要合并必须更新且不得丢失后端权威轮次字段')

const apiSource = readFileSync('../frontend/src/api/domains/chat.ts', 'utf8')
const viewSource = readFileSync('../frontend/src/views/chat/ChatView.vue', 'utf8')
assert.match(apiSource, /getConversation: \(conversationId: string\) => unwrap<ChatConversation>\(http\.get\(`\/my-chat\/conversations\/\$\{encodeURIComponent\(conversationId\)\}`\)\)/, '前端必须通过编码后的会话 ID 读取后端单会话权威摘要')
assert.match(viewSource, /const refreshConversationSummary = createChatConversationSummaryRefresher\(\{\s*load: chatApi\.getConversation,\s*apply:/, '权威摘要刷新器必须接入单会话摘要 API')
assert.match(viewSource, /chatGenerationRuntime\.subscribe\(conversation\.systemAccountId, conversation\.id, applyRuntimeTurn\)/, '页面必须订阅应用级 runtime，而不是自行持有 message.started 流生命周期')
const runtimeApplySource = viewSource.slice(viewSource.indexOf('function applyRuntimeTurn'), viewSource.indexOf('async function refreshConversationFromSync'))
assert.match(runtimeApplySource, /if \(turn\.turnId && active\?\.request\.clientMessageId === turn\.clientMessageId\)[\s\S]{0,900}refreshConversationFromSync\(turn\.conversationId\)[\s\S]{0,220}refreshConversationSummary\(turn\.conversationId\)/, 'runtime 确认 accepted turn 后必须同步消息并非阻塞刷新权威标题与轮次摘要')
assert.match(runtimeApplySource, /turn\.status === 'completed'[\s\S]{0,700}refreshConversationFromSync\(turn\.conversationId\)[\s\S]{0,220}refreshContextStatus\(turn\.conversationId\)/, 'runtime 终态必须重新同步会话以清除 activeTurn，并刷新上下文状态')
const submissionOutcomeSource = viewSource.slice(viewSource.indexOf('async function applySubmissionOutcome'), viewSource.indexOf('function enterPendingConfirmation'))
assert.match(submissionOutcomeSource, /if \(input\.reconciliation\.accepted\) \{\s*try \{ await refreshConversationSummary\(input\.request\.conversationId\) \} catch \{\}/, '流断开后的 accepted 对账结果也必须再次确认权威摘要')
assert.doesNotMatch(viewSource, /current\.title === '新对话'[\s\S]{0,200}first\.contentText/, '前端不得继续自行推导标题覆盖后端标题来源规则')

console.log('AI 问答替换标题权威刷新与过期响应隔离回归通过')
