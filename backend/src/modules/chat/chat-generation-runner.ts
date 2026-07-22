import type { ChatMessageStatus } from '../../storage/chat.repository.js'
import {
  AssistantTimeline,
  type AssistantContentBlock,
  type AssistantJsonObject,
  type AssistantOutputImageBlock,
  type AssistantToolCallBlock
} from './chat-assistant-timeline.js'
import { classifyChatGenerationError, type PublicChatGenerationError } from './chat-generation-error.js'

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
  status: 'started' | 'updated' | 'completed' | 'failed' | 'canceled'
  item?: Record<string, unknown>
}

export interface ChatGenerationImageEvent {
  id: string
  status: 'started' | 'updated' | 'completed' | 'failed' | 'canceled'
  item?: Record<string, unknown>
}

export interface ChatGenerationToolEventProjection {
  id: string
  type: string
  status: ChatGenerationToolEvent['status']
  item?: Record<string, unknown>
}

export interface ChatGenerationAssistantProjection {
  id: string
  status: ChatMessageStatus
  contentText: string
  reasoningText: string
  toolEvents: ChatGenerationToolEventProjection[]
  contentBlocks: AssistantContentBlock[]
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
  reasoningCompleted?: boolean
  toolEvent?: ChatGenerationToolEvent
  imageEvent?: ChatGenerationImageEvent
}

export type ChatGenerationTerminalStatus = 'completed' | 'failed' | 'canceled'

export interface ChatGenerationTerminalResult {
  status: ChatGenerationTerminalStatus
  data: Record<string, any>
}

export interface ChatGenerationStatusSnapshot {
  state: 'running' | 'terminal'
  eventVersion: number
  lastSemanticActivityAt: string
  assistantMessageId: string
}

export interface ChatGenerationExecutionContext {
  signal: AbortSignal
  publish: ChatGenerationRunner['publish']
}

export interface ChatGenerationRunnerOptions {
  identity: ChatGenerationIdentity
  execute(context: ChatGenerationExecutionContext): Promise<ChatGenerationTerminalResult>
  onUnexpectedError?(error: PublicChatGenerationError): Promise<void>
  reportUnexpectedError?(error: unknown, stage: 'execute' | 'finalizer'): unknown
  reportCleanupError?(error: unknown): void
  now?(): string
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
  private readonly onUnexpectedError?: ChatGenerationRunnerOptions['onUnexpectedError']
  private readonly reportUnexpectedError?: ChatGenerationRunnerOptions['reportUnexpectedError']
  private readonly reportCleanupError?: ChatGenerationRunnerOptions['reportCleanupError']
  private readonly now: NonNullable<ChatGenerationRunnerOptions['now']>
  private readonly subscribers = new Map<ChatGenerationSubscriber, RegisteredSubscriber>()
  private readonly timeline = new AssistantTimeline()
  private resolveCompletion!: () => void
  private eventVersion = 0
  private lastSemanticActivityAt: string
  private started = false
  private authoritativeTerminalState = false
  private currentState: 'pending' | 'running' | ChatGenerationTerminalStatus = 'pending'

  constructor(options: ChatGenerationRunnerOptions) {
    this.identity = Object.freeze({ ...options.identity })
    this.execute = options.execute
    this.onUnexpectedError = options.onUnexpectedError
    this.reportUnexpectedError = options.reportUnexpectedError
    this.reportCleanupError = options.reportCleanupError
    this.now = options.now ?? (() => new Date().toISOString())
    this.lastSemanticActivityAt = this.now()
    this.completion = new Promise<void>((resolve) => { this.resolveCompletion = resolve })
  }

  get state(): 'pending' | 'running' | ChatGenerationTerminalStatus { return this.currentState }
  get signal(): AbortSignal { return this.controller.signal }
  get terminal(): boolean { return this.currentState === 'completed' || this.currentState === 'failed' || this.currentState === 'canceled' }
  get authoritativeTerminal(): boolean { return this.authoritativeTerminalState }
  snapshotContentBlocks(): AssistantContentBlock[] { return this.timeline.snapshot().contentBlocks }
  statusSnapshot(): ChatGenerationStatusSnapshot {
    return {
      state: this.terminal ? 'terminal' : 'running',
      eventVersion: this.eventVersion,
      lastSemanticActivityAt: this.lastSemanticActivityAt,
      assistantMessageId: this.identity.assistantMessageId
    }
  }

  start(onSettled?: () => void): boolean {
    if (this.started) return false
    this.started = true
    this.currentState = 'running'
    void Promise.resolve().then(() => this.run(onSettled))
    return true
  }

  publish = (type: string, data: Record<string, any>, update: ChatGenerationProjectionUpdate = {}): boolean => {
    if (this.currentState !== 'running') return false
    if (!hasProjectionUpdate(update)) return this.emitEvent(type, data)
    this.applyProjectionUpdate(update)
    return true
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
      this.finalizeTimeline(result.status)
      this.currentState = result.status
      this.authoritativeTerminalState = true
      this.emitEvent(`message.${result.status}`, result.data)
    } catch (error) {
      if (!this.terminal) {
        this.reportUnexpected(error, 'execute')
        const publicError = classifyChatGenerationError(error)
        let authoritativeFailure = false
        try {
          if (this.onUnexpectedError) {
            await this.onUnexpectedError(publicError)
            authoritativeFailure = true
          }
        } catch (finalizerError) {
          this.reportUnexpected(finalizerError, 'finalizer')
        }
        if (authoritativeFailure) {
          this.finalizeTimeline('failed')
          this.currentState = 'failed'
          this.authoritativeTerminalState = true
          this.emitEvent('message.failed', {
            messageId: this.identity.assistantMessageId,
            code: publicError.code,
            message: publicError.message
          })
        } else {
          this.timeline.finalize('failed')
          this.currentState = 'failed'
        }
      }
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

  private emitEvent(type: string, data: Record<string, any>): boolean {
    if (this.eventVersion >= Number.MAX_SAFE_INTEGER) throw new RangeError('chat generation eventVersion exhausted')
    this.eventVersion += 1
    this.lastSemanticActivityAt = this.now()
    const event: ChatGenerationEvent = { type, eventVersion: this.eventVersion, data }
    for (const registration of this.subscribers.values()) {
      if (event.eventVersion <= registration.deliveredVersion) continue
      if (!this.trySend(registration, event)) this.subscribers.delete(registration.subscriber)
    }
    return true
  }

  private reportUnexpected(error: unknown, stage: 'execute' | 'finalizer'): void {
    try {
      const result = this.reportUnexpectedError?.(error, stage)
      if (result && typeof (result as { then?: unknown }).then === 'function') {
        void Promise.resolve(result).catch(() => undefined)
      }
    } catch {}
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
    if (update.contentTextDelta) this.appendText(update.contentTextDelta)
    if (update.reasoningTextDelta) this.appendReasoning(update.reasoningTextDelta)
    if (update.reasoningCompleted) this.completeReasoning()
    if (update.toolEvent) this.applyToolEvent(update.toolEvent)
    if (update.imageEvent) this.applyImageEvent(update.imageEvent)
  }

  private appendText(input: string): void {
    const snapshot = this.timeline.snapshot()
    const delta = boundedUtf8(String(input), remainingTextBytes(snapshot.contentBlocks, 'output_text', CHAT_GENERATION_TEXT_MAX_BYTES))
    if (!delta) return
    const last = snapshot.contentBlocks.at(-1)
    if (last?.type !== 'output_text' && !canAppendBlock(snapshot.contentBlocks, {
      type: 'output_text', blockId: nextBlockId(snapshot.contentBlocks), order: snapshot.contentBlocks.length + 1, text: delta
    })) return
    const block = this.timeline.appendText(delta)
    if (!block) return
    this.emitEvent(last?.blockId === block.blockId ? 'content_block.delta' : 'content_block.started',
      last?.blockId === block.blockId
        ? { messageId: this.identity.assistantMessageId, blockId: block.blockId, delta }
        : { messageId: this.identity.assistantMessageId, block })
  }

  private appendReasoning(input: string): void {
    const snapshot = this.timeline.snapshot()
    const delta = boundedUtf8(String(input), remainingTextBytes(snapshot.contentBlocks, 'reasoning', CHAT_GENERATION_REASONING_MAX_BYTES))
    if (!delta) return
    const last = snapshot.contentBlocks.at(-1)
    if (!(last?.type === 'reasoning' && last.status === 'started') && !canAppendBlock(snapshot.contentBlocks, {
      type: 'reasoning', blockId: nextBlockId(snapshot.contentBlocks), order: snapshot.contentBlocks.length + 1, text: delta, status: 'started'
    })) return
    const block = this.timeline.appendReasoning(delta)
    if (!block) return
    this.emitEvent(last?.blockId === block.blockId ? 'content_block.delta' : 'content_block.started',
      last?.blockId === block.blockId
        ? { messageId: this.identity.assistantMessageId, blockId: block.blockId, delta }
        : { messageId: this.identity.assistantMessageId, block })
  }

  private completeReasoning(): void {
    const block = this.timeline.snapshot().contentBlocks.find((candidate) => (
      candidate.type === 'reasoning' && candidate.status === 'started'
    ))
    if (!block) return
    const completed = this.timeline.completeBlock(block.blockId)
    this.emitEvent('content_block.completed', { messageId: this.identity.assistantMessageId, block: completed })
  }

  private applyToolEvent(input: ChatGenerationToolEvent): void {
    const event = sanitizeToolEvent(input)
    const snapshot = this.timeline.snapshot()
    const existing = snapshot.contentBlocks.find((block): block is AssistantToolCallBlock => block.type === 'tool_call' && block.callId === event.id)
    if (!existing) {
      const item = toolItemWithinBudget(snapshot.contentBlocks, {
        type: 'tool_call', blockId: nextBlockId(snapshot.contentBlocks), order: snapshot.contentBlocks.length + 1,
        callId: event.id, toolType: event.toolType, status: 'started', ...(event.item ? { item: event.item as AssistantJsonObject } : {})
      })
      const candidate: AssistantToolCallBlock = {
        type: 'tool_call', blockId: nextBlockId(snapshot.contentBlocks), order: snapshot.contentBlocks.length + 1,
        callId: event.id, toolType: event.toolType, status: 'started', ...(item ? { item } : {})
      }
      if (!canAppendBlock(snapshot.contentBlocks, candidate) || !toolBlocksWithinBudget([...snapshot.contentBlocks, candidate])) return
      const started = this.timeline.startTool({ callId: event.id, toolType: event.toolType, ...(item ? { item } : {}) })
      this.emitEvent('content_block.started', { messageId: this.identity.assistantMessageId, block: started })
      if (event.status !== 'started') this.updateTool(started, event.status, event.item)
      return
    }
    if (existing.toolType !== event.toolType) throw new Error(`助手工具类型不一致: ${event.id}`)
    if (event.status === 'started') {
      const item = existing.item ? undefined : toolItemWithinBudget(snapshot.contentBlocks, { ...existing, item: event.item as AssistantJsonObject })
      if (!item) return
      const block = this.timeline.startTool({ callId: event.id, toolType: event.toolType, item })
      this.emitEvent('content_block.updated', { messageId: this.identity.assistantMessageId, blockId: block.blockId, patch: { item: block.item } })
      return
    }
    this.updateTool(existing, event.status, event.item)
  }

  private applyImageEvent(input: ChatGenerationImageEvent): void {
    const event = sanitizeImageEvent(input)
    const assetId = typeof event.item?.assetId === 'string' ? event.item.assetId : undefined
    if (!assetId) return
    const snapshot = this.timeline.snapshot()
    const existing = snapshot.contentBlocks.find((block): block is AssistantOutputImageBlock => block.type === 'output_image' && block.assetId === assetId)
    if (!existing) {
      if (event.status === 'failed' || event.status === 'canceled') return
      const block = this.timeline.startImage({
        assetId,
        mimeType: stringValue(event.item?.mimeType),
        width: positiveInteger(event.item?.width),
        height: positiveInteger(event.item?.height),
        revisedPrompt: stringValue(event.item?.revisedPrompt)
      })
      this.emitEvent('content_block.started', { messageId: this.identity.assistantMessageId, block })
      if (event.status !== 'started') this.updateImage(block, event.status, event.item)
      return
    }
    if (event.status === 'started') return
    this.updateImage(existing, event.status, event.item)
  }

  private updateImage(existing: AssistantOutputImageBlock, status: Exclude<ChatGenerationImageEvent['status'], 'started'>, item?: Record<string, unknown>): void {
    const block = this.timeline.updateImage({
      assetId: existing.assetId,
      status: normalizeImageStatus(status),
      mimeType: stringValue(item?.mimeType),
      width: positiveInteger(item?.width),
      height: positiveInteger(item?.height),
      revisedPrompt: stringValue(item?.revisedPrompt)
    })
    if (block.status === existing.status && block.mimeType === existing.mimeType && block.width === existing.width && block.height === existing.height && block.revisedPrompt === existing.revisedPrompt) return
    if (block.status === 'completed') {
      this.emitEvent('content_block.completed', { messageId: this.identity.assistantMessageId, block })
      return
    }
    this.emitEvent('content_block.updated', {
      messageId: this.identity.assistantMessageId,
      blockId: block.blockId,
      patch: {
        status: block.status,
        ...(block.mimeType ? { mimeType: block.mimeType } : {}),
        ...(block.width ? { width: block.width } : {}),
        ...(block.height ? { height: block.height } : {}),
        ...(block.revisedPrompt ? { revisedPrompt: block.revisedPrompt } : {})
      }
    })
  }

  private updateTool(existing: AssistantToolCallBlock, status: Exclude<ChatGenerationToolEvent['status'], 'started'>, inputItem?: Record<string, unknown>): void {
    const snapshot = this.timeline.snapshot()
    const item = inputItem
      ? toolItemWithinBudget(snapshot.contentBlocks, { ...existing, status, item: inputItem as AssistantJsonObject })
      : undefined
    const block = this.timeline.updateTool({
      callId: existing.callId,
      status: normalizeImageStatus(status),
      ...(item ? { item } : {})
    })
    if (block.status === existing.status && JSON.stringify(block.item) === JSON.stringify(existing.item)) return
    if (block.status === 'completed') {
      this.emitEvent('content_block.completed', { messageId: this.identity.assistantMessageId, block })
      return
    }
    const patch: Record<string, unknown> = { status: block.status }
    if (item) patch.item = block.item
    this.emitEvent('content_block.updated', { messageId: this.identity.assistantMessageId, blockId: block.blockId, patch })
  }

  private finalizeTimeline(status: ChatGenerationTerminalStatus): void {
    const before = this.timeline.snapshot()
    const after = this.timeline.finalize(status)
    for (const block of after.contentBlocks) {
      if (block.type === 'output_text') continue
      const previous = before.contentBlocks.find((candidate) => candidate.blockId === block.blockId)
      if (!previous || previous.type === 'output_text' || previous.status === block.status) continue
      if (block.status === 'completed') {
        this.emitEvent('content_block.completed', { messageId: this.identity.assistantMessageId, block })
      } else {
        this.emitEvent('content_block.updated', {
          messageId: this.identity.assistantMessageId,
          blockId: block.blockId,
          patch: { status: block.status }
        })
      }
    }
  }

  private snapshotAssistant(): ChatGenerationAssistantProjection {
    const snapshot = this.timeline.snapshot()
    const reasoningText = snapshot.contentBlocks
      .filter((block) => block.type === 'reasoning')
      .map((block) => block.text)
      .join('')
    const toolEvents = snapshot.contentBlocks
      .filter((block): block is AssistantToolCallBlock => block.type === 'tool_call')
      .map((block) => ({
        id: block.callId,
        type: block.toolType,
        status: block.status,
        ...(block.item ? { item: block.item } : {})
      }))
    return {
      id: this.identity.assistantMessageId,
      status: this.currentState === 'pending' || this.currentState === 'running' ? 'streaming' : this.currentState,
      contentText: snapshot.contentText,
      reasoningText,
      toolEvents,
      contentBlocks: snapshot.contentBlocks
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
  const item = cloneJsonObject(input.item)
  if (!item) return base
  const withItem: ChatGenerationToolEvent = { ...base, item }
  return jsonBytes(withItem) <= CHAT_GENERATION_TOOL_JSON_MAX_BYTES ? withItem : base
}

function cloneJsonObject(value: Record<string, unknown>): AssistantJsonObject | undefined {
  try { return JSON.parse(JSON.stringify(value)) as AssistantJsonObject } catch { return undefined }
}

function sanitizeImageEvent(input: ChatGenerationImageEvent): ChatGenerationImageEvent {
  const item = input.item ? cloneJsonObject(input.item) : undefined
  if (item) {
    for (const key of ['result', 'b64_json', 'partial_image', 'partial_image_b64', 'image']) delete item[key]
  }
  return {
    id: boundedUtf8(String(input.id), 512),
    status: input.status,
    ...(item ? { item } : {})
  }
}

function jsonBytes(value: unknown): number {
  try { return Buffer.byteLength(JSON.stringify(value), 'utf8') } catch { return Number.POSITIVE_INFINITY }
}

function boundedUtf8(value: string, maxBytes: number): string {
  if (maxBytes <= 0) return ''
  const bytes = Buffer.from(value, 'utf8')
  if (bytes.byteLength <= maxBytes) return value
  let end = maxBytes
  const decoder = new TextDecoder('utf-8', { fatal: true })
  while (end > 0) {
    try { return decoder.decode(bytes.subarray(0, end)) } catch { end -= 1 }
  }
  return ''
}

function hasProjectionUpdate(update: ChatGenerationProjectionUpdate): boolean {
  return Boolean(update.contentTextDelta || update.reasoningTextDelta || update.reasoningCompleted || update.toolEvent || update.imageEvent)
}

function stringValue(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

function positiveInteger(value: unknown): number | undefined {
  const number = Number(value)
  return Number.isSafeInteger(number) && number > 0 ? number : undefined
}

function normalizeImageStatus(status: ChatGenerationImageEvent['status']): 'started' | 'completed' | 'failed' | 'canceled' {
  return status === 'updated' ? 'started' : status
}

function remainingTextBytes(blocks: AssistantContentBlock[], type: 'output_text' | 'reasoning', maxBytes: number): number {
  return Math.max(0, maxBytes - blocks.reduce((total, block) => (
    block.type === type ? total + Buffer.byteLength(block.text, 'utf8') : total
  ), 0))
}

function nextBlockId(blocks: AssistantContentBlock[]): string {
  return `assistant_block_${blocks.length + 1}`
}

function canAppendBlock(blocks: AssistantContentBlock[], block: AssistantContentBlock): boolean {
  return jsonBytes([...blocks, block]) <= CHAT_GENERATION_TEXT_MAX_BYTES
    + CHAT_GENERATION_REASONING_MAX_BYTES
    + CHAT_GENERATION_TOOL_JSON_MAX_BYTES
    + 64 * 1024
}

function toolItemWithinBudget(blocks: AssistantContentBlock[], candidate: AssistantToolCallBlock): AssistantJsonObject | undefined {
  if (!candidate.item) return undefined
  const next = blocks.map((block) => block.type === 'tool_call' && block.callId === candidate.callId ? candidate : block)
  if (!next.some((block) => block.type === 'tool_call' && block.callId === candidate.callId)) next.push(candidate)
  return toolBlocksWithinBudget(next) ? candidate.item : undefined
}

function toolBlocksWithinBudget(blocks: AssistantContentBlock[]): boolean {
  return jsonBytes(blocks.filter((block) => block.type === 'tool_call')) <= CHAT_GENERATION_TOOL_JSON_MAX_BYTES
}
