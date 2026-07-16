import assert from 'node:assert/strict'

import * as reconciliationModule from '../../views/chat/chatTurnReconciliation'
import { applyChatReconciliationIfActive, isRetryableChatSubmissionLookupError, reconcileChatSubmission, type ChatSubmissionReconciliation } from '../../views/chat/chatTurnReconciliation'
import type { ChatPendingSubmission } from '../../views/chat/chatPendingSubmissionStorage'
import type { ChatMessage, ChatSubmissionStatus } from '../../types/domain/chat'

function pair(status: ChatMessage['status'], clientMessageId = 'client_1', turnId = 'turn_1'): ChatMessage[] {
  return [
    { id: `user_${turnId}`, conversationId: 'conv_1', turnId, sequenceNo: 1, clientMessageId, role: 'user', status: 'completed', contentText: '问题', contentBlocks: [{ type: 'input_text', text: '问题', order: 0 }], model: 'mock', createdAt: '2026-07-13T00:00:00.000Z', expiresAt: '2026-07-20T00:00:00.000Z' },
    { id: `assistant_${turnId}`, conversationId: 'conv_1', turnId, sequenceNo: 2, role: 'assistant', status, contentText: '', contentBlocks: [], model: 'mock', createdAt: '2026-07-13T00:00:00.000Z', expiresAt: '2026-07-20T00:00:00.000Z' }
  ]
}

async function reconcileSequence(sequence: ChatSubmissionStatus[], maxAttempts = 8) {
  let statusCalls = 0
  let listCalls = 0
  const stoppedTurnIds: string[] = []
  const waits: number[] = []
  let now = 0
  const result = await reconcileChatSubmission({
    getSubmissionStatus: async () => sequence[Math.min(statusCalls++, sequence.length - 1)]!,
    listMessages: async () => {
      listCalls += 1
      const current = sequence[Math.min(statusCalls - 1, sequence.length - 1)]!
      return current.state === 'accepted' ? pair(current.assistantStatus, 'client_1', current.turnId) : []
    },
    stop: async (turnId) => { stoppedTurnIds.push(turnId) },
    wait: async (milliseconds) => { waits.push(milliseconds); now += milliseconds },
    now: () => now,
    maxAttempts
  })
  return { result, statusCalls, listCalls, stoppedTurnIds, waits }
}

const lifecycle = await reconcileSequence([
  { state: 'not_found' },
  { state: 'preparing' },
  { state: 'accepted', turnId: 'turn_1', assistantStatus: 'streaming' },
  { state: 'accepted', turnId: 'turn_1', assistantStatus: 'canceled' }
])
assert.deepEqual({ accepted: lifecycle.result.accepted, terminal: lifecycle.result.terminal, status: lifecycle.result.assistantStatus, statusCalls: lifecycle.statusCalls, stoppedTurnIds: lifecycle.stoppedTurnIds }, {
  accepted: true, terminal: true, status: 'canceled', statusCalls: 4, stoppedTurnIds: []
}, 'not_found/preparing/accepted streaming 必须最终等待助手终态')
assert.deepEqual(lifecycle.waits, [100, 200, 350])

const notAccepted = await reconcileSequence([{ state: 'not_found' }], 8)
assert.equal(notAccepted.result.confirmed, true)
assert.equal(notAccepted.result.accepted, false)
assert.ok(notAccepted.statusCalls >= 3, '必须至少连续三次 not_found 才能确认未提交')
assert.ok(notAccepted.waits.reduce((total, delay) => total + delay, 0) >= 1_000, 'not_found 确认必须跨越真实时间 grace，不能在 POST 抢跑窗口内恢复草稿')

const lateAccepted = await reconcileSequence([
  { state: 'not_found' },
  { state: 'accepted', turnId: 'turn_late', assistantStatus: 'completed' }
], 4)
assert.equal(lateAccepted.result.accepted, true, '单次 not_found 后仍可能进入 accepted，不能提前恢复草稿')

let failedReads = 0
const unknown = await reconcileChatSubmission({
  getSubmissionStatus: async () => { failedReads += 1; throw new Error('持续不可用') },
  listMessages: async () => [],
  stop: async () => undefined,
  wait: async () => undefined,
  maxAttempts: 3
})
assert.deepEqual({ confirmed: unknown.confirmed, accepted: unknown.accepted, failedReads }, { confirmed: false, accepted: false, failedReads: 3 }, '状态接口全部失败只能返回 unknown')

let definitiveReads = 0
const definitiveError = { response: { status: 404 } }
const definitive = await reconcileChatSubmission({
  getSubmissionStatus: async () => { definitiveReads += 1; throw definitiveError },
  listMessages: async () => [],
  stop: async () => undefined,
  wait: async () => { throw new Error('确定性错误不应等待') }
})
assert.equal(definitive.confirmed, true)
assert.equal(definitive.lookupError, definitiveError)
assert.equal(definitiveReads, 1, '确定性 4xx 必须立即结束对账')

let transientReads = 0
const transient = await reconcileChatSubmission({
  getSubmissionStatus: async () => {
    transientReads += 1
    if (transientReads === 1) throw { response: { status: 503 } }
    return { state: 'accepted', turnId: 'turn_retry', assistantStatus: 'completed' }
  },
  listMessages: async () => pair('completed', 'client_retry', 'turn_retry'),
  stop: async () => undefined,
  wait: async () => undefined,
  maxAttempts: 3
})
assert.equal(transient.accepted, true)
assert.equal(transientReads, 2, '5xx 必须重试后识别 accepted')

let interruptedSequenceCall = 0
let interruptedNow = 0
const interruptedNotFound = await reconcileChatSubmission({
  getSubmissionStatus: async () => {
    interruptedSequenceCall += 1
    if (interruptedSequenceCall === 2) throw { response: { status: 503 } }
    return { state: 'not_found' }
  },
  listMessages: async () => [],
  stop: async () => undefined,
  wait: async (milliseconds) => { interruptedNow += milliseconds },
  now: () => interruptedNow,
  maxAttempts: 5
})
assert.equal(interruptedNotFound.confirmed, false, '临时错误必须打断连续 not_found 计数，不能组合成未提交结论')

const acceptedThenMissing = await reconcileSequence([
  { state: 'accepted', turnId: 'turn_monotonic', assistantStatus: 'streaming' },
  { state: 'not_found' },
  { state: 'not_found' },
  { state: 'not_found' }
], 4)
assert.deepEqual({ accepted: acceptedThenMissing.result.accepted, terminal: acceptedThenMissing.result.terminal, turnId: acceptedThenMissing.result.turnId }, {
  accepted: true, terminal: false, turnId: 'turn_monotonic'
}, 'accepted 是单调事实，后续 not_found 不得降级并恢复草稿')

type ReconcileInput = Parameters<typeof reconcileChatSubmission>[0]
type ReconcileWithInitialFact = (input: ReconcileInput & {
  initialAcceptedTurnId?: string
  initialAssistantStatus?: ChatMessage['status']
}) => ReturnType<typeof reconcileChatSubmission>
const reconcileWithInitialFact = reconcileChatSubmission as ReconcileWithInitialFact

let initialFactNow = 0
const firstAcceptedFactStops: string[] = []
const firstAcceptedFact = await reconcileWithInitialFact({
  initialAcceptedTurnId: 'turn_started_before_disconnect',
  initialAssistantStatus: 'streaming',
  getSubmissionStatus: async () => ({ state: 'not_found' }),
  listMessages: async () => [],
  stop: async (turnId) => { firstAcceptedFactStops.push(turnId) },
  wait: async (milliseconds) => { initialFactNow += milliseconds },
  now: () => initialFactNow,
  maxAttempts: 4
})
assert.deepEqual({
  accepted: firstAcceptedFact.accepted,
  terminal: firstAcceptedFact.terminal,
  turnId: firstAcceptedFact.turnId,
  assistantStatus: firstAcceptedFact.assistantStatus
}, {
  accepted: true,
  terminal: false,
  turnId: 'turn_started_before_disconnect',
  assistantStatus: 'streaming'
}, 'SSE message.started 是权威 accepted 事实，同一轮 reconcile 内连续 not_found 也不得降级')
assert.deepEqual(firstAcceptedFactStops, [], '已知 accepted 后必须交给 runtime 续跑，确权流程不得停止')

let secondAcceptedFactCalls = 0
const secondAcceptedFactStops: string[] = []
const secondAcceptedFact = await reconcileWithInitialFact({
  initialAcceptedTurnId: firstAcceptedFact.turnId,
  initialAssistantStatus: firstAcceptedFact.assistantStatus,
  getSubmissionStatus: async () => {
    secondAcceptedFactCalls += 1
    if (secondAcceptedFactCalls === 1) throw { response: { status: 503 } }
    return secondAcceptedFactCalls === 2 ? { state: 'preparing' } : { state: 'not_found' }
  },
  listMessages: async () => [],
  stop: async (turnId) => { secondAcceptedFactStops.push(turnId) },
  wait: async () => undefined,
  maxAttempts: 3
})
assert.deepEqual({ accepted: secondAcceptedFact.accepted, turnId: secondAcceptedFact.turnId, assistantStatus: secondAcceptedFact.assistantStatus }, {
  accepted: true,
  turnId: 'turn_started_before_disconnect',
  assistantStatus: 'streaming'
}, '上一轮后台确权的 accepted 事实必须跨多次 reconcile 保持单调')
assert.deepEqual(secondAcceptedFactStops, [], '跨轮重试也不得停止已接受 turn')

type RecoveryPendingSubmission = ChatPendingSubmission & { acceptedAssistantStatus?: ChatMessage['status'] }
type PendingRecoveryAction = {
  action: 'missing' | 'retry' | 'apply'
  pending: RecoveryPendingSubmission
  reconciliation?: ChatSubmissionReconciliation
}
type RecoverPendingSubmission = (input: {
  pending: RecoveryPendingSubmission
  ensureConversation: () => Promise<'ready' | 'not_found' | 'retry'>
  reconcile: (initial: { initialAcceptedTurnId?: string; initialAssistantStatus?: ChatMessage['status'] }) => Promise<ChatSubmissionReconciliation>
}) => Promise<PendingRecoveryAction>
const recoverPendingSubmission = (reconciliationModule as unknown as { reconcileChatPendingSubmissionRecovery?: RecoverPendingSubmission }).reconcileChatPendingSubmissionRecovery
assert.equal(typeof recoverPendingSubmission, 'function', '待确认恢复必须由可测试状态机保证会话 ready 后才应用 outcome')
if (!recoverPendingSubmission) throw new Error('reconcileChatPendingSubmissionRecovery 未实现')

const recoverySnapshot = { type: 'doc', content: [{ type: 'paragraph', content: [{ type: 'text', text: '必须保留的原草稿' }] }] }
const notFoundPending: ChatPendingSubmission = {
  request: {
    systemAccountId: 'sys_1',
    conversationId: 'conv_outside_first_page',
    clientMessageId: 'client_not_found_after_503',
    snapshot: recoverySnapshot
  },
  streamStarted: false,
  silent: false,
  errorMessage: '连接中断'
}
const notFoundWhileConversationUnavailable = await recoverPendingSubmission({
  pending: notFoundPending,
  ensureConversation: async () => 'retry',
  reconcile: async () => ({ messages: [], confirmed: true, accepted: false, terminal: false, submissionState: 'not_found' })
})
assert.equal(notFoundWhileConversationUnavailable.action, 'retry', 'targeted GET 503 后 submission not_found 也不能在不存在的 composer 上恢复或清存储')
assert.deepEqual(notFoundWhileConversationUnavailable.pending.request.snapshot, recoverySnapshot, 'targeted GET 503 后必须完整保留草稿快照')
const notFoundAfterConversationReady = await recoverPendingSubmission({
  pending: notFoundWhileConversationUnavailable.pending,
  ensureConversation: async () => 'ready',
  reconcile: async () => ({ messages: [], confirmed: true, accepted: false, terminal: false, submissionState: 'not_found' })
})
assert.equal(notFoundAfterConversationReady.action, 'apply', '原会话成功加载并选择后才允许恢复 not_found 草稿')

const terminalMessages = pair('completed', 'client_terminal_after_503', 'turn_terminal_after_503')
const terminalWhileConversationUnavailable = await recoverPendingSubmission({
  pending: {
    ...notFoundPending,
    request: { ...notFoundPending.request, clientMessageId: 'client_terminal_after_503' }
  },
  ensureConversation: async () => 'retry',
  reconcile: async () => ({
    messages: terminalMessages,
    confirmed: true,
    accepted: true,
    terminal: true,
    submissionState: 'accepted',
    turnId: 'turn_terminal_after_503',
    assistantStatus: 'completed'
  })
})
assert.equal(terminalWhileConversationUnavailable.action, 'retry', 'targeted GET 503 后 terminal 结果也必须等待原会话可用')
assert.deepEqual({
  turnId: terminalWhileConversationUnavailable.pending.startedTurnId,
  assistantStatus: terminalWhileConversationUnavailable.pending.acceptedAssistantStatus,
  snapshot: terminalWhileConversationUnavailable.pending.request.snapshot
}, {
  turnId: 'turn_terminal_after_503',
  assistantStatus: 'completed',
  snapshot: recoverySnapshot
}, '等待原会话期间必须持久保存 terminal 单调事实和原草稿')
let terminalRetryInitial: { initialAcceptedTurnId?: string; initialAssistantStatus?: ChatMessage['status'] } | undefined
const terminalAfterConversationReady = await recoverPendingSubmission({
  pending: terminalWhileConversationUnavailable.pending,
  ensureConversation: async () => 'ready',
  reconcile: async (initial) => {
    terminalRetryInitial = initial
    return {
      messages: terminalMessages,
      confirmed: true,
      accepted: true,
      terminal: true,
      submissionState: 'accepted',
      turnId: 'turn_terminal_after_503',
      assistantStatus: 'completed'
    }
  }
})
assert.deepEqual(terminalRetryInitial, { initialAcceptedTurnId: 'turn_terminal_after_503', initialAssistantStatus: 'completed' }, '下一轮确权必须继承上一轮 accepted terminal 事实')
assert.equal(terminalAfterConversationReady.action, 'apply')
assert.deepEqual(terminalAfterConversationReady.reconciliation?.messages, terminalMessages, '原会话 ready 后必须保留并应用 terminal 消息')

const missingConversation = await recoverPendingSubmission({
  pending: notFoundPending,
  ensureConversation: async () => 'not_found',
  reconcile: async () => { throw new Error('conversation 404 后不应继续 submission 确权') }
})
assert.equal(missingConversation.action, 'missing', '只有原会话权威 404 才允许清理 pending')

let terminalListAttempts = 0
const terminalRefresh = await reconcileChatSubmission({
  getSubmissionStatus: async () => ({ state: 'accepted', turnId: 'turn_terminal_refresh', assistantStatus: 'completed' }),
  listMessages: async () => {
    terminalListAttempts += 1
    if (terminalListAttempts < 3) throw new Error('消息刷新暂不可用')
    return pair('completed', 'client_terminal_refresh', 'turn_terminal_refresh')
  },
  stop: async () => undefined,
  wait: async () => undefined,
  maxAttempts: 4
})
assert.deepEqual({ confirmed: terminalRefresh.confirmed, terminal: terminalRefresh.terminal, listAttempts: terminalListAttempts }, {
  confirmed: true, terminal: true, listAttempts: 3
}, 'accepted terminal 必须等消息列表刷新成功后再解除待确认门禁')

let malformedCalls = 0
const malformed = await reconcileChatSubmission({
  getSubmissionStatus: async () => { malformedCalls += 1; return { state: 'unexpected' } as unknown as ChatSubmissionStatus },
  listMessages: async () => [],
  stop: async () => undefined,
  wait: async () => undefined,
  maxAttempts: 3
})
assert.deepEqual({ confirmed: malformed.confirmed, accepted: malformed.accepted, malformedCalls }, { confirmed: false, accepted: false, malformedCalls: 3 }, '畸形 200 状态必须按临时协议错误重试，不能抛出后永久停掉后台 timer')

assert.equal(isRetryableChatSubmissionLookupError({ response: { status: 408 } }), true)
assert.equal(isRetryableChatSubmissionLookupError({ response: { status: 429 } }), true)
assert.equal(isRetryableChatSubmissionLookupError({ response: { status: 500 } }), true)
assert.equal(isRetryableChatSubmissionLookupError({ response: { status: 400 } }), false)
assert.equal(isRetryableChatSubmissionLookupError({ response: { status: 404 } }), false)
assert.equal(isRetryableChatSubmissionLookupError(new TypeError('Network Error')), true)

for (const reconciliation of [
  { messages: [], confirmed: false, accepted: false, terminal: false },
  { messages: pair('streaming'), confirmed: true, accepted: true, terminal: false, turnId: 'turn_1', assistantStatus: 'streaming' as const }
]) {
  let disposed = false
  let resolveReconciliation!: (value: typeof reconciliation) => void
  const deferred = new Promise<typeof reconciliation>((resolve) => { resolveReconciliation = resolve })
  let applied = 0
  const applying = applyChatReconciliationIfActive({
    reconcile: () => deferred,
    isDisposed: () => disposed,
    apply: async () => { applied += 1 }
  })
  disposed = true
  resolveReconciliation(reconciliation)
  assert.equal(await applying, false)
  assert.equal(applied, 0, '卸载后返回的延迟对账不得进入副作用阶段')
}

console.log('AI 问答专用提交状态、断流停止与后台确权回归通过')
