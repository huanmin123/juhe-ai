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
assert.match(viewSource, /最近一轮已变化，已保留当前草稿/, '替换冲突必须显示中文顶部提示')
assert.match(viewSource, /function cancelTurnEdit/, '取消编辑必须是独立的零后端副作用操作')
assert.match(viewSource, /await cancelTurnEdit\(\)[\s\S]{0,300}selectedConversationId\.value = id/, '切换会话前必须先退出编辑态')
assert.doesNotMatch(viewSource, /if \(generating\.value \|\| blockedBySubmission\) return false/, '生成中必须允许切换会话，不能把 runtime 生命周期绑在当前页面')
assert.match(viewSource, /:editable-message-id="generating \|\| submissionBlocked \? undefined : editableUserMessageId"/, '当前会话生成或待确认期间必须移除编辑入口')
assert.match(viewSource, /@edit-message="beginTurnEdit"/, '消息列表编辑入口必须接入页面状态')

assert.match(listSource, /editableMessageId\?: string/, '消息列表必须只接收一个后端事实可编辑消息 id')
assert.match(listSource, /editingTurnId\?: string/, '消息列表必须能淡化正在编辑的完整轮次')
assert.match(listSource, /edit-message/, '消息列表必须向页面发出编辑事件')
assert.match(listSource, /is-editing-turn/, '编辑中的用户与助手消息必须共享低强调样式')

assert.match(viewSource, /chatGenerationRuntime\.stop/, '显式停止必须优先精确停止应用级 runtime turn')
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
