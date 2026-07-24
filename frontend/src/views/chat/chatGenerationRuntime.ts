import { attachChatStream, chatApi, ChatStreamHttpError, ChatStreamProtocolError, streamChatMessage } from '@/api/domains/chat'
import type { ChatMessage, ChatReasoningEffort, ChatServiceTier, ChatStreamEvent, ChatSubmissionStatus } from '@/types/domain/chat'

import { applyChatStreamEvent } from './chatStream'

export type ChatGenerationRuntimeStatus = 'preparing' | 'running' | 'completed' | 'failed' | 'canceled'
export type ChatGenerationReconciliationReason = 'runner_terminal' | 'runner_missing' | 'http_error' | 'protocol_error' | 'reconnect_exhausted'
export type ChatGenerationLivenessState = 'active' | 'checking' | 'reconnecting'

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
  readonly userProjection?: DeepReadonly<ChatMessage>
  readonly startedAt: number
  readonly lastTransportActivityAt: number
  readonly lastSemanticActivityAt: number
  readonly livenessState: ChatGenerationLivenessState
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
  onActivity: () => void
  onEvent: (event: ChatStreamEvent) => void
}

export interface ChatGenerationRuntimeDependencies {
  streamMessage(input: RuntimeStreamInput & Omit<ChatGenerationRuntimeStartInput, 'systemAccountId'>): Promise<void>
  attachStream(input: RuntimeStreamInput & { turnId: string }): Promise<void>
  stop(conversationId: string, target: { clientMessageId: string; turnId?: string }): Promise<{ stopped: boolean }>
  schedule(callback: () => void, delayMs: number): unknown
  cancelSchedule(handle: unknown): void
  getSubmissionStatus?(conversationId: string, clientMessageId: string): Promise<ChatSubmissionStatus>
  scheduleWatchdog?(callback: () => void, delayMs: number): unknown
  cancelWatchdog?(handle: unknown): void
  onReconcileRequired?(turn: RunningTurn): void
}

export interface ChatGenerationRuntimeOptions {
  reconnectDelaysMs?: readonly number[]
  terminalProjectionLimit?: number
  terminalProjectionTtlMs?: number
  now?: () => number
  staleAfterMs?: number
  watchdogMaxChecks?: number
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
  userProjection?: ChatMessage
  startedAt: number
  lastTransportActivityAt: number
  lastSemanticActivityAt: number
  lastLivenessCheckAt: number
  livenessState: ChatGenerationLivenessState
  projection: ChatMessage
  reconciliationReason?: ChatGenerationReconciliationReason
  error?: ChatGenerationRuntimeError
  reconnectTimer?: unknown
  watchdogTimer?: unknown
  watchdogChecking: boolean
  watchdogCheckCount: number
  watchdogLookupFailureCount: number
  watchdogNotFoundCount: number
  watchdogFirstNotFoundAt?: number
  watchdogExhausted: boolean
  stopRequested: boolean
  connectionActive: boolean
  accepted: boolean
  terminalAt?: number
  lastAccessAt: number
  terminalTimer?: unknown
}

const defaultDependencies: ChatGenerationRuntimeDependencies = {
  streamMessage: streamChatMessage,
  attachStream: attachChatStream,
  stop: (conversationId, target) => chatApi.stop(conversationId, target),
  schedule: (callback, delayMs) => window.setTimeout(callback, delayMs),
  cancelSchedule: (handle) => window.clearTimeout(handle as number),
  getSubmissionStatus: (conversationId, clientMessageId) => chatApi.getSubmissionStatus(conversationId, clientMessageId),
  scheduleWatchdog: (callback, delayMs) => window.setTimeout(callback, delayMs),
  cancelWatchdog: (handle) => window.clearTimeout(handle as number)
}

export class ChatGenerationRuntime {
  private readonly turns = new Map<string, InternalTurn>()
  private readonly subscribers = new Map<string, Set<TurnSubscriber>>()
  private readonly blockedConversations = new Set<string>()
  private readonly reconnectDelaysMs: readonly number[]
  private readonly terminalProjectionLimit: number
  private readonly terminalProjectionTtlMs: number
  private readonly now: () => number
  private readonly staleAfterMs: number
  private readonly watchdogMaxChecks: number
  private activeSystemAccountId?: string

  constructor(
    private readonly dependencies: ChatGenerationRuntimeDependencies = defaultDependencies,
    options: ChatGenerationRuntimeOptions = {}
  ) {
    this.reconnectDelaysMs = options.reconnectDelaysMs ?? [250, 500, 1000]
    this.terminalProjectionLimit = Math.max(1, Math.min(128, Math.floor(options.terminalProjectionLimit ?? 32)))
    this.terminalProjectionTtlMs = Math.max(1_000, Math.min(86_400_000, Math.floor(options.terminalProjectionTtlMs ?? 5 * 60_000)))
    this.now = options.now ?? Date.now
    this.staleAfterMs = Math.max(1_000, Math.min(60_000, Math.floor(options.staleAfterMs ?? 10_000)))
    this.watchdogMaxChecks = Math.max(1, Math.min(10_000, Math.floor(options.watchdogMaxChecks ?? 180)))
  }

  get(systemAccountId: string, conversationId: string): RunningTurn | undefined {
    this.pruneTerminalProjections()
    const turn = this.turns.get(runtimeKey(systemAccountId, conversationId))
    if (turn) turn.lastAccessAt = this.now()
    return snapshotTurn(turn)
  }

  subscribe(systemAccountId: string, conversationId: string, subscriber: TurnSubscriber): () => void {
    this.pruneTerminalProjections()
    const key = runtimeKey(systemAccountId, conversationId)
    const listeners = this.subscribers.get(key) ?? new Set<TurnSubscriber>()
    listeners.add(subscriber)
    this.subscribers.set(key, listeners)
    const turn = this.turns.get(key)
    if (turn) turn.lastAccessAt = this.now()
    if (!this.deliverSubscriber(key, listeners, subscriber, turn)) return () => undefined
    if (turn?.status === 'running' && turn.turnId && !turn.reconciliationReason && !turn.connectionActive && turn.reconnectTimer === undefined) {
      turn.reconnectAttempt = 0
      void this.runAttach(key, turn, false)
    }
    return () => {
      listeners.delete(subscriber)
      if (!listeners.size) this.subscribers.delete(key)
      const current = this.turns.get(key)
      if (current && isTerminal(current.status)) this.scheduleTerminalEviction(current)
    }
  }

  start(input: ChatGenerationRuntimeStartInput): RunningTurn {
    this.assertAvailable(input.systemAccountId, input.conversationId)
    const key = runtimeKey(input.systemAccountId, input.conversationId)
    const existing = this.turns.get(key)
    if (existing && (existing.status === 'preparing' || existing.status === 'running')) return snapshotTurn(existing)!
    if (existing) this.releaseConnection(existing)

    const startedAt = this.now()
    const turn: InternalTurn = {
      systemAccountId: input.systemAccountId,
      conversationId: input.conversationId,
      clientMessageId: input.clientMessageId,
      eventVersion: -1,
      status: 'preparing',
      controller: new AbortController(),
      reconnectAttempt: 0,
      userProjection: optimisticUserProjection(input),
      startedAt,
      lastTransportActivityAt: startedAt,
      lastSemanticActivityAt: startedAt,
      lastLivenessCheckAt: startedAt,
      livenessState: 'active',
      projection: emptyAssistantProjection(input.conversationId, input.model, `optimistic-turn:${input.clientMessageId}`, `optimistic-assistant:${input.clientMessageId}`, input.clientMessageId),
      stopRequested: false,
      connectionActive: false,
      accepted: false,
      lastAccessAt: startedAt,
      watchdogChecking: false,
      watchdogCheckCount: 0,
      watchdogLookupFailureCount: 0,
      watchdogNotFoundCount: 0,
      watchdogExhausted: false
    }
    this.turns.set(key, turn)
    this.scheduleWatchdog(key, turn)
    this.notify(key)
    void this.runPost(key, turn, input)
    return snapshotTurn(turn)!
  }

  attach(input: ChatGenerationRuntimeAttachInput): RunningTurn {
    this.assertAvailable(input.systemAccountId, input.conversationId)
    const key = runtimeKey(input.systemAccountId, input.conversationId)
    const existing = this.turns.get(key)
    if (existing && isTerminal(existing.status) && existing.turnId === input.turnId) return snapshotTurn(existing)!
    if (existing && existing.status === 'running' && existing.turnId === input.turnId) {
      existing.lastAccessAt = this.now()
      const authoritativeProgress = hasAuthoritativeServerProgress(existing, input)
      if ((existing.reconciliationReason === 'reconnect_exhausted' || existing.watchdogExhausted) && !authoritativeProgress) return snapshotTurn(existing)!
      if (existing.watchdogExhausted) {
        existing.watchdogCheckCount = 0
        this.resetWatchdogFailures(existing)
      }
      this.applyAttachProgress(existing, input)
      if (isTerminal(existing.status)) {
        this.markTerminal(existing, existing.status)
        this.notify(key)
        return snapshotTurn(existing)!
      }
      if (!existing.connectionActive && existing.reconnectTimer === undefined) {
        existing.reconciliationReason = undefined
        existing.error = undefined
        existing.reconnectAttempt = 0
        void this.runAttach(key, existing, false)
      }
      this.scheduleWatchdog(key, existing)
      return snapshotTurn(existing)!
    }
    if (existing) this.releaseConnection(existing)

    const projectedStatus = input.projection?.status
    const initialStatus: ChatGenerationRuntimeStatus = projectedStatus === 'completed' || projectedStatus === 'failed' || projectedStatus === 'canceled' ? projectedStatus : 'running'
    const startedAt = this.now()
    const turn: InternalTurn = {
      systemAccountId: input.systemAccountId,
      conversationId: input.conversationId,
      clientMessageId: input.clientMessageId ?? '',
      turnId: input.turnId,
      assistantMessageId: input.assistantMessageId,
      eventVersion: input.eventVersion ?? -1,
      status: initialStatus,
      controller: new AbortController(),
      reconnectAttempt: 0,
      startedAt,
      lastTransportActivityAt: startedAt,
      lastSemanticActivityAt: startedAt,
      lastLivenessCheckAt: startedAt,
      livenessState: 'active',
      projection: input.projection ? cloneJsonSafe(input.projection) : emptyAssistantProjection(input.conversationId, '', input.turnId, input.assistantMessageId),
      stopRequested: false,
      connectionActive: false,
      accepted: true,
      lastAccessAt: startedAt,
      watchdogChecking: false,
      watchdogCheckCount: 0,
      watchdogLookupFailureCount: 0,
      watchdogNotFoundCount: 0,
      watchdogExhausted: false
    }
    this.turns.set(key, turn)
    if (isTerminal(initialStatus)) this.markTerminal(turn, initialStatus)
    else this.scheduleWatchdog(key, turn)
    this.notify(key)
    if (!isTerminal(initialStatus)) void this.runAttach(key, turn)
    return snapshotTurn(turn)!
  }

  async stop(
    systemAccountId: string,
    conversationId: string,
    expected?: { clientMessageId: string; turnId?: string }
  ): Promise<boolean> {
    const key = runtimeKey(systemAccountId, conversationId)
    const turn = this.turns.get(key)
    if (!turn || !turn.clientMessageId || turn.stopRequested) return false
    if (expected && (expected.clientMessageId !== turn.clientMessageId || (expected.turnId !== undefined && expected.turnId !== turn.turnId))) return false
    turn.stopRequested = true
    const stoppingBeforeAcceptance = !turn.turnId
    const target = {
      clientMessageId: turn.clientMessageId,
      ...(turn.turnId ? { turnId: turn.turnId } : {})
    }
    this.releaseConnection(turn)
    try {
      await this.dependencies.stop(conversationId, target)
    } catch (error) {
      if (stoppingBeforeAcceptance && error instanceof ChatStreamHttpError && error.status === 404 && error.code === 'chat_generation_not_found') {
        if (this.turns.get(key) === turn && !isTerminal(turn.status)) {
          this.markTerminal(turn, 'canceled')
          this.notify(key)
        }
        return true
      }
      if (this.turns.get(key) === turn && !isTerminal(turn.status)) {
        turn.stopRequested = false
        turn.reconnectAttempt = 0
        void this.runAttach(key, turn, false)
      }
      throw error
    }
    if (this.turns.get(key) === turn && (turn.status === 'preparing' || turn.status === 'running')) {
      this.markTerminal(turn, 'canceled')
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

  isConversationBlocked(systemAccountId: string, conversationId: string): boolean {
    return this.blockedConversations.has(runtimeKey(systemAccountId, conversationId))
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
        onActivity: () => this.handleTransportActivity(key, turn),
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
    if (reconnect) {
      turn.reconnectAttempt += 1
      turn.livenessState = 'reconnecting'
      this.notify(key)
    }
    this.scheduleWatchdog(key, turn)
    let failure: unknown
    try {
      await this.dependencies.attachStream({
        conversationId: turn.conversationId,
        turnId: turn.turnId,
        signal: controller.signal,
        onActivity: () => this.handleTransportActivity(key, turn),
        onEvent: (event) => this.handleEvent(key, turn, event)
      })
    } catch (error) {
      failure = error
    } finally {
      if (turn.controller === controller) turn.connectionActive = false
    }
    this.handleConnectionEnd(key, turn, controller, failure)
  }

  private handleTransportActivity(key: string, turn: InternalTurn): void {
    if (this.turns.get(key) !== turn || isTerminal(turn.status)) return
    const changed = turn.livenessState !== 'active'
    turn.lastTransportActivityAt = this.now()
    turn.livenessState = 'active'
    this.resetWatchdogFailures(turn)
    this.scheduleWatchdog(key, turn)
    if (changed) this.notify(key)
  }

  private markSemanticActivity(key: string, turn: InternalTurn): void {
    const now = this.now()
    turn.lastTransportActivityAt = now
    turn.lastSemanticActivityAt = now
    turn.livenessState = 'active'
    this.resetWatchdogFailures(turn)
    this.scheduleWatchdog(key, turn)
  }

  private handleEvent(key: string, turn: InternalTurn, event: ChatStreamEvent): void {
    if (this.turns.get(key) !== turn || isTerminal(turn.status)) return
    if (event.type === 'message.started') {
      if (turn.turnId && turn.turnId !== event.data.turnId) return
      this.markSemanticActivity(key, turn)
      turn.turnId = event.data.turnId
      turn.accepted = true
      turn.assistantMessageId = event.data.assistantMessage.id
      turn.userProjection = cloneJsonSafe(event.data.userMessage)
      turn.projection = cloneJsonSafe(event.data.assistantMessage)
      turn.status = 'running'
      turn.reconciliationReason = undefined
      turn.error = undefined
      this.notify(key)
      return
    }
    if (!Number.isSafeInteger(event.data.eventVersion) || event.data.eventVersion < 0) return
    if (event.type === 'message.snapshot' ? event.data.eventVersion < turn.eventVersion : event.data.eventVersion <= turn.eventVersion) return
    if (event.type === 'message.snapshot') {
      if (turn.turnId && event.data.turnId !== turn.turnId) return
      if (turn.assistantMessageId && event.data.assistant.id !== turn.assistantMessageId) return
      turn.turnId = event.data.turnId
      turn.accepted = true
      turn.assistantMessageId = event.data.assistant.id
    } else if (turn.assistantMessageId && event.data.messageId !== turn.assistantMessageId) {
      return
    }
    this.markSemanticActivity(key, turn)
    applyChatStreamEvent([turn.projection], event)
    turn.eventVersion = event.data.eventVersion
    turn.reconciliationReason = undefined
    turn.error = undefined
    if (event.type === 'message.snapshot') turn.status = event.data.assistant.status === 'streaming' ? 'running' : event.data.assistant.status
    else if (event.type === 'message.completed') turn.status = 'completed'
    else if (event.type === 'message.failed') turn.status = 'failed'
    else if (event.type === 'message.canceled') turn.status = 'canceled'
    else turn.status = 'running'
    if (isTerminal(turn.status)) {
      turn.terminalAt = this.now()
      this.releaseConnection(turn)
      this.scheduleTerminalEviction(turn)
      this.pruneTerminalProjections()
    }
    this.notify(key)
  }

  private handleConnectionEnd(key: string, turn: InternalTurn, controller: AbortController, failure: unknown): void {
    if (this.turns.get(key) !== turn || turn.controller !== controller || controller.signal.aborted || isTerminal(turn.status)) return
    const stableFailure = classifyStableFailure(failure)
    if (stableFailure) {
      if (!turn.accepted) {
        this.markTerminal(turn, 'failed')
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
      this.markTerminal(turn, 'failed')
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
    turn.livenessState = 'reconnecting'
    this.notify(key)
    turn.reconnectTimer = this.dependencies.schedule(() => {
      turn.reconnectTimer = undefined
      void this.runAttach(key, turn)
    }, delay)
  }

  private scheduleWatchdog(key: string, turn: InternalTurn): void {
    const schedule = this.dependencies.scheduleWatchdog
    const cancel = this.dependencies.cancelWatchdog
    if (!schedule || !cancel || !this.dependencies.getSubmissionStatus || isTerminal(turn.status) || turn.watchdogExhausted) return
    if (turn.watchdogTimer !== undefined) cancel(turn.watchdogTimer)
    const baseline = Math.max(turn.lastTransportActivityAt, turn.lastLivenessCheckAt)
    const delay = Math.max(1, baseline + this.staleAfterMs - this.now())
    turn.watchdogTimer = schedule(() => {
      turn.watchdogTimer = undefined
      void this.checkLiveness(key, turn)
    }, delay)
  }

  private async checkLiveness(key: string, turn: InternalTurn): Promise<void> {
    const getSubmissionStatus = this.dependencies.getSubmissionStatus
    if (!getSubmissionStatus || this.turns.get(key) !== turn || isTerminal(turn.status) || turn.watchdogChecking) return
    const baseline = Math.max(turn.lastTransportActivityAt, turn.lastLivenessCheckAt)
    if (this.now() - baseline < this.staleAfterMs) { this.scheduleWatchdog(key, turn); return }
    if (!turn.clientMessageId) { this.scheduleWatchdog(key, turn); return }
    turn.watchdogChecking = true
    turn.watchdogCheckCount += 1
    turn.lastLivenessCheckAt = this.now()
    turn.livenessState = 'checking'
    this.notify(key)
    let reattaching = false
    try {
      const status = await getSubmissionStatus(turn.conversationId, turn.clientMessageId)
      if (this.turns.get(key) !== turn || isTerminal(turn.status)) return
      turn.lastLivenessCheckAt = this.now()
      turn.watchdogLookupFailureCount = 0
      if (status.state === 'preparing') {
        turn.watchdogNotFoundCount = 0
        turn.watchdogFirstNotFoundAt = undefined
        if (turn.watchdogCheckCount >= this.watchdogMaxChecks) this.exhaustWatchdog(key, turn)
        return
      }
      if (status.state === 'not_found') {
        turn.watchdogNotFoundCount += 1
        turn.watchdogFirstNotFoundAt ??= this.now()
        if (turn.watchdogNotFoundCount >= 3 && this.now() - turn.watchdogFirstNotFoundAt >= 1_000) {
          turn.reconciliationReason = 'http_error'
          turn.error = { name: 'ChatSubmissionNotFoundError', message: '发送请求未被服务端接受' }
          this.markTerminal(turn, 'failed')
          this.notify(key)
        } else if (turn.watchdogCheckCount >= this.watchdogMaxChecks) {
          this.exhaustWatchdog(key, turn)
        }
        return
      }
      turn.watchdogNotFoundCount = 0
      turn.watchdogFirstNotFoundAt = undefined
      if (turn.turnId && status.turnId !== turn.turnId) {
        this.requestReconciliation(key, turn, 'http_error', {
          name: 'ChatSubmissionIdentityChangedError',
          message: '生成状态已变化，正在同步服务端消息'
        })
        return
      }
      turn.turnId = status.turnId
      turn.assistantMessageId = status.assistantMessageId
      if (status.traceId) turn.projection.traceId = status.traceId
      turn.accepted = true
      if (status.assistantStatus !== 'streaming' || status.runnerState === 'terminal') {
        this.requestReconciliation(key, turn, 'runner_terminal', {
          name: 'ChatRunnerTerminalError',
          message: status.errorMessage || '生成已结束，正在同步服务端消息'
        })
        return
      }
      if (turn.watchdogCheckCount >= this.watchdogMaxChecks) {
        this.exhaustWatchdog(key, turn)
        return
      }
      reattaching = true
      turn.livenessState = 'reconnecting'
      turn.reconciliationReason = undefined
      turn.error = undefined
      turn.reconnectAttempt = 0
      this.notify(key)
      this.releaseConnection(turn)
      turn.lastLivenessCheckAt = this.now()
      void this.runAttach(key, turn, false)
    } catch {
      if (this.turns.get(key) === turn && !isTerminal(turn.status)) {
        turn.lastLivenessCheckAt = this.now()
        turn.watchdogLookupFailureCount += 1
        if (turn.watchdogLookupFailureCount >= 5) {
          turn.watchdogExhausted = true
          this.requestReconciliation(key, turn, 'http_error', {
            name: 'ChatSubmissionStatusUnavailableError',
            message: '生成状态暂时无法确认，请稍后重新进入会话'
          })
        } else if (turn.watchdogCheckCount >= this.watchdogMaxChecks) {
          this.exhaustWatchdog(key, turn)
        }
      }
    } finally {
      turn.watchdogChecking = false
      if (!reattaching && this.turns.get(key) === turn && !isTerminal(turn.status)) this.scheduleWatchdog(key, turn)
    }
  }

  private resetWatchdogFailures(turn: InternalTurn): void {
    turn.watchdogLookupFailureCount = 0
    turn.watchdogNotFoundCount = 0
    turn.watchdogFirstNotFoundAt = undefined
    turn.watchdogExhausted = false
  }

  private exhaustWatchdog(key: string, turn: InternalTurn): void {
    if (this.turns.get(key) !== turn || isTerminal(turn.status) || turn.watchdogExhausted) return
    turn.watchdogExhausted = true
    if (!turn.accepted || !turn.turnId) {
      turn.reconciliationReason = 'http_error'
      turn.error = {
        name: 'ChatSubmissionConfirmationTimeoutError',
        code: 'chat_submission_confirmation_timeout',
        message: '长时间无法确认消息是否已被服务端接受，请重新发送'
      }
      this.markTerminal(turn, 'failed')
      this.notify(key)
      return
    }
    this.releaseConnection(turn)
    this.requestReconciliation(key, turn, 'http_error', {
      name: 'ChatSubmissionStatusCheckLimitError',
      code: 'chat_submission_status_check_limit',
      message: '已停止自动确认生成状态，请稍后重新进入会话或手动停止'
    })
  }

  private scheduleTerminalEviction(turn: InternalTurn): void {
    if (turn.terminalAt === undefined || turn.terminalTimer !== undefined) return
    const delay = Math.max(1, turn.terminalAt + this.terminalProjectionTtlMs - this.now())
    turn.terminalTimer = this.dependencies.schedule(() => {
      turn.terminalTimer = undefined
      this.pruneTerminalProjections()
      const key = runtimeKey(turn.systemAccountId, turn.conversationId)
      if (this.turns.get(key) === turn && isTerminal(turn.status) && !this.subscribers.get(key)?.size) this.scheduleTerminalEviction(turn)
    }, delay)
  }

  private markTerminal(turn: InternalTurn, status: Extract<ChatGenerationRuntimeStatus, 'completed' | 'failed' | 'canceled'>): void {
    turn.status = status
    turn.projection.status = status
    turn.terminalAt ??= this.now()
    this.releaseConnection(turn)
    this.scheduleTerminalEviction(turn)
    this.pruneTerminalProjections()
  }

  private applyAttachProgress(turn: InternalTurn, input: ChatGenerationRuntimeAttachInput): void {
    if (input.eventVersion !== undefined && input.eventVersion > turn.eventVersion) turn.eventVersion = input.eventVersion
    if (input.projection) {
      turn.projection = cloneJsonSafe(input.projection)
      turn.assistantMessageId = input.assistantMessageId
    }
    const status = input.projection?.status
    if (status === 'completed' || status === 'failed' || status === 'canceled') turn.status = status
  }

  private pruneTerminalProjections(): void {
    const now = this.now()
    for (const [key, turn] of this.turns) {
      if (!isTerminal(turn.status) || turn.terminalAt === undefined || now - turn.terminalAt < this.terminalProjectionTtlMs || this.subscribers.get(key)?.size) continue
      this.releaseConnection(turn)
      this.turns.delete(key)
    }
    const remaining = [...this.turns.entries()]
      .filter(([, turn]) => isTerminal(turn.status))
      .sort(([, left], [, right]) => left.lastAccessAt - right.lastAccessAt)
    while (remaining.length > this.terminalProjectionLimit) {
      const candidateIndex = remaining.findIndex(([key]) => !this.subscribers.get(key)?.size)
      if (candidateIndex < 0) break
      const [key, turn] = remaining.splice(candidateIndex, 1)[0]!
      this.releaseConnection(turn)
      this.turns.delete(key)
    }
  }

  private releaseConnection(turn: InternalTurn): void {
    turn.controller.abort()
    turn.connectionActive = false
    if (turn.reconnectTimer !== undefined) {
      this.dependencies.cancelSchedule(turn.reconnectTimer)
      turn.reconnectTimer = undefined
    }
    if (turn.terminalTimer !== undefined) {
      this.dependencies.cancelSchedule(turn.terminalTimer)
      turn.terminalTimer = undefined
    }
    if (turn.watchdogTimer !== undefined) {
      this.dependencies.cancelWatchdog?.(turn.watchdogTimer)
      turn.watchdogTimer = undefined
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
    turn.livenessState = 'checking'
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
    this.pruneTerminalProjections()
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

function isTerminal(status: ChatGenerationRuntimeStatus): status is Extract<ChatGenerationRuntimeStatus, 'completed' | 'failed' | 'canceled'> {
  return status === 'completed' || status === 'failed' || status === 'canceled'
}

function emptyAssistantProjection(conversationId: string, model: string, turnId = '', id = '', clientMessageId?: string): ChatMessage {
  return {
    id,
    conversationId,
    turnId,
    sequenceNo: 0,
    ...(clientMessageId ? { clientMessageId } : {}),
    role: 'assistant',
    status: 'streaming',
    contentText: '',
    contentBlocks: [],
    model,
    createdAt: '',
    expiresAt: ''
  }
}

function optimisticUserProjection(input: ChatGenerationRuntimeStartInput): ChatMessage {
  const contentBlocks = (input.contentBlocks?.length ? input.contentBlocks : [{ type: 'input_text' as const, text: input.content }])
    .map((block, order) => ({ ...block, order }))
  return {
    id: '',
    conversationId: input.conversationId,
    turnId: '',
    sequenceNo: 0,
    clientMessageId: input.clientMessageId,
    role: 'user',
    status: 'completed',
    contentText: input.content,
    contentBlocks,
    model: input.model,
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
    userProjection: turn.userProjection ? cloneJsonSafe(turn.userProjection) : undefined,
    startedAt: turn.startedAt,
    lastTransportActivityAt: turn.lastTransportActivityAt,
    lastSemanticActivityAt: turn.lastSemanticActivityAt,
    livenessState: turn.livenessState,
    projection: cloneJsonSafe(turn.projection),
    reconciliationReason: turn.reconciliationReason,
    error: turn.error ? { ...turn.error } : undefined
  }
}

function hasAuthoritativeServerProgress(turn: InternalTurn, input: ChatGenerationRuntimeAttachInput): boolean {
  if (input.eventVersion !== undefined && input.eventVersion > turn.eventVersion) return true
  return input.projection?.status === 'completed' || input.projection?.status === 'failed' || input.projection?.status === 'canceled'
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
