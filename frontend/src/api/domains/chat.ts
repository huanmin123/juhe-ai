import { apiUrl, http, readFetchErrorMessage, unwrap } from '../http'
import type { ChatApiKeyOption, ChatConversation, ChatMessage, ChatModelOption, ChatReasoningEffort, ChatServiceTier, ChatStreamEvent } from '@/types/domain/chat'
import { parseChatSseBlock } from '@/views/chat/chatStream'

export const chatApi = {
  listApiKeys: () => unwrap<ChatApiKeyOption[]>(http.get('/my-chat/api-keys')),
  listConversations: (params?: { beforeLastMessageAt?: string; beforeId?: string; limit?: number }) => unwrap<ChatConversation[]>(http.get('/my-chat/conversations', { params })),
  createConversation: (apiKeyId: string) => unwrap<ChatConversation>(http.post('/my-chat/conversations', { apiKeyId })),
  listMessages: (conversationId: string, params?: { beforeSequenceNo?: number; limit?: number }) => unwrap<ChatMessage[]>(http.get(`/my-chat/conversations/${conversationId}/messages`, { params })),
  listModels: (conversationId: string) => unwrap<ChatModelOption[]>(http.get(`/my-chat/conversations/${conversationId}/models`)),
  updateConversation: (conversationId: string, payload: { title?: string; isPinned?: boolean }) => unwrap<ChatConversation>(http.patch(`/my-chat/conversations/${conversationId}`, payload)),
  stop: (conversationId: string) => unwrap<{ stopped: boolean }>(http.post(`/my-chat/conversations/${conversationId}/stop`)),
  deleteConversation: (conversationId: string) => http.delete(`/my-chat/conversations/${conversationId}`)
}

export async function streamChatMessage(input: {
  conversationId: string
  clientMessageId: string
  content: string
  contentBlocks?: Array<{ type: 'input_text' | 'input_image'; text?: string; dataUrl?: string }>
  model: string
  reasoningEffort?: ChatReasoningEffort
  serviceTier?: ChatServiceTier
  contextWindowTokens?: number
  signal: AbortSignal
  onEvent: (event: ChatStreamEvent) => void
}): Promise<void> {
  const path = `/my-chat/conversations/${input.conversationId}/stream`
  const response = await fetch(apiUrl(path), {
    method: 'POST',
    credentials: 'include',
    headers: { 'content-type': 'application/json', accept: 'text/event-stream' },
    body: JSON.stringify({ clientMessageId: input.clientMessageId, content: input.content, contentBlocks: input.contentBlocks, model: input.model, reasoningEffort: input.reasoningEffort, serviceTier: input.serviceTier, contextWindowTokens: input.contextWindowTokens }),
    signal: input.signal
  })
  if (!response.ok || !response.body) throw new Error(await readFetchErrorMessage(response, path))
  const reader = response.body.getReader()
  const decoder = new TextDecoder('utf-8', { fatal: true })
  let buffer = ''
  try {
    while (true) {
      const next = await reader.read()
      if (next.done) break
      buffer += decoder.decode(next.value, { stream: true })
      let boundary = findBoundary(buffer)
      while (boundary) {
        const event = parseChatSseBlock(buffer.slice(0, boundary.index))
        buffer = buffer.slice(boundary.index + boundary.length)
        if (event) input.onEvent(event)
        boundary = findBoundary(buffer)
      }
    }
    buffer += decoder.decode()
    const event = parseChatSseBlock(buffer)
    if (event) input.onEvent(event)
  } finally {
    reader.releaseLock()
  }
}

function findBoundary(value: string): { index: number; length: number } | undefined {
  const lf = value.indexOf('\n\n')
  const crlf = value.indexOf('\r\n\r\n')
  if (lf < 0 && crlf < 0) return undefined
  return crlf >= 0 && (lf < 0 || crlf < lf) ? { index: crlf, length: 4 } : { index: lf, length: 2 }
}
