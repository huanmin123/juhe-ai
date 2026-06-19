import type { Request } from 'express'

import type { AccountSupportedEndpointMode } from '../../../../domain/types.js'
import type { ParsedUsage } from '../../usage/types.js'
import type {
  ResponseEndpointFamily,
  ResponseProtocolCode,
  ResponseSemanticFrame
} from '../openai-v1/response-semantics.js'

export interface GatewayProtocolDriverRequestShape {
  endpoint?: string
  stream: boolean
}

export type GatewayProtocolClientErrorProtocol = 'openai' | 'anthropic'
export type GatewayProtocolDefaultClientProfile = 'generic_openai' | 'generic_anthropic'

export interface GatewayStreamUsageFallbackInput {
  outputReceived: boolean
  estimatedOutputTokens?: number
}

export interface GatewayStreamUsageFallbackResult {
  usage: ParsedUsage
  estimated: boolean
  estimatedInputTokens?: number
  estimatedOutputTokens?: number
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
  isEndpointCapabilityFailure?(req: Request, statusCode: number): boolean
  responseEndpointFamilyForRequest(req: Request): ResponseEndpointFamily
  extractJsonSemanticFrames(value: unknown, req: Request): ResponseSemanticFrame[]
  parseUsageFromJsonBuffer(responseBody: Buffer): ParsedUsage
  parseUsageFromJsonTextFragment(text?: string): ParsedUsage
  applyStreamUsageFallback(
    req: Request,
    usage: ParsedUsage,
    input: GatewayStreamUsageFallbackInput
  ): GatewayStreamUsageFallbackResult
}
