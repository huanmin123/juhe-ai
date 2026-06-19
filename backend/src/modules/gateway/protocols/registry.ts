import type { Request } from 'express'

import type { ProviderProtocolProfileDefinition } from '../../../domain/provider-protocol.js'
import type { ParsedUsage } from '../usage/types.js'
import { anthropicV1ProtocolDriver } from './anthropic-v1/driver.js'
import { openAIV1ProtocolDriver } from './openai-v1/driver.js'
import type {
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

export function gatewayProtocolClientErrorProtocolForProfile(
  profile: ProviderProtocolProfileDefinition | undefined
): GatewayProtocolClientErrorProtocol {
  return gatewayProtocolDriverForProfileOrDefault(profile).clientErrorProtocol
}

export function gatewayProtocolClientErrorProtocolForRequest(req: Request): GatewayProtocolClientErrorProtocol {
  return gatewayProtocolDrivers.find((driver) => driver.isNativeRequest?.(req) === true)?.clientErrorProtocol ?? 'openai'
}

export function gatewayProtocolDefaultClientProfileForProfile(
  profile: ProviderProtocolProfileDefinition | undefined
): GatewayProtocolDefaultClientProfile {
  return gatewayProtocolDriverForProfileOrDefault(profile).defaultClientProfile
}

export function gatewayProtocolResponseEndpointFamilyForRequest(
  req: Request,
  profile: ProviderProtocolProfileDefinition | undefined
): ResponseEndpointFamily {
  return gatewayProtocolDriverForProfileOrDefault(profile).responseEndpointFamilyForRequest(req)
}

export function extractGatewayProtocolJsonSemanticFrames(
  value: unknown,
  req: Request,
  profile: ProviderProtocolProfileDefinition | undefined
): ResponseSemanticFrame[] {
  return gatewayProtocolDriverForProfileOrDefault(profile).extractJsonSemanticFrames(value, req)
}

export function parseGatewayProtocolUsageFromJsonBuffer(
  profile: ProviderProtocolProfileDefinition | undefined,
  responseBody: Buffer
): ParsedUsage {
  return gatewayProtocolDriverForProfileOrDefault(profile).parseUsageFromJsonBuffer(responseBody)
}

export function parseGatewayProtocolUsageFromJsonTextFragment(
  profile: ProviderProtocolProfileDefinition | undefined,
  text?: string
): ParsedUsage {
  return gatewayProtocolDriverForProfileOrDefault(profile).parseUsageFromJsonTextFragment(text)
}

export function applyGatewayProtocolStreamUsageFallback(
  req: Request,
  profile: ProviderProtocolProfileDefinition | undefined,
  usage: ParsedUsage,
  input: GatewayStreamUsageFallbackInput
): GatewayStreamUsageFallbackResult {
  return gatewayProtocolDriverForProfileOrDefault(profile).applyStreamUsageFallback(req, usage, input)
}
