export type ChatToolRuntimeEnvironment = 'development' | 'test' | 'production'
export type ChatToolExecutionKind = 'inline' | 'network_adapter' | 'worker'
export type ChatToolDuplicatePolicy = 'reuse_exact' | 'allow_repeat'
export type ChatToolExecutionOwner = 'application'

export interface ChatToolLimits {
  maxArgumentBytes: number
  maxResultBytes: number
  timeoutMs: number
}

export interface ChatToolAvailabilityPolicy {
  environments?: readonly ChatToolRuntimeEnvironment[]
  requiresInternalToolsEnabled?: boolean
  requiresImageGenerationEnabled?: boolean
}

export interface ChatToolExecutionContext {
  environment: ChatToolRuntimeEnvironment
  ownerId: string
  conversationId: string
  turnId: string
  assistantMessageId: string
  signal: AbortSignal
  apiKey?: string
  gatewayBaseUrl?: string
  traceId?: string
  userContent?: string
  defaultImageModel?: ChatImageModel
  loadImageEditReferences?: (assetIds: readonly string[]) => Promise<ChatImageEditReference[]>
  imageGeneration?: (input: {
    operation: ChatImageGenerationOperation
    model: string
    prompt: string
    size: string
    allowLarge: boolean
    quality: string
    outputFormat: string
    references: readonly ChatImageEditReference[]
    signal: AbortSignal
  }) => Promise<ChatImageToolTransportResult>
  artifactSink?: ChatGeneratedImageArtifactSink
}

export interface ChatImageToolTransportResult {
  path: string
  bytes: number
  sha256: string
  mimeType: 'image/jpeg' | 'image/png' | 'image/webp'
  width: number
  height: number
  revisedPrompt?: string
}

export interface ChatGeneratedImageArtifactSink {
  commitGeneratedImage(input: {
    result: ChatImageToolTransportResult
    generation: {
      operation: ChatImageGenerationOperation
      model: ChatImageModel
      prompt: string
      sourceAssetIds: readonly string[]
      size: string
      quality: string
      outputFormat: string
    }
  }): Promise<{
    assetId: string
    mimeType: string
    width: number
    height: number
    bytes: number
    previewMimeType: string
    previewWidth: number
    previewHeight: number
    previewBytes: number
  }>
}

export interface ChatToolExecutionResult {
  modelOutput: string
  publicResult?: Record<string, unknown>
}

export interface ChatInternalToolDefinition {
  id: string
  version: string
  modelName: string
  title: string
  description: string
  inputSchema: Record<string, unknown>
  executionKind: ChatToolExecutionKind
  executionOwner: ChatToolExecutionOwner
  limits: ChatToolLimits
  availability: ChatToolAvailabilityPolicy
  duplicatePolicy: ChatToolDuplicatePolicy
  execute: (input: Record<string, unknown>, context: ChatToolExecutionContext) => Promise<ChatToolExecutionResult>
  projectResult: (result: ChatToolExecutionResult) => Record<string, unknown> | undefined
}

export interface ChatToolCall {
  callId: string
  toolName: string
  argumentsJson: string
  sourceOrder: number
}

export interface ChatToolExecutionEvent {
  status: 'started' | 'completed' | 'failed' | 'canceled'
  callId: string
  toolName: string
  executionOwner: ChatToolExecutionOwner
  publicResult?: Record<string, unknown>
  errorCode?: string
  reused?: boolean
}

export interface ChatToolExecutionOutput {
  callId: string
  toolName: string
  modelOutput: string
  publicResult?: Record<string, unknown>
  reused: boolean
}
import type { ChatImageEditReference } from '../chat-image-edit-references.js'
import type { ChatImageGenerationOperation } from '../../../storage/chat-image-generations.repository.js'
import type { ChatImageModel } from '../../../storage/chat.repository.js'
