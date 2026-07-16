import assert from 'node:assert/strict'

import {
  ChatConversationSyncCoordinator,
  activateChatConversationSyncAccount,
  decideChatConversationSync,
  hasOlderChatMessages,
  projectChatMessagesWithRuntime,
  restoreChatActiveTurnFromSync,
  synchronizeChatConversation,
  type ChatConversationSyncDependencies
} from '../../views/chat/chatConversationSync'
import { ChatRuntimeReconciliationScheduler } from '../../views/chat/chatRuntimeReconciliation'
import type { ChatConversationSyncHead, ChatMessage } from '../../types/domain/chat'

function message(sequenceNo: number, id: string, status: ChatMessage['status'] = 'completed'): ChatMessage {
  return {
    id,
    conversationId: 'conv_1',
    turnId: `turn_${Math.ceil(sequenceNo / 2)}`,
    sequenceNo,
    role: sequenceNo % 2 ? 'user' : 'assistant',
    status,
    contentText: id,
    contentBlocks: [],
    model: 'mock',
    createdAt: '2026-07-16T00:00:00.000Z',
    expiresAt: '2026-07-19T00:00:00.000Z'
  }
}

function head(messageRevision: number, tail: ChatConversationSyncHead['tail'], lastSequenceNo = tail.at(-1)?.sequenceNo ?? 0): ChatConversationSyncHead {
  return {
    conversationId: 'conv_1',
    messageRevision,
    unchanged: false,
    serverTime: '2026-07-16T00:00:00.000Z',
    lastSequenceNo,
    tail
  }
}

function deferred<T>(): { promise: Promise<T>; resolve: (value: T) => void } {
  let resolve!: (value: T) => void
  return { promise: new Promise<T>((next) => { resolve = next }), resolve }
}

const local = [message(1, 'user_1'), message(2, 'assistant_1')]
activateChatConversationSyncAccount('sys_1')
assert.equal(hasOlderChatMessages(Array.from({ length: 200 }, (_, index) => message(index + 51, `cached_${index}`))), true, 'IndexedDB 最新 200 条从 51 开始时仍必须允许向前分页')
assert.equal(hasOlderChatMessages(Array.from({ length: 100 }, (_, index) => message(index + 1, `complete_${index}`))), false, '已从 sequence 1 开始时不得仅凭页长误报更早消息')
assert.equal(hasOlderChatMessages(Array.from({ length: 100 }, (_, index) => message(index + 51, `retained_${index}`)), 0), false, '服务端向前分页返回空时必须记住保留边界并停止继续加载')
assert.deepEqual(decideChatConversationSync({ localRevision: 4, localMessages: local, server: { ...head(4, []), unchanged: true } }), { type: 'unchanged' })
assert.deepEqual(decideChatConversationSync({ localRevision: 4, localMessages: local, server: head(5, [
  { id: 'user_2', turnId: 'turn_2', sequenceNo: 3, role: 'user', status: 'completed', expiresAt: '2026-07-19T00:00:00.000Z' },
  { id: 'assistant_2', turnId: 'turn_2', sequenceNo: 4, role: 'assistant', status: 'streaming', expiresAt: '2026-07-19T00:00:00.000Z' }
], 4) }), { type: 'append', afterSequenceNo: 2 })
assert.deepEqual(decideChatConversationSync({ localRevision: 4, localMessages: [message(1, 'user_1'), message(2, 'assistant_1', 'streaming')], server: head(5, [
  { id: 'user_1', turnId: 'turn_1', sequenceNo: 1, role: 'user', status: 'completed', expiresAt: '2026-07-19T00:00:00.000Z' },
  { id: 'assistant_1', turnId: 'turn_1', sequenceNo: 2, role: 'assistant', status: 'completed', expiresAt: '2026-07-19T00:00:00.000Z' }
]) }), { type: 'refresh_from', fromSequenceNo: 2 })
assert.deepEqual(decideChatConversationSync({ localRevision: 4, localMessages: local, server: head(5, [
  { id: 'user_replaced', turnId: 'turn_1b', sequenceNo: 1, role: 'user', status: 'completed', expiresAt: '2026-07-19T00:00:00.000Z' },
  { id: 'assistant_replaced', turnId: 'turn_1b', sequenceNo: 2, role: 'assistant', status: 'completed', expiresAt: '2026-07-19T00:00:00.000Z' }
]) }), { type: 'replace_tail', fromSequenceNo: 1 })
assert.deepEqual(decideChatConversationSync({ localRevision: 6, localMessages: local, server: head(5, []) }), { type: 'rebuild' })

const runtimeProjection = message(2, 'assistant_1', 'streaming')
runtimeProjection.contentText = 'runtime-new'
const projected = projectChatMessagesWithRuntime({
  messages: [message(1, 'user_1'), { ...message(2, 'assistant_1', 'streaming'), contentText: 'db-old' }],
  activeTurn: { turnId: 'turn_1', assistantMessageId: 'assistant_1' },
  runtimeTurn: { turnId: 'turn_1', assistantMessageId: 'assistant_1', eventVersion: 12, status: 'running', projection: runtimeProjection }
})
assert.equal(projected[1]?.contentText, 'runtime-new', 'DB 旧 streaming 正文不得覆盖更高 eventVersion runtime 投影')

const terminalRuntimeProjection = { ...runtimeProjection, status: 'completed' as const, contentText: 'runtime-terminal' }
const terminalProjected = projectChatMessagesWithRuntime({
  messages: [message(1, 'user_1'), { ...message(2, 'assistant_1', 'streaming'), contentText: 'db-stale-active' }],
  activeTurn: { turnId: 'turn_1', assistantMessageId: 'assistant_1' },
  runtimeTurn: { turnId: 'turn_1', assistantMessageId: 'assistant_1', eventVersion: 13, status: 'completed', projection: terminalRuntimeProjection }
})
assert.equal(terminalProjected[1]?.status, 'completed', '旧 active sync head 不得覆盖同 turn 的 terminal runtime 投影')
assert.equal(terminalProjected[1]?.contentText, 'runtime-terminal')
const terminalDatabaseWins = projectChatMessagesWithRuntime({
  messages: [message(1, 'user_1'), { ...message(2, 'assistant_1', 'completed'), contentText: 'db-terminal' }],
  activeTurn: { turnId: 'turn_1', assistantMessageId: 'assistant_1' },
  runtimeTurn: { turnId: 'turn_1', assistantMessageId: 'assistant_1', eventVersion: 12, status: 'running', projection: runtimeProjection }
})
assert.equal(terminalDatabaseWins[1]?.contentText, 'db-terminal', '明确 terminal DB 投影不得被旧 running runtime 降级')

const projectionCommitGate = deferred<void>()
let projectionVersion = 1
let committedProjection = ''
const projectionRaceCoordinator = new ChatConversationSyncCoordinator()
projectionRaceCoordinator.activateAccount('sys_projection_race')
const projectionRace = projectionRaceCoordinator.synchronize({
  systemAccountId: 'sys_projection_race',
  conversationId: 'conv_1',
  projectMessages: (items) => ({
    messages: items.map((item) => item.role === 'assistant' ? { ...item, contentText: `runtime-${projectionVersion}` } : item),
    eventVersion: projectionVersion
  }),
  dependencies: {
    readCache: async () => ({ head: { messageRevision: 1 }, messages: local }),
    getSyncHead: async () => ({ ...head(1, []), unchanged: true }),
    listMessages: async () => [],
    deleteConversation: async () => undefined,
    commitSnapshot: async (_account, _head, items) => {
      committedProjection = items.find((item) => item.role === 'assistant')?.contentText ?? ''
      if (committedProjection === 'runtime-1') await projectionCommitGate.promise
      return true
    }
  }
})
await Promise.resolve(); await Promise.resolve()
projectionVersion = 2
projectionCommitGate.resolve()
const projectionRaceResult = await projectionRace
assert.equal(projectionRaceResult.state, 'ready')
if (projectionRaceResult.state !== 'ready') throw new Error('投影竞态同步应 ready')
assert.equal(projectionRaceResult.projectionEventVersion, 2, 'sync 返回前必须重新读取最新 runtime eventVersion')
assert.equal(projectionRaceResult.messages.find((item) => item.role === 'assistant')?.contentText, 'runtime-2', '最终 UI 结果不得被旧投影覆盖倒退')
assert.equal(committedProjection, 'runtime-2', 'IDB 提交期间投影推进时必须有界重投影并再次提交')

let casReadCount = 0
let casCommitCount = 0
const casWinnerCoordinator = new ChatConversationSyncCoordinator()
casWinnerCoordinator.activateAccount('sys_cas_winner')
const casWinner = await casWinnerCoordinator.synchronize({
  systemAccountId: 'sys_cas_winner',
  conversationId: 'conv_1',
  projectMessages: (items) => ({
    messages: items.map((item) => item.role === 'assistant' ? { ...item, contentText: 'local-version-4' } : item),
    eventVersion: 4,
    status: 'streaming',
    turnId: 'turn_1',
    assistantMessageId: 'assistant_1'
  }),
  dependencies: {
    readCache: async () => {
      casReadCount += 1
      if (casReadCount === 1) return { head: { messageRevision: 7 }, messages: local }
      return {
        head: {
          messageRevision: 7,
          projectionEventVersion: 8,
          projectionStatus: 'streaming',
          projectionTurnId: 'turn_1',
          projectionAssistantMessageId: 'assistant_1'
        },
        messages: [message(1, 'user_1'), { ...message(2, 'assistant_1', 'streaming'), contentText: 'winner-version-8' }],
        runningTurn: {
          systemAccountId: 'sys_cas_winner',
          conversationId: 'conv_1',
          turnId: 'turn_1',
          assistantMessageId: 'assistant_1',
          startedAt: '2026-07-16T00:00:00.000Z',
          eventVersion: 8,
          status: 'streaming' as const
        }
      }
    },
    getSyncHead: async () => ({
      ...head(7, []),
      unchanged: true,
      activeTurn: { turnId: 'turn_1', assistantMessageId: 'assistant_1', startedAt: '2026-07-16T00:00:00.000Z' }
    }),
    listMessages: async () => { throw new Error('same revision CAS 收敛不应重新拉正文') },
    deleteConversation: async () => undefined,
    commitSnapshot: async () => { casCommitCount += 1; return false }
  }
})
assert.equal(casWinner.state, 'ready', '同 revision CAS 失败后必须有界重读赢家投影并向 UI 返回 ready')
if (casWinner.state !== 'ready') throw new Error('CAS 赢家同步应 ready')
assert.equal(casWinner.projectionEventVersion, 8)
assert.equal(casWinner.messages[1]?.contentText, 'winner-version-8')
assert.equal(casReadCount, 2, 'CAS 失败只能额外重读一次缓存')
assert.equal(casCommitCount, 1, '发现更高 eventVersion 赢家后不得再用旧投影争抢写入')

let restoredProjectionVersion: number | undefined
const restoredCoordinator = new ChatConversationSyncCoordinator()
restoredCoordinator.activateAccount('sys_restore')
const restoredResult = await restoredCoordinator.synchronize({
  systemAccountId: 'sys_restore',
  conversationId: 'conv_1',
  dependencies: {
    readCache: async () => ({
      head: {
        messageRevision: 7,
        projectionEventVersion: 12,
        projectionStatus: 'streaming',
        projectionTurnId: 'turn_1',
        projectionAssistantMessageId: 'assistant_1'
      },
      messages: [message(1, 'user_1'), { ...message(2, 'assistant_1', 'streaming'), contentText: 'cached-stream' }],
      runningTurn: {
        systemAccountId: 'sys_restore',
        conversationId: 'conv_1',
        turnId: 'turn_1',
        assistantMessageId: 'assistant_1',
        startedAt: '2026-07-16T00:00:00.000Z',
        eventVersion: 12,
        status: 'streaming'
      }
    }),
    getSyncHead: async () => ({
      ...head(7, []),
      unchanged: true,
      activeTurn: { turnId: 'turn_1', assistantMessageId: 'assistant_1', startedAt: '2026-07-16T00:00:00.000Z' }
    }),
    listMessages: async () => { throw new Error('same revision 不应读取正文') },
    deleteConversation: async () => undefined,
    commitSnapshot: async (_account, _head, _items, projection) => {
      restoredProjectionVersion = projection?.eventVersion
      return projection?.eventVersion === 12
    }
  }
})
assert.equal(restoredResult.state, 'ready', '刷新后 runtime 为空时必须以 IndexedDB 投影水位完成同 revision 权威同步')
if (restoredResult.state !== 'ready') throw new Error('缓存恢复同步应 ready')
assert.equal(restoredResult.projectionEventVersion, 12)
assert.equal(restoredProjectionVersion, 12)
let restoredAttachVersion: number | undefined
assert.equal(restoreChatActiveTurnFromSync({
  systemAccountId: 'sys_restore',
  syncHead: restoredResult.syncHead,
  messages: restoredResult.messages,
  projectionEventVersion: restoredResult.projectionEventVersion,
  attach: (input) => { restoredAttachVersion = input.eventVersion }
}), true, 'same revision active turn 必须继续触发 attach')
assert.equal(restoredAttachVersion, 12, 'attach 必须继承缓存水位，拒绝重放低版本 SSE')

let clock = 0
const reconciliationScheduler = new ChatRuntimeReconciliationScheduler({ now: () => clock })
const stalledTurn = {
  systemAccountId: 'sys_retry', conversationId: 'conv_retry', clientMessageId: 'client_retry', turnId: 'turn_retry', assistantMessageId: 'assistant_retry',
  eventVersion: 4, status: 'running' as const, reconnectAttempt: 3, projection: message(2, 'assistant_retry', 'streaming'), reconciliationReason: 'reconnect_exhausted' as const
}
assert.equal(reconciliationScheduler.begin(stalledTurn), true, '首次 reconnect_exhausted 必须立即权威同步')
reconciliationScheduler.complete(stalledTurn, stalledTurn)
assert.equal(reconciliationScheduler.begin(stalledTurn), false, '同一 5 秒窗口不得高频重复同步')
clock = 4_999
assert.equal(reconciliationScheduler.begin(stalledTurn), false)
clock = 5_000
assert.equal(reconciliationScheduler.begin(stalledTurn), true, '首次临时失败或 active 无进展后 5 秒必须允许重试')
reconciliationScheduler.complete(stalledTurn, stalledTurn)
clock = 10_000
assert.equal(reconciliationScheduler.begin(stalledTurn), false, '第二次无进展后必须退避，避免每 5 秒触发 attach 循环')
clock = 15_000
assert.equal(reconciliationScheduler.begin(stalledTurn), true, '有界退避后仍必须继续发现服务端终态')
reconciliationScheduler.complete(stalledTurn, { ...stalledTurn, eventVersion: 5, reconciliationReason: undefined, status: 'completed' })
assert.equal(reconciliationScheduler.size, 0, '终态或运行态进展必须清理重试 key')

const calls: string[] = []
let headCall = 0
const dependencies: ChatConversationSyncDependencies = {
  readCache: async () => ({ head: { messageRevision: 4 }, messages: local }),
  getSyncHead: async (_conversationId, knownRevision) => {
    calls.push(`head:${knownRevision}`)
    headCall += 1
    return headCall === 1
      ? head(5, [{ id: 'assistant_1', turnId: 'turn_1', sequenceNo: 2, role: 'assistant', status: 'completed', expiresAt: '2026-07-19T00:00:00.000Z' }])
      : { ...head(6, [{ id: 'assistant_1', turnId: 'turn_1', sequenceNo: 2, role: 'assistant', status: 'completed', expiresAt: '2026-07-19T00:00:00.000Z' }]), unchanged: false }
  },
  listMessages: async (_conversationId, cursor) => {
    calls.push(`messages:${JSON.stringify(cursor)}`)
    return [message(2, 'assistant_1', 'completed')]
  },
  deleteConversation: async () => { calls.push('delete-conversation') },
  commitSnapshot: async (_accountId, syncHead) => { calls.push(`commit:${syncHead.messageRevision}`); return true }
}
const synchronized = await synchronizeChatConversation({ systemAccountId: 'sys_1', conversationId: 'conv_1', dependencies })
assert.equal(synchronized.state, 'ready')
if (synchronized.state !== 'ready') throw new Error('同步应返回 ready')
assert.equal(synchronized.messageRevision, 6)
assert.equal(headCall, 2, '同步期间 revision 持续变化时最多复查一次')
assert.deepEqual(calls.filter((item) => item.startsWith('messages:')), [
  'messages:{"fromSequenceNo":2,"limit":100}',
  'messages:{"fromSequenceNo":2,"limit":100}'
], '同 ID streaming 到终态必须使用 inclusive from 游标')

let unchangedBodyCalls = 0
await synchronizeChatConversation({
  systemAccountId: 'sys_1',
  conversationId: 'conv_1',
  dependencies: {
    ...dependencies,
    readCache: async () => ({ head: { messageRevision: 9 }, messages: local }),
    getSyncHead: async () => ({ ...head(9, []), unchanged: true }),
    listMessages: async () => { unchangedBodyCalls += 1; return [] }
  }
})
assert.equal(unchangedBodyCalls, 0, 'revision 相等时不得请求消息正文')

let deletedOnNotFound = 0
const notFound = await synchronizeChatConversation({
  systemAccountId: 'sys_1',
  conversationId: 'conv_1',
  dependencies: {
    ...dependencies,
    getSyncHead: async () => { throw { response: { status: 404 } } },
    deleteConversation: async () => { deletedOnNotFound += 1 }
  }
})
assert.equal(notFound.state, 'not_found')
assert.equal(deletedOnNotFound, 1, '权威 404 必须删除当前账户下的会话缓存')

let deletedOnForbidden = 0
const forbidden = await synchronizeChatConversation({
  systemAccountId: 'sys_1',
  conversationId: 'conv_1',
  dependencies: {
    ...dependencies,
    getSyncHead: async () => { throw { response: { status: 403 } } },
    deleteConversation: async () => { deletedOnForbidden += 1 }
  }
})
assert.equal(forbidden.state, 'forbidden')
assert.equal(deletedOnForbidden, 0, '普通 403 只隐藏正文，必须保留重新认证后可复用的本地缓存')

const midSyncForbiddenCoordinator = new ChatConversationSyncCoordinator()
midSyncForbiddenCoordinator.activateAccount('sys_mid_forbidden')
const midSyncForbidden = await midSyncForbiddenCoordinator.synchronize({
  systemAccountId: 'sys_mid_forbidden', conversationId: 'conv_1', dependencies: {
    ...dependencies,
    readCache: async () => ({ head: { messageRevision: 1 }, messages: local }),
    getSyncHead: async () => head(2, [], 4),
    listMessages: async () => { throw { response: { status: 403 } } }
  }
}).catch(() => undefined)
assert.equal(midSyncForbidden?.state, 'forbidden', 'sync head 后权限失效时也必须返回 forbidden 并阻断 runtime')

const coordinator = new ChatConversationSyncCoordinator()
coordinator.activateAccount('sys_concurrent')
const delayedHead = deferred<ChatConversationSyncHead>()
let concurrentHeadCalls = 0
let concurrentWrites = 0
const concurrentDependencies: ChatConversationSyncDependencies = {
  ...dependencies,
  readCache: async () => ({ head: { messageRevision: 1 }, messages: local }),
  getSyncHead: async () => { concurrentHeadCalls += 1; return delayedHead.promise },
  commitSnapshot: async () => { concurrentWrites += 1; return true }
}
const firstConcurrent = coordinator.synchronize({ systemAccountId: 'sys_concurrent', conversationId: 'conv_1', dependencies: concurrentDependencies })
const secondConcurrent = coordinator.synchronize({ systemAccountId: 'sys_concurrent', conversationId: 'conv_1', dependencies: concurrentDependencies })
assert.equal(firstConcurrent, secondConcurrent, '同账户同会话同步必须合并为一个 single-flight')
delayedHead.resolve({ ...head(1, []), unchanged: true })
const [firstConcurrentResult, secondConcurrentResult] = await Promise.all([firstConcurrent, secondConcurrent])
assert.equal(concurrentHeadCalls, 1, '合并同步不得重复请求 sync head')
assert.equal(concurrentWrites, 1, '合并同步只能持久化一次 head')
assert.deepEqual(firstConcurrentResult, secondConcurrentResult)

const provenRevision = await coordinator.synchronize({
  systemAccountId: 'sys_concurrent', conversationId: 'conv_monotonic', dependencies: {
    ...dependencies,
    readCache: async () => ({ head: { messageRevision: 10 }, messages: local }),
    getSyncHead: async () => ({ ...head(10, []), conversationId: 'conv_monotonic', unchanged: true })
  }
})
assert.equal(provenRevision.state, 'ready')
let regressiveWrites = 0
const regressive = await coordinator.synchronize({
  systemAccountId: 'sys_concurrent', conversationId: 'conv_monotonic', dependencies: {
    ...dependencies,
    readCache: async () => ({ head: { messageRevision: 9 }, messages: local }),
    getSyncHead: async () => ({ ...head(9, []), conversationId: 'conv_monotonic', unchanged: true }),
    commitSnapshot: async () => { regressiveWrites += 1; return true }
  }
})
assert.equal(regressive.state, 'superseded', '已经确认更高服务端 revision 后不得接受迟到的低 revision')
assert.equal(regressiveWrites, 0, '低 revision 结果不得回写 IndexedDB')

const logoutCoordinator = new ChatConversationSyncCoordinator()
logoutCoordinator.activateAccount('sys_logout')
const logoutHead = deferred<ChatConversationSyncHead>()
let logoutWrites = 0
let logoutAttachEligible = false
const logoutFlight = logoutCoordinator.synchronize({
  systemAccountId: 'sys_logout', conversationId: 'conv_logout', dependencies: {
    ...dependencies,
    readCache: async () => ({ head: { messageRevision: 1 }, messages: local }),
    getSyncHead: async () => logoutHead.promise,
    commitSnapshot: async () => { logoutWrites += 1; return true }
  }
}).then((result) => { logoutAttachEligible = result.state === 'ready'; return result })
logoutCoordinator.invalidateAccount('sys_logout')
logoutHead.resolve({ ...head(1, []), conversationId: 'conv_logout', unchanged: true })
const logoutResult = await logoutFlight
await logoutCoordinator.drainAccount('sys_logout')
assert.equal(logoutResult.state, 'superseded', 'logout 后迟到的 sync 必须失效')
assert.equal(logoutWrites, 0, 'logout 后迟到的 sync 不得回写缓存')
assert.equal(logoutAttachEligible, false, 'logout 后迟到的 sync 不得触发 attach')
let postLogoutHeadCalls = 0
const postLogout = await logoutCoordinator.synchronize({
  systemAccountId: 'sys_logout', conversationId: 'conv_logout', dependencies: {
    ...dependencies,
    getSyncHead: async () => { postLogoutHeadCalls += 1; return head(2, []) }
  }
})
assert.equal(postLogout.state, 'superseded', 'logout 后旧页面发起的新 sync 也必须被账号栅栏拒绝')
assert.equal(postLogoutHeadCalls, 0, 'logout 后旧页面不得再访问会话 sync API')

console.log('AI 问答 IndexedDB cache-first 与 revision 差异同步回归通过')
