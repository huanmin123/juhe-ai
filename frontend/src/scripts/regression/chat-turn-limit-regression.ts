import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { isDefinitiveChatHttpRejection, resolveChatSubmitFailure } from '../../views/chat/chatTurnEditing'

const helperUrl = new URL('../../views/chat/chatTurnLimit.ts', import.meta.url)
assert.equal(existsSync(helperUrl), true, '必须提供可独立验证的会话轮次限制纯函数')

const {
  chatTurnLimitMessage,
  canSubmitChatTurn,
  isChatTurnLimitReached,
  markChatConversationTurnLimitReached
} = await import('../../views/chat/chatTurnLimit')

assert.equal(isChatTurnLimitReached(49, 50), false, '第 49 轮仍可创建新轮次')
assert.equal(isChatTurnLimitReached(50, 50), true, '第 50 轮后达到上限')
assert.equal(isChatTurnLimitReached(51, 50), true, '超过上限仍保持限制')
for (const limit of [0, -1, 1.5, Number.NaN, Number.POSITIVE_INFINITY, Number.MAX_SAFE_INTEGER + 1]) {
  assert.equal(isChatTurnLimitReached(50, limit), false, `异常 limit ${String(limit)} 不得误锁会话`)
}
for (const count of [-1, 1.5, Number.NaN, Number.POSITIVE_INFINITY, Number.MAX_SAFE_INTEGER + 1, '50' as unknown as number]) {
  assert.equal(isChatTurnLimitReached(count, 50), false, `异常 count ${String(count)} 不得误锁会话，后端仍为权威来源`)
}
assert.equal(canSubmitChatTurn({ userTurnCount: 50, userTurnLimit: 50 }), false, '达到上限不得新建 turn')
assert.equal(canSubmitChatTurn({ userTurnCount: 50, userTurnLimit: 50, replaceTurnId: 'turn_50' }), true, 'replace 已有 turn 始终允许')
assert.equal(chatTurnLimitMessage(50), '本会话已达到 50 轮，请新建对话')
const belowLimit = { id: 'conv_1', userTurnCount: 49, userTurnLimit: 50 }
const markedAtLimit = markChatConversationTurnLimitReached(belowLimit)
assert.notEqual(markedAtLimit, belowLimit, '本地 reached 标记不得修改原会话对象')
assert.deepEqual(belowLimit, { id: 'conv_1', userTurnCount: 49, userTurnLimit: 50 }, '本地 reached 标记不得变异原会话字段')
assert.equal(markedAtLimit.userTurnCount, 50, '第 51 次竞态拒绝后必须立即把本地 count 提升到 limit')
assert.equal(markChatConversationTurnLimitReached({ ...belowLimit, userTurnCount: 51 }).userTurnCount, 51, '本地 reached 标记不得降低已超过 limit 的 count')
assert.equal(isDefinitiveChatHttpRejection({ status: 409, code: 'chat_turn_limit_exceeded' }), true, '轮次上限 409 必须归类为确定性拒绝')
assert.deepEqual(resolveChatSubmitFailure({
  streamStarted: false,
  accepted: false,
  confirmed: true,
  replaceConflict: false
}), { restoreSubmittedDraft: true, clearEditing: false }, '第51次竞态拒绝必须恢复草稿且不进入待确认/重试')

const typeSource = readFileSync(new URL('../../types/domain/chat.ts', import.meta.url), 'utf8')
const viewSource = readFileSync(new URL('../../views/chat/ChatView.vue', import.meta.url), 'utf8')
const composerSource = readFileSync(new URL('../../views/chat/composer/AIComposer.vue', import.meta.url), 'utf8')

assert.match(typeSource, /interface ChatConversation[\s\S]{0,500}userTurnCount: number[\s\S]{0,200}userTurnLimit: number/, '会话类型必须声明后端权威轮次字段')
assert.match(composerSource, /turnLimitReached: boolean[\s\S]{0,300}turnLimitMessage: string/, 'Composer 必须接收独立轮次限制状态和提示')
assert.match(composerSource, /canSubmit[\s\S]{0,300}!props\.turnLimitReached/, '轮次上限必须参与发送门禁')
const canSubmitSource = composerSource.slice(composerSource.indexOf('const canSubmit = computed'), composerSource.indexOf('const sendTooltip = computed'))
assert.ok(canSubmitSource.includes('!props.disabled'), 'disabled=true 且未达到轮次上限时发送按钮也必须禁用')
assert.ok(canSubmitSource.includes('!props.turnLimitReached'), 'disabled=false 但达到轮次上限时发送按钮必须禁用')
assert.match(composerSource, /function submit[\s\S]{0,180}props\.turnLimitReached/, '键盘和按钮发送必须在 submit 内共同阻止')
assert.match(composerSource, /sendTooltip[\s\S]{0,300}props\.turnLimitReached[\s\S]{0,160}props\.turnLimitMessage/, '发送按钮 tooltip 必须优先展示轮次限制文案')
assert.doesNotMatch(composerSource, /editable:\s*!props\.disabled\s*&&[\s\S]{0,80}turnLimitReached/, '轮次限制不得锁死编辑器')
const submitSource = composerSource.slice(composerSource.indexOf('function submit(): void'), composerSource.indexOf('function getSnapshot(): JSONContent'))
const turnLimitGuardIndex = submitSource.indexOf('props.turnLimitReached')
assert.ok(turnLimitGuardIndex >= 0, 'submit 必须显式处理轮次上限')
assert.ok(turnLimitGuardIndex < submitSource.indexOf('replaceEditorContentWithoutHistory'), '达到上限的键盘提交必须在清空编辑器前返回')
assert.ok(turnLimitGuardIndex < submitSource.indexOf("emit('submit'"), '达到上限的键盘提交不得 emit submit')

assert.match(viewSource, /const turnLimitReached = computed\([\s\S]{0,300}isChatTurnLimitReached/, 'ChatView 必须从当前会话权威字段计算 reached')
assert.match(viewSource, /class="turn-limit-bar"[\s\S]{0,300}turnLimitMessage[\s\S]{0,300}@click="createConversation"[\s\S]{0,120}新建对话/, 'Composer 上方必须显示低噪提示和现有新建入口')
assert.match(viewSource, /:turn-limit-reached="turnLimitReached && !editingTurn"/, '编辑最近一轮时 Composer 必须放开 replace 提交')
assert.match(viewSource, /const activeEdit =[\s\S]{0,300}canSubmitChatTurn\(\{[\s\S]{0,300}replaceTurnId: activeEdit\?\.replaceTurnId/, '发送前必须用权威 count 再检查；仅服务端已接受轮次的 activeEdit 才使用替换例外')
const handleFailureSource = viewSource.slice(viewSource.indexOf('async function handleSubmitFailure'), viewSource.indexOf('async function applySubmissionOutcome'))
assert.ok(handleFailureSource.includes('if (replaceConflict || isDefinitiveChatRejection(error))'), '确定性拒绝必须进入本地结算分支')
assert.ok(handleFailureSource.includes('await applySubmissionOutcome({'), '确定性拒绝必须应用草稿恢复决策，不得进入待确认重试')
const settlementIndex = handleFailureSource.indexOf('await applySubmissionOutcome({')
const localGateIndex = handleFailureSource.indexOf('markChatConversationTurnLimitReached')
const backgroundRefreshIndex = handleFailureSource.indexOf('void refreshConversationSummary(request.conversationId).catch(() => undefined)')
assert.ok(settlementIndex >= 0 && localGateIndex > settlementIndex, '第 51 次竞态拒绝必须先恢复 snapshot，再设置本地 reached gate')
assert.ok(backgroundRefreshIndex > localGateIndex, '本地 reached gate 必须在启动权威摘要刷新前生效')
assert.doesNotMatch(handleFailureSource, /await refreshConversationSummary\(request\.conversationId\)/, '轮次上限摘要刷新不得阻塞 snapshot 恢复最多 15 秒')
assert.ok(handleFailureSource.includes('void refreshConversationSummary(request.conversationId).catch(() => undefined)'), '后台摘要刷新失败必须被隔离且不得解除本地发送 gate')
const applyOutcomeSource = viewSource.slice(viewSource.indexOf('async function applySubmissionOutcome'), viewSource.indexOf('function enterPendingConfirmation'))
assert.ok(applyOutcomeSource.includes('if (resolution.restoreSubmittedDraft && !rolledBackReplacement && isRequestUiCurrent(input.request)) composer.value?.restore(input.request.snapshot)'), '草稿恢复决策必须接回当前 lifecycle 的不可变请求快照，且不得覆盖替换轮次的专用回滚结果')

console.log('AI 问答会话轮次限制回归通过')
