import { chatApi, type ChatMessageListParams } from '@/api/domains/chat'
import type { ChatConversationSyncHead, ChatMessage, ChatMessageStatus } from '@/types/domain/chat'
import { getDefaultChatLocalCache, type ChatCacheProjectionWatermark, type ChatRunningTurn } from './chatLocalCache'

export type ChatConversationSyncDecision =
  | { type: 'unchanged' }
  | { type: 'append'; afterSequenceNo: number }
  | { type: 'refresh_from'; fromSequenceNo: number }
  | { type: 'replace_tail'; fromSequenceNo: number }
  | { type: 'rebuild' }

export interface ChatConversationSyncDependencies {
  readCache(systemAccountId: string, conversationId: string): Promise<{
    head?: {
      messageRevision: number
      projectionEventVersion?: number
      projectionStatus?: ChatMessageStatus
      projectionTurnId?: string
      projectionAssistantMessageId?: string
    }
    messages: ChatMessage[]
    runningTurn?: ChatRunningTurn
  }>
  getSyncHead(conversationId: string, knownRevision: number): Promise<ChatConversationSyncHead>
  listMessages(conversationId: string, cursor: ChatMessageListParams): Promise<ChatMessage[]>
  deleteConversation(systemAccountId: string, conversationId: string): Promise<unknown>
  commitSnapshot(systemAccountId: string, head: ChatConversationSyncHead, messages: readonly ChatMessage[], projection?: ChatCacheProjectionWatermark): Promise<boolean | undefined>
}

export interface ChatConversationSynchronizationResult {
  messages: ChatMessage[]
  messageRevision: number
  syncHead: ChatConversationSyncHead
  passes: number
  projectionEventVersion?: number
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
  projectMessages?(messages: readonly ChatMessage[], head: ChatConversationSyncHead): ChatMessage[] | ChatProjectedMessages
}

export interface ChatProjectedMessages {
  messages: ChatMessage[]
  eventVersion?: number
  status?: ChatMessageStatus
  turnId?: string
  assistantMessageId?: string
}

export interface ChatActiveTurnAttachInput {
  systemAccountId: string
  conversationId: string
  clientMessageId?: string
  turnId: string
  assistantMessageId: string
  eventVersion?: number
  projection?: ChatMessage
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

export function hasOlderChatMessages(values: readonly ChatMessage[], lastFetchedOlderCount?: number): boolean {
  if (lastFetchedOlderCount === 0) return false
  return (values[0]?.sequenceNo ?? 1) > 1
}

export function restoreChatActiveTurnFromSync(input: {
  systemAccountId: string
  syncHead: ChatConversationSyncHead
  messages: readonly ChatMessage[]
  clientMessageId?: string
  projectionEventVersion?: number
  attach(value: ChatActiveTurnAttachInput): unknown
}): boolean {
  const active = input.syncHead.activeTurn
  if (!active) return false
  input.attach({
    systemAccountId: input.systemAccountId,
    conversationId: input.syncHead.conversationId,
    clientMessageId: input.clientMessageId,
    turnId: active.turnId,
    assistantMessageId: active.assistantMessageId,
    eventVersion: input.projectionEventVersion,
    projection: input.messages.find((item) => item.id === active.assistantMessageId)
  })
  return true
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
  const terminalRuntime = runtimeTurn && isTerminalMessageStatus(runtimeTurn.status)
  const matchesActive = Boolean(activeTurn && runtimeTurn?.turnId === activeTurn.turnId && runtimeTurn.assistantMessageId === activeTurn.assistantMessageId)
  const matchesExistingMessage = Boolean(terminalRuntime && runtimeTurn?.assistantMessageId && input.messages.some((message) => message.id === runtimeTurn.assistantMessageId && message.turnId === runtimeTurn.turnId))
  if (!runtimeTurn || runtimeTurn.eventVersion < 0 || (!matchesActive && !matchesExistingMessage)
    || !projectionCandidate?.id || !projectionCandidate.sequenceNo || projectionCandidate.sequenceNo <= 0) return input.messages as ChatMessage[]
  const databaseProjection = input.messages.find((message) => message.id === projectionCandidate.id && message.turnId === projectionCandidate.turnId)
  if (databaseProjection && isTerminalMessageStatus(databaseProjection.status) && !terminalRuntime) return input.messages as ChatMessage[]
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
  let projectionEventVersion: number | undefined

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
    const cachedProjection = projectionFromCache(cached, latestHead, localRevision)
    if (decision.type === 'unchanged') {
      const committed = await commitProjectedSnapshot(input, messages, latestHead, isCurrent, cachedProjection)
      if (!committed.current) return supersededOutcome(passes)
      messages = committed.messages
      projectionEventVersion = committed.eventVersion
      return { state: 'ready', messages, messageRevision: latestHead.messageRevision, syncHead: latestHead, passes, projectionEventVersion }
    }

    if (decision.type === 'rebuild') {
      messages = await dependencies.listMessages(conversationId, { limit: 100 })
    } else if (decision.type === 'append') {
      const appended = await dependencies.listMessages(conversationId, { afterSequenceNo: decision.afterSequenceNo, limit: 100 })
      messages = mergeBySequence(messages, appended)
    } else {
      const refreshed = await dependencies.listMessages(conversationId, { fromSequenceNo: decision.fromSequenceNo, limit: 100 })
      messages = mergeBySequence(messages.filter((message) => message.sequenceNo < decision.fromSequenceNo), refreshed)
    }
    if (!isCurrent()) return supersededOutcome(passes)
    const committed = await commitProjectedSnapshot(input, messages, latestHead, isCurrent, cachedProjection)
    if (!committed.current) return supersededOutcome(passes)
    messages = committed.messages
    projectionEventVersion = committed.eventVersion
    localRevision = latestHead.messageRevision
  }

  return { state: 'ready', messages, messageRevision: latestHead.messageRevision, syncHead: latestHead, passes, projectionEventVersion }
}

export function createDefaultChatConversationSyncDependencies(options: { pendingConversationIds?: () => ReadonlySet<string> } = {}): ChatConversationSyncDependencies {
  const cache = getDefaultChatLocalCache()
  return {
    readCache: async (systemAccountId, conversationId) => (await cache.readConversation(systemAccountId, conversationId)).value ?? { messages: [] },
    getSyncHead: chatApi.getConversationSync,
    listMessages: chatApi.listMessages,
    deleteConversation: (systemAccountId, conversationId) => cache.deleteConversation(systemAccountId, conversationId),
    commitSnapshot: async (systemAccountId, head, values, projection) => {
      const result = await cache.commitSyncSnapshot(systemAccountId, head, values, {
        currentConversationId: head.conversationId,
        pendingConfirmationConversationIds: options.pendingConversationIds?.(),
        projection
      })
      await cache.cleanupExpired(head.serverTime)
      return result.ok ? result.value?.committed : undefined
    }
  }
}

function mergeBySequence(current: readonly ChatMessage[], incoming: readonly ChatMessage[]): ChatMessage[] {
  const merged = new Map(current.map((message) => [message.sequenceNo, message]))
  for (const message of incoming) merged.set(message.sequenceNo, message)
  return [...merged.values()].sort((left, right) => left.sequenceNo - right.sequenceNo)
}

async function commitProjectedSnapshot(
  input: ChatConversationSyncInput,
  authoritativeMessages: readonly ChatMessage[],
  head: ChatConversationSyncHead,
  isCurrent: () => boolean,
  cachedProjection?: ChatProjectedMessages
): Promise<{ current: boolean; messages: ChatMessage[]; eventVersion?: number }> {
  let projection = chooseProjection(readProjection(input, authoritativeMessages, head), cachedProjection)
  for (let attempt = 0; attempt < 2; attempt += 1) {
    if (!isCurrent()) return { current: false, messages: [] }
    const committed = await input.dependencies.commitSnapshot(input.systemAccountId, head, projection.messages, projectionWatermark(projection))
    if (!isCurrent()) return { current: false, messages: [] }
    if (committed === false) {
      const winner = await input.dependencies.readCache(input.systemAccountId, head.conversationId)
      if (!isCurrent()) return { current: false, messages: [] }
      const winnerProjection = projectionFromCache(winner, head, head.messageRevision)
      if (!winnerProjection || !isProjectionAtLeastAsNew(winnerProjection, projection)) return { current: false, messages: [] }
      return { current: true, messages: winnerProjection.messages, eventVersion: winnerProjection.eventVersion }
    }
    if (!input.projectMessages) break
    const latest = chooseProjection(readProjection(input, authoritativeMessages, head), cachedProjection)
    if (latest.eventVersion === undefined || projection.eventVersion === undefined || latest.eventVersion <= projection.eventVersion) break
    projection = latest
  }
  return { current: isCurrent(), messages: projection.messages, eventVersion: projection.eventVersion }
}

function isProjectionAtLeastAsNew(candidate: ChatProjectedMessages, current: ChatProjectedMessages): boolean {
  if (candidate.turnId !== current.turnId || candidate.assistantMessageId !== current.assistantMessageId) return false
  const statusDelta = projectionStatusPriority(candidate.status) - projectionStatusPriority(current.status)
  if (statusDelta !== 0) return statusDelta > 0
  if (candidate.eventVersion === undefined) return current.eventVersion === undefined
  return current.eventVersion === undefined || candidate.eventVersion >= current.eventVersion
}

function projectionFromCache(
  cached: Awaited<ReturnType<ChatConversationSyncDependencies['readCache']>>,
  head: ChatConversationSyncHead,
  localRevision?: number
): ChatProjectedMessages | undefined {
  const active = head.activeTurn
  const cacheHead = cached.head
  const running = cached.runningTurn
  if (!active || localRevision !== head.messageRevision || cacheHead?.messageRevision !== head.messageRevision) return undefined
  if (cacheHead.projectionTurnId !== active.turnId || cacheHead.projectionAssistantMessageId !== active.assistantMessageId) return undefined
  if (running?.turnId !== active.turnId || (running.assistantMessageId && running.assistantMessageId !== active.assistantMessageId)) return undefined
  const assistant = cached.messages.find((item) => item.id === active.assistantMessageId && item.turnId === active.turnId)
  if (!assistant || !cacheHead.projectionStatus) return undefined
  return {
    messages: cached.messages,
    eventVersion: cacheHead.projectionEventVersion ?? running.eventVersion,
    status: cacheHead.projectionStatus,
    turnId: active.turnId,
    assistantMessageId: active.assistantMessageId
  }
}

function chooseProjection(current: ChatProjectedMessages, cached?: ChatProjectedMessages): ChatProjectedMessages {
  if (!cached) return current
  if (current.turnId && (current.turnId !== cached.turnId || current.assistantMessageId !== cached.assistantMessageId)) return current
  const statusDelta = projectionStatusPriority(current.status) - projectionStatusPriority(cached.status)
  if (statusDelta > 0) return current
  if (statusDelta < 0) return cached
  if (current.eventVersion === undefined) return cached
  if (cached.eventVersion !== undefined && current.eventVersion < cached.eventVersion) return cached
  return current
}

function projectionStatusPriority(status?: ChatMessageStatus): number {
  return isTerminalMessageStatus(status ?? '') ? 2 : status === 'streaming' ? 1 : 0
}

function readProjection(
  input: ChatConversationSyncInput,
  messages: readonly ChatMessage[],
  head: ChatConversationSyncHead
): ChatProjectedMessages {
  const projected = input.projectMessages?.(messages, head)
  if (!projected) return { messages: messages as ChatMessage[] }
  return Array.isArray(projected) ? { messages: projected } : projected
}

function projectionWatermark(projection: ChatProjectedMessages): ChatCacheProjectionWatermark | undefined {
  const assistant = projection.assistantMessageId
    ? projection.messages.find((message) => message.id === projection.assistantMessageId)
    : [...projection.messages].reverse().find((message) => message.role === 'assistant')
  const eventVersion = Number.isSafeInteger(projection.eventVersion) && (projection.eventVersion ?? -1) >= 0 ? projection.eventVersion : undefined
  if (!assistant || (eventVersion === undefined && !isTerminalMessageStatus(assistant.status))) return undefined
  return {
    eventVersion,
    status: assistant.status,
    turnId: assistant.turnId,
    assistantMessageId: assistant.id
  }
}

function isTerminalMessageStatus(status: string): status is Extract<ChatMessageStatus, 'completed' | 'failed' | 'canceled'> {
  return status === 'completed' || status === 'failed' || status === 'canceled'
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
