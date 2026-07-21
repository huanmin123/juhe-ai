export type ChatMessageRole = 'user' | 'assistant'
export type ChatMessageStatus = 'completed' | 'streaming' | 'failed' | 'canceled'

export interface ChatConversation {
  id: string
  systemAccountId: string
  apiKeyId?: string
  apiKeyNameSnapshot: string
  defaultModel?: ChatModelListOption
  title: string
  isPinned: boolean
  lastModel?: string
  activeTurnId?: string
  userTurnCount: number
  messageRevision: number
  userTurnLimit: number
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
  | { type: 'input_text'; text: string; order: number }
  | { type: 'input_image'; assetId: string; order: number }
  | { type: 'output_text'; blockId: string; order: number; text: string }
  | { type: 'reasoning'; text: string; blockId?: string; order?: number; status?: ChatContentBlockStatus }
  | { type: 'tool_call'; id: string; toolType: string; status: ChatToolStatus; blockId?: string; order?: number; callId?: string; item?: Record<string, unknown> }
  | { type: 'output_image'; blockId: string; order: number; assetId: string; status: ChatContentBlockStatus; mimeType?: string; width?: number; height?: number; revisedPrompt?: string }

export type ChatContentBlockStatus = 'started' | 'updated' | 'completed' | 'failed' | 'canceled'
export type ChatToolStatus = ChatContentBlockStatus
export interface ChatToolEvent { id: string; type: string; status: ChatToolStatus; item?: Record<string, unknown> }

export interface ChatApiKeyOption { id: string; name: string; status: string }

export interface ChatAsset {
  id: string
  fileName: string
  mimeType: string
  width: number
  height: number
  byteSize: number
}

export interface ChatContextStatus {
  usedTokens: number
  limitTokens?: number
  ratio: number
  state: 'ready' | 'compact_pending' | 'compacting' | 'compact_failed'
  usageEstimated: boolean
  compactedThroughSequence: number
  revision: number
}

export interface ChatMessageTail {
  id: string
  turnId: string
  sequenceNo: number
  role: ChatMessageRole
  status: ChatMessageStatus
  completedAt?: string
  expiresAt: string
}

export interface ChatConversationActiveTurn {
  turnId: string
  assistantMessageId: string
  startedAt: string
}

export interface ChatConversationSyncHead {
  serverTime: string
  unchanged: boolean
  conversationId: string
  messageRevision: number
  lastSequenceNo: number
  activeTurn?: ChatConversationActiveTurn
  tail: ChatMessageTail[]
}

export type ChatSubmissionStatus =
  | { state: 'preparing' }
  | { state: 'not_found' }
  | { state: 'accepted'; turnId: string; assistantStatus: ChatMessageStatus }

export type ChatReasoningEffort = 'minimal' | 'low' | 'medium' | 'high' | 'xhigh' | 'max'
export type ChatServiceTier = 'default' | 'priority' | 'flex'
export interface ChatModelListOption {
  id: string
  name: string
}

export interface ChatModelCapabilities {
  id: string
  name: string
  supportsPromptCaching: boolean
  supportedReasoningEfforts: ChatReasoningEffort[]
  defaultReasoningEffort?: ChatReasoningEffort
  supportedServiceTiers: ChatServiceTier[]
  contextWindowTokens?: number
  maxInputTokens?: number
  maxOutputTokens?: number
  supportedApiProtocols: string[]
  inputModalities: string[]
  outputModalities: string[]
  supportedTools: string[]
}

export type ChatStreamEvent =
  | { type: 'message.started'; data: { turnId: string; userMessage: ChatMessage; assistantMessage: ChatMessage } }
  | { type: 'message.snapshot'; data: { turnId: string; assistant: ChatStreamAssistantSnapshot; eventVersion: number } }
  | { type: 'message.delta'; data: { messageId: string; delta: string; eventVersion: number } }
  | { type: 'reasoning.delta'; data: { messageId: string; delta: string; eventVersion: number } }
  | { type: 'tool.started' | 'tool.updated' | 'tool.completed'; data: { messageId: string; item: Record<string, unknown>; eventVersion: number } }
  | { type: 'content_block.started'; data: { messageId: string; block: ChatMessageContentBlock; eventVersion: number } }
  | { type: 'content_block.delta'; data: { messageId: string; blockId: string; delta: string; eventVersion: number } }
  | { type: 'content_block.updated'; data: { messageId: string; blockId: string; patch: Record<string, unknown>; eventVersion: number } }
  | { type: 'content_block.completed'; data: { messageId: string; block: ChatMessageContentBlock; eventVersion: number } }
  | { type: 'message.completed'; data: { messageId: string; finishReason?: string; traceId?: string; eventVersion: number } }
  | { type: 'message.failed'; data: { messageId: string; code: string; message: string; eventVersion: number } }
  | { type: 'message.canceled'; data: { messageId: string; traceId?: string; eventVersion: number } }

export interface ChatStreamAssistantSnapshot {
  id: string
  status: ChatMessageStatus
  contentText: string
  reasoningText: string
  toolEvents: ChatToolEvent[]
  contentBlocks: ChatMessageContentBlock[]
}
