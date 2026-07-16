import type { ChatMessage, ChatStreamEvent } from '@/types/domain/chat'

export function parseChatSseBlock(block: string): ChatStreamEvent | undefined {
  let eventType = ''
  const dataLines: string[] = []
  for (const line of block.split(/\r?\n/)) {
    if (line.startsWith('event:')) eventType = line.slice(6).trim()
    if (line.startsWith('data:')) dataLines.push(line.slice(5).replace(/^ /, ''))
  }
  if (!eventType || !dataLines.length) return undefined
  try {
    const data = JSON.parse(dataLines.join('\n')) as unknown
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
    if (!known.has(event.data.userMessage.id)) messages.push(event.data.userMessage)
    if (!known.has(event.data.assistantMessage.id)) messages.push(event.data.assistantMessage)
    return
  }
  if (event.type === 'message.snapshot') {
    const message = messages.find((item) => item.id === event.data.assistant.id)
    if (!message) return
    message.status = event.data.assistant.status
    message.contentText = event.data.assistant.contentText
    message.reasoningText = event.data.assistant.reasoningText
    message.toolEvents = cloneJsonSafe(event.data.assistant.toolEvents)
    message.contentBlocks = cloneJsonSafe(event.data.assistant.contentBlocks)
    return
  }
  const message = messages.find((item) => item.id === event.data.messageId)
  if (!message) return
  if (event.type === 'message.delta') {
    message.contentText += event.data.delta
    return
  }
  if (event.type === 'reasoning.delta') { message.reasoningText = `${message.reasoningText ?? ''}${event.data.delta}`; return }
  if (event.type === 'tool.started' || event.type === 'tool.updated' || event.type === 'tool.completed') {
    const item = event.data.item
    const id = String(item.id ?? `${event.type}-${message.toolEvents?.length ?? 0}`)
    const status = event.type === 'tool.started' ? 'started' : event.type === 'tool.updated' ? 'updated' : 'completed'
    const existing = message.toolEvents?.find((tool) => tool.id === id)
    if (existing) { existing.status = status; existing.item = item }
    else (message.toolEvents ??= []).push({ id, type: String(item.type ?? 'tool'), status, item })
    return
  }
  if (event.type === 'message.completed') {
    message.status = 'completed'
    message.finishReason = event.data.finishReason
    message.traceId = event.data.traceId
    return
  }
  if (event.type === 'message.canceled') {
    message.status = 'canceled'
    message.traceId = event.data.traceId
    return
  }
  if (event.type !== 'message.failed') return
  message.status = 'failed'
  message.errorCode = event.data.code
}

function hasSafeEventVersion(data: unknown): data is Record<string, any> & { eventVersion: number } {
  return typeof data === 'object' && data !== null
    && Number.isSafeInteger((data as { eventVersion?: unknown }).eventVersion)
    && Number((data as { eventVersion: number }).eventVersion) >= 0
}

function isValidChatStreamData(eventType: string, data: unknown): boolean {
  if (!isRecord(data)) return false
  if (eventType === 'message.started') {
    return nonEmptyString(data.turnId) && isChatMessage(data.userMessage) && isChatMessage(data.assistantMessage)
  }
  if (!hasSafeEventVersion(data)) return false
  if (eventType === 'message.snapshot') return nonEmptyString(data.turnId) && isAssistantSnapshot(data.assistant)
  if (eventType === 'message.delta' || eventType === 'reasoning.delta') return nonEmptyString(data.messageId) && typeof data.delta === 'string'
  if (eventType === 'tool.started' || eventType === 'tool.updated' || eventType === 'tool.completed') return nonEmptyString(data.messageId) && isRecord(data.item)
  if (eventType === 'message.completed') return nonEmptyString(data.messageId) && optionalString(data.finishReason) && optionalString(data.traceId)
  if (eventType === 'message.failed') return nonEmptyString(data.messageId) && nonEmptyString(data.code) && typeof data.message === 'string'
  if (eventType === 'message.canceled') return nonEmptyString(data.messageId) && optionalString(data.traceId)
  return false
}

function isAssistantSnapshot(value: unknown): boolean {
  if (!isRecord(value)) return false
  return nonEmptyString(value.id)
    && isMessageStatus(value.status)
    && typeof value.contentText === 'string'
    && typeof value.reasoningText === 'string'
    && Array.isArray(value.toolEvents)
    && value.toolEvents.every(isToolEvent)
    && Array.isArray(value.contentBlocks)
    && value.contentBlocks.every(isContentBlock)
}

function isChatMessage(value: unknown): boolean {
  if (!isRecord(value)) return false
  return nonEmptyString(value.id)
    && nonEmptyString(value.conversationId)
    && nonEmptyString(value.turnId)
    && Number.isSafeInteger(value.sequenceNo)
    && (value.role === 'user' || value.role === 'assistant')
    && isMessageStatus(value.status)
    && typeof value.contentText === 'string'
    && typeof value.model === 'string'
    && typeof value.createdAt === 'string'
    && typeof value.expiresAt === 'string'
}

function isToolEvent(value: unknown): boolean {
  return isRecord(value)
    && nonEmptyString(value.id)
    && nonEmptyString(value.type)
    && (value.status === 'started' || value.status === 'updated' || value.status === 'completed' || value.status === 'failed')
    && (value.item === undefined || isRecord(value.item))
}

function isContentBlock(value: unknown): boolean {
  if (!isRecord(value) || typeof value.type !== 'string') return false
  if (value.type === 'reasoning') return typeof value.text === 'string'
  if (value.type === 'tool_call') return nonEmptyString(value.id) && nonEmptyString(value.toolType)
    && (value.status === 'started' || value.status === 'updated' || value.status === 'completed' || value.status === 'failed')
    && (value.item === undefined || isRecord(value.item))
  if (value.type === 'input_text') return typeof value.text === 'string' && Number.isSafeInteger(value.order)
  if (value.type === 'input_image') return nonEmptyString(value.assetId) && Number.isSafeInteger(value.order)
  return false
}

function isMessageStatus(value: unknown): boolean {
  return value === 'completed' || value === 'streaming' || value === 'failed' || value === 'canceled'
}

function optionalString(value: unknown): boolean {
  return value === undefined || typeof value === 'string'
}

function nonEmptyString(value: unknown): value is string {
  return typeof value === 'string' && value.length > 0
}

function isRecord(value: unknown): value is Record<string, any> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function cloneJsonSafe<T>(value: T): T {
  try { return JSON.parse(JSON.stringify(value)) as T } catch { return [] as T }
}
