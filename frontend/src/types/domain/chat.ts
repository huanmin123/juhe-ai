export type ChatMessageRole = 'user' | 'assistant'
export type ChatMessageStatus = 'completed' | 'streaming' | 'failed' | 'canceled'

export interface ChatConversation {
  id: string
  systemAccountId: string
  apiKeyId?: string
  apiKeyNameSnapshot: string
  title: string
  lastModel?: string
  activeTurnId?: string
  lastMessageAt: string
  createdAt: string
  updatedAt: string
}

export interface ChatMessage {
  id: string
  conversationId: string
  turnId: string
  sequenceNo: number
  clientMessageId?: string
  role: ChatMessageRole
  status: ChatMessageStatus
  contentText: string
  contentBlocks?: ChatMessageContentBlock[]
  model: string
  traceId?: string
  finishReason?: string
  errorCode?: string
  createdAt: string
  completedAt?: string
  expiresAt: string
  reasoningText?: string
  toolEvents?: ChatToolEvent[]
}

export type ChatMessageContentBlock =
  | { type: 'reasoning'; text: string }
  | { type: 'tool_call'; id: string; toolType: string; status: ChatToolStatus; item?: Record<string, unknown> }

export type ChatToolStatus = 'started' | 'updated' | 'completed' | 'failed'
export interface ChatToolEvent { id: string; type: string; status: ChatToolStatus; item?: Record<string, unknown> }

export interface ChatApiKeyOption { id: string; name: string; status: string }

export type ChatStreamEvent =
  | { type: 'message.started'; data: { turnId: string; userMessage: ChatMessage; assistantMessage: ChatMessage } }
  | { type: 'message.delta'; data: { messageId: string; delta: string } }
  | { type: 'reasoning.delta'; data: { messageId: string; delta: string } }
  | { type: 'tool.started' | 'tool.updated' | 'tool.completed'; data: { messageId: string; item: Record<string, unknown> } }
  | { type: 'message.completed'; data: { messageId: string; finishReason?: string; traceId?: string } }
  | { type: 'message.failed'; data: { messageId: string; code: string; message: string } }
