import type { Request } from 'express'

import type { AccountSupportedEndpointMode } from '../../../../domain/types.js'
import type { ParsedUsage } from '../../usage/types.js'
import type {
  ParsedOpenAIStreamEvent
} from '../openai-v1/stream-events.js'
import type {
  ResponseEndpointFamily,
  ResponseProtocolCode,
  ResponseSemanticFrame
} from '../openai-v1/response-semantics.js'

export interface GatewayProtocolDriverRequestShape {
  endpoint?: string
  stream: boolean
}

export type GatewayProtocolClientErrorProtocol = 'openai' | 'anthropic' | 'gemini'
export type GatewayProtocolDefaultClientProfile = 'generic_openai' | 'generic_anthropic' | 'generic_gemini'

export interface GatewayStreamUsageFallbackInput {
  completed?: boolean
  outputReceived: boolean
  estimatedOutputTokens?: number
}

export interface GatewayStreamUsageFallbackResult {
  usage: ParsedUsage
  estimated: boolean
  estimatedInputTokens?: number
  estimatedOutputTokens?: number
}

export type GatewayProtocolErrorPayload = Record<string, unknown> & {
  code?: unknown
  type?: unknown
  message?: unknown
}

export interface GatewayStreamInspection {
  terminalReceived: boolean
  failedReceived: boolean
  outputReceived: boolean
  imageOutputReceived: boolean
  outputEventCount: number
  estimatedOutputTokens?: number
  eventCount: number
  eventTypeCounts: Record<string, number>
  lastEventType?: string
  recentEventTypes: string[]
  pendingEvent: boolean
  skipped: boolean
  skipReason?: string
  errorCode?: string
  errorMessage?: string
  responseResourceId?: string
  usage: ParsedUsage
}

export interface GatewayStreamInspector {
  pushChunk(
    chunk: Buffer | Uint8Array | string,
    options?: { lightweightImageStream?: boolean }
  ): GatewayStreamInspection
  pushText(text: string): GatewayStreamInspection
  finish(): GatewayStreamInspection
  snapshot(): GatewayStreamInspection
  drainEventSummariesCanEndStream(): boolean
}

export interface GatewayProtocolDriver {
  id: string
  protocolCode: string
  protocolVersion: string
  responseProtocol: ResponseProtocolCode
  clientErrorProtocol: GatewayProtocolClientErrorProtocol
  defaultClientProfile: GatewayProtocolDefaultClientProfile
  supportsProfile(profile: { protocolCode?: string; protocolVersion?: string } | undefined): boolean
  endpointModeForRequestShape(input: GatewayProtocolDriverRequestShape): AccountSupportedEndpointMode | undefined
  isNativeRequest?(req: Request): boolean
  isModelsRequest?(req: Request): boolean
  responseEndpointFamilyForRequest(req: Request): ResponseEndpointFamily
  extractJsonSemanticFrames(value: unknown, req: Request): ResponseSemanticFrame[]
  createStreamInspector(): GatewayStreamInspector
  responseInspectionEndpointFamily(endpointFamily?: ResponseEndpointFamily): ResponseEndpointFamily
  extractSseSemanticFrames(event: ParsedOpenAIStreamEvent, endpointFamily?: ResponseEndpointFamily): ResponseSemanticFrame[]
  sseResponseInspectionFailureEvent: 'default' | 'none'
  drainForKeepAliveAfterTerminal: boolean
  parseUsageFromJsonBuffer(responseBody: Buffer): ParsedUsage
  parseUsageFromJsonTextFragment(text?: string): ParsedUsage
  parseErrorPayload(text: string, headers: Headers): GatewayProtocolErrorPayload
  applyStreamUsageFallback(
    req: Request,
    usage: ParsedUsage,
    input: GatewayStreamUsageFallbackInput
  ): GatewayStreamUsageFallbackResult
}
