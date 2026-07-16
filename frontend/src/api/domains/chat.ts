import { apiUrl, http, readFetchErrorMessage, unwrap } from '../http'
import type { ChatApiKeyOption, ChatAsset, ChatContextStatus, ChatConversation, ChatConversationSyncHead, ChatMessage, ChatModelOption, ChatReasoningEffort, ChatServiceTier, ChatStreamEvent, ChatSubmissionStatus } from '@/types/domain/chat'
import { parseChatSseBlock } from '@/views/chat/chatStream'

export const chatApi = {
  listApiKeys: () => unwrap<ChatApiKeyOption[]>(http.get('/my-chat/api-keys')),
  listConversations: (params?: { beforeIsPinned?: boolean; beforeLastMessageAt?: string; beforeId?: string; limit?: number }) => unwrap<ChatConversation[]>(http.get('/my-chat/conversations', { params })),
  createConversation: (apiKeyId: string) => unwrap<ChatConversation>(http.post('/my-chat/conversations', { apiKeyId })),
  getConversation: (conversationId: string) => unwrap<ChatConversation>(http.get(`/my-chat/conversations/${encodeURIComponent(conversationId)}`)),
  listMessages: (conversationId: string, params?: ChatMessageListParams) => unwrap<ChatMessage[]>(http.get(`/my-chat/conversations/${encodeURIComponent(conversationId)}/messages`, { params })),
  getConversationSync: (conversationId: string, knownRevision?: number) => unwrap<ChatConversationSyncHead>(http.get(`/my-chat/conversations/${encodeURIComponent(conversationId)}/sync`, { params: { knownRevision: knownRevision ?? 0 } })),
  getSubmissionStatus: (conversationId: string, clientMessageId: string) => unwrap<ChatSubmissionStatus>(http.get(`/my-chat/conversations/${encodeURIComponent(conversationId)}/submissions/${encodeURIComponent(clientMessageId)}`)),
  listModels: (conversationId: string) => unwrap<ChatModelOption[]>(http.get(`/my-chat/conversations/${encodeURIComponent(conversationId)}/models`)),
  getContextStatus: (conversationId: string) => unwrap<ChatContextStatus>(http.get(`/my-chat/conversations/${encodeURIComponent(conversationId)}/context-status`)),
  uploadAsset: (
    conversationId: string,
    file: File,
    options?: { signal?: AbortSignal; onProgress?: (percent: number) => void }
  ) => {
    const body = new FormData()
    body.append('file', file, file.name)
    return unwrap<ChatAsset>(http.post(`/my-chat/conversations/${encodeURIComponent(conversationId)}/assets`, body, {
      signal: options?.signal,
      timeout: 0,
      onUploadProgress: (event) => {
        if (!event.total || event.total <= 0) return
        options?.onProgress?.(Math.min(100, Math.max(0, Math.round((event.loaded / event.total) * 100))))
      }
    }))
  },
  deleteAsset: (conversationId: string, assetId: string) => http.delete(`/my-chat/conversations/${encodeURIComponent(conversationId)}/assets/${encodeURIComponent(assetId)}`),
  updateConversation: (conversationId: string, payload: { title?: string; isPinned?: boolean }) => unwrap<ChatConversation>(http.patch(`/my-chat/conversations/${encodeURIComponent(conversationId)}`, payload)),
  stop: (conversationId: string, target: { clientMessageId?: string; turnId?: string }) => unwrap<{ stopped: boolean }>(http.post(`/my-chat/conversations/${encodeURIComponent(conversationId)}/stop`, target)),
  deleteConversation: (conversationId: string) => http.delete(`/my-chat/conversations/${encodeURIComponent(conversationId)}`)
}

type ChatMessageCursor =
  | { beforeSequenceNo: number; afterSequenceNo?: never; fromSequenceNo?: never }
  | { beforeSequenceNo?: never; afterSequenceNo: number; fromSequenceNo?: never }
  | { beforeSequenceNo?: never; afterSequenceNo?: never; fromSequenceNo: number }
  | { beforeSequenceNo?: never; afterSequenceNo?: never; fromSequenceNo?: never }

export type ChatMessageListParams = ChatMessageCursor & { limit?: number }

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

export class ChatStreamProtocolError extends Error {
  constructor(message: string) {
    super(message)
    this.name = 'ChatStreamProtocolError'
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
  signal?: AbortSignal
  onEvent: (event: ChatStreamEvent) => void
}): Promise<void> {
  const path = `/my-chat/conversations/${encodeURIComponent(input.conversationId)}/stream`
  const response = await fetch(apiUrl(path), {
    method: 'POST',
    credentials: 'include',
    headers: { 'content-type': 'application/json', accept: 'text/event-stream' },
    body: JSON.stringify({ clientMessageId: input.clientMessageId, replaceTurnId: input.replaceTurnId, content: input.content, contentBlocks: input.contentBlocks, model: input.model, reasoningEffort: input.reasoningEffort, serviceTier: input.serviceTier }),
    signal: input.signal
  })
  await consumeChatSseResponse(response, path, input.onEvent)
}

export async function attachChatStream(input: {
  conversationId: string
  turnId: string
  signal?: AbortSignal
  onEvent: (event: ChatStreamEvent) => void
}): Promise<void> {
  const path = `/my-chat/conversations/${encodeURIComponent(input.conversationId)}/streams/${encodeURIComponent(input.turnId)}`
  const response = await fetch(apiUrl(path), {
    method: 'GET',
    credentials: 'include',
    headers: { accept: 'text/event-stream' },
    signal: input.signal
  })
  await consumeChatSseResponse(response, path, input.onEvent)
}

async function consumeChatSseResponse(response: Response, path: string, onEvent: (event: ChatStreamEvent) => void): Promise<void> {
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
        const block = buffer.slice(0, boundary.index)
        buffer = buffer.slice(boundary.index + boundary.length)
        consumeChatSseBlock(block, onEvent)
        boundary = findBoundary(buffer)
      }
    }
    buffer += decoder.decode()
    consumeChatSseBlock(buffer, onEvent)
  } finally {
    reader.releaseLock()
  }
}

function consumeChatSseBlock(block: string, onEvent: (event: ChatStreamEvent) => void): void {
  const trimmed = block.trim()
  if (!trimmed || trimmed.startsWith(':')) return
  const event = parseChatSseBlock(block)
  if (!event) throw new ChatStreamProtocolError('收到格式无效的聊天流事件')
  onEvent(event)
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
