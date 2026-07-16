import type { ChatConversationSyncHead, ChatMessage } from '@/types/domain/chat'

export type ChatConversationSyncDecision =
  | { type: 'unchanged' }
  | { type: 'append'; afterSequenceNo: number }
  | { type: 'refresh_from'; fromSequenceNo: number }
  | { type: 'replace_tail'; fromSequenceNo: number }
  | { type: 'rebuild' }

export interface ChatConversationSyncDependencies {
  readCache(systemAccountId: string, conversationId: string): Promise<{ head?: { messageRevision: number }; messages: ChatMessage[] }>
  getSyncHead(conversationId: string, knownRevision: number): Promise<ChatConversationSyncHead>
  listMessages(conversationId: string, cursor: { limit: number; afterSequenceNo?: number; fromSequenceNo?: number }): Promise<ChatMessage[]>
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
  return { type: 'rebuild' }
}

export async function synchronizeChatConversation(input: {
  systemAccountId: string
  conversationId: string
  dependencies: ChatConversationSyncDependencies
}): Promise<ChatConversationSynchronizationResult> {
  const { systemAccountId, conversationId, dependencies } = input
  const cached = await dependencies.readCache(systemAccountId, conversationId)
  let localRevision = cached.head?.messageRevision
  let messages = [...cached.messages].sort((left, right) => left.sequenceNo - right.sequenceNo)
  let latestHead!: ChatConversationSyncHead
  let passes = 0

  while (passes < 2) {
    passes += 1
    latestHead = await dependencies.getSyncHead(conversationId, localRevision ?? 0)
    const decision = decideChatConversationSync({ localRevision, localMessages: messages, server: latestHead })
    if (decision.type === 'unchanged') {
      await persistHead(dependencies, systemAccountId, conversationId, latestHead)
      return { messages, messageRevision: latestHead.messageRevision, syncHead: latestHead, passes }
    }

    if (decision.type === 'rebuild') {
      await dependencies.deleteConversation(systemAccountId, conversationId)
      messages = await dependencies.listMessages(conversationId, { limit: 100, fromSequenceNo: 0 })
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

  return { messages, messageRevision: latestHead.messageRevision, syncHead: latestHead, passes }
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
