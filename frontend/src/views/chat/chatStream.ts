import type { ChatMessage, ChatMessageContentBlock, ChatStreamEvent } from '@/types/domain/chat'

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
    const clientMessageId = event.data.userMessage.clientMessageId
    const userIndex = clientMessageId ? messages.findIndex((message) => message.role === 'user' && message.clientMessageId === clientMessageId) : -1
    if (userIndex >= 0) messages[userIndex] = event.data.userMessage
    else if (!messages.some((message) => message.id === event.data.userMessage.id)) messages.push(event.data.userMessage)
    const assistantIndex = clientMessageId ? messages.findIndex((message) => message.role === 'assistant' && message.clientMessageId === clientMessageId) : -1
    if (assistantIndex >= 0) messages[assistantIndex] = { ...messages[assistantIndex], ...event.data.assistantMessage, clientMessageId }
    else if (!messages.some((message) => message.id === event.data.assistantMessage.id)) messages.push({ ...event.data.assistantMessage, ...(clientMessageId ? { clientMessageId } : {}) })
    return
  }
  if (event.type === 'message.snapshot') {
    const message = messages.find((item) => item.id === event.data.assistant.id)
    if (!message) return
    if (!acceptEventVersion(message, event.data.eventVersion, true)) return
    message.status = event.data.assistant.status
    message.contentText = event.data.assistant.contentText
    message.reasoningText = event.data.assistant.reasoningText
    message.toolEvents = cloneJsonSafe(event.data.assistant.toolEvents)
    message.contentBlocks = sortBlocks(event.data.assistant.contentBlocks.map(sanitizeContentBlock))
    message.eventVersion = event.data.eventVersion
    message.renderRevision = (message.renderRevision ?? 0) + 1
    return
  }
  const message = messages.find((item) => item.id === event.data.messageId)
  if (!message) return
  if (!acceptEventVersion(message, event.data.eventVersion)) return
  if (event.type === 'content_block.started') {
    const block = sanitizeContentBlock(event.data.block)
    const blocks = message.contentBlocks ?? (message.contentBlocks = [])
    if (!blocks.some((item) => blockIdOf(item) === (block as { blockId?: string }).blockId)) blocks.push(block)
    message.contentBlocks = sortBlocks(blocks)
    syncLegacyProjection(message)
    return
  }
  if (event.type === 'content_block.delta') {
    const block = message.contentBlocks?.find((item) => blockIdOf(item) === event.data.blockId)
    if (block && 'text' in block) block.text += event.data.delta
    else if (!block) (message.contentBlocks ??= []).push({ type: 'output_text', blockId: event.data.blockId, order: (message.contentBlocks?.length ?? 0) + 1, text: event.data.delta })
    syncLegacyProjection(message)
    message.renderRevision = (message.renderRevision ?? 0) + 1
    return
  }
  if (event.type === 'content_block.updated') {
    const blocks = message.contentBlocks ?? (message.contentBlocks = [])
    const index = blocks.findIndex((item) => blockIdOf(item) === event.data.blockId)
    if (index >= 0) blocks[index] = sanitizeContentBlock({ ...blocks[index], ...cloneJsonSafe(event.data.patch) } as typeof blocks[number])
    message.contentBlocks = sortBlocks(blocks)
    syncLegacyProjection(message)
    return
  }
  if (event.type === 'content_block.completed') {
    const blocks = message.contentBlocks ?? (message.contentBlocks = [])
    const next = sanitizeContentBlock(event.data.block)
    const index = blocks.findIndex((item) => blockIdOf(item) === (next as { blockId?: string }).blockId)
    if (index >= 0) blocks[index] = next
    else blocks.push(next)
    message.contentBlocks = sortBlocks(blocks)
    syncLegacyProjection(message)
    return
  }
  if (event.type === 'message.delta') {
    message.contentText += event.data.delta
    appendTextBlock(message, event.data.delta)
    return
  }
  if (event.type === 'reasoning.delta') { message.reasoningText = `${message.reasoningText ?? ''}${event.data.delta}`; appendReasoningBlock(message, event.data.delta); return }
  if (event.type === 'tool.started' || event.type === 'tool.updated' || event.type === 'tool.completed' || event.type === 'tool.failed' || event.type === 'tool.canceled') {
    const item = event.data.item
    const id = String(item.id ?? `${event.type}-${message.toolEvents?.length ?? 0}`)
    const status = event.type === 'tool.started'
      ? 'started'
      : event.type === 'tool.updated'
        ? 'updated'
        : event.type === 'tool.failed'
          ? 'failed'
          : event.type === 'tool.canceled'
            ? 'canceled'
            : 'completed'
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
  message.errorMessage = event.data.message
  message.traceId = event.data.traceId
}

function acceptEventVersion(message: ChatMessage, next: number, allowSnapshot = false): boolean {
  const current = message.eventVersion
  if (current === undefined) {
    if (!(allowSnapshot || next === 0 || next === 1)) return false
    message.eventVersion = next
    return true
  }
  if ((allowSnapshot ? next < current : next <= current) || (!allowSnapshot && next !== current + 1)) return false
  message.eventVersion = next
  return true
}

function sortBlocks(blocks: ChatMessage['contentBlocks']): ChatMessage['contentBlocks'] {
  return [...(blocks ?? [])].sort((left, right) => Number(('order' in left ? left.order : 0) ?? 0) - Number(('order' in right ? right.order : 0) ?? 0))
}

function blockIdOf(block: NonNullable<ChatMessage['contentBlocks']>[number]): string | undefined {
  return typeof block === 'object' && block !== null && 'blockId' in block && typeof block.blockId === 'string' ? block.blockId : undefined
}

function appendTextBlock(message: ChatMessage, delta: string): void {
  const blocks = message.contentBlocks ?? (message.contentBlocks = [])
  const last = blocks.at(-1)
  if (last?.type === 'output_text') last.text += delta
  else blocks.push({ type: 'output_text', blockId: `legacy_text_${blocks.length + 1}`, order: blocks.length + 1, text: delta })
}

function appendReasoningBlock(message: ChatMessage, delta: string): void {
  const blocks = message.contentBlocks ?? (message.contentBlocks = [])
  const last = blocks.at(-1)
  if (last?.type === 'reasoning' && last.status === 'started') last.text += delta
  else blocks.push({ type: 'reasoning', blockId: `legacy_reasoning_${blocks.length + 1}`, order: blocks.length + 1, text: delta, status: 'started' })
}

function syncLegacyProjection(message: ChatMessage): void {
  const blocks = message.contentBlocks ?? []
  message.contentText = blocks.filter((item): item is Extract<NonNullable<ChatMessage['contentBlocks']>[number], { type: 'output_text' }> => item.type === 'output_text').map((item) => item.text).join('')
  message.reasoningText = blocks.filter((item): item is Extract<NonNullable<ChatMessage['contentBlocks']>[number], { type: 'reasoning' }> => item.type === 'reasoning').map((item) => item.text).join('') || undefined
  message.toolEvents = blocks.filter((item): item is Extract<NonNullable<ChatMessage['contentBlocks']>[number], { type: 'tool_call' }> => item.type === 'tool_call').map((item) => ({ id: item.callId ?? item.id ?? item.blockId ?? 'tool', type: item.toolType, status: item.status, ...(item.item ? { item: item.item } : {}) }))
  message.renderRevision = (message.renderRevision ?? 0) + 1
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
  if (eventType === 'tool.started' || eventType === 'tool.updated' || eventType === 'tool.completed' || eventType === 'tool.failed' || eventType === 'tool.canceled') return nonEmptyString(data.messageId) && isRecord(data.item)
  if (eventType === 'content_block.started' || eventType === 'content_block.completed') return nonEmptyString(data.messageId) && isContentBlock(data.block)
  if (eventType === 'content_block.delta') return nonEmptyString(data.messageId) && nonEmptyString(data.blockId) && typeof data.delta === 'string'
  if (eventType === 'content_block.updated') return nonEmptyString(data.messageId) && nonEmptyString(data.blockId) && isSafeContentBlockPatch(data.patch)
  if (eventType === 'message.completed') return nonEmptyString(data.messageId) && optionalString(data.finishReason) && optionalString(data.traceId)
  if (eventType === 'message.failed') return nonEmptyString(data.messageId) && nonEmptyString(data.code) && typeof data.message === 'string' && optionalString(data.traceId)
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
    && (value.status === 'started' || value.status === 'updated' || value.status === 'completed' || value.status === 'failed' || value.status === 'canceled')
    && (value.item === undefined || isRecord(value.item))
}

function isContentBlock(value: unknown): value is ChatMessageContentBlock {
  if (!isRecord(value) || typeof value.type !== 'string') return false
  if (value.type === 'input_text') return typeof value.text === 'string' && validOrder(value.order)
  if (value.type === 'input_image') return nonEmptyString(value.assetId) && validOrder(value.order)
  if (value.type === 'output_text') return nonEmptyString(value.blockId) && validOrder(value.order) && typeof value.text === 'string'
  if (value.type === 'reasoning') return typeof value.text === 'string'
    && (value.blockId === undefined || nonEmptyString(value.blockId))
    && (value.order === undefined || validOrder(value.order))
    && (value.status === undefined || isProcessStatus(value.status))
  if (value.type === 'tool_call') return (nonEmptyString(value.id) || nonEmptyString(value.callId)) && nonEmptyString(value.toolType)
    && (value.blockId === undefined || nonEmptyString(value.blockId))
    && (value.callId === undefined || nonEmptyString(value.callId))
    && (value.order === undefined || validOrder(value.order))
    && isToolStatus(value.status)
    && (value.item === undefined || isRecord(value.item))
  if (value.type === 'output_image') return nonEmptyString(value.blockId) && validOrder(value.order) && nonEmptyString(value.assetId)
    && isProcessStatus(value.status)
    && optionalString(value.mimeType) && optionalInteger(value.width) && optionalInteger(value.height) && optionalString(value.revisedPrompt)
    && Object.keys(value).every((key) => ['type', 'blockId', 'order', 'assetId', 'status', 'mimeType', 'width', 'height', 'revisedPrompt'].includes(key))
  return false
}

function isSafeContentBlockPatch(value: unknown): value is Record<string, unknown> {
  if (!isRecord(value)) return false
  return !Object.keys(value).some((key) => ['data', 'base64', 'url', 'image', 'blob'].includes(key.toLowerCase()))
}

function sanitizeContentBlock(value: ChatMessageContentBlock): ChatMessageContentBlock {
  if (value.type !== 'output_image') return cloneJsonSafe(value)
  return {
    type: 'output_image',
    blockId: value.blockId,
    order: value.order,
    assetId: value.assetId,
    status: value.status,
    ...(value.mimeType ? { mimeType: value.mimeType } : {}),
    ...(value.width !== undefined ? { width: value.width } : {}),
    ...(value.height !== undefined ? { height: value.height } : {}),
    ...(value.revisedPrompt ? { revisedPrompt: value.revisedPrompt } : {})
  }
}

function isProcessStatus(value: unknown): boolean {
  return value === 'started' || value === 'completed' || value === 'failed' || value === 'canceled'
}

function isToolStatus(value: unknown): boolean {
  return isProcessStatus(value) || value === 'updated'
}

function isMessageStatus(value: unknown): boolean {
  return value === 'completed' || value === 'streaming' || value === 'failed' || value === 'canceled'
}

function optionalString(value: unknown): boolean {
  return value === undefined || typeof value === 'string'
}

function optionalInteger(value: unknown): boolean {
  return value === undefined || validOrder(value)
}

function validOrder(value: unknown): value is number {
  return Number.isSafeInteger(value) && Number(value) >= 0
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
