import type { ChatContentBlockStatus, ChatMessage, ChatMessageContentBlock, ChatStreamEvent } from '@/types/domain/chat'

// A message object is the runtime projection boundary. Keeping the watermark out of
// the persisted DTO prevents it from becoming part of the server message contract.
const messageEventVersions = new WeakMap<ChatMessage, number>()

export function parseChatSseBlock(block: string): ChatStreamEvent | undefined {
  let eventType = ''
  const dataLines: string[] = []
  for (const line of block.split(/\r?\n/)) {
    if (line.startsWith('event:')) eventType = line.slice(6).trim()
    if (line.startsWith('data:')) dataLines.push(line.slice(5).replace(/^ /, ''))
  }
  if (!eventType || !dataLines.length) return undefined
  try {
    const raw = JSON.parse(dataLines.join('\n')) as unknown
    const data = normalizeEventData(raw)
    if (isValidChatStreamData(eventType, data)) return { type: eventType, data } as ChatStreamEvent
  } catch {
  }
  return undefined
}

export function applyChatStreamEvent(messages: ChatMessage[], event: ChatStreamEvent, options?: { replaceTurnId?: string }): void {
  if (event.type === 'message.started') {
    if (options?.replaceTurnId) {
      for (let index = messages.length - 1; index >= 0; index -= 1) {
        if (messages[index]?.turnId === options.replaceTurnId) messages.splice(index, 1)
      }
    }
    const known = new Set(messages.map((message) => message.id))
    if (!known.has(event.data.userMessage.id)) messages.push(cloneJsonSafe(event.data.userMessage))
    if (!known.has(event.data.assistantMessage.id)) messages.push(cloneJsonSafe(event.data.assistantMessage))
    messageEventVersions.delete(event.data.assistantMessage)
    return
  }

  if (event.type === 'message.snapshot') {
    const message = messages.find((item) => item.id === event.data.assistant.id)
    if (!message || !acceptEventVersion(message, event.data.eventVersion)) return
    message.status = event.data.assistant.status
    message.contentText = event.data.assistant.contentText
    message.reasoningText = event.data.assistant.reasoningText
    message.toolEvents = cloneJsonSafe(event.data.assistant.toolEvents)
    message.contentBlocks = event.data.assistant.contentBlocks.map(sanitizeContentBlock)
    markEventVersion(message, event.data.eventVersion)
    return
  }

  const messageId = 'messageId' in event.data ? event.data.messageId : undefined
  const message = messageId ? messages.find((item) => item.id === messageId) : undefined
  if (!message || !acceptEventVersion(message, event.data.eventVersion)) return

  if (event.type === 'content_block.started') {
    upsertContentBlock(message, event.data.block)
  } else if (event.type === 'content_block.delta') {
    appendContentBlockDelta(message, event.data.blockId, event.data.delta)
  } else if (event.type === 'content_block.updated') {
    patchContentBlock(message, event.data.blockId, event.data.patch)
  } else if (event.type === 'content_block.completed') {
    upsertContentBlock(message, event.data.block)
  } else if (event.type === 'message.delta') {
    message.contentText += event.data.delta
  } else if (event.type === 'reasoning.delta') {
    message.reasoningText = `${message.reasoningText ?? ''}${event.data.delta}`
    appendOrCreateReasoningBlock(message, event.data.delta)
  } else if (event.type === 'tool.started' || event.type === 'tool.updated' || event.type === 'tool.completed') {
    applyLegacyToolEvent(message, event.type, event.data.item)
  } else if (event.type === 'message.completed') {
    message.status = 'completed'
    message.finishReason = event.data.finishReason
    message.traceId = event.data.traceId
  } else if (event.type === 'message.canceled') {
    message.status = 'canceled'
    message.traceId = event.data.traceId
  } else if (event.type === 'message.failed') {
    message.status = 'failed'
    message.errorCode = event.data.code
  }
  markEventVersion(message, event.data.eventVersion)
}

function acceptEventVersion(message: ChatMessage, eventVersion: number): boolean {
  if (!Number.isSafeInteger(eventVersion) || eventVersion < 0) return false
  const current = messageEventVersions.get(message)
  return current === undefined || eventVersion > current
}

function markEventVersion(message: ChatMessage, eventVersion: number): void {
  messageEventVersions.set(message, eventVersion)
}

function upsertContentBlock(message: ChatMessage, value: ChatMessageContentBlock): void {
  const block = sanitizeContentBlock(value)
  const blocks = message.contentBlocks ?? (message.contentBlocks = [])
  const identity = contentBlockIdentity(block)
  const index = blocks.findIndex((item) => contentBlockIdentity(item) === identity)
  if (index >= 0) blocks[index] = block
  else blocks.push(block)
  blocks.sort(compareContentBlocks)
  syncProjectionText(message, block)
}

function appendContentBlockDelta(message: ChatMessage, blockId: string, delta: string): void {
  const blocks = message.contentBlocks ?? (message.contentBlocks = [])
  const block = blocks.find((item) => contentBlockIdentity(item) === blockId)
  if (!block) return
  if (block.type === 'output_text' || block.type === 'reasoning') {
    block.text += delta
    syncProjectionText(message, block)
  }
}

function patchContentBlock(message: ChatMessage, blockId: string, patch: Record<string, unknown>): void {
  const blocks = message.contentBlocks ?? (message.contentBlocks = [])
  const index = blocks.findIndex((item) => contentBlockIdentity(item) === blockId)
  if (index < 0) return
  const existing = blocks[index]!
  const next = sanitizeContentBlock({ ...existing, ...patch, type: existing.type } as ChatMessageContentBlock)
  blocks[index] = next
  blocks.sort(compareContentBlocks)
  syncProjectionText(message, next)
}

function appendOrCreateReasoningBlock(message: ChatMessage, delta: string): void {
  const blocks = message.contentBlocks ?? (message.contentBlocks = [])
  const existing = blocks.find((item) => item.type === 'reasoning')
  if (existing && existing.type === 'reasoning') existing.text += delta
  else blocks.push({ type: 'reasoning', text: delta })
}

function applyLegacyToolEvent(message: ChatMessage, eventType: 'tool.started' | 'tool.updated' | 'tool.completed', item: Record<string, unknown>): void {
  const id = String(item.id ?? item.call_id ?? `${eventType}-${message.toolEvents?.length ?? 0}`)
  const status = eventType === 'tool.started' ? 'started' : eventType === 'tool.updated' ? 'updated' : 'completed'
  const existing = message.toolEvents?.find((tool) => tool.id === id)
  if (existing) { existing.status = status; existing.item = cloneJsonSafe(item) }
  else (message.toolEvents ??= []).push({ id, type: String(item.type ?? 'tool'), status, item: cloneJsonSafe(item) })
  upsertContentBlock(message, { type: 'tool_call', id, blockId: id, callId: id, order: message.contentBlocks?.length ?? 0, toolType: String(item.type ?? 'tool'), status, item: cloneJsonSafe(item) })
}

function syncProjectionText(message: ChatMessage, block: ChatMessageContentBlock): void {
  if (block.type === 'output_text') message.contentText = (message.contentBlocks ?? []).filter((item): item is Extract<ChatMessageContentBlock, { type: 'output_text' }> => item.type === 'output_text').sort(compareContentBlocks).map((item) => item.text).join('')
  if (block.type === 'reasoning') message.reasoningText = (message.contentBlocks ?? []).filter((item): item is Extract<ChatMessageContentBlock, { type: 'reasoning' }> => item.type === 'reasoning').sort(compareContentBlocks).map((item) => item.text).join('\n')
}

function contentBlockIdentity(block: ChatMessageContentBlock): string {
  if (block.type === 'tool_call') return block.blockId ?? block.id
  if (block.type === 'reasoning' && block.blockId) return block.blockId
  if (block.type === 'input_text' || block.type === 'input_image') return `${block.type}:${block.order}`
  if (block.type === 'output_text' || block.type === 'output_image') return block.blockId
  return block.blockId ?? `${block.type}:${block.order ?? 0}`
}

function compareContentBlocks(left: ChatMessageContentBlock, right: ChatMessageContentBlock): number {
  const leftOrder = 'order' in left && Number.isSafeInteger(left.order) ? Number(left.order) : Number.MAX_SAFE_INTEGER
  const rightOrder = 'order' in right && Number.isSafeInteger(right.order) ? Number(right.order) : Number.MAX_SAFE_INTEGER
  return leftOrder - rightOrder
}

function normalizeEventData(value: unknown): unknown {
  if (!isRecord(value)) return value
  if (isRecord(value.data) && value.eventVersion !== undefined) return { ...value.data, eventVersion: value.eventVersion }
  return value
}

function hasSafeEventVersion(data: unknown): data is Record<string, any> & { eventVersion: number } {
  return typeof data === 'object' && data !== null
    && Number.isSafeInteger((data as { eventVersion?: unknown }).eventVersion)
    && Number((data as { eventVersion: number }).eventVersion) >= 0
}

function isValidChatStreamData(eventType: string, data: unknown): boolean {
  if (!isRecord(data)) return false
  if (eventType === 'message.started') return nonEmptyString(data.turnId) && isChatMessage(data.userMessage) && isChatMessage(data.assistantMessage)
  if (!hasSafeEventVersion(data)) return false
  if (eventType === 'message.snapshot') return nonEmptyString(data.turnId) && isAssistantSnapshot(data.assistant)
  if (eventType === 'message.delta' || eventType === 'reasoning.delta') return nonEmptyString(data.messageId) && typeof data.delta === 'string'
  if (eventType === 'tool.started' || eventType === 'tool.updated' || eventType === 'tool.completed') return nonEmptyString(data.messageId) && isRecord(data.item)
  if (eventType === 'content_block.started' || eventType === 'content_block.completed') return nonEmptyString(data.messageId) && isContentBlock(data.block)
  if (eventType === 'content_block.delta') return nonEmptyString(data.messageId) && nonEmptyString(data.blockId) && typeof data.delta === 'string'
  if (eventType === 'content_block.updated') return nonEmptyString(data.messageId) && nonEmptyString(data.blockId) && isSafeContentBlockPatch(data.patch)
  if (eventType === 'message.completed') return nonEmptyString(data.messageId) && optionalString(data.finishReason) && optionalString(data.traceId)
  if (eventType === 'message.failed') return nonEmptyString(data.messageId) && nonEmptyString(data.code) && typeof data.message === 'string'
  if (eventType === 'message.canceled') return nonEmptyString(data.messageId) && optionalString(data.traceId)
  return false
}

function isAssistantSnapshot(value: unknown): boolean {
  if (!isRecord(value)) return false
  return nonEmptyString(value.id) && isMessageStatus(value.status) && typeof value.contentText === 'string' && typeof value.reasoningText === 'string'
    && Array.isArray(value.toolEvents) && value.toolEvents.every(isToolEvent) && Array.isArray(value.contentBlocks) && value.contentBlocks.every(isContentBlock)
}

function isChatMessage(value: unknown): boolean {
  if (!isRecord(value)) return false
  return nonEmptyString(value.id) && nonEmptyString(value.conversationId) && nonEmptyString(value.turnId) && Number.isSafeInteger(value.sequenceNo)
    && (value.role === 'user' || value.role === 'assistant') && isMessageStatus(value.status) && typeof value.contentText === 'string'
    && typeof value.model === 'string' && typeof value.createdAt === 'string' && typeof value.expiresAt === 'string'
}

function isToolEvent(value: unknown): boolean {
  return isRecord(value) && nonEmptyString(value.id) && nonEmptyString(value.type) && isBlockStatus(value.status) && (value.item === undefined || isRecord(value.item))
}

function isContentBlock(value: unknown): value is ChatMessageContentBlock {
  if (!isRecord(value) || typeof value.type !== 'string') return false
  if (value.type === 'input_text') return typeof value.text === 'string' && validOrder(value.order)
  if (value.type === 'input_image') return nonEmptyString(value.assetId) && validOrder(value.order)
  if (value.type === 'output_text') return nonEmptyString(value.blockId) && validOrder(value.order) && typeof value.text === 'string'
  if (value.type === 'reasoning') return typeof value.text === 'string' && (value.blockId === undefined || nonEmptyString(value.blockId))
    && (value.order === undefined || validOrder(value.order)) && (value.status === undefined || isBlockStatus(value.status))
  if (value.type === 'tool_call') return nonEmptyString(value.toolType) && (nonEmptyString(value.id) || nonEmptyString(value.blockId))
    && (value.blockId === undefined || nonEmptyString(value.blockId)) && (value.callId === undefined || nonEmptyString(value.callId))
    && (value.order === undefined || validOrder(value.order)) && isBlockStatus(value.status) && (value.item === undefined || isRecord(value.item))
  if (value.type === 'output_image') return nonEmptyString(value.blockId) && validOrder(value.order) && nonEmptyString(value.assetId) && isBlockStatus(value.status)
    && optionalString(value.mimeType) && optionalInteger(value.width) && optionalInteger(value.height) && optionalString(value.revisedPrompt)
    && Object.keys(value).every((key) => ['type', 'blockId', 'order', 'assetId', 'status', 'mimeType', 'width', 'height', 'revisedPrompt'].includes(key))
  return false
}

function isSafeContentBlockPatch(value: unknown): value is Record<string, unknown> {
  if (!isRecord(value)) return false
  if (value.type === 'output_image' && !isContentBlock({ ...value, blockId: value.blockId ?? 'patch', order: value.order ?? 0, assetId: value.assetId ?? 'asset', status: value.status ?? 'updated' })) return false
  return !Object.keys(value).some((key) => ['data', 'base64', 'url', 'image', 'blob'].includes(key.toLowerCase()))
}

function sanitizeContentBlock(value: ChatMessageContentBlock): ChatMessageContentBlock {
  if (value.type === 'output_image') {
    return {
      type: 'output_image', blockId: value.blockId, order: value.order, assetId: value.assetId, status: value.status,
      ...(value.mimeType ? { mimeType: value.mimeType } : {}), ...(value.width !== undefined ? { width: value.width } : {}),
      ...(value.height !== undefined ? { height: value.height } : {}), ...(value.revisedPrompt ? { revisedPrompt: value.revisedPrompt } : {})
    }
  }
  return cloneJsonSafe(value)
}

function isBlockStatus(value: unknown): value is ChatContentBlockStatus {
  return value === 'started' || value === 'updated' || value === 'completed' || value === 'failed' || value === 'canceled'
}

function isMessageStatus(value: unknown): boolean { return value === 'completed' || value === 'streaming' || value === 'failed' || value === 'canceled' }
function validOrder(value: unknown): value is number { return Number.isSafeInteger(value) && Number(value) >= 0 }
function optionalInteger(value: unknown): boolean { return value === undefined || (Number.isSafeInteger(value) && Number(value) >= 0) }
function optionalString(value: unknown): boolean { return value === undefined || typeof value === 'string' }
function nonEmptyString(value: unknown): value is string { return typeof value === 'string' && value.length > 0 }
function isRecord(value: unknown): value is Record<string, any> { return typeof value === 'object' && value !== null && !Array.isArray(value) }
function cloneJsonSafe<T>(value: T): T { try { return JSON.parse(JSON.stringify(value)) as T } catch { return [] as T }
}
