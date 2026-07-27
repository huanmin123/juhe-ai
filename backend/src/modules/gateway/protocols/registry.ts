import type { Request } from 'express'

import { openAIEndpointFamilyFromPath } from '../../../domain/openai-endpoint-modes.js'
import type { ProviderProtocolProfileDefinition } from '../../../domain/provider-protocol.js'
import type { ParsedUsage } from '../usage/types.js'
import { anthropicV1ProtocolDriver } from './anthropic-v1/driver.js'
import { geminiV1BetaProtocolDriver } from './gemini-v1beta/driver.js'
import { openAIV1ProtocolDriver } from './openai-v1/driver.js'
import type {
  GatewayProtocolErrorPayload,
  GatewayProtocolClientErrorProtocol,
  GatewayProtocolDefaultClientProfile,
  GatewayProtocolDriver,
  GatewayStreamUsageFallbackInput,
  GatewayStreamUsageFallbackResult
} from './_shared/types.js'
import type {
  ResponseEndpointFamily,
  ResponseProtocolCode,
  ResponseSemanticFrame
} from './openai-v1/response-semantics.js'

const gatewayProtocolDrivers: readonly GatewayProtocolDriver[] = [
  openAIV1ProtocolDriver,
  anthropicV1ProtocolDriver,
  geminiV1BetaProtocolDriver
] as const

export function listGatewayProtocolDrivers(): readonly GatewayProtocolDriver[] {
  return gatewayProtocolDrivers
}

export function gatewayProtocolDriverForProfile(profile: ProviderProtocolProfileDefinition | undefined): GatewayProtocolDriver | undefined {
  return gatewayProtocolDrivers.find((driver) => driver.supportsProfile(profile))
}

export function requireGatewayProtocolDriverForProfile(profile: ProviderProtocolProfileDefinition | undefined): GatewayProtocolDriver {
  const driver = gatewayProtocolDriverForProfile(profile)
  if (!driver) {
    throw new Error(`未配置网关协议驱动：${profile?.id ?? 'missing_profile'}`)
  }
  return driver
}

export function gatewayProtocolDriverForRequestOrProfile(
  req: Request,
  profile: ProviderProtocolProfileDefinition | undefined
): GatewayProtocolDriver {
  const endpoint = req.originalUrl || req.path || ''
  if (isOpenAIProtocolRequestPath(endpoint)) {
    return openAIV1ProtocolDriver
  }
  return gatewayProtocolDrivers.find((driver) => driver.isNativeRequest?.(req) === true)
    ?? requireGatewayProtocolDriverForProfile(profile)
}

export function requireGatewayProtocolDriverForResponseProtocol(
  responseProtocol: ResponseProtocolCode | undefined
): GatewayProtocolDriver {
  const driver = gatewayProtocolDrivers.find((item) => item.responseProtocol === responseProtocol)
  if (!driver) {
    throw new Error(`未配置响应协议驱动：${responseProtocol ?? 'missing_response_protocol'}`)
  }
  return driver
}

export function isGatewayProtocolModelsRequest(req: Request, profile: ProviderProtocolProfileDefinition | undefined): boolean {
  return gatewayProtocolDriverForProfile(profile)?.isModelsRequest?.(req) === true
}

export function gatewayProtocolResponseProtocolForProfile(profile: ProviderProtocolProfileDefinition | undefined): ResponseProtocolCode {
  return requireGatewayProtocolDriverForProfile(profile).responseProtocol
}

export function gatewayProtocolResponseProtocolForRequest(
  req: Request,
  profile: ProviderProtocolProfileDefinition | undefined
): ResponseProtocolCode {
  return gatewayProtocolDriverForRequestOrProfile(req, profile).responseProtocol
}

export function gatewayProtocolClientErrorProtocolForProfile(
  profile: ProviderProtocolProfileDefinition | undefined
): GatewayProtocolClientErrorProtocol {
  return requireGatewayProtocolDriverForProfile(profile).clientErrorProtocol
}

export function gatewayProtocolClientErrorProtocolForRequest(
  req: Request,
  profile?: ProviderProtocolProfileDefinition | undefined
): GatewayProtocolClientErrorProtocol {
  return gatewayProtocolDriverForRequestOrProfile(req, profile).clientErrorProtocol
}

export function gatewayProtocolClientErrorProtocolForNativeRequest(req: Request): GatewayProtocolClientErrorProtocol {
  const driver = gatewayProtocolDrivers.find((item) => item.isNativeRequest?.(req) === true)
  if (!driver) {
    throw new Error('未识别原生网关请求协议')
  }
  return driver.clientErrorProtocol
}

export function isGatewayProtocolNativeRequest(req: Request, protocolCode: string): boolean {
  const endpoint = req.originalUrl || req.path || ''
  if (isOpenAIProtocolRequestPath(endpoint)) {
    return protocolCode === openAIV1ProtocolDriver.protocolCode
  }
  return gatewayProtocolDrivers.some((driver) =>
    driver.protocolCode === protocolCode
    && driver.isNativeRequest?.(req) === true
  )
}

export function gatewayProtocolDefaultClientProfileForProfile(
  profile: ProviderProtocolProfileDefinition | undefined
): GatewayProtocolDefaultClientProfile {
  return requireGatewayProtocolDriverForProfile(profile).defaultClientProfile
}

export function gatewayProtocolDefaultClientProfileForRequest(
  req: Request,
  profile: ProviderProtocolProfileDefinition | undefined
): GatewayProtocolDefaultClientProfile {
  return gatewayProtocolDriverForRequestOrProfile(req, profile).defaultClientProfile
}

function isOpenAIProtocolRequestPath(endpoint: string): boolean {
  if (openAIEndpointFamilyFromPath(endpoint)) {
    return true
  }
  const path = endpoint.split('?', 1)[0]?.trim().toLowerCase() ?? ''
  const normalizedPath = path.replace(/^\/v1(?=\/|$)/, '') || '/'
  return normalizedPath === '/models'
    || normalizedPath === '/images'
    || normalizedPath.startsWith('/images/')
    || normalizedPath === '/embeddings'
    || normalizedPath === '/audio'
    || normalizedPath.startsWith('/audio/')
}

export function gatewayProtocolResponseEndpointFamilyForRequest(
  req: Request,
  profile: ProviderProtocolProfileDefinition | undefined
): ResponseEndpointFamily {
  return gatewayProtocolDriverForRequestOrProfile(req, profile).responseEndpointFamilyForRequest(req)
}

export function extractGatewayProtocolJsonSemanticFrames(
  value: unknown,
  req: Request,
  profile: ProviderProtocolProfileDefinition | undefined
): ResponseSemanticFrame[] {
  return requireGatewayProtocolDriverForProfile(profile).extractJsonSemanticFrames(value, req)
}

export function extractGatewayProtocolJsonSemanticFramesForRequest(
  value: unknown,
  req: Request,
  profile: ProviderProtocolProfileDefinition | undefined
): ResponseSemanticFrame[] {
  return gatewayProtocolDriverForRequestOrProfile(req, profile).extractJsonSemanticFrames(value, req)
}

export function parseGatewayProtocolUsageFromJsonBuffer(
  profile: ProviderProtocolProfileDefinition | undefined,
  responseBody: Buffer
): ParsedUsage {
  return requireGatewayProtocolDriverForProfile(profile).parseUsageFromJsonBuffer(responseBody)
}

export function parseGatewayProtocolUsageFromJsonBufferForRequest(
  req: Request,
  profile: ProviderProtocolProfileDefinition | undefined,
  responseBody: Buffer
): ParsedUsage {
  return gatewayProtocolDriverForRequestOrProfile(req, profile).parseUsageFromJsonBuffer(responseBody)
}

export function parseGatewayProtocolUsageFromJsonValue(
  profile: ProviderProtocolProfileDefinition | undefined,
  value: unknown
): ParsedUsage {
  return requireGatewayProtocolDriverForProfile(profile).parseUsageFromJsonValue(value)
}

export function parseGatewayProtocolUsageFromJsonValueForRequest(
  req: Request,
  profile: ProviderProtocolProfileDefinition | undefined,
  value: unknown
): ParsedUsage {
  return gatewayProtocolDriverForRequestOrProfile(req, profile).parseUsageFromJsonValue(value)
}

export function parseGatewayProtocolUsageFromJsonTextFragment(
  profile: ProviderProtocolProfileDefinition | undefined,
  text?: string,
  skipFullDocumentParse = false
): ParsedUsage {
  return requireGatewayProtocolDriverForProfile(profile).parseUsageFromJsonTextFragment(text, skipFullDocumentParse)
}

export function parseGatewayProtocolUsageFromJsonTextFragmentForRequest(
  req: Request,
  profile: ProviderProtocolProfileDefinition | undefined,
  text?: string,
  skipFullDocumentParse = false
): ParsedUsage {
  return gatewayProtocolDriverForRequestOrProfile(req, profile).parseUsageFromJsonTextFragment(text, skipFullDocumentParse)
}

export function parseGatewayProtocolErrorPayload(
  profile: ProviderProtocolProfileDefinition | undefined,
  text: string,
  headers: Headers
): GatewayProtocolErrorPayload {
  return requireGatewayProtocolDriverForProfile(profile).parseErrorPayload(text, headers)
}

export function parseGatewayProtocolErrorPayloadForRequest(
  req: Request,
  profile: ProviderProtocolProfileDefinition | undefined,
  text: string,
  headers: Headers
): GatewayProtocolErrorPayload {
  return gatewayProtocolDriverForRequestOrProfile(req, profile).parseErrorPayload(text, headers)
}

export function parseGatewayProtocolErrorPayloadFromJsonValue(
  profile: ProviderProtocolProfileDefinition | undefined,
  value: unknown
): GatewayProtocolErrorPayload {
  return requireGatewayProtocolDriverForProfile(profile).parseErrorPayloadFromJsonValue(value)
}

export function parseGatewayProtocolErrorPayloadFromJsonValueForRequest(
  req: Request,
  profile: ProviderProtocolProfileDefinition | undefined,
  value: unknown
): GatewayProtocolErrorPayload {
  return gatewayProtocolDriverForRequestOrProfile(req, profile).parseErrorPayloadFromJsonValue(value)
}

export function applyGatewayProtocolStreamUsageFallback(
  req: Request,
  profile: ProviderProtocolProfileDefinition | undefined,
  usage: ParsedUsage,
  input: GatewayStreamUsageFallbackInput
): GatewayStreamUsageFallbackResult {
  return requireGatewayProtocolDriverForProfile(profile).applyStreamUsageFallback(req, usage, input)
}

export function applyGatewayProtocolStreamUsageFallbackForRequest(
  req: Request,
  profile: ProviderProtocolProfileDefinition | undefined,
  usage: ParsedUsage,
  input: GatewayStreamUsageFallbackInput
): GatewayStreamUsageFallbackResult {
  return gatewayProtocolDriverForRequestOrProfile(req, profile).applyStreamUsageFallback(req, usage, input)
}
