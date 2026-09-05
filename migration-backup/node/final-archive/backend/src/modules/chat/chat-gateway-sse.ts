import type { ChatToolCall } from './tools/contracts.js'

export interface OpenAIChatSseResult {
  content: string
  finishReason?: string
  done: boolean
  inputTokens?: number
  outputTokens?: number
  toolCalls: ChatToolCall[]
  continuationItems: unknown[]
}

export async function collectOpenAIChatSse(
  chunks: AsyncIterable<Uint8Array>,
  maxContentBytes: number,
  onDelta?: (delta: string) => void,
  maxEvents = 65_536
): Promise<OpenAIChatSseResult> {
  const decoder = new TextDecoder('utf-8', { fatal: true })
  const encoder = new TextEncoder()
  const maxEventBytes = 64 * 1024
  let buffer = ''
  let content = ''
  let finishReason: string | undefined
  let done = false
  let inputTokens: number | undefined
  let outputTokens: number | undefined
  let eventCount = 0
  const toolCallParts = new Map<number, ChatToolCallPart>()

  const consumeEvent = (eventText: string): void => {
    const dataLines = eventText.split(/\r?\n/)
      .filter((line) => line.startsWith('data:'))
      .map((line) => line.slice(5).replace(/^ /, ''))
    if (!dataLines.length) return
    const data = dataLines.join('\n')
    if (data === '[DONE]') {
      done = true
      return
    }
    let payload: {
      choices?: Array<{
        delta?: {
          content?: unknown
          tool_calls?: Array<{
            index?: unknown
            id?: unknown
            type?: unknown
            function?: { name?: unknown; arguments?: unknown }
          }>
        }
        finish_reason?: unknown
      }>
      usage?: { prompt_tokens?: unknown; completion_tokens?: unknown }
      error?: { message?: unknown }
    }
    try {
      payload = JSON.parse(data) as typeof payload
    } catch {
      throw new Error('上游返回了无效的 SSE JSON')
    }
    if (payload.error) throw new Error(String(payload.error.message ?? '上游流式请求失败'))
    const nextInputTokens = nonNegativeInteger(payload.usage?.prompt_tokens)
    const nextOutputTokens = nonNegativeInteger(payload.usage?.completion_tokens)
    if (nextInputTokens !== undefined) inputTokens = nextInputTokens
    if (nextOutputTokens !== undefined) outputTokens = nextOutputTokens
    const choice = payload.choices?.[0]
    const delta = typeof choice?.delta?.content === 'string' ? choice.delta.content : ''
    if (delta) {
      const nextBytes = Buffer.byteLength(content, 'utf8') + Buffer.byteLength(delta, 'utf8')
      if (nextBytes > maxContentBytes) throw new Error('回答内容超过 192 KiB 上限')
      content += delta
      onDelta?.(delta)
    }
    for (const toolCall of choice?.delta?.tool_calls ?? []) {
      const index = nonNegativeInteger(toolCall.index)
      if (index === undefined || index > 255) throw new Error('Chat 工具调用 index 无效')
      const existing = toolCallParts.get(index) ?? { index, id: '', name: '', arguments: '' }
      if (typeof toolCall.id === 'string') existing.id = mergeStableToolField(existing.id, toolCall.id)
      if (typeof toolCall.function?.name === 'string') existing.name = mergeStableToolField(existing.name, toolCall.function.name)
      if (typeof toolCall.function?.arguments === 'string') {
        existing.arguments += toolCall.function.arguments
        if (Buffer.byteLength(existing.arguments, 'utf8') > 64 * 1024) throw new Error('Chat 单个工具参数超过 64 KiB 上限')
      }
      toolCallParts.set(index, existing)
    }
    if (typeof choice?.finish_reason === 'string' && choice.finish_reason) finishReason = choice.finish_reason
  }

  for await (const chunk of chunks) {
    buffer += decoder.decode(chunk, { stream: true })
    let boundary = findEventBoundary(buffer)
    while (boundary) {
      const eventText = buffer.slice(0, boundary.index)
      eventCount += 1
      if (eventCount > maxEvents) throw new Error(`上游 Chat Completions 事件数量超过 ${maxEvents} 上限`)
      if (encoder.encode(eventText).byteLength > maxEventBytes) throw new Error('上游 Chat Completions 单个事件超过 64 KiB 上限')
      consumeEvent(eventText)
      buffer = buffer.slice(boundary.index + boundary.length)
      boundary = findEventBoundary(buffer)
    }
    if (encoder.encode(buffer).byteLength > maxEventBytes) throw new Error('上游 Chat Completions 单个事件超过 64 KiB 上限')
  }
  buffer += decoder.decode()
  if (encoder.encode(buffer).byteLength > maxEventBytes) throw new Error('上游 Chat Completions 单个事件超过 64 KiB 上限')
  if (buffer.trim()) {
    eventCount += 1
    if (eventCount > maxEvents) throw new Error(`上游 Chat Completions 事件数量超过 ${maxEvents} 上限`)
    consumeEvent(buffer)
  }
  if (!done) throw new Error('上游流式响应缺少 [DONE]')
  const toolCalls = [...toolCallParts.values()].sort((left, right) => left.index - right.index).map((part) => {
    if (!part.id || !part.name || !part.arguments) throw new Error('Chat 工具调用缺少 id、name 或 arguments')
    return { callId: part.id, toolName: part.name, argumentsJson: part.arguments, sourceOrder: part.index }
  })
  const continuationItems = toolCalls.length
    ? [{
        role: 'assistant',
        content: content || null,
        tool_calls: toolCalls.map((toolCall) => ({
          id: toolCall.callId,
          type: 'function',
          function: { name: toolCall.toolName, arguments: toolCall.argumentsJson }
        }))
      }]
    : []
  return { content, finishReason, done, inputTokens, outputTokens, toolCalls, continuationItems }
}

interface ChatToolCallPart {
  index: number
  id: string
  name: string
  arguments: string
}

function nonNegativeInteger(value: unknown): number | undefined {
  const number = Number(value)
  return Number.isSafeInteger(number) && number >= 0 ? number : undefined
}

function findEventBoundary(value: string): { index: number; length: number } | undefined {
  const lf = value.indexOf('\n\n')
  const crlf = value.indexOf('\r\n\r\n')
  if (lf < 0 && crlf < 0) return undefined
  if (crlf >= 0 && (lf < 0 || crlf < lf)) return { index: crlf, length: 4 }
  return { index: lf, length: 2 }
}

function mergeStableToolField(current: string, chunk: string): string {
  if (!chunk || chunk === current) return current
  if (!current) return chunk
  if (current.endsWith(chunk)) return current
  return current + chunk
}
