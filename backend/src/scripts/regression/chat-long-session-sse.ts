export interface ParsedChatLongSessionSseEvent {
  name: string
  data: Record<string, unknown>
}

export interface BoundedChatLongSessionSseParser {
  readonly eventCount: number
  push(chunk: string | Uint8Array): void
  finish(): void
}

export function resolveChatLongSessionMaxEventCount(raw: string | undefined): number {
  if (!raw?.trim()) return 65_536
  const value = Number(raw)
  if (!Number.isInteger(value) || value < 2_048 || value > 262_144) {
    throw new Error('JUHE_AI_CHAT_UPSTREAM_SSE_MAX_EVENTS 必须是 2048 到 262144 的整数')
  }
  return value
}

export function createBoundedSseParser(input: {
  maxEventCount: number
  maxBufferChars: number
  onEvent: (event: ParsedChatLongSessionSseEvent) => void
}): BoundedChatLongSessionSseParser {
  let buffer = ''
  let eventCount = 0
  const decoder = new TextDecoder()

  const drain = (): void => {
    while (true) {
      const boundary = nextBoundary(buffer)
      if (!boundary) return
      const raw = buffer.slice(0, boundary.index)
      buffer = buffer.slice(boundary.index + boundary.length)
      const event = parseSseEvent(raw)
      if (!event) continue
      if (eventCount >= input.maxEventCount) throw new Error('chat_long_session_sse_event_count_exceeded')
      eventCount += 1
      input.onEvent(event)
    }
  }

  return {
    get eventCount() { return eventCount },
    push(chunk: string | Uint8Array): void {
      buffer += typeof chunk === 'string' ? chunk : decoder.decode(chunk, { stream: true })
      buffer = normalizePendingLineEndings(buffer, false)
      drain()
      if (buffer.length > input.maxBufferChars) throw new Error('chat_long_session_sse_event_buffer_exceeded')
    },
    finish(): void {
      buffer += decoder.decode()
      buffer = normalizePendingLineEndings(buffer, true)
      if (buffer.trim()) {
        buffer += '\n\n'
        drain()
      }
    }
  }
}

function nextBoundary(buffer: string): { index: number; length: number } | undefined {
  const index = buffer.indexOf('\n\n')
  return index < 0 ? undefined : { index, length: 2 }
}

function parseSseEvent(raw: string): ParsedChatLongSessionSseEvent | null {
  let name = 'message'
  const dataLines: string[] = []
  for (const line of raw.split('\n')) {
    if (line.startsWith('event:')) name = line.slice(6).trim()
    if (line.startsWith('data:')) dataLines.push(line.slice(5).trimStart())
  }
  if (!dataLines.length) return null
  try {
    const parsed = JSON.parse(dataLines.join('\n'))
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed) || (Object.getPrototypeOf(parsed) !== Object.prototype && Object.getPrototypeOf(parsed) !== null)) {
      throw new ChatLongSessionSseShapeError()
    }
    return { name, data: parsed as Record<string, unknown> }
  } catch (error) {
    if (error instanceof ChatLongSessionSseShapeError) throw error
    if (name === 'message.completed') throw new Error('chat_long_session_sse_terminal_json_invalid')
    throw new Error('chat_long_session_sse_json_invalid')
  }
}

class ChatLongSessionSseShapeError extends Error {
  constructor() { super('chat_long_session_sse_data_not_record') }
}

function normalizePendingLineEndings(value: string, flush: boolean): string {
  const withoutCrlf = value.replace(/\r\n/g, '\n')
  return flush ? withoutCrlf.replace(/\r/g, '\n') : withoutCrlf.replace(/\r(?!$)/g, '\n')
}
