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
  | { state: 'superseded'; messages: []; passes: number }

interface ChatConversationSyncInput {
  systemAccountId: string
  conversationId: string
  dependencies: ChatConversationSyncDependencies
  projectMessages?(messages: readonly ChatMessage[], head: ChatConversationSyncHead): ChatMessage[]
}

interface SyncFlight {
  accountEpoch: number
  promise: Promise<ChatConversationSyncOutcome>
}

export class ChatConversationSyncCoordinator {
  private activeSystemAccountId?: string
  private readonly accountEpochs = new Map<string, number>()
  private readonly flights = new Map<string, SyncFlight>()
  private readonly serverRevisionHighWatermarks = new Map<string, number>()

  activateAccount(systemAccountId?: string): void {
    if (this.activeSystemAccountId === systemAccountId) return
    const previous = this.activeSystemAccountId
    this.activeSystemAccountId = systemAccountId
    if (previous) this.bumpEpoch(previous)
    if (systemAccountId && !this.accountEpochs.has(systemAccountId)) this.accountEpochs.set(systemAccountId, 0)
  }

  invalidateAccount(systemAccountId: string): void {
    this.bumpEpoch(systemAccountId)
    if (this.activeSystemAccountId === systemAccountId) this.activeSystemAccountId = undefined
    for (const key of this.serverRevisionHighWatermarks.keys()) {
      if (syncKeyBelongsTo(key, systemAccountId)) this.serverRevisionHighWatermarks.delete(key)
    }
  }

  synchronize(input: ChatConversationSyncInput): Promise<ChatConversationSyncOutcome> {
    if (this.activeSystemAccountId !== input.systemAccountId) return Promise.resolve(supersededOutcome())
    const key = syncKey(input.systemAccountId, input.conversationId)
    const accountEpoch = this.accountEpoch(input.systemAccountId)
    const existing = this.flights.get(key)
    if (existing?.accountEpoch === accountEpoch) return existing.promise

    const isCurrent = () => this.activeSystemAccountId === input.systemAccountId && this.accountEpoch(input.systemAccountId) === accountEpoch
    const promise = synchronizeChatConversationInternal(
      input,
      isCurrent,
      this.serverRevisionHighWatermarks.get(key)
    ).catch(async (error: unknown): Promise<ChatConversationSyncOutcome> => {
      if (!isCurrent()) return supersededOutcome()
      const status = chatHttpStatus(error)
      if (status === 403) return { state: 'forbidden', messages: [], passes: 1 }
      if (status === 404) {
        await input.dependencies.deleteConversation(input.systemAccountId, input.conversationId)
        return isCurrent() ? { state: 'not_found', messages: [], passes: 1 } : supersededOutcome(1)
      }
      throw error
    }).then((result) => {
      if (result.state === 'ready' && this.activeSystemAccountId === input.systemAccountId && this.accountEpoch(input.systemAccountId) === accountEpoch) {
        const current = this.serverRevisionHighWatermarks.get(key) ?? -1
        if (result.messageRevision < current) return supersededOutcome(result.passes)
        this.serverRevisionHighWatermarks.set(key, result.messageRevision)
      }
      return result
    }).finally(() => {
      if (this.flights.get(key)?.promise === promise) this.flights.delete(key)
    })
    this.flights.set(key, { accountEpoch, promise })
    return promise
  }

  async drainAccount(systemAccountId: string): Promise<void> {
    const pending = [...this.flights.entries()]
      .filter(([key]) => syncKeyBelongsTo(key, systemAccountId))
      .map(([, flight]) => flight.promise)
    await Promise.allSettled(pending)
  }

  private accountEpoch(systemAccountId: string): number {
    return this.accountEpochs.get(systemAccountId) ?? 0
  }

  private bumpEpoch(systemAccountId: string): void {
    this.accountEpochs.set(systemAccountId, this.accountEpoch(systemAccountId) + 1)
  }
}

const defaultSyncCoordinator = new ChatConversationSyncCoordinator()

export function activateChatConversationSyncAccount(systemAccountId?: string): void {
  defaultSyncCoordinator.activateAccount(systemAccountId)
}

export function invalidateChatConversationSyncAccount(systemAccountId: string): void {
  defaultSyncCoordinator.invalidateAccount(systemAccountId)
}

export function drainChatConversationSyncAccount(systemAccountId: string): Promise<void> {
  return defaultSyncCoordinator.drainAccount(systemAccountId)
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
  const firstServerTail = input.server.tail[0]
  if (firstServerTail) return { type: 'refresh_from', fromSequenceNo: firstServerTail.sequenceNo }
  return { type: 'rebuild' }
}

export function projectChatMessagesWithRuntime(input: {
  messages: readonly ChatMessage[]
  activeTurn?: { turnId: string; assistantMessageId: string }
  runtimeTurn?: {
    turnId?: string
    assistantMessageId?: string
    eventVersion: number
    status: string
    projection: unknown
  }
}): ChatMessage[] {
  const { activeTurn, runtimeTurn } = input
  const projectionCandidate = runtimeTurn?.projection as Partial<ChatMessage> | undefined
  if (!activeTurn || !runtimeTurn || runtimeTurn.status !== 'running' || runtimeTurn.eventVersion < 0
    || runtimeTurn.turnId !== activeTurn.turnId || runtimeTurn.assistantMessageId !== activeTurn.assistantMessageId
    || !projectionCandidate?.id || !projectionCandidate.sequenceNo || projectionCandidate.sequenceNo <= 0) return input.messages as ChatMessage[]
  const projection = JSON.parse(JSON.stringify(projectionCandidate)) as ChatMessage
  const projected = input.messages.map((item) => item.id === projection.id ? projection : item)
  if (!projected.some((item) => item.id === projection.id)) projected.push(projection)
  return projected.sort((left, right) => left.sequenceNo - right.sequenceNo)
}

export function synchronizeChatConversation(input: ChatConversationSyncInput): Promise<ChatConversationSyncOutcome> {
  return defaultSyncCoordinator.synchronize(input)
}

async function synchronizeChatConversationInternal(
  input: ChatConversationSyncInput,
  isCurrent: () => boolean,
  minimumServerRevision?: number
): Promise<ChatConversationSyncOutcome> {
  const { systemAccountId, conversationId, dependencies } = input
  const cached = await dependencies.readCache(systemAccountId, conversationId)
  if (!isCurrent()) return supersededOutcome()
  let localRevision = cached.head?.messageRevision
  let messages = [...cached.messages].sort((left, right) => left.sequenceNo - right.sequenceNo)
  let latestHead!: ChatConversationSyncHead
  let passes = 0

  while (passes < 2) {
    passes += 1
    try {
      latestHead = await dependencies.getSyncHead(conversationId, localRevision ?? 0)
      if (!isCurrent()) return supersededOutcome(passes)
      if (minimumServerRevision !== undefined && latestHead.messageRevision < minimumServerRevision) return supersededOutcome(passes)
    } catch (error) {
      if (!isCurrent()) return supersededOutcome(passes)
      const status = chatHttpStatus(error)
      if (status === 404) {
        if (!isCurrent()) return supersededOutcome(passes)
        await dependencies.deleteConversation(systemAccountId, conversationId)
        if (!isCurrent()) return supersededOutcome(passes)
        return { state: 'not_found', messages: [], passes }
      }
      if (status === 403) return { state: 'forbidden', messages: [], passes }
      throw error
    }
    const decision = decideChatConversationSync({ localRevision, localMessages: messages, server: latestHead })
    if (decision.type === 'unchanged') {
      const projected = input.projectMessages?.(messages, latestHead) ?? messages
      if (projected !== messages) {
        if (!isCurrent()) return supersededOutcome(passes)
        await dependencies.writeMessages(systemAccountId, conversationId, projected)
        if (!isCurrent()) return supersededOutcome(passes)
        messages = projected
      }
      if (!await persistHeadIfCurrent(dependencies, systemAccountId, conversationId, latestHead, isCurrent)) return supersededOutcome(passes)
      return { state: 'ready', messages, messageRevision: latestHead.messageRevision, syncHead: latestHead, passes }
    }

    if (decision.type === 'rebuild') {
      if (!isCurrent()) return supersededOutcome(passes)
      await dependencies.deleteConversation(systemAccountId, conversationId)
      if (!isCurrent()) return supersededOutcome(passes)
      messages = await dependencies.listMessages(conversationId, { limit: 100 })
    } else if (decision.type === 'append') {
      const appended = await dependencies.listMessages(conversationId, { afterSequenceNo: decision.afterSequenceNo, limit: 100 })
      messages = mergeBySequence(messages, appended)
    } else {
      if (!isCurrent()) return supersededOutcome(passes)
      await dependencies.deleteFromSequence(systemAccountId, conversationId, decision.fromSequenceNo)
      if (!isCurrent()) return supersededOutcome(passes)
      const refreshed = await dependencies.listMessages(conversationId, { fromSequenceNo: decision.fromSequenceNo, limit: 100 })
      messages = mergeBySequence(messages.filter((message) => message.sequenceNo < decision.fromSequenceNo), refreshed)
    }
    if (!isCurrent()) return supersededOutcome(passes)
    messages = input.projectMessages?.(messages, latestHead) ?? messages
    await dependencies.writeMessages(systemAccountId, conversationId, messages)
    if (!isCurrent()) return supersededOutcome(passes)
    if (!await persistHeadIfCurrent(dependencies, systemAccountId, conversationId, latestHead, isCurrent)) return supersededOutcome(passes)
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

async function persistHeadIfCurrent(
  dependencies: ChatConversationSyncDependencies,
  systemAccountId: string,
  conversationId: string,
  head: ChatConversationSyncHead,
  isCurrent: () => boolean
): Promise<boolean> {
  if (!isCurrent()) return false
  await dependencies.writeHead(systemAccountId, head)
  if (!isCurrent()) return false
  if (head.activeTurn) await dependencies.writeRunningTurn(systemAccountId, conversationId, head.activeTurn)
  else await dependencies.removeRunningTurn(systemAccountId, conversationId)
  return isCurrent()
}

function supersededOutcome(passes = 0): ChatConversationSyncOutcome {
  return { state: 'superseded', messages: [], passes }
}

function syncKey(systemAccountId: string, conversationId: string): string {
  return JSON.stringify([systemAccountId, conversationId])
}

function syncKeyBelongsTo(key: string, systemAccountId: string): boolean {
  try {
    const parsed = JSON.parse(key) as unknown
    return Array.isArray(parsed) && parsed[0] === systemAccountId
  } catch {
    return false
  }
}

function chatHttpStatus(error: unknown): number | undefined {
  const candidate = error as { status?: unknown; response?: { status?: unknown } } | undefined
  const status = candidate?.response?.status ?? candidate?.status
  return typeof status === 'number' ? status : undefined
}
