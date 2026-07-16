import { chatApi, type ChatMessageListParams } from '@/api/domains/chat'
import type { ChatConversationSyncHead, ChatMessage } from '@/types/domain/chat'
import { getDefaultChatLocalCache } from './chatLocalCache'

export type ChatConversationSyncDecision =
  | { type: 'unchanged' }
  | { type: 'append'; afterSequenceNo: number }
  | { type: 'refresh_from'; fromSequenceNo: number }
  | { type: 'replace_tail'; fromSequenceNo: number }
  | { type: 'rebuild' }

export interface ChatConversationSyncDependencies {
  readCache(systemAccountId: string, conversationId: string): Promise<{ head?: { messageRevision: number }; messages: ChatMessage[] }>
  getSyncHead(conversationId: string, knownRevision: number): Promise<ChatConversationSyncHead>
  listMessages(conversationId: string, cursor: ChatMessageListParams): Promise<ChatMessage[]>
  deleteFromSequence(systemAccountId: string, conversationId: string, sequenceNo: number): Promise<unknown>
  deleteConversation(systemAccountId: string, conversationId: string): Promise<unknown>
  writeMessages(systemAccountId: string, conversationId: string, messages: readonly ChatMessage[]): Promise<unknown>
  writeHead(systemAccountId: string, head: ChatConversationSyncHead): Promise<unknown>
  writeRunningTurn(systemAccountId: string, conversationId: string, turn: ChatConversationSyncHead['activeTurn']): Promise<unknown>
  removeRunningTurn(systemAccountId: string, conversationId: string): Promise<unknown>
}

export interface ChatConversationSynchronizationResult {
  messages: ChatMessage[]
  messageRevision: number
  syncHead: ChatConversationSyncHead
  passes: number
}

export type ChatConversationSyncOutcome =
  | ({ state: 'ready' } & ChatConversationSynchronizationResult)
  | { state: 'not_found'; messages: []; passes: number }
  | { state: 'forbidden'; messages: []; passes: number }

export function decideChatConversationSync(input: {
  localRevision?: number
  localMessages: readonly ChatMessage[]
  server: ChatConversationSyncHead
}): ChatConversationSyncDecision {
  if (input.localRevision === undefined || input.localRevision > input.server.messageRevision) return { type: 'rebuild' }
  if (input.localRevision === input.server.messageRevision || input.server.unchanged) return { type: 'unchanged' }
  const localBySequence = new Map(input.localMessages.map((message) => [message.sequenceNo, message]))
  for (const tail of input.server.tail) {
    const local = localBySequence.get(tail.sequenceNo)
    if (!local) continue
    if (local.id !== tail.id || local.turnId !== tail.turnId) return { type: 'replace_tail', fromSequenceNo: tail.sequenceNo }
    if (local.status !== tail.status) return { type: 'refresh_from', fromSequenceNo: tail.sequenceNo }
  }
  const lastLocalSequence = input.localMessages.at(-1)?.sequenceNo
  if (lastLocalSequence !== undefined && lastLocalSequence < input.server.lastSequenceNo) {
    return { type: 'append', afterSequenceNo: lastLocalSequence }
  }
  const firstServerTail = input.server.tail[0]
  if (firstServerTail) return { type: 'refresh_from', fromSequenceNo: firstServerTail.sequenceNo }
  return { type: 'rebuild' }
}

export async function synchronizeChatConversation(input: {
  systemAccountId: string
  conversationId: string
  dependencies: ChatConversationSyncDependencies
}): Promise<ChatConversationSyncOutcome> {
  const { systemAccountId, conversationId, dependencies } = input
  const cached = await dependencies.readCache(systemAccountId, conversationId)
  let localRevision = cached.head?.messageRevision
  let messages = [...cached.messages].sort((left, right) => left.sequenceNo - right.sequenceNo)
  let latestHead!: ChatConversationSyncHead
  let passes = 0

  while (passes < 2) {
    passes += 1
    try {
      latestHead = await dependencies.getSyncHead(conversationId, localRevision ?? 0)
    } catch (error) {
      const status = chatHttpStatus(error)
      if (status === 404) {
        await dependencies.deleteConversation(systemAccountId, conversationId)
        return { state: 'not_found', messages: [], passes }
      }
      if (status === 403) return { state: 'forbidden', messages: [], passes }
      throw error
    }
    const decision = decideChatConversationSync({ localRevision, localMessages: messages, server: latestHead })
    if (decision.type === 'unchanged') {
      await persistHead(dependencies, systemAccountId, conversationId, latestHead)
      return { state: 'ready', messages, messageRevision: latestHead.messageRevision, syncHead: latestHead, passes }
    }

    if (decision.type === 'rebuild') {
      await dependencies.deleteConversation(systemAccountId, conversationId)
      messages = await dependencies.listMessages(conversationId, { limit: 100 })
    } else if (decision.type === 'append') {
      const appended = await dependencies.listMessages(conversationId, { afterSequenceNo: decision.afterSequenceNo, limit: 100 })
      messages = mergeBySequence(messages, appended)
    } else {
      await dependencies.deleteFromSequence(systemAccountId, conversationId, decision.fromSequenceNo)
      const refreshed = await dependencies.listMessages(conversationId, { fromSequenceNo: decision.fromSequenceNo, limit: 100 })
      messages = mergeBySequence(messages.filter((message) => message.sequenceNo < decision.fromSequenceNo), refreshed)
    }
    await dependencies.writeMessages(systemAccountId, conversationId, messages)
    await persistHead(dependencies, systemAccountId, conversationId, latestHead)
    localRevision = latestHead.messageRevision
  }

  return { state: 'ready', messages, messageRevision: latestHead.messageRevision, syncHead: latestHead, passes }
}

export function createDefaultChatConversationSyncDependencies(options: { pendingConversationIds?: () => ReadonlySet<string> } = {}): ChatConversationSyncDependencies {
  const cache = getDefaultChatLocalCache()
  return {
    readCache: async (systemAccountId, conversationId) => (await cache.readConversation(systemAccountId, conversationId)).value ?? { messages: [] },
    getSyncHead: chatApi.getConversationSync,
    listMessages: chatApi.listMessages,
    deleteFromSequence: (systemAccountId, conversationId, sequenceNo) => cache.deleteFromSequence(systemAccountId, conversationId, sequenceNo),
    deleteConversation: (systemAccountId, conversationId) => cache.deleteConversation(systemAccountId, conversationId),
    writeMessages: (systemAccountId, conversationId, values) => cache.putMessages(systemAccountId, conversationId, values, {
      currentConversationId: conversationId,
      pendingConfirmationConversationIds: options.pendingConversationIds?.()
    }),
    writeHead: async (systemAccountId, head) => {
      await cache.putHead(systemAccountId, head)
      await cache.cleanupExpired(head.serverTime)
    },
    writeRunningTurn: (systemAccountId, conversationId, turn) => turn
      ? cache.putRunningTurn(systemAccountId, conversationId, turn)
      : cache.removeRunningTurn(systemAccountId, conversationId),
    removeRunningTurn: (systemAccountId, conversationId) => cache.removeRunningTurn(systemAccountId, conversationId)
  }
}

function mergeBySequence(current: readonly ChatMessage[], incoming: readonly ChatMessage[]): ChatMessage[] {
  const merged = new Map(current.map((message) => [message.sequenceNo, message]))
  for (const message of incoming) merged.set(message.sequenceNo, message)
  return [...merged.values()].sort((left, right) => left.sequenceNo - right.sequenceNo)
}

async function persistHead(
  dependencies: ChatConversationSyncDependencies,
  systemAccountId: string,
  conversationId: string,
  head: ChatConversationSyncHead
): Promise<void> {
  await dependencies.writeHead(systemAccountId, head)
  if (head.activeTurn) await dependencies.writeRunningTurn(systemAccountId, conversationId, head.activeTurn)
  else await dependencies.removeRunningTurn(systemAccountId, conversationId)
}

function chatHttpStatus(error: unknown): number | undefined {
  const candidate = error as { status?: unknown; response?: { status?: unknown } } | undefined
  const status = candidate?.response?.status ?? candidate?.status
  return typeof status === 'number' ? status : undefined
}
