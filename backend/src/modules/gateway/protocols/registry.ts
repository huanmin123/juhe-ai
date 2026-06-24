import type { Request } from 'express'

import { openAIEndpointFamilyFromPath } from '../../../domain/openai-endpoint-modes.js'
import type { ProviderProtocolProfileDefinition } from '../../../domain/provider-protocol.js'
import type { ParsedUsage } from '../usage/types.js'
import { anthropicV1ProtocolDriver } from './anthropic-v1/driver.js'
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
  anthropicV1ProtocolDriver
] as const

export function listGatewayProtocolDrivers(): readonly GatewayProtocolDriver[] {
  return gatewayProtocolDrivers
}

export function gatewayProtocolDriverForProfile(profile: ProviderProtocolProfileDefinition | undefined): GatewayProtocolDriver | undefined {
  return gatewayProtocolDrivers.find((driver) => driver.supportsProfile(profile))
}

export function gatewayProtocolDriverForProfileOrDefault(profile: ProviderProtocolProfileDefinition | undefined): GatewayProtocolDriver {
  return gatewayProtocolDriverForProfile(profile) ?? openAIV1ProtocolDriver
}

export function gatewayProtocolDriverForRequestOrProfile(
  req: Request,
  profile: ProviderProtocolProfileDefinition | undefined
): GatewayProtocolDriver {
  const endpoint = req.originalUrl || req.path || ''
  if (openAIEndpointFamilyFromPath(endpoint)) {
    return openAIV1ProtocolDriver
  }
  return gatewayProtocolDrivers.find((driver) => driver.isNativeRequest?.(req) === true)
    ?? gatewayProtocolDriverForProfileOrDefault(profile)
}

export function gatewayProtocolDriverForResponseProtocolOrDefault(
  responseProtocol: ResponseProtocolCode | undefined
): GatewayProtocolDriver {
  return gatewayProtocolDrivers.find((driver) => driver.responseProtocol === responseProtocol) ?? openAIV1ProtocolDriver
}

export function isGatewayProtocolModelsRequest(req: Request, profile: ProviderProtocolProfileDefinition | undefined): boolean {
  return gatewayProtocolDriverForProfile(profile)?.isModelsRequest?.(req) === true
}

export function isGatewayProtocolEndpointCapabilityFailure(
  req: Request,
  profile: ProviderProtocolProfileDefinition | undefined,
  statusCode: number
): boolean {
  return gatewayProtocolDriverForProfile(profile)?.isEndpointCapabilityFailure?.(req, statusCode) === true
}

export function gatewayProtocolResponseProtocolForProfile(profile: ProviderProtocolProfileDefinition | undefined): ResponseProtocolCode {
  return gatewayProtocolDriverForProfileOrDefault(profile).responseProtocol
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
  return gatewayProtocolDriverForProfileOrDefault(profile).clientErrorProtocol
}

export function gatewayProtocolClientErrorProtocolForRequest(
  req: Request,
  profile?: ProviderProtocolProfileDefinition | undefined
): GatewayProtocolClientErrorProtocol {
  return gatewayProtocolDriverForRequestOrProfile(req, profile).clientErrorProtocol
}

export function gatewayProtocolClientErrorProtocolForNativeRequest(req: Request): GatewayProtocolClientErrorProtocol {
  return gatewayProtocolDrivers.find((driver) => driver.isNativeRequest?.(req) === true)?.clientErrorProtocol ?? 'openai'
}

export function isGatewayProtocolNativeRequest(req: Request, protocolCode: string): boolean {
  return gatewayProtocolDrivers.some((driver) =>
    driver.protocolCode === protocolCode
    && driver.isNativeRequest?.(req) === true
  )
}

export function gatewayProtocolDefaultClientProfileForProfile(
  profile: ProviderProtocolProfileDefinition | undefined
): GatewayProtocolDefaultClientProfile {
  return gatewayProtocolDriverForProfileOrDefault(profile).defaultClientProfile
}

export function gatewayProtocolDefaultClientProfileForRequest(
  req: Request,
  profile: ProviderProtocolProfileDefinition | undefined
): GatewayProtocolDefaultClientProfile {
  return gatewayProtocolDriverForRequestOrProfile(req, profile).defaultClientProfile
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
  return gatewayProtocolDriverForProfileOrDefault(profile).extractJsonSemanticFrames(value, req)
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
  return gatewayProtocolDriverForProfileOrDefault(profile).parseUsageFromJsonBuffer(responseBody)
}

export function parseGatewayProtocolUsageFromJsonBufferForRequest(
  req: Request,
  profile: ProviderProtocolProfileDefinition | undefined,
  responseBody: Buffer
): ParsedUsage {
  return gatewayProtocolDriverForRequestOrProfile(req, profile).parseUsageFromJsonBuffer(responseBody)
}

export function parseGatewayProtocolUsageFromJsonTextFragment(
  profile: ProviderProtocolProfileDefinition | undefined,
  text?: string
): ParsedUsage {
  return gatewayProtocolDriverForProfileOrDefault(profile).parseUsageFromJsonTextFragment(text)
}

export function parseGatewayProtocolUsageFromJsonTextFragmentForRequest(
  req: Request,
  profile: ProviderProtocolProfileDefinition | undefined,
  text?: string
): ParsedUsage {
  return gatewayProtocolDriverForRequestOrProfile(req, profile).parseUsageFromJsonTextFragment(text)
}

export function parseGatewayProtocolErrorPayload(
  profile: ProviderProtocolProfileDefinition | undefined,
  text: string,
  headers: Headers
): GatewayProtocolErrorPayload {
  return gatewayProtocolDriverForProfileOrDefault(profile).parseErrorPayload(text, headers)
}

export function parseGatewayProtocolErrorPayloadForRequest(
  req: Request,
  profile: ProviderProtocolProfileDefinition | undefined,
  text: string,
  headers: Headers
): GatewayProtocolErrorPayload {
  return gatewayProtocolDriverForRequestOrProfile(req, profile).parseErrorPayload(text, headers)
}

export function applyGatewayProtocolStreamUsageFallback(
  req: Request,
  profile: ProviderProtocolProfileDefinition | undefined,
  usage: ParsedUsage,
  input: GatewayStreamUsageFallbackInput
): GatewayStreamUsageFallbackResult {
  return gatewayProtocolDriverForProfileOrDefault(profile).applyStreamUsageFallback(req, usage, input)
}

export function applyGatewayProtocolStreamUsageFallbackForRequest(
  req: Request,
  profile: ProviderProtocolProfileDefinition | undefined,
  usage: ParsedUsage,
  input: GatewayStreamUsageFallbackInput
): GatewayStreamUsageFallbackResult {
  return gatewayProtocolDriverForRequestOrProfile(req, profile).applyStreamUsageFallback(req, usage, input)
}
