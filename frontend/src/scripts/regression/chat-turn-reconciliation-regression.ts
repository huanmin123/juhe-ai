import assert from 'node:assert/strict'

import { reconcileChatSubmission } from '../../views/chat/chatTurnReconciliation'
import type { ChatMessage } from '../../types/domain/chat'

function pair(status: ChatMessage['status']): ChatMessage[] {
  return [
    { id: 'user_1', conversationId: 'conv_1', turnId: 'turn_1', sequenceNo: 1, clientMessageId: 'client_1', role: 'user', status: 'completed', contentText: '问题', contentBlocks: [{ type: 'input_marker', inputType: 'input_text', order: 0 }], model: 'mock', createdAt: '2026-07-13T00:00:00.000Z', expiresAt: '2026-07-20T00:00:00.000Z' },
    { id: 'assistant_1', conversationId: 'conv_1', turnId: 'turn_1', sequenceNo: 2, role: 'assistant', status, contentText: '', contentBlocks: [], model: 'mock', createdAt: '2026-07-13T00:00:00.000Z', expiresAt: '2026-07-20T00:00:00.000Z' }
  ]
}

const snapshots = [pair('streaming'), pair('streaming'), pair('canceled')]
let listCalls = 0
let stopCalls = 0
const waits: number[] = []
const settled = await reconcileChatSubmission({
  clientMessageId: 'client_1',
  listMessages: async () => snapshots[Math.min(listCalls++, snapshots.length - 1)]!,
  stop: async () => {
    stopCalls += 1
    if (stopCalls === 1) throw new Error('active stream 尚未登记')
  },
  wait: async (milliseconds) => { waits.push(milliseconds) },
  maxAttempts: 5
})
assert.deepEqual({ accepted: settled.accepted, terminal: settled.terminal, status: settled.assistantStatus, listCalls, stopCalls }, {
  accepted: true,
  terminal: true,
  status: 'canceled',
  listCalls: 3,
  stopCalls: 2
}, '第一次刷新仍 streaming 且第一次 stop 早到失败时，必须重试直到助手终态')
assert.deepEqual(waits, [50, 100])

let pendingLists = 0
const lateAccepted = await reconcileChatSubmission({
  clientMessageId: 'client_late',
  confirmPendingAcceptance: true,
  listMessages: async () => {
    pendingLists += 1
    return pendingLists === 1 ? [] : pair('completed').map((item) => item.role === 'user' ? { ...item, clientMessageId: 'client_late' } : item)
  },
  stop: async () => { throw new Error('终态不应 stop') },
  wait: async () => undefined,
  maxAttempts: 3
})
assert.equal(lateAccepted.accepted, true, '未知网络错误必须给正在接受的请求一个有界对账窗口')
assert.equal(lateAccepted.terminal, true)

let definitiveLists = 0
const notAccepted = await reconcileChatSubmission({
  clientMessageId: 'client_invalid',
  confirmPendingAcceptance: false,
  listMessages: async () => { definitiveLists += 1; return [] },
  stop: async () => undefined,
  wait: async () => undefined
})
assert.deepEqual({ accepted: notAccepted.accepted, terminal: notAccepted.terminal, definitiveLists }, { accepted: false, terminal: false, definitiveLists: 1 }, '明确 HTTP 拒绝不应无意义轮询')

let completedAfterErrorReads = 0
const completedAfterError = await reconcileChatSubmission({
  clientMessageId: 'client_1',
  confirmPendingAcceptance: true,
  listMessages: async () => {
    completedAfterErrorReads += 1
    if (completedAfterErrorReads === 1) throw new Error('临时 GET 失败')
    return pair('completed')
  },
  stop: async () => undefined,
  wait: async () => undefined,
  maxAttempts: 3
})
assert.deepEqual({ confirmed: completedAfterError.confirmed, accepted: completedAfterError.accepted, status: completedAfterError.assistantStatus, reads: completedAfterErrorReads }, {
  confirmed: true, accepted: true, status: 'completed', reads: 2
}, '单次 listMessages 异常必须在窗口内重试并识别 completed')

let canceledAfterErrorReads = 0
const canceledAfterError = await reconcileChatSubmission({
  clientMessageId: 'client_1',
  acceptedTurnId: 'turn_1',
  confirmPendingAcceptance: true,
  listMessages: async () => {
    canceledAfterErrorReads += 1
    if (canceledAfterErrorReads === 1) throw new Error('临时 GET 失败')
    return pair('canceled')
  },
  stop: async () => undefined,
  wait: async () => undefined,
  maxAttempts: 3
})
assert.deepEqual({ confirmed: canceledAfterError.confirmed, status: canceledAfterError.assistantStatus, reads: canceledAfterErrorReads }, {
  confirmed: true, status: 'canceled', reads: 2
}, '已知 turn 的首轮 GET 异常后仍必须识别 canceled')

let failedReads = 0
const unknown = await reconcileChatSubmission({
  clientMessageId: 'client_unknown',
  confirmPendingAcceptance: true,
  listMessages: async () => { failedReads += 1; throw new Error('持续不可用') },
  stop: async () => undefined,
  wait: async () => undefined,
  maxAttempts: 3
})
assert.deepEqual({ confirmed: unknown.confirmed, accepted: unknown.accepted, terminal: unknown.terminal, failedReads }, {
  confirmed: false, accepted: false, terminal: false, failedReads: 3
}, '全部权威读取失败只能返回 unknown，绝不能伪装成未接受')

console.log('AI 问答接受后断流与停止终态对账回归通过')
