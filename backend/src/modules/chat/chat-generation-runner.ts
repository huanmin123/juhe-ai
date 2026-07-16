import type { ChatMessageContentBlock, ChatMessageStatus } from '../../storage/chat.repository.js'

export const CHAT_GENERATION_TEXT_MAX_BYTES = 192 * 1024
export const CHAT_GENERATION_REASONING_MAX_BYTES = 192 * 1024
export const CHAT_GENERATION_TOOL_JSON_MAX_BYTES = 192 * 1024

export interface ChatGenerationIdentity {
  readonly ownerId: string
  readonly conversationId: string
  readonly turnId: string
  readonly assistantMessageId: string
}

export interface ChatGenerationToolEvent {
  id: string
  toolType: string
  status: 'started' | 'updated' | 'completed' | 'failed'
  item?: Record<string, unknown>
}

export interface ChatGenerationAssistantProjection {
  id: string
  status: ChatMessageStatus
  contentText: string
  reasoningText: string
  toolEvents: ChatGenerationToolEvent[]
  contentBlocks: ChatMessageContentBlock[]
}

export interface ChatGenerationEvent {
  type: string
  eventVersion: number
  data: Record<string, any>
}

export interface ChatGenerationSubscriber {
  trySend(event: ChatGenerationEvent): boolean | void
}

export interface ChatGenerationProjectionUpdate {
  contentTextDelta?: string
  reasoningTextDelta?: string
  toolEvent?: ChatGenerationToolEvent
}

export type ChatGenerationTerminalStatus = 'completed' | 'failed' | 'canceled'

export interface ChatGenerationTerminalResult {
  status: ChatGenerationTerminalStatus
  data: Record<string, any>
}

export interface ChatGenerationExecutionContext {
  signal: AbortSignal
  publish: ChatGenerationRunner['publish']
}

export interface ChatGenerationRunnerOptions {
  identity: ChatGenerationIdentity
  execute(context: ChatGenerationExecutionContext): Promise<ChatGenerationTerminalResult>
  reportCleanupError?(error: unknown): void
}

interface RegisteredSubscriber {
  subscriber: ChatGenerationSubscriber
  deliveredVersion: number
}

export class ChatGenerationRunner {
  readonly identity: ChatGenerationIdentity
  readonly controller = new AbortController()
  readonly completion: Promise<void>

  private readonly execute: ChatGenerationRunnerOptions['execute']
  private readonly reportCleanupError?: ChatGenerationRunnerOptions['reportCleanupError']
  private readonly subscribers = new Map<ChatGenerationSubscriber, RegisteredSubscriber>()
  private resolveCompletion!: () => void
  private eventVersion = 0
  private started = false
  private contentText = ''
  private reasoningText = ''
  private toolEvents: ChatGenerationToolEvent[] = []
  private currentState: 'pending' | 'running' | ChatGenerationTerminalStatus = 'pending'

  constructor(options: ChatGenerationRunnerOptions) {
    this.identity = Object.freeze({ ...options.identity })
    this.execute = options.execute
    this.reportCleanupError = options.reportCleanupError
    this.completion = new Promise<void>((resolve) => { this.resolveCompletion = resolve })
  }

  get state(): 'pending' | 'running' | ChatGenerationTerminalStatus { return this.currentState }
  get signal(): AbortSignal { return this.controller.signal }
  get terminal(): boolean { return this.currentState === 'completed' || this.currentState === 'failed' || this.currentState === 'canceled' }

  start(onSettled?: () => void): boolean {
    if (this.started) return false
    this.started = true
    this.currentState = 'running'
    void Promise.resolve().then(() => this.run(onSettled))
    return true
  }

  publish = (type: string, data: Record<string, any>, update: ChatGenerationProjectionUpdate = {}): boolean => {
    if (this.currentState !== 'running') return false
    return this.publishInternal(type, data, update)
  }

  subscribe(subscriber: ChatGenerationSubscriber): boolean {
    if (this.subscribers.has(subscriber)) return true
    const registration = { subscriber, deliveredVersion: this.eventVersion }
    this.subscribers.set(subscriber, registration)
    const delivered = this.trySend(registration, {
      type: 'message.snapshot',
      eventVersion: this.eventVersion,
      data: {
        turnId: this.identity.turnId,
        assistant: this.snapshotAssistant()
      }
    })
    if (!delivered) this.subscribers.delete(subscriber)
    return delivered
  }

  unsubscribe(subscriber: ChatGenerationSubscriber): boolean {
    return this.subscribers.delete(subscriber)
  }

  abort(): boolean {
    if (this.terminal || this.controller.signal.aborted) return false
    this.controller.abort()
    return true
  }

  private async run(onSettled?: () => void): Promise<void> {
    try {
      const result = await this.execute({ signal: this.controller.signal, publish: this.publish })
      if (this.terminal) return
      this.currentState = result.status
      this.publishInternal(`message.${result.status}`, result.data, {})
    } catch {
      // The execute callback owns authoritative DB finalization. An unexpected throw
      // cannot safely invent a client terminal event, but the runner must still settle.
      if (!this.terminal) this.currentState = 'failed'
    } finally {
      try {
        onSettled?.()
      } catch (error) {
        try { this.reportCleanupError?.(error) } catch {}
      } finally {
        this.resolveCompletion()
      }
    }
  }

  private publishInternal(type: string, data: Record<string, any>, update: ChatGenerationProjectionUpdate): boolean {
    if (this.eventVersion >= Number.MAX_SAFE_INTEGER) throw new RangeError('chat generation eventVersion exhausted')
    this.applyProjectionUpdate(update)
    this.eventVersion += 1
    const event: ChatGenerationEvent = { type, eventVersion: this.eventVersion, data }
    for (const registration of this.subscribers.values()) {
      if (event.eventVersion <= registration.deliveredVersion) continue
      if (!this.trySend(registration, event)) this.subscribers.delete(registration.subscriber)
    }
    return true
  }

  private trySend(registration: RegisteredSubscriber, event: ChatGenerationEvent): boolean {
    try {
      if (registration.subscriber.trySend(event) === false) return false
      registration.deliveredVersion = event.eventVersion
      return true
    } catch {
      return false
    }
  }

  private applyProjectionUpdate(update: ChatGenerationProjectionUpdate): void {
    if (update.contentTextDelta) {
      this.contentText = boundedUtf8(`${this.contentText}${update.contentTextDelta}`, CHAT_GENERATION_TEXT_MAX_BYTES)
    }
    if (update.reasoningTextDelta) {
      this.reasoningText = boundedUtf8(`${this.reasoningText}${update.reasoningTextDelta}`, CHAT_GENERATION_REASONING_MAX_BYTES)
    }
    if (update.toolEvent) this.upsertToolEvent(update.toolEvent)
  }

  private upsertToolEvent(input: ChatGenerationToolEvent): void {
    const event = sanitizeToolEvent(input)
    const existingIndex = this.toolEvents.findIndex((item) => item.id === event.id)
    const next = this.toolEvents.slice()
    if (existingIndex >= 0) next[existingIndex] = event
    else next.push(event)
    while (next.length && jsonBytes(next) > CHAT_GENERATION_TOOL_JSON_MAX_BYTES) next.shift()
    this.toolEvents = next
  }

  private snapshotAssistant(): ChatGenerationAssistantProjection {
    const toolEvents = cloneToolEvents(this.toolEvents)
    const contentBlocks: ChatMessageContentBlock[] = [
      ...(this.reasoningText ? [{ type: 'reasoning' as const, text: this.reasoningText }] : []),
      ...toolEvents.map((event) => ({ type: 'tool_call' as const, ...event }))
    ]
    return {
      id: this.identity.assistantMessageId,
      status: this.currentState === 'pending' || this.currentState === 'running' ? 'streaming' : this.currentState,
      contentText: this.contentText,
      reasoningText: this.reasoningText,
      toolEvents,
      contentBlocks
    }
  }
}

function sanitizeToolEvent(input: ChatGenerationToolEvent): ChatGenerationToolEvent {
  const base: ChatGenerationToolEvent = {
    id: boundedUtf8(String(input.id), 512),
    toolType: boundedUtf8(String(input.toolType), 512),
    status: input.status
  }
  if (!input.item) return base
  const withItem = { ...base, item: cloneJsonObject(input.item) }
  return jsonBytes(withItem) <= CHAT_GENERATION_TOOL_JSON_MAX_BYTES ? withItem : base
}

function cloneToolEvents(events: ChatGenerationToolEvent[]): ChatGenerationToolEvent[] {
  return events.map((event) => event.item ? { ...event, item: cloneJsonObject(event.item) } : { ...event })
}

function cloneJsonObject(value: Record<string, unknown>): Record<string, unknown> | undefined {
  try { return JSON.parse(JSON.stringify(value)) as Record<string, unknown> } catch { return undefined }
}

function jsonBytes(value: unknown): number {
  try { return Buffer.byteLength(JSON.stringify(value), 'utf8') } catch { return Number.POSITIVE_INFINITY }
}

function boundedUtf8(value: string, maxBytes: number): string {
  const bytes = Buffer.from(value, 'utf8')
  if (bytes.byteLength <= maxBytes) return value
  let end = maxBytes
  const decoder = new TextDecoder('utf-8', { fatal: true })
  while (end > 0) {
    try { return decoder.decode(bytes.subarray(0, end)) } catch { end -= 1 }
  }
  return ''
}
