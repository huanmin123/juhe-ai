import { apiUrl, http, readFetchErrorMessage, unwrap } from '../http'
import type { ChatApiKeyOption, ChatAsset, ChatContextStatus, ChatConversation, ChatMessage, ChatModelOption, ChatReasoningEffort, ChatServiceTier, ChatStreamEvent } from '@/types/domain/chat'
import { parseChatSseBlock } from '@/views/chat/chatStream'

export const chatApi = {
  listApiKeys: () => unwrap<ChatApiKeyOption[]>(http.get('/my-chat/api-keys')),
  listConversations: (params?: { beforeIsPinned?: boolean; beforeLastMessageAt?: string; beforeId?: string; limit?: number }) => unwrap<ChatConversation[]>(http.get('/my-chat/conversations', { params })),
  createConversation: (apiKeyId: string) => unwrap<ChatConversation>(http.post('/my-chat/conversations', { apiKeyId })),
  getConversation: (conversationId: string) => unwrap<ChatConversation>(http.get(`/my-chat/conversations/${conversationId}`)),
  listMessages: (conversationId: string, params?: { beforeSequenceNo?: number; limit?: number }) => unwrap<ChatMessage[]>(http.get(`/my-chat/conversations/${conversationId}/messages`, { params })),
  listModels: (conversationId: string) => unwrap<ChatModelOption[]>(http.get(`/my-chat/conversations/${conversationId}/models`)),
  getContextStatus: (conversationId: string) => unwrap<ChatContextStatus>(http.get(`/my-chat/conversations/${conversationId}/context-status`)),
  uploadAsset: (
    conversationId: string,
    file: File,
    options?: { signal?: AbortSignal; onProgress?: (percent: number) => void }
  ) => {
    const body = new FormData()
    body.append('file', file, file.name)
    return unwrap<ChatAsset>(http.post(`/my-chat/conversations/${conversationId}/assets`, body, {
      signal: options?.signal,
      timeout: 0,
      onUploadProgress: (event) => {
        if (!event.total || event.total <= 0) return
        options?.onProgress?.(Math.min(100, Math.max(0, Math.round((event.loaded / event.total) * 100))))
      }
    }))
  },
  updateConversation: (conversationId: string, payload: { title?: string; isPinned?: boolean }) => unwrap<ChatConversation>(http.patch(`/my-chat/conversations/${conversationId}`, payload)),
  stop: (conversationId: string) => unwrap<{ stopped: boolean }>(http.post(`/my-chat/conversations/${conversationId}/stop`)),
  deleteConversation: (conversationId: string) => http.delete(`/my-chat/conversations/${conversationId}`)
}

export function chatAssetContentUrl(conversationId: string, assetId: string): string {
  return apiUrl(`/my-chat/conversations/${encodeURIComponent(conversationId)}/assets/${encodeURIComponent(assetId)}/content`)
}

export class ChatStreamHttpError extends Error {
  constructor(
    public readonly status: number,
    public readonly code: string | undefined,
    message: string
  ) {
    super(message)
    this.name = 'ChatStreamHttpError'
  }
}

export async function streamChatMessage(input: {
  conversationId: string
  clientMessageId: string
  replaceTurnId?: string
  content: string
  contentBlocks?: Array<{ type: 'input_text'; text: string } | { type: 'input_image'; assetId: string }>
  model: string
  reasoningEffort?: ChatReasoningEffort
  serviceTier?: ChatServiceTier
  signal: AbortSignal
  onEvent: (event: ChatStreamEvent) => void
}): Promise<void> {
  const path = `/my-chat/conversations/${input.conversationId}/stream`
  const response = await fetch(apiUrl(path), {
    method: 'POST',
    credentials: 'include',
    headers: { 'content-type': 'application/json', accept: 'text/event-stream' },
    body: JSON.stringify({ clientMessageId: input.clientMessageId, replaceTurnId: input.replaceTurnId, content: input.content, contentBlocks: input.contentBlocks, model: input.model, reasoningEffort: input.reasoningEffort, serviceTier: input.serviceTier }),
    signal: input.signal
  })
  if (!response.ok || !response.body) {
    const message = await readFetchErrorMessage(response.clone(), path)
    const code = await readChatErrorCode(response)
    throw new ChatStreamHttpError(response.status, code, message)
  }
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

async function readChatErrorCode(response: Response): Promise<string | undefined> {
  try {
    const payload = JSON.parse(await response.text()) as { code?: unknown }
    return typeof payload.code === 'string' && payload.code.trim() ? payload.code : undefined
  } catch {
    return undefined
  }
}

function findBoundary(value: string): { index: number; length: number } | undefined {
  const lf = value.indexOf('\n\n')
  const crlf = value.indexOf('\r\n\r\n')
  if (lf < 0 && crlf < 0) return undefined
  return crlf >= 0 && (lf < 0 || crlf < lf) ? { index: crlf, length: 4 } : { index: lf, length: 2 }
}
