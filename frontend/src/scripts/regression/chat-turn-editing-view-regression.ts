import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import {
  chatPendingSubmissionStorageKey,
  clearChatPendingSubmission,
  readChatPendingSubmission,
  writeChatPendingSubmission
} from '../../views/chat/chatPendingSubmissionStorage'

const viewSource = readFileSync('../frontend/src/views/chat/ChatView.vue', 'utf8')
const listSource = readFileSync('../frontend/src/views/chat/ChatMessageList.vue', 'utf8')
const composerSource = readFileSync('../frontend/src/views/chat/composer/AIComposer.vue', 'utf8')

assert.match(viewSource, /interface ChatTurnEditingState[\s\S]*conversationId:[\s\S]*turnId:[\s\S]*userMessageId:[\s\S]*assistantMessageId:[\s\S]*content:[\s\S]*displacedDraft:[\s\S]*phase:/, '编辑状态必须完整保存会话、轮次、消息、原文、被替换草稿和阶段')
assert.match(viewSource, /Object\.freeze\(\{[\s\S]{0,500}clientMessageId[\s\S]{0,500}replaceTurnId[\s\S]{0,500}snapshot/, '每次发送必须捕获不可变请求上下文')
assert.match(viewSource, /event\.type === 'message\.started'[\s\S]{0,600}replaceTurnId/, '只有 message.started 才能投影替换旧轮次')
assert.match(viewSource, /ChatStreamHttpError[\s\S]{0,800}chat_replace_conflict/, '替换冲突必须按 typed HTTP code 单独处理')
assert.match(viewSource, /reconcileChatSubmission\(\{[\s\S]{0,500}getSubmissionStatus:[\s\S]{0,300}request\.clientMessageId[\s\S]{0,400}refreshMessages\(request\.conversationId\)/, '开始前网络错误必须按 clientMessageId 查询专用提交状态并刷新消息')
assert.match(viewSource, /handleSubmitFailure[\s\S]{0,1800}reconcileChatSubmission\(\{[\s\S]{0,300}initialAcceptedTurnId: streamStarted \? startedTurnId : undefined[\s\S]{0,220}initialAssistantStatus: streamStarted && startedTurnId \? 'streaming' : undefined/, '当前 SSE 已 started 的 turn 必须作为第一次失败对账的单调初始事实')
assert.match(viewSource, /最近一轮已变化，已保留当前草稿/, '替换冲突必须显示中文顶部提示')
assert.doesNotMatch(viewSource, /if \(!controller\.signal\.aborted\) await handleSubmitFailure/, 'message.started 前主动停止也必须进入 clientMessageId 对账，不能遗留 submitting 编辑态')
assert.match(viewSource, /handleSubmitFailure\(error, requestContext, streamStarted, startedTurnId, controller\.signal\.aborted\)/, '主动停止必须把静默标记交给同一失败对账路径')
assert.match(viewSource, /event\.type === 'message\.completed' \|\| event\.type === 'message\.failed'\) streamTerminal = true/, '只允许模型终态事件确认流完成')
assert.match(viewSource, /if \(!streamTerminal\) throw new Error\('模型流已中断，正在确认消息终态'\)/, '流连接结束但没有终态事件时必须进入对账，不能把 streaming 当成功')
assert.match(viewSource, /reconcileChatSubmission/, '接受后断流必须使用有界终态对账，而不是只立即刷新一次')
assert.match(viewSource, /resolveChatReconciliationNotice\(\{ accepted, assistantStatus:[^}]+silent:/, '主动停止与 completed 终态必须通过可测试的提示决策，不能一律显示发送失败')
assert.match(viewSource, /function cancelTurnEdit/, '取消编辑必须是独立的零后端副作用操作')
assert.match(viewSource, /await cancelTurnEdit\(\)[\s\S]{0,300}selectedConversationId\.value = id/, '切换会话前必须先退出编辑态')
assert.match(viewSource, /:editable-message-id="generating \|\| submissionBlocked \? undefined : editableUserMessageId"/, '提交或待确认门禁期间必须移除编辑入口')
assert.match(viewSource, /@edit-message="beginTurnEdit"/, '消息列表编辑入口必须接入页面状态')

assert.match(listSource, /editableMessageId\?: string/, '消息列表必须只接收一个后端事实可编辑消息 id')
assert.match(listSource, /editingTurnId\?: string/, '消息列表必须能淡化正在编辑的完整轮次')
assert.match(listSource, /edit-message/, '消息列表必须向页面发出编辑事件')
assert.match(listSource, /is-editing-turn/, '编辑中的用户与助手消息必须共享低强调样式')

assert.match(viewSource, /const stopping = ref\(false\)/, '慢 stop HTTP 与旧发送对账期间必须持有独立 stopping gate')
assert.match(viewSource, /if \(generating\.value \|\| submissionBlocked\.value/, '发送、编辑或切换必须拒绝跨越 stopping/pending gate')
assert.match(viewSource, /:disabled="generating \|\| submissionBlocked"/, 'stopping/pending 期间 composer 必须持续禁用，即使旧 send finally 已把 generating 置 false')
assert.match(viewSource, /const controller = streamController[\s\S]{0,300}stopActiveChatGeneration/, 'stop 必须捕获旧 controller 并交给隔离 helper')
assert.match(viewSource, /pendingConfirmation/, '全部权威读取失败时必须保留不可变 requestContext 进入待确认状态')
assert.match(viewSource, /重新确认/, '待确认状态必须提供最小人工重试入口')
assert.match(viewSource, /schedulePendingConfirmation/, '待确认状态必须自动后台重试，不能要求用户刷新页面')
assert.match(viewSource, /reconciliation\.accepted && !reconciliation\.terminal/, 'accepted 但仍 streaming 时必须保持待确认门禁直到终态')
assert.match(viewSource, /writeStoredPendingConfirmation\(\{[\s\S]{0,1500}await streamChatMessage/, 'clientMessageId 与草稿必须在 POST 前持久化，不能等首次对账失败')
assert.match(viewSource, /if \(!writeStoredPendingConfirmation\(\{[\s\S]{0,700}composer\.value\?\.restore\(requestContext\.snapshot\)[\s\S]{0,300}return[\s\S]{0,300}generating\.value = true[\s\S]{0,1500}await streamChatMessage/, 'sessionStorage 写入失败必须在 POST 与发送门禁之前停止发送并恢复原草稿')
assert.match(viewSource, /function writeStoredPendingConfirmation[\s\S]{0,160}: boolean[\s\S]{0,160}return writeChatPendingSubmission/, '页面持久化入口必须把按账号存储结果返回给发送路径')
assert.match(viewSource, /readStoredPendingConfirmation\(\)[\s\S]{0,700}ensurePendingConversationAvailability\(storedPending\)/, '待确认会话不在首屏 50 条时必须按 id 定向加载，不能误判删除')
assert.match(viewSource, /readStoredPendingConfirmation\(\)[\s\S]{0,1200}schedulePendingConfirmation\(\)/, '页面加载必须恢复待确认请求并继续后台确权')
const restoreGateIndex = viewSource.indexOf('restorePendingConfirmation(storedPending)')
const targetedConversationLookupIndex = viewSource.indexOf('await ensurePendingConversationAvailability(storedPending)')
assert.ok(restoreGateIndex >= 0 && restoreGateIndex < targetedConversationLookupIndex, '读取 pending 后必须在 targeted 查询前同步建立门禁，503 期间也不能开放发送')
assert.match(viewSource, /availability === 'not_found'[\s\S]{0,300}clearPendingConfirmation[\s\S]{0,700}else if \(conversationItems\[0\]\)/, 'targeted 会话查询 503 必须保留 pending 且不回退首屏；只有 conversation 404 才能清理')
assert.match(viewSource, /async function selectConversation\(id: string, options: \{[\s\S]{0,180}allowPendingRecovery\?: boolean[\s\S]{0,180}forceReload\?: boolean[\s\S]{0,180}\} = \{\}\)[\s\S]{0,400}options\.allowPendingRecovery/, '初始化恢复原会话必须只绕过 pending 本身并支持后台强制重载，普通交互仍受全局门禁约束')
assert.match(viewSource, /function ensurePendingConversationAvailability[\s\S]{0,900}chatApi\.getConversation[\s\S]{0,900}selectConversation\([\s\S]{0,300}allowPendingRecovery: true,[\s\S]{0,160}forceReload: true/, '后台确权每轮必须重试 targeted 加载并强制选择原会话')
assert.match(viewSource, /reconcileChatPendingSubmissionRecovery\(\{[\s\S]{0,400}ensureConversation: \(\) => ensurePendingConversationAvailability\(pending\)[\s\S]{0,500}reconcile: \(initial\) => reconcileChatSubmission\(\{[\s\S]{0,160}\.\.\.initial/, '持久 pending 的 started/accepted 事实必须传入每一轮 reconcile')
assert.match(viewSource, /recovery\.action === 'missing'[\s\S]{0,500}clearPendingConfirmation[\s\S]{0,700}recovery\.action === 'retry'[\s\S]{0,500}setPendingConfirmation\(recovery\.pending\)[\s\S]{0,300}schedulePendingConfirmation[\s\S]{0,600}applySubmissionOutcome/, 'conversation 404 才能清理；targeted 503 或未终态必须持久化最新事实并继续门禁，ready 后才应用 outcome')
assert.match(viewSource, /chatApi\.stop\([^,]+,\s*\{[\s\S]{0,160}clientMessageId:[\s\S]{0,160}turnId:/, 'stop 必须携带 clientMessageId 与期望 turnId，不能按会话误停新轮次')
assert.match(viewSource, /const pending = pendingConfirmation\.value[\s\S]{0,800}resolveChatStopTarget\([\s\S]{0,800}startedTurnId/, '活动发送句柄释放后必须从待确权记录恢复精确停止目标')
assert.match(viewSource, /stopActiveChatGeneration\([\s\S]{0,700}retryPendingConfirmation\(true\)/, '停止待确权任务后必须继续权威对账，不能直接释放门禁')
assert.match(viewSource, /:stoppable="generating \|\| Boolean\(pendingConfirmation\)"/, '只有真实发送或待确权任务才能显示停止按钮')
assert.match(composerSource, /v-if="stoppable"[\s\S]{0,180}aria-label="停止生成"/, 'composer 不能把普通 disabled 状态误渲染成无效停止按钮')
assert.match(viewSource, /const submissionBlocked = computed\(\(\) => stopping\.value \|\| confirmingSubmission\.value/, '确权结果应用完成前必须保持统一交互门禁')
assert.match(viewSource, /Math\.min\(15_000,[\s\S]{0,120}pendingConfirmationRetryCount/, '后台确认失败必须指数退避并设置上限')
assert.match(viewSource, /submissionBlocked/, '待确认期间必须统一禁止发送、编辑和切换会话')
assert.match(viewSource, /let disposed = false/, '组件必须记录卸载状态')
assert.match(viewSource, /disposed = true[\s\S]{0,300}clearTimeout\(pendingConfirmationTimer\)/, '卸载时必须清理待确认 timer')
assert.match(viewSource, /if \(disposed \|\| pendingConfirmation\.value !== pending\) return/, '进行中的重新确认在卸载后不得 apply 结果')
assert.match(viewSource, /applyChatReconciliationIfActive/, '初始失败对账必须通过卸载感知边界应用结果')
assert.match(viewSource, /async function applySubmissionOutcome[\s\S]{0,300}if \(disposed\) return/, '结果副作用入口必须拒绝已卸载组件')
assert.match(viewSource, /function enterPendingConfirmation[\s\S]{0,120}if \(disposed\) return/, '待确认状态入口必须拒绝已卸载组件')
assert.match(viewSource, /\.submission-confirmation-bar\s*\{/, '待确认提示必须有基础布局样式')
assert.doesNotMatch(viewSource, /void sendSettled\.finally\(/, '清理 rejected send 不能使用会产生新拒绝 Promise 的 finally')
assert.match(viewSource, /const clearActiveSend = \(\) =>[\s\S]{0,180}void sendSettled\.then\(clearActiveSend, clearActiveSend\)/, '发送成功或失败都必须通过已观察的双分支清理活动 Promise')

class MemoryStorage implements Storage {
  private readonly values = new Map<string, string>()
  get length(): number { return this.values.size }
  clear(): void { this.values.clear() }
  getItem(key: string): string | null { return this.values.get(key) ?? null }
  key(index: number): string | null { return [...this.values.keys()][index] ?? null }
  removeItem(key: string): void { this.values.delete(key) }
  setItem(key: string, value: string): void { this.values.set(key, value) }
}

class RejectingStorage extends MemoryStorage {
  constructor(private readonly errorName: 'QuotaExceededError' | 'SecurityError') { super() }
  override setItem(): void { throw new DOMException('Storage rejected', this.errorName) }
}

const pendingStorage = new MemoryStorage()
const pendingRecord = {
  request: {
    systemAccountId: 'sys_account_a',
    conversationId: 'conv_pending_old_page',
    clientMessageId: 'client_pending_1',
    snapshot: { type: 'doc', content: [{ type: 'paragraph', content: [{ type: 'text', text: '账号 A 私有草稿' }] }] }
  },
  streamStarted: false,
  silent: false,
  errorMessage: '网络中断'
}
assert.equal(writeChatPendingSubmission(pendingStorage, pendingRecord), true, '成功写入待确认记录必须返回明确成功')
assert.equal(readChatPendingSubmission(pendingStorage, 'sys_account_b'), undefined, '账号 B 绝不能读取账号 A 的待确认草稿')
assert.equal(readChatPendingSubmission(pendingStorage, 'sys_account_a')?.request.clientMessageId, 'client_pending_1', '原账号重载必须恢复同一个 clientMessageId')
assert.notEqual(chatPendingSubmissionStorageKey('sys_account_a'), chatPendingSubmissionStorageKey('sys_account_b'), '待确认存储 key 必须按系统账号分区')
clearChatPendingSubmission(pendingStorage, 'sys_account_b')
assert.ok(readChatPendingSubmission(pendingStorage, 'sys_account_a'), '清理账号 B 不得删除账号 A 的恢复状态')
clearChatPendingSubmission(pendingStorage, 'sys_account_a')
assert.equal(readChatPendingSubmission(pendingStorage, 'sys_account_a'), undefined)
assert.equal(writeChatPendingSubmission(new RejectingStorage('QuotaExceededError'), pendingRecord), false, 'sessionStorage quota 异常必须返回明确失败，不能被当作已持久化')
assert.equal(writeChatPendingSubmission(new RejectingStorage('SecurityError'), pendingRecord), false, 'sessionStorage security 异常必须返回明确失败，不能被当作已持久化')
assert.equal(writeChatPendingSubmission(pendingStorage, {
  ...pendingRecord,
  streamStarted: true,
  startedTurnId: 'turn_terminal_persisted',
  acceptedAssistantStatus: 'completed'
}), true)
assert.equal(readChatPendingSubmission(pendingStorage, 'sys_account_a')?.acceptedAssistantStatus, 'completed', '后台确权得到的 terminal 状态必须跨刷新持久化')

console.log('AI 问答编辑/取消/替换页面竞态接线回归通过')
