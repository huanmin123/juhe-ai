import assert from 'node:assert/strict'

import {
  decideChatConversationSync,
  synchronizeChatConversation,
  type ChatConversationSyncDependencies
} from '../../views/chat/chatConversationSync'
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

const local = [message(1, 'user_1'), message(2, 'assistant_1')]
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
  deleteFromSequence: async (_accountId, _conversationId, sequenceNo) => { calls.push(`delete:${sequenceNo}`) },
  deleteConversation: async () => { calls.push('delete-conversation') },
  writeMessages: async () => { calls.push('write-messages') },
  writeHead: async (_accountId, syncHead) => { calls.push(`write-head:${syncHead.messageRevision}`) },
  writeRunningTurn: async () => undefined,
  removeRunningTurn: async () => undefined
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

console.log('AI 问答 IndexedDB cache-first 与 revision 差异同步回归通过')
