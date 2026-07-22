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

assert.match(viewSource, /interface ChatTurnEditingState[\s\S]*conversationId:[\s\S]*turnId:[\s\S]*userMessageId:[\s\S]*assistantMessageId:[\s\S]*content:[\s\S]*displacedDraft:[\s\S]*phase:/, '编辑状态必须完整保存最近轮次与被替换草稿')
assert.match(viewSource, /Object\.freeze\(\{[\s\S]{0,500}clientMessageId[\s\S]{0,500}replaceTurnId[\s\S]{0,500}snapshot/, '每次发送必须捕获不可变请求上下文')
assert.match(viewSource, /writeStoredPendingConfirmation\(\{[\s\S]{0,1600}chatGenerationRuntime\.start\(\{/, 'clientMessageId 与草稿必须在 POST runtime 启动前持久化')
assert.match(viewSource, /chatGenerationRuntime\.start\(\{[\s\S]{0,500}replaceTurnId:\s*requestContext\.replaceTurnId/, '最近轮次替换必须交给应用级 runtime 保留 replaceTurnId')
assert.match(viewSource, /applyRuntimeTurn[\s\S]{0,1800}finishAcceptedTurnEdit\(active\.request\)/, 'runtime 获得 accepted turn 后才能完成替换编辑态')
assert.match(viewSource, /ChatStreamHttpError[\s\S]{0,900}chat_replace_conflict/, '替换冲突仍必须按 typed HTTP code 单独处理')
assert.match(viewSource, /reconcileChatSubmission\(\{[\s\S]{0,500}getSubmissionStatus:[\s\S]{0,300}request\.clientMessageId/, 'POST 是否接受未知时必须按 clientMessageId 查询专用提交状态')
const runtimePreacceptFailureSource = viewSource.slice(
  viewSource.indexOf("if (turn.status === 'failed' && !turn.turnId"),
  viewSource.indexOf('async function refreshConversationFromSync')
)
assert.doesNotMatch(
  runtimePreacceptFailureSource,
  /await handleSubmitFailure\([\s\S]{0,1200}composer\.value\?\.restore\(failedRequest\.snapshot\)/,
  'runtime 接受前失败必须由权威对账唯一决定是否恢复草稿，外层 finally 不得覆盖 accepted 或 pending 结果'
)
assert.match(
  runtimePreacceptFailureSource,
  /await handleSubmitFailure\([\s\S]{0,800}pendingConfirmation\.value\?\.request\.clientMessageId === failedRequest\.clientMessageId[\s\S]{0,160}return[\s\S]{0,800}clearPendingConfirmation\(failedRequest\.systemAccountId\)/,
  '权威对账进入 accepted/pending 后，runtime 外层 finally 必须保留待确认记录、停止目标和 lifecycle'
)
assert.match(
  runtimePreacceptFailureSource,
  /!reconcilingSubmissionClientMessageIds\.has\(turn\.clientMessageId\)[\s\S]{0,900}confirmingSubmission\.value = true[\s\S]{0,500}await handleSubmitFailure\([\s\S]{0,500}finally[\s\S]{0,500}reconcilingSubmissionClientMessageIds\.delete\(turn\.clientMessageId\)[\s\S]{0,300}confirmingSubmission\.value = reconcilingSubmissionClientMessageIds\.size > 0/,
  'runtime 接受前失败到权威对账完成之间必须保持 confirming 门禁，并防止同一失败投影重复启动对账'
)
assert.match(viewSource, /最近一轮已变化，已保留当前草稿/, '替换冲突必须显示中文顶部提示')
assert.match(viewSource, /function cancelTurnEdit/, '取消编辑必须是独立的零后端副作用操作')
assert.match(viewSource, /await cancelTurnEdit\(\)[\s\S]{0,300}selectedConversationId\.value = id/, '切换会话前必须先退出编辑态')
assert.doesNotMatch(viewSource, /if \(generating\.value \|\| blockedBySubmission\) return false/, '生成中必须允许切换会话，不能把 runtime 生命周期绑在当前页面')
assert.match(viewSource, /:editable-message-id="generating \|\| submissionBlocked \? undefined : editableUserMessageId"/, '当前会话生成或待确认期间必须移除编辑入口')
assert.match(viewSource, /@edit-message="beginTurnEdit"/, '消息列表编辑入口必须接入页面状态')
assert.match(viewSource, /selectedModel\.value = model\s+resetModelControls\(\)[\s\S]{0,260}await sendMessage\(/, '失败尾轮切回原模型后必须同步重置模型参数再发送')
assert.match(viewSource, /chatGenerationRuntime\.forget\([\s\S]{0,260}candidate\.replaceTurnId[\s\S]{0,260}await sendMessage\(/, '重试终态轮次前必须清理旧 runtime 投影，避免 canceled/failed 状态覆盖新的替换提交')
assert.match(viewSource, /async function retryLatestTurn[\s\S]{0,220}modelsLoading\.value[\s\S]{0,220}模型仍在加载，请稍后重试/, '模型目录刷新期间必须阻止一键重试，不能用过期能力快照发送')
assert.match(viewSource, /replaceTurnId:\s*activeEdit\?\.replaceTurnId/, '未接受乐观轮次重试必须作为新提交，不能把本地 optimistic turn id 发送给后端')
assert.match(viewSource, /rollbackUnacceptedTurnEdit\(input\.request\)/, '替换在服务端未接受时必须恢复原尾轮并移除乐观占位')
assert.match(viewSource, /source:\s*'retry'/, '一键重试必须与手动编辑区分回滚行为')
assert.match(viewSource, /removeInvalidatedGeneratedAssetsFromDraft/, '替换接受后必须清理草稿中已随旧回答失效的生成图片')
assert.match(viewSource, /原回答中的生成图片已失效/, '清理失效生成图片时必须向用户明确反馈')

assert.match(listSource, /editableMessageId\?: string/, '消息列表必须只接收一个后端事实可编辑消息 id')
assert.match(listSource, /editingTurnId\?: string/, '消息列表必须能淡化正在编辑的完整轮次')
assert.match(listSource, /edit-message/, '消息列表必须向页面发出编辑事件')
assert.match(listSource, /is-editing-turn/, '编辑中的用户与助手消息必须共享低强调样式')

assert.match(viewSource, /chatGenerationRuntime\.stop/, '显式停止必须优先精确停止应用级 runtime turn')
assert.doesNotMatch(viewSource, /conversation\s*&&\s*target\.turnId\s*\?\s*await chatGenerationRuntime\.stop/, 'preparing 阶段没有 turnId 时也必须先由 runtime abort 本地提交，不能直接绕过 runtime')
assert.match(viewSource, /chatApi\.stop\([^,]+,\s*\{[\s\S]{0,160}clientMessageId:[\s\S]{0,160}turnId:/, 'fallback stop 必须携带 clientMessageId 与期望 turnId')
assert.match(viewSource, /const pending = pendingConfirmation\.value[\s\S]{0,800}resolveChatStopTarget\([\s\S]{0,800}startedTurnId/, '刷新恢复后必须从待确权记录恢复精确停止目标')
assert.match(viewSource, /:stoppable="generating \|\| Boolean\(pendingConfirmation\)"/, '只有真实生成或待确权任务才能显示停止按钮')
assert.match(composerSource, /v-if="stoppable"[\s\S]{0,180}aria-label="停止生成"/, 'composer 不能把普通 disabled 状态误渲染成停止按钮')
assert.match(viewSource, /const submissionBlocked = computed\(\(\) => stopping\.value \|\| confirmingSubmission\.value/, '确权结果应用完成前必须保持统一交互门禁')
assert.match(viewSource, /pendingConfirmation/, 'POST 接受事实未知时必须保留不可变 requestContext')
assert.match(viewSource, /schedulePendingConfirmation/, '待确认状态必须自动后台重试')
assert.match(viewSource, /readStoredPendingConfirmation\(\)[\s\S]{0,700}ensurePendingConversationAvailability\(storedPending\)/, '待确认会话不在首屏时必须按 id 定向加载')
assert.match(viewSource, /reconcileChatPendingSubmissionRecovery/, '刷新后必须恢复 POST 接受前的草稿确权')
assert.match(viewSource, /onDeactivated\(deactivateChatPage\)/, 'KeepAlive 离开页面必须暂停 UI 监听和 timer')
assert.doesNotMatch(viewSource, /onBeforeUnmount\([\s\S]{0,900}(?:abort\(\)|chatApi\.stop)/, '卸载不得中止或停止已接受生成')

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
assert.equal(writeChatPendingSubmission(pendingStorage, pendingRecord), true)
assert.equal(readChatPendingSubmission(pendingStorage, 'sys_account_b'), undefined, '账号 B 绝不能读取账号 A 的待确认草稿')
assert.equal(readChatPendingSubmission(pendingStorage, 'sys_account_a')?.request.clientMessageId, 'client_pending_1')
assert.notEqual(chatPendingSubmissionStorageKey('sys_account_a'), chatPendingSubmissionStorageKey('sys_account_b'))
clearChatPendingSubmission(pendingStorage, 'sys_account_b')
assert.ok(readChatPendingSubmission(pendingStorage, 'sys_account_a'))
clearChatPendingSubmission(pendingStorage, 'sys_account_a')
assert.equal(readChatPendingSubmission(pendingStorage, 'sys_account_a'), undefined)
assert.equal(writeChatPendingSubmission(new RejectingStorage('QuotaExceededError'), pendingRecord), false)
assert.equal(writeChatPendingSubmission(new RejectingStorage('SecurityError'), pendingRecord), false)

console.log('AI 问答编辑/替换与应用级 runtime 页面接线回归通过')
