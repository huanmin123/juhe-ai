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
    if (eventType === 'message.started' || eventType === 'message.delta' || eventType === 'message.completed' || eventType === 'message.failed' || eventType === 'reasoning.delta' || eventType === 'tool.started' || eventType === 'tool.updated' || eventType === 'tool.completed') {
      return { type: eventType, data } as ChatStreamEvent
    }
  } catch {
  }
  return undefined
}

export function applyChatStreamEvent(messages: ChatMessage[], event: ChatStreamEvent): void {
  if (event.type === 'message.started') {
    const known = new Set(messages.map((message) => message.id))
    if (!known.has(event.data.userMessage.id)) messages.push(event.data.userMessage)
    if (!known.has(event.data.assistantMessage.id)) messages.push(event.data.assistantMessage)
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
  if (event.type !== 'message.failed') return
  message.status = 'failed'
  message.errorCode = event.data.code
}
