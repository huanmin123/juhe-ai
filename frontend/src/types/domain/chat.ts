export type ChatMessageRole = 'user' | 'assistant'
export type ChatMessageStatus = 'completed' | 'streaming' | 'failed' | 'canceled'
export type ChatImageModel = 'gpt-image-2' | 'grok-imagine-image' | 'grok-imagine-image-quality'
export type ChatConversationToolId = 'web_search' | 'generate_image' | 'diagnostic_echo'
export type ChatConversationToolKind = 'code' | 'model'

/**
 * 模型工具的会话级绑定候选/绑定条目（「账号 + 模型」二元组，工具体系设计 §6.3/§8.1）。
 */
export interface ChatToolBindingCandidate {
  accountId: string
  accountName: string
  modelId: string
  modelName: string
}

/**
 * 单个工具的绑定状态（工具体系设计 §8.1/§8.3）：bound/binding/valid/candidates；
 * code 工具无绑定概念（仅列出 id/kind）。valid=false 时 invalidReason 说明原因。
 */
export interface ChatConversationToolCapability {
  id: ChatConversationToolId
  kind: ChatConversationToolKind
  bound?: boolean
  binding?: ChatToolBindingCandidate | null
  valid?: boolean
  invalidReason?: string
  candidates?: ChatToolBindingCandidate[]
}

export interface ChatConversationToolCapabilities {
  tools: ChatConversationToolCapability[]
}

export interface ChatConversation {
  id: string
  systemAccountId: string
  apiKeyId?: string
  apiKeyNameSnapshot: string
  bindAccountId?: string
  bindAccountName?: string
  /** 存量旧模式（api_key/group）会话的一次性迁移只读标记；true 时发送入口禁用。 */
  archived: boolean
  /** 模型工具的会话级绑定列（空 = 未绑定，工具体系设计 §7）。 */
  searchAccountId?: string
  searchModelId?: string
  imageAccountId?: string
  defaultModel?: ChatModelListOption
  title: string
  isPinned: boolean
  lastModel?: string
  defaultImageModel: ChatImageModel
  toolCapabilities?: ChatConversationToolCapabilities
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
  errorMessage?: string
  createdAt: string
  completedAt?: string
  expiresAt: string
  reasoningText?: string
  toolEvents?: ChatToolEvent[]
  eventVersion?: number
  renderRevision?: number
}

export type ChatMessageContentBlock =
  | { type: 'output_text'; blockId?: string; order: number; text: string }
  | { type: 'reasoning'; blockId?: string; order?: number; text: string; status?: ChatProcessStatus }
  | { type: 'tool_call'; blockId?: string; order?: number; id?: string; callId?: string; toolType: string; status: ChatToolStatus; item?: Record<string, unknown> }
  | { type: 'output_image'; blockId: string; order: number; assetId: string; status: ChatProcessStatus; mimeType?: string; width?: number; height?: number; revisedPrompt?: string }
  | { type: 'input_text'; text: string; order: number }
  | { type: 'input_image'; assetId: string; order: number }

export type ChatProcessStatus = 'started' | 'completed' | 'failed' | 'canceled'
export type ChatToolStatus = ChatProcessStatus | 'updated'
export interface ChatToolEvent { id: string; type: string; status: ChatToolStatus; item?: Record<string, unknown> }

export interface ChatAsset {
  id: string
  fileName: string
  mimeType: string
  width: number
  height: number
  byteSize: number
}

export interface ChatImageOptimizationPolicy {
  mimeType: 'image/webp'
  maxEdge: number
  quality: number
  maxBytes: number
}

export interface ChatImagePolicy {
  input: ChatImageOptimizationPolicy
}

export interface ChatContextStatus {
  usedTokens: number
  limitTokens?: number
  ratio: number
  state: 'ready' | 'compact_pending' | 'compacting' | 'compact_failed'
  usageEstimated: boolean
  compactedThroughSequence: number
  revision: number
  errorCode?: string
  retryAt?: string
  attemptCount: number
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
  | { state: 'preparing'; phase: 'preparing' | 'accepting'; serverTime: string }
  | { state: 'not_found'; serverTime: string }
  | {
      state: 'accepted'
      turnId: string
      assistantMessageId: string
      assistantStatus: ChatMessageStatus
      runnerState: 'running' | 'missing' | 'terminal'
      eventVersion?: number
      lastSemanticActivityAt?: string
      errorCode?: string
      errorMessage?: string
      traceId?: string
      completedAt?: string
      serverTime: string
    }

export type ChatReasoningEffort = 'minimal' | 'low' | 'medium' | 'high' | 'xhigh' | 'max'
export type ChatServiceTier = 'default' | 'priority' | 'flex'
export type ChatGenerationParameter = 'temperature' | 'topP' | 'frequencyPenalty' | 'presencePenalty' | 'maxOutputTokens' | 'seed'
export type ChatGenerationParameters = Partial<Record<ChatGenerationParameter, number>>
export interface ChatGenerationParameterCapability {
  parameter: ChatGenerationParameter
  min: number
  max: number
  step: number
  defaultValue: number
}
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
  /** 「协议 × 工具」矩阵（工具体系设计 6.4）：键为协议枚举，值为该协议下可用工具集。一维 supportedTools 已退场。 */
  supportedToolsByProtocol: Record<string, string[]>
  generationParameters: ChatGenerationParameterCapability[]
}

export type ChatStreamEvent =
  | { type: 'message.started'; data: { turnId: string; userMessage: ChatMessage; assistantMessage: ChatMessage } }
  | { type: 'message.snapshot'; data: { turnId: string; assistant: ChatStreamAssistantSnapshot; eventVersion: number } }
  | { type: 'message.delta'; data: { messageId: string; delta: string; eventVersion: number } }
  | { type: 'reasoning.delta'; data: { messageId: string; delta: string; eventVersion: number } }
  | { type: 'tool.started' | 'tool.updated' | 'tool.completed' | 'tool.failed' | 'tool.canceled'; data: { messageId: string; item: Record<string, unknown>; eventVersion: number } }
  | { type: 'tool.binding_required'; data: { messageId: string; item: { callId?: string; toolId: string; candidates?: ChatToolBindingCandidate[]; [key: string]: unknown }; eventVersion: number } }
  | { type: 'content_block.started'; data: { messageId: string; block: ChatMessageContentBlock; eventVersion: number } }
  | { type: 'content_block.delta'; data: { messageId: string; blockId: string; delta: string; eventVersion: number } }
  | { type: 'content_block.updated'; data: { messageId: string; blockId: string; patch: Partial<ChatMessageContentBlock>; eventVersion: number } }
  | { type: 'content_block.completed'; data: { messageId: string; block: ChatMessageContentBlock; eventVersion: number } }
  | { type: 'message.completed'; data: { messageId: string; finishReason?: string; traceId?: string; eventVersion: number } }
  | { type: 'message.failed'; data: { messageId: string; code: string; message: string; traceId?: string; eventVersion: number } }
  | { type: 'message.canceled'; data: { messageId: string; traceId?: string; eventVersion: number } }

export interface ChatStreamAssistantSnapshot {
  id: string
  status: ChatMessageStatus
  contentText: string
  reasoningText: string
  toolEvents: ChatToolEvent[]
  contentBlocks: ChatMessageContentBlock[]
}
