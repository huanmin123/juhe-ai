import { NativeIndexedDbChatCacheAdapter } from '../../views/chat/chatLocalCache'
import { ChatConversationSyncCoordinator, type ChatConversationSyncDependencies } from '../../views/chat/chatConversationSync'
import type { ChatConversationSyncHead, ChatMessage } from '../../types/domain/chat'

const result = document.querySelector('#result')!
function assert(condition: unknown, message: string): asserts condition { if (!condition) throw new Error(message) }
function request<T>(value: IDBRequest<T>): Promise<T> { return new Promise((resolve, reject) => { value.onsuccess = () => resolve(value.result); value.onerror = () => reject(value.error) }) }
function txDone(tx: IDBTransaction): Promise<void> { return new Promise((resolve, reject) => { tx.oncomplete = () => resolve(); tx.onabort = () => reject(tx.error) }) }
function message(sequenceNo: number, conversationId = 'tail', expiresAt = '2099-01-01T00:00:00.000Z'): ChatMessage { return { id: `m_${sequenceNo}`, conversationId, turnId: `t_${sequenceNo}`, sequenceNo, role: sequenceNo % 2 ? 'user' : 'assistant', status: 'completed', contentText: `message-${sequenceNo}`, model: 'model', createdAt: '2026-07-16T00:00:00.000Z', expiresAt } }
function deferred<T>(): { promise: Promise<T>; resolve: (value: T) => void } { let resolve!: (value: T) => void; return { promise: new Promise<T>((next) => { resolve = next }), resolve } }
function syncHead(revision: number): ChatConversationSyncHead { return { conversationId: 'race', messageRevision: revision, unchanged: false, serverTime: '2026-07-16T00:00:00.000Z', lastSequenceNo: 2, tail: [] } }

try {
  await new Promise<void>((resolve) => { const deletion = indexedDB.deleteDatabase('juhe-ai-chat-cache'); deletion.onsuccess = () => resolve(); deletion.onerror = () => resolve(); deletion.onblocked = () => resolve() })
  const oldDatabase = await new Promise<IDBDatabase>((resolve, reject) => {
    const opening = indexedDB.open('juhe-ai-chat-cache', 1)
    opening.onupgradeneeded = () => {
      const db = opening.result
      const heads = db.createObjectStore('conversation_heads', { keyPath: ['systemAccountId', 'conversationId'] }); heads.createIndex('by_last_access', 'lastAccessAt'); heads.createIndex('by_account', 'systemAccountId')
      const messages = db.createObjectStore('messages', { keyPath: ['systemAccountId', 'conversationId', 'sequenceNo'] }); messages.createIndex('by_conversation', ['systemAccountId', 'conversationId']); messages.createIndex('by_expires_at', 'expiresAt')
      const running = db.createObjectStore('running_turns', { keyPath: ['systemAccountId', 'conversationId'] }); running.createIndex('by_account', 'systemAccountId')
    }
    opening.onsuccess = () => resolve(opening.result); opening.onerror = () => reject(opening.error)
  })
  const oldSeed = oldDatabase.transaction(['conversation_heads', 'messages'], 'readwrite'); const oldSeeded = txDone(oldSeed)
  oldSeed.objectStore('conversation_heads').put({ systemAccountId: 'legacy', conversationId: 'discarded', messageRevision: 1, lastAccessAt: 1, byteSize: 10 })
  oldSeed.objectStore('messages').put({ ...message(1, 'discarded'), systemAccountId: 'legacy', byteSize: 10 })
  await oldSeeded
  oldDatabase.close()
  const adapter = new NativeIndexedDbChatCacheAdapter()
  await adapter.putMessages('A', 'tail', Array.from({ length: 250 }, (_, index) => message(index + 1)), { now: 1 })
  const tail = await adapter.readConversation('A', 'tail')
  assert(tail.messages.length === 200 && tail.messages[0]?.sequenceNo === 51 && tail.messages[199]?.sequenceNo === 250, '必须返回最新 200 条并升序')
  const beforeHead = tail.head!.byteSize
  assert(await adapter.getTotalBytes() === beforeHead, '全局 byte metadata 必须与首个会话一致')
  await adapter.putHead({ systemAccountId: 'A', conversationId: 'tail', messageRevision: 9, lastAccessAt: 2, byteSize: 0, title: 'updated' })
  assert((await adapter.readConversation('A', 'tail')).head?.byteSize === beforeHead, 'putHead 必须事务合并保留 byteSize')

  const lowCoordinator = new ChatConversationSyncCoordinator()
  const highCoordinator = new ChatConversationSyncCoordinator()
  lowCoordinator.activateAccount('C'); highCoordinator.activateAccount('C')
  const lowListStarted = deferred<void>()
  const releaseLowList = deferred<void>()
  const dependencies = (revision: number, text: string, waitForRelease = false): ChatConversationSyncDependencies => ({
    readCache: async (account, conversation) => adapter.readConversation(account, conversation),
    getSyncHead: async () => syncHead(revision),
    listMessages: async () => {
      if (waitForRelease) { lowListStarted.resolve(); await releaseLowList.promise }
      return [{ ...message(1, 'race'), contentText: text }, { ...message(2, 'race'), contentText: `${text}-assistant` }]
    },
    deleteConversation: (account, conversation) => adapter.deleteConversation(account, conversation),
    commitSnapshot: async (account, head, messages) => (await adapter.commitSyncSnapshot({
      systemAccountId: account,
      conversationId: head.conversationId,
      messageRevision: head.messageRevision,
      messages: [...messages],
      now: revision
    })).committed
  })
  const lowSync = lowCoordinator.synchronize({ systemAccountId: 'C', conversationId: 'race', dependencies: dependencies(5, 'low', true) })
  await lowListStarted.promise
  const highSync = await highCoordinator.synchronize({ systemAccountId: 'C', conversationId: 'race', dependencies: dependencies(6, 'high') })
  assert(highSync.state === 'ready', '较高 revision coordinator 必须先提交成功')
  releaseLowList.resolve()
  const lowSyncResult = await lowSync
  assert(lowSyncResult.state === 'superseded', '低 revision 晚到的另一 coordinator 必须被原子 CAS 拒绝')
  const raceCache = await adapter.readConversation('C', 'race')
  assert(raceCache.head?.messageRevision === 6 && raceCache.messages[0]?.contentText === 'high', '跨标签页交错写不得回退 head/messages')
  await adapter.deleteConversation('C', 'race')

  const database = await new Promise<IDBDatabase>((resolve, reject) => { const opening = indexedDB.open('juhe-ai-chat-cache', 2); opening.onsuccess = () => resolve(opening.result); opening.onerror = () => reject(opening.error) })
  assert(database.version === 2 && database.objectStoreNames.contains('cache_metadata'), '真实 v1 schema 必须升级并安全重建为 v2')
  assert((await adapter.readConversation('legacy', 'discarded')).messages.length === 0, 'v1 展示缓存数据必须在升级时丢弃')
  const seed = database.transaction(['messages', 'running_turns'], 'readwrite'); const seeded = txDone(seed)
  seed.objectStore('messages').put({ ...message(1, 'orphan'), systemAccountId: 'A', byteSize: 10 })
  seed.objectStore('running_turns').put({ systemAccountId: 'A', conversationId: 'orphan', turnId: 't', startedAt: 'x' })
  await seeded
  await adapter.clearAccount('A')
  const verify = database.transaction(['conversation_heads', 'messages', 'running_turns'], 'readonly'); const verified = txDone(verify)
  const counts = await Promise.all([request(verify.objectStore('conversation_heads').index('by_account').count('A')), request(verify.objectStore('messages').index('by_account').count('A')), request(verify.objectStore('running_turns').index('by_account').count('A'))])
  await verified
  assert(counts.every((count) => count === 0), 'clearAccount 必须清理无 head 的 messages/running_turns')
  assert(await adapter.getTotalBytes() === 0, 'clearAccount 必须同步扣减账户 head bytes')

  for (let index = 0; index < 66; index += 1) await adapter.putMessages('A', `many_${index}`, [{ ...message(1, `many_${index}`), conversationId: `many_${index}` }], { now: index + 10 })
  await adapter.putMessages('B', 'preserved', [{ ...message(1, 'preserved'), conversationId: 'preserved' }], { now: 100 })
  const preservedBytes = (await adapter.readConversation('B', 'preserved')).head!.byteSize
  await adapter.clearAccount('A')
  assert(await adapter.getTotalBytes() === preservedBytes, '65+ 会话清理后 totalBytes 必须精确保留其他账户')
  const manyVerify = database.transaction(['conversation_heads', 'messages', 'running_turns'], 'readonly'); const manyVerified = txDone(manyVerify)
  const manyCounts = await Promise.all([request(manyVerify.objectStore('conversation_heads').index('by_account').count('A')), request(manyVerify.objectStore('messages').index('by_account').count('A')), request(manyVerify.objectStore('running_turns').index('by_account').count('A'))]); await manyVerified
  assert(manyCounts.every((count) => count === 0), '65+ 会话的所有账户 store 必须清空')

  let aborted = false
  try { await adapter.putMessages('B', 'abort', [{ ...message(1, 'abort'), sequenceNo: Number.NaN }], { now: 200 }) } catch { aborted = true }
  assert(aborted, '非法 key 请求必须 abort 事务并返回失败')
  await adapter.putMessages('B', 'after_abort', [{ ...message(1, 'after_abort'), conversationId: 'after_abort' }], { now: 201 })
  assert((await adapter.readConversation('B', 'after_abort')).messages.length === 1, 'abort 终态后 adapter 必须可继续新事务')

  await adapter.putMessages('B', 'expiry', [message(1, 'expiry', '2026-01-01T00:00:00.000Z'), message(2, 'expiry')], { now: 3 })
  const expiryBefore = (await adapter.readConversation('B', 'expiry')).head!.byteSize
  const cleanup = await adapter.cleanupExpired('2026-07-16T00:00:00.000Z', 1, 1)
  const expiryAfter = await adapter.readConversation('B', 'expiry')
  assert(cleanup.messages === 1 && expiryAfter.messages.length === 1 && expiryAfter.head!.byteSize < expiryBefore, 'expiry 游标必须有界删除并扣减 head bytes')
  adapter.close(); database.close()
  result.textContent = 'PASS: native IndexedDB adapter regression'
  document.body.dataset.status = 'passed'
} catch (error) {
  result.textContent = `FAIL: ${error instanceof Error ? error.message : 'unknown'}`
  document.body.dataset.status = 'failed'
  throw error
}
