import type { ChatGenerationSubscriber } from './chat-generation-runner.js'

export interface ChatSseResponse {
  destroyed: boolean
  writableEnded: boolean
  write(chunk: string): boolean
  end(): void
}

export function writeChatSseEvent(response: ChatSseResponse, event: string, data: unknown): boolean {
  if (response.destroyed || response.writableEnded) return false
  try {
    return response.write(`event: ${event}\ndata: ${JSON.stringify(data)}\n\n`)
  } catch {
    return false
  }
}

export function createChatSseSubscriber(input: {
  response: ChatSseResponse
  detach(): void
}): ChatGenerationSubscriber {
  let detached = false
  const detach = (): void => {
    if (detached) return
    detached = true
    try { input.detach() } catch {}
    try { if (!input.response.writableEnded) input.response.end() } catch {}
  }
  return {
    trySend(event) {
      if (detached || input.response.destroyed || input.response.writableEnded) {
        detach()
        return false
      }
      try {
        const writable = writeChatSseEvent(input.response, event.type, { ...event.data, eventVersion: event.eventVersion })
        if (!writable) {
          detach()
          return false
        }
        if (event.type === 'message.completed' || event.type === 'message.failed' || event.type === 'message.canceled') {
          queueMicrotask(detach)
        }
        return true
      } catch {
        detach()
        return false
      }
    }
  }
}
