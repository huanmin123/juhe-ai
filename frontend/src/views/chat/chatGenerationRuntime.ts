import { attachChatStream, chatApi, ChatStreamHttpError, ChatStreamProtocolError, streamChatMessage } from '@/api/domains/chat'
import type { ChatMessage, ChatReasoningEffort, ChatServiceTier, ChatStreamEvent } from '@/types/domain/chat'

import { applyChatStreamEvent } from './chatStream'

export type ChatGenerationRuntimeStatus = 'preparing' | 'running' | 'completed' | 'failed' | 'canceled'
export type ChatGenerationReconciliationReason = 'runner_terminal' | 'runner_missing' | 'http_error' | 'protocol_error' | 'reconnect_exhausted'

export interface ChatGenerationRuntimeError {
  name: string
  message: string
  status?: number
  code?: string
}

type DeepReadonly<T> = T extends (...args: any[]) => unknown
  ? T
  : T extends readonly (infer Item)[]
    ? readonly DeepReadonly<Item>[]
    : T extends object
      ? { readonly [Key in keyof T]: DeepReadonly<T[Key]> }
      : T

export interface RunningTurn {
  readonly systemAccountId: string
  readonly conversationId: string
  readonly clientMessageId: string
  readonly turnId?: string
  readonly assistantMessageId?: string
  readonly eventVersion: number
  readonly status: ChatGenerationRuntimeStatus
  readonly reconnectAttempt: number
  readonly projection: DeepReadonly<ChatMessage>
  readonly reconciliationReason?: ChatGenerationReconciliationReason
  readonly error?: Readonly<ChatGenerationRuntimeError>
}

export interface ChatGenerationRuntimeStartInput {
  systemAccountId: string
  conversationId: string
  clientMessageId: string
  replaceTurnId?: string
  content: string
  contentBlocks?: Array<{ type: 'input_text'; text: string } | { type: 'input_image'; assetId: string }>
  model: string
  reasoningEffort?: ChatReasoningEffort
  serviceTier?: ChatServiceTier
}

export interface ChatGenerationRuntimeAttachInput {
  systemAccountId: string
  conversationId: string
  clientMessageId?: string
  turnId: string
  assistantMessageId: string
  eventVersion?: number
  projection?: ChatMessage
}

interface RuntimeStreamInput {
  conversationId: string
  signal: AbortSignal
  onEvent: (event: ChatStreamEvent) => void
}

export interface ChatGenerationRuntimeDependencies {
  streamMessage(input: RuntimeStreamInput & Omit<ChatGenerationRuntimeStartInput, 'systemAccountId'>): Promise<void>
  attachStream(input: RuntimeStreamInput & { turnId: string }): Promise<void>
  stop(conversationId: string, target: { clientMessageId: string; turnId: string }): Promise<{ stopped: boolean }>
  schedule(callback: () => void, delayMs: number): unknown
  cancelSchedule(handle: unknown): void
  onReconcileRequired?(turn: RunningTurn): void
}

export interface ChatGenerationRuntimeOptions {
  reconnectDelaysMs?: readonly number[]
}

type TurnSubscriber = (turn: RunningTurn | undefined) => void

interface InternalTurn {
  systemAccountId: string
  conversationId: string
  clientMessageId: string
  turnId?: string
  assistantMessageId?: string
  eventVersion: number
  status: ChatGenerationRuntimeStatus
  controller: AbortController
  reconnectAttempt: number
  projection: ChatMessage
  reconciliationReason?: ChatGenerationReconciliationReason
  error?: ChatGenerationRuntimeError
  reconnectTimer?: unknown
  stopRequested: boolean
  connectionActive: boolean
  accepted: boolean
}

const defaultDependencies: ChatGenerationRuntimeDependencies = {
  streamMessage: streamChatMessage,
  attachStream: attachChatStream,
  stop: (conversationId, target) => chatApi.stop(conversationId, target),
  schedule: (callback, delayMs) => window.setTimeout(callback, delayMs),
  cancelSchedule: (handle) => window.clearTimeout(handle as number)
}

export class ChatGenerationRuntime {
  private readonly turns = new Map<string, InternalTurn>()
  private readonly subscribers = new Map<string, Set<TurnSubscriber>>()
  private readonly blockedConversations = new Set<string>()
  private readonly reconnectDelaysMs: readonly number[]
  private activeSystemAccountId?: string

  constructor(
    private readonly dependencies: ChatGenerationRuntimeDependencies = defaultDependencies,
    options: ChatGenerationRuntimeOptions = {}
  ) {
    this.reconnectDelaysMs = options.reconnectDelaysMs ?? [250, 500, 1000]
  }

  get(systemAccountId: string, conversationId: string): RunningTurn | undefined {
    return snapshotTurn(this.turns.get(runtimeKey(systemAccountId, conversationId)))
  }

  subscribe(systemAccountId: string, conversationId: string, subscriber: TurnSubscriber): () => void {
    const key = runtimeKey(systemAccountId, conversationId)
    const listeners = this.subscribers.get(key) ?? new Set<TurnSubscriber>()
    listeners.add(subscriber)
    this.subscribers.set(key, listeners)
    const turn = this.turns.get(key)
    if (!this.deliverSubscriber(key, listeners, subscriber, turn)) return () => undefined
    if (turn?.status === 'running' && turn.turnId && !turn.reconciliationReason && !turn.connectionActive && turn.reconnectTimer === undefined) {
      turn.reconnectAttempt = 0
      void this.runAttach(key, turn, false)
    }
    return () => {
      listeners.delete(subscriber)
      if (!listeners.size) this.subscribers.delete(key)
    }
  }

  start(input: ChatGenerationRuntimeStartInput): RunningTurn {
    this.assertAvailable(input.systemAccountId, input.conversationId)
    const key = runtimeKey(input.systemAccountId, input.conversationId)
    const existing = this.turns.get(key)
    if (existing && (existing.status === 'preparing' || existing.status === 'running')) return snapshotTurn(existing)!
    if (existing) this.releaseConnection(existing)

    const turn: InternalTurn = {
      systemAccountId: input.systemAccountId,
      conversationId: input.conversationId,
      clientMessageId: input.clientMessageId,
      eventVersion: -1,
      status: 'preparing',
      controller: new AbortController(),
      reconnectAttempt: 0,
      projection: emptyAssistantProjection(input.conversationId, input.model),
      stopRequested: false,
      connectionActive: false,
      accepted: false
    }
    this.turns.set(key, turn)
    this.notify(key)
    void this.runPost(key, turn, input)
    return snapshotTurn(turn)!
  }

  attach(input: ChatGenerationRuntimeAttachInput): RunningTurn {
    this.assertAvailable(input.systemAccountId, input.conversationId)
    const key = runtimeKey(input.systemAccountId, input.conversationId)
    const existing = this.turns.get(key)
    if (existing && existing.status === 'running' && existing.turnId === input.turnId) {
      if (!existing.connectionActive && existing.reconnectTimer === undefined) {
        existing.reconciliationReason = undefined
        existing.error = undefined
        existing.reconnectAttempt = 0
        void this.runAttach(key, existing, false)
      }
      return snapshotTurn(existing)!
    }
    if (existing) this.releaseConnection(existing)

    const turn: InternalTurn = {
      systemAccountId: input.systemAccountId,
      conversationId: input.conversationId,
      clientMessageId: input.clientMessageId ?? '',
      turnId: input.turnId,
      assistantMessageId: input.assistantMessageId,
      eventVersion: input.eventVersion ?? -1,
      status: 'running',
      controller: new AbortController(),
      reconnectAttempt: 0,
      projection: input.projection ? cloneJsonSafe(input.projection) : emptyAssistantProjection(input.conversationId, '', input.turnId, input.assistantMessageId),
      stopRequested: false,
      connectionActive: false,
      accepted: true
    }
    this.turns.set(key, turn)
    this.notify(key)
    void this.runAttach(key, turn)
    return snapshotTurn(turn)!
  }

  async stop(
    systemAccountId: string,
    conversationId: string,
    expected?: { clientMessageId: string; turnId: string }
  ): Promise<boolean> {
    const key = runtimeKey(systemAccountId, conversationId)
    const turn = this.turns.get(key)
    if (!turn || !turn.turnId || !turn.clientMessageId || turn.stopRequested) return false
    if (expected && (expected.clientMessageId !== turn.clientMessageId || expected.turnId !== turn.turnId)) return false
    turn.stopRequested = true
    const target = { clientMessageId: turn.clientMessageId, turnId: turn.turnId }
    this.releaseConnection(turn)
    try {
      await this.dependencies.stop(conversationId, target)
    } catch (error) {
      if (this.turns.get(key) === turn && !isTerminal(turn.status)) {
        turn.stopRequested = false
        turn.reconnectAttempt = 0
        void this.runAttach(key, turn, false)
      }
      throw error
    }
    if (this.turns.get(key) === turn && (turn.status === 'preparing' || turn.status === 'running')) {
      turn.status = 'canceled'
      turn.projection.status = 'canceled'
      this.notify(key)
    }
    return true
  }

  activateAccount(systemAccountId?: string): void {
    if (this.activeSystemAccountId === systemAccountId) return
    this.activeSystemAccountId = systemAccountId
    for (const [key, turn] of this.turns) {
      if (turn.systemAccountId === systemAccountId) continue
      this.releaseConnection(turn)
      this.turns.delete(key)
      this.subscribers.delete(key)
    }
    for (const key of this.blockedConversations) {
      if (!systemAccountId || !runtimeKeyBelongsTo(key, systemAccountId)) this.blockedConversations.delete(key)
    }
  }

  switchAccount(systemAccountId?: string): void {
    this.activateAccount(systemAccountId)
  }

  blockConversation(systemAccountId: string, conversationId: string): void {
    const key = runtimeKey(systemAccountId, conversationId)
    this.blockedConversations.add(key)
    const turn = this.turns.get(key)
    if (turn) this.releaseConnection(turn)
    this.turns.delete(key)
    this.notify(key)
  }

  allowConversation(systemAccountId: string, conversationId: string): void {
    this.blockedConversations.delete(runtimeKey(systemAccountId, conversationId))
  }

  forget(systemAccountId: string, conversationId: string, expectedTurnId?: string): boolean {
    const key = runtimeKey(systemAccountId, conversationId)
    const turn = this.turns.get(key)
    if (!turn || (expectedTurnId !== undefined && turn.turnId !== expectedTurnId)) return false
    this.releaseConnection(turn)
    this.turns.delete(key)
    this.notify(key)
    return true
  }

  close(systemAccountId?: string): void {
    for (const [key, turn] of this.turns) {
      if (systemAccountId !== undefined && turn.systemAccountId !== systemAccountId) continue
      this.releaseConnection(turn)
      this.turns.delete(key)
      this.subscribers.delete(key)
    }
    if (systemAccountId === undefined) this.blockedConversations.clear()
    else {
      for (const key of this.blockedConversations) {
        if (runtimeKeyBelongsTo(key, systemAccountId)) this.blockedConversations.delete(key)
      }
    }
    if (systemAccountId === undefined || this.activeSystemAccountId === systemAccountId) this.activeSystemAccountId = undefined
  }

  private async runPost(key: string, turn: InternalTurn, input: ChatGenerationRuntimeStartInput): Promise<void> {
    const controller = turn.controller
    turn.connectionActive = true
    let failure: unknown
    try {
      await this.dependencies.streamMessage({
        conversationId: input.conversationId,
        clientMessageId: input.clientMessageId,
        replaceTurnId: input.replaceTurnId,
        content: input.content,
        contentBlocks: input.contentBlocks,
        model: input.model,
        reasoningEffort: input.reasoningEffort,
        serviceTier: input.serviceTier,
        signal: controller.signal,
        onEvent: (event) => this.handleEvent(key, turn, event)
      })
    } catch (error) {
      failure = error
    } finally {
      if (turn.controller === controller) turn.connectionActive = false
    }
    this.handleConnectionEnd(key, turn, controller, failure)
  }

  private async runAttach(key: string, turn: InternalTurn, reconnect = true): Promise<void> {
    if (!turn.turnId || this.turns.get(key) !== turn || isTerminal(turn.status)) return
    turn.controller = new AbortController()
    const controller = turn.controller
    turn.connectionActive = true
    if (reconnect) turn.reconnectAttempt += 1
    let failure: unknown
    try {
      await this.dependencies.attachStream({
        conversationId: turn.conversationId,
        turnId: turn.turnId,
        signal: controller.signal,
        onEvent: (event) => this.handleEvent(key, turn, event)
      })
    } catch (error) {
      failure = error
    } finally {
      if (turn.controller === controller) turn.connectionActive = false
    }
    this.handleConnectionEnd(key, turn, controller, failure)
  }

  private handleEvent(key: string, turn: InternalTurn, event: ChatStreamEvent): void {
    if (this.turns.get(key) !== turn || isTerminal(turn.status)) return
    if (event.type === 'message.started') {
      if (turn.turnId && turn.turnId !== event.data.turnId) return
      turn.turnId = event.data.turnId
      turn.accepted = true
      turn.assistantMessageId = event.data.assistantMessage.id
      turn.projection = cloneJsonSafe(event.data.assistantMessage)
      turn.status = 'running'
      turn.reconciliationReason = undefined
      turn.error = undefined
      this.notify(key)
      return
    }
    if (!Number.isSafeInteger(event.data.eventVersion) || event.data.eventVersion < 0 || event.data.eventVersion <= turn.eventVersion) return
    if (event.type === 'message.snapshot') {
      if (turn.turnId && event.data.turnId !== turn.turnId) return
      if (turn.assistantMessageId && event.data.assistant.id !== turn.assistantMessageId) return
      turn.turnId = event.data.turnId
      turn.accepted = true
      turn.assistantMessageId = event.data.assistant.id
    } else if (turn.assistantMessageId && event.data.messageId !== turn.assistantMessageId) {
      return
    }
    applyChatStreamEvent([turn.projection], event)
    turn.eventVersion = event.data.eventVersion
    turn.reconciliationReason = undefined
    turn.error = undefined
    if (event.type === 'message.snapshot') turn.status = event.data.assistant.status === 'streaming' ? 'running' : event.data.assistant.status
    else if (event.type === 'message.completed') turn.status = 'completed'
    else if (event.type === 'message.failed') turn.status = 'failed'
    else if (event.type === 'message.canceled') turn.status = 'canceled'
    else turn.status = 'running'
    if (isTerminal(turn.status)) this.releaseConnection(turn)
    this.notify(key)
  }

  private handleConnectionEnd(key: string, turn: InternalTurn, controller: AbortController, failure: unknown): void {
    if (this.turns.get(key) !== turn || turn.controller !== controller || controller.signal.aborted || isTerminal(turn.status)) return
    const stableFailure = classifyStableFailure(failure)
    if (stableFailure) {
      if (!turn.accepted) {
        turn.status = 'failed'
        turn.projection.status = 'failed'
        turn.error = stableFailure.error
        this.notify(key)
        return
      }
      turn.reconciliationReason = stableFailure.reason
      turn.error = stableFailure.error
      this.notify(key)
      try {
        const snapshot = snapshotTurn(turn)
        if (snapshot) this.dependencies.onReconcileRequired?.(snapshot)
      } catch {
      }
      return
    }
    this.handleDisconnect(key, turn, controller)
  }

  private handleDisconnect(key: string, turn: InternalTurn, controller: AbortController): void {
    if (this.turns.get(key) !== turn || turn.controller !== controller || controller.signal.aborted || isTerminal(turn.status)) return
    if (!turn.turnId) {
      turn.status = 'failed'
      turn.projection.status = 'failed'
      this.notify(key)
      return
    }
    const delay = this.reconnectDelaysMs[turn.reconnectAttempt]
    if (delay === undefined) {
      this.requestReconciliation(key, turn, 'reconnect_exhausted', {
        name: 'ChatStreamReconnectExhaustedError',
        message: '生成连接重试已耗尽，正在同步服务端状态'
      })
      return
    }
    if (turn.reconnectTimer !== undefined) return
    turn.reconnectTimer = this.dependencies.schedule(() => {
      turn.reconnectTimer = undefined
      void this.runAttach(key, turn)
    }, delay)
  }

  private releaseConnection(turn: InternalTurn): void {
    turn.controller.abort()
    turn.connectionActive = false
    if (turn.reconnectTimer !== undefined) {
      this.dependencies.cancelSchedule(turn.reconnectTimer)
      turn.reconnectTimer = undefined
    }
  }

  private requestReconciliation(
    key: string,
    turn: InternalTurn,
    reason: ChatGenerationReconciliationReason,
    error: ChatGenerationRuntimeError
  ): void {
    if (this.turns.get(key) !== turn || isTerminal(turn.status) || turn.reconciliationReason === reason) return
    turn.reconciliationReason = reason
    turn.error = error
    this.notify(key)
    try {
      const snapshot = snapshotTurn(turn)
      if (snapshot) this.dependencies.onReconcileRequired?.(snapshot)
    } catch {
    }
  }

  private assertAvailable(systemAccountId: string, conversationId: string): void {
    if (this.activeSystemAccountId !== systemAccountId) throw new Error('chat generation runtime account inactive')
    if (this.blockedConversations.has(runtimeKey(systemAccountId, conversationId))) throw new Error('chat generation runtime conversation blocked')
  }

  private notify(key: string): void {
    const turn = this.turns.get(key)
    const listeners = this.subscribers.get(key)
    if (!listeners) return
    for (const subscriber of [...listeners]) this.deliverSubscriber(key, listeners, subscriber, turn)
  }

  private deliverSubscriber(key: string, listeners: Set<TurnSubscriber>, subscriber: TurnSubscriber, turn?: InternalTurn): boolean {
    try {
      subscriber(snapshotTurn(turn))
      return true
    } catch {
      listeners.delete(subscriber)
      if (!listeners.size) this.subscribers.delete(key)
      return false
    }
  }
}

export const chatGenerationRuntime = new ChatGenerationRuntime()

function runtimeKey(systemAccountId: string, conversationId: string): string {
  return JSON.stringify([systemAccountId, conversationId])
}

function runtimeKeyBelongsTo(key: string, systemAccountId: string): boolean {
  try {
    const parsed = JSON.parse(key) as unknown
    return Array.isArray(parsed) && parsed[0] === systemAccountId
  } catch {
    return false
  }
}

function isTerminal(status: ChatGenerationRuntimeStatus): boolean {
  return status === 'completed' || status === 'failed' || status === 'canceled'
}

function emptyAssistantProjection(conversationId: string, model: string, turnId = '', id = ''): ChatMessage {
  return {
    id,
    conversationId,
    turnId,
    sequenceNo: 0,
    role: 'assistant',
    status: 'streaming',
    contentText: '',
    contentBlocks: [],
    model,
    createdAt: '',
    expiresAt: ''
  }
}

function cloneJsonSafe<T>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T
}

function snapshotTurn(turn?: InternalTurn): RunningTurn | undefined {
  if (!turn) return undefined
  return {
    systemAccountId: turn.systemAccountId,
    conversationId: turn.conversationId,
    clientMessageId: turn.clientMessageId,
    turnId: turn.turnId,
    assistantMessageId: turn.assistantMessageId,
    eventVersion: turn.eventVersion,
    status: turn.status,
    reconnectAttempt: turn.reconnectAttempt,
    projection: cloneJsonSafe(turn.projection),
    reconciliationReason: turn.reconciliationReason,
    error: turn.error ? { ...turn.error } : undefined
  }
}

function classifyStableFailure(failure: unknown): { reason: ChatGenerationReconciliationReason; error: ChatGenerationRuntimeError } | undefined {
  if (failure instanceof ChatStreamProtocolError) {
    return { reason: 'protocol_error', error: { name: failure.name, message: failure.message } }
  }
  if (!(failure instanceof ChatStreamHttpError) || failure.status >= 500) return undefined
  const reason = failure.code === 'chat_stream_terminal'
    ? 'runner_terminal'
    : failure.code === 'chat_stream_runner_missing'
      ? 'runner_missing'
      : 'http_error'
  return {
    reason,
    error: { name: failure.name, message: failure.message, status: failure.status, code: failure.code }
  }
}
