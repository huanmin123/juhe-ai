import type { ChatGenerationSubscriber } from './chat-generation-runner.js'

export interface ChatSseResponse {
  destroyed: boolean
  writableEnded: boolean
  write(chunk: string): boolean
  end(): void
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
        const writable = input.response.write(`event: ${event.type}\ndata: ${JSON.stringify(event.data)}\n\n`)
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
