import { attachChatStream, chatApi, streamChatMessage } from '@/api/domains/chat'
import type { ChatMessage, ChatReasoningEffort, ChatServiceTier, ChatStreamEvent } from '@/types/domain/chat'

import { applyChatStreamEvent } from './chatStream'

export type ChatGenerationRuntimeStatus = 'preparing' | 'running' | 'completed' | 'failed' | 'canceled'

export interface RunningTurn {
  readonly systemAccountId: string
  readonly conversationId: string
  readonly clientMessageId: string
  turnId?: string
  assistantMessageId?: string
  eventVersion: number
  status: ChatGenerationRuntimeStatus
  controller: AbortController
  reconnectAttempt: number
  projection: ChatMessage
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
}

export interface ChatGenerationRuntimeOptions {
  reconnectDelaysMs?: readonly number[]
}

type TurnSubscriber = (turn: RunningTurn | undefined) => void

interface InternalTurn extends RunningTurn {
  reconnectTimer?: unknown
  stopRequested: boolean
  connectionActive: boolean
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
  private readonly reconnectDelaysMs: readonly number[]
  private activeSystemAccountId?: string

  constructor(
    private readonly dependencies: ChatGenerationRuntimeDependencies = defaultDependencies,
    options: ChatGenerationRuntimeOptions = {}
  ) {
    this.reconnectDelaysMs = options.reconnectDelaysMs ?? [250, 500, 1000]
  }

  get(systemAccountId: string, conversationId: string): RunningTurn | undefined {
    return this.turns.get(runtimeKey(systemAccountId, conversationId))
  }

  subscribe(systemAccountId: string, conversationId: string, subscriber: TurnSubscriber): () => void {
    const key = runtimeKey(systemAccountId, conversationId)
    const listeners = this.subscribers.get(key) ?? new Set<TurnSubscriber>()
    listeners.add(subscriber)
    this.subscribers.set(key, listeners)
    const turn = this.turns.get(key)
    subscriber(turn)
    if (turn?.status === 'running' && turn.turnId && !turn.connectionActive && turn.reconnectTimer === undefined) {
      turn.reconnectAttempt = 0
      void this.runAttach(key, turn, false)
    }
    return () => {
      listeners.delete(subscriber)
      if (!listeners.size) this.subscribers.delete(key)
    }
  }

  start(input: ChatGenerationRuntimeStartInput): RunningTurn {
    const key = runtimeKey(input.systemAccountId, input.conversationId)
    const existing = this.turns.get(key)
    if (existing && (existing.status === 'preparing' || existing.status === 'running')) return existing
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
      connectionActive: false
    }
    this.turns.set(key, turn)
    this.notify(key)
    void this.runPost(key, turn, input)
    return turn
  }

  attach(input: ChatGenerationRuntimeAttachInput): RunningTurn {
    const key = runtimeKey(input.systemAccountId, input.conversationId)
    const existing = this.turns.get(key)
    if (existing && existing.status === 'running' && existing.turnId === input.turnId) {
      if (!existing.connectionActive && existing.reconnectTimer === undefined) {
        existing.reconnectAttempt = 0
        void this.runAttach(key, existing, false)
      }
      return existing
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
      connectionActive: false
    }
    this.turns.set(key, turn)
    this.notify(key)
    void this.runAttach(key, turn)
    return turn
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
  }

  switchAccount(systemAccountId?: string): void {
    this.activateAccount(systemAccountId)
  }

  close(systemAccountId?: string): void {
    for (const [key, turn] of this.turns) {
      if (systemAccountId !== undefined && turn.systemAccountId !== systemAccountId) continue
      this.releaseConnection(turn)
      this.turns.delete(key)
      this.subscribers.delete(key)
    }
    if (systemAccountId === undefined || this.activeSystemAccountId === systemAccountId) this.activeSystemAccountId = undefined
  }

  private async runPost(key: string, turn: InternalTurn, input: ChatGenerationRuntimeStartInput): Promise<void> {
    const controller = turn.controller
    turn.connectionActive = true
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
    } catch {
    } finally {
      if (turn.controller === controller) turn.connectionActive = false
    }
    this.handleDisconnect(key, turn, controller)
  }

  private async runAttach(key: string, turn: InternalTurn, reconnect = true): Promise<void> {
    if (!turn.turnId || this.turns.get(key) !== turn || isTerminal(turn.status)) return
    turn.controller = new AbortController()
    const controller = turn.controller
    turn.connectionActive = true
    if (reconnect) turn.reconnectAttempt += 1
    try {
      await this.dependencies.attachStream({
        conversationId: turn.conversationId,
        turnId: turn.turnId,
        signal: controller.signal,
        onEvent: (event) => this.handleEvent(key, turn, event)
      })
    } catch {
    } finally {
      if (turn.controller === controller) turn.connectionActive = false
    }
    this.handleDisconnect(key, turn, controller)
  }

  private handleEvent(key: string, turn: InternalTurn, event: ChatStreamEvent): void {
    if (this.turns.get(key) !== turn || isTerminal(turn.status)) return
    if (event.type === 'message.started') {
      if (turn.turnId && turn.turnId !== event.data.turnId) return
      turn.turnId = event.data.turnId
      turn.assistantMessageId = event.data.assistantMessage.id
      turn.projection = cloneJsonSafe(event.data.assistantMessage)
      turn.status = 'running'
      this.notify(key)
      return
    }
    if (!Number.isSafeInteger(event.data.eventVersion) || event.data.eventVersion < 0 || event.data.eventVersion <= turn.eventVersion) return
    if (event.type === 'message.snapshot') {
      if (turn.turnId && event.data.turnId !== turn.turnId) return
      if (turn.assistantMessageId && event.data.assistant.id !== turn.assistantMessageId) return
      turn.turnId = event.data.turnId
      turn.assistantMessageId = event.data.assistant.id
    } else if (turn.assistantMessageId && event.data.messageId !== turn.assistantMessageId) {
      return
    }
    applyChatStreamEvent([turn.projection], event)
    turn.eventVersion = event.data.eventVersion
    if (event.type === 'message.snapshot') turn.status = event.data.assistant.status === 'streaming' ? 'running' : event.data.assistant.status
    else if (event.type === 'message.completed') turn.status = 'completed'
    else if (event.type === 'message.failed') turn.status = 'failed'
    else if (event.type === 'message.canceled') turn.status = 'canceled'
    else turn.status = 'running'
    if (isTerminal(turn.status)) this.releaseConnection(turn)
    this.notify(key)
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
    if (delay === undefined || turn.reconnectTimer !== undefined) return
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

  private notify(key: string): void {
    const turn = this.turns.get(key)
    for (const subscriber of this.subscribers.get(key) ?? []) subscriber(turn)
  }
}

export const chatGenerationRuntime = new ChatGenerationRuntime()

function runtimeKey(systemAccountId: string, conversationId: string): string {
  return JSON.stringify([systemAccountId, conversationId])
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
