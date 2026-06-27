import type { Request } from 'express'

import type { GatewayRequestEndpointFamily, ProviderCode } from '../../../domain/types.js'
import { openAIEndpointModeForRequestShape } from '../../../domain/openai-endpoint-modes.js'
import { GPT_VENDOR_CODE, normalizeProviderToken, type ProviderProtocolProfileDefinition } from '../../../domain/provider-protocol.js'
import type { DispatchAccountSecret } from '../../../storage/openai-account-selector.types.js'
import { isEffectiveOpenAIStreamRequest, type GatewayUpstreamResponse } from '../../gateway/upstream/request.js'
import { anthropicProviderDriver } from './anthropic/driver.js'
import { deepSeekProviderDriver } from './deepseek/driver.js'
import type {
  ProviderDriver,
  ProviderGatewayRequestContext,
  ProviderRequestCapabilityMismatchReason,
  ProviderUsageModelResolution
} from './_shared/types.js'
import { geminiProviderDriver } from './gemini/driver.js'
import { glmProviderDriver } from './glm/driver.js'
import { gptProviderDriver } from './gpt/driver.js'
import { hybridProviderDriver } from './hybrid/driver.js'
import { openAICompatibleProviderDriver } from './openai-compatible/driver.js'

const providerDrivers: readonly ProviderDriver[] = [
  openAICompatibleProviderDriver,
  gptProviderDriver,
  deepSeekProviderDriver,
  anthropicProviderDriver,
  geminiProviderDriver,
  glmProviderDriver,
  hybridProviderDriver
] as const

export function listProviderDrivers(): readonly ProviderDriver[] {
  return providerDrivers
}

export function providerDriverForProfile(profile: ProviderProtocolProfileDefinition | undefined): ProviderDriver | undefined {
  return providerDrivers.find((driver) => driver.supportsProfile(profile))
}

export function providerDriverForAccount(account: ProviderProtocolProfileDefinition | undefined): ProviderDriver | undefined {
  return providerDriverForProfile(account)
}

export function providerDriverForProviderCode(providerCode: string | undefined): ProviderDriver | undefined {
  const normalizedProviderCode = normalizeProviderToken(providerCode)
  if (!normalizedProviderCode) return undefined
  return providerDrivers.find((driver) => driver.providerCode === normalizedProviderCode)
}

export function defaultGatewayUsageProviderCode(): ProviderCode {
  return GPT_VENDOR_CODE
}

export function usageSemanticForProviderCode(providerCode: string | undefined): string {
  return providerDriverForProviderCode(providerCode)?.usageSemantic ?? 'openai'
}

export function usageSemanticForProfile(profile: ProviderProtocolProfileDefinition | undefined): string {
  const profileDriver = providerDriverForProfile(profile)
  const profileSemantic = profileDriver?.usageSemanticForProfile?.(profile)
  if (profileSemantic) return profileSemantic
  if (profileDriver) return profileDriver.usageSemantic
  return usageSemanticForProviderCode(profile?.providerCode)
}

export function resolveGatewayUsageModel(
  account: DispatchAccountSecret,
  requestedModel?: string,
  sourceEndpointFamily?: GatewayRequestEndpointFamily
): ProviderUsageModelResolution {
  return providerDriverForAccount(account)?.resolveUsageModel(account, requestedModel, sourceEndpointFamily) ?? {
    upstreamModel: requestedModel,
    modelMappingApplied: false
  }
}

export async function prepareGatewayUpstreamAccount(
  account: DispatchAccountSecret,
  signal?: AbortSignal
): Promise<DispatchAccountSecret> {
  const driver = providerDriverForAccount(account)
  return await (driver?.prepareAccountBeforeDispatch?.(account, { signal }) ?? account)
}

export function accountSupportsGatewayRequest(
  req: Request,
  account: DispatchAccountSecret,
  context?: ProviderGatewayRequestContext
): boolean {
  const driver = providerDriverForAccount(account)
  if (!driver) {
    return false
  }
  if (buildGatewayUpstreamUrlsForAccount(account, req).length === 0) return false
  return driver.accountSupportsRequest(req, account, context)
}

export function buildGatewayUpstreamUrlsForAccount(account: DispatchAccountSecret, req: Request): string[] {
  return providerDriverForAccount(account)?.buildUpstreamUrls(account, req) ?? []
}

export async function buildGatewayUpstreamRequestParts(
  req: Request,
  account: DispatchAccountSecret,
  identity: { systemAccountId: string; apiKeyId?: string; groupId: string },
  signal?: AbortSignal,
  context?: ProviderGatewayRequestContext
): Promise<{ headers: Headers; body?: Buffer | string }> {
  const driver = providerDriverForAccount(account)
  if (!driver) {
    throw new Error(`供应商协议档案未注册请求构造器：${account.providerProtocolProfileId}`)
  }
  return await driver.buildUpstreamRequestParts(req, account, identity, signal, context)
}

export function transformGatewayUpstreamResponseForAccount(
  req: Request,
  account: DispatchAccountSecret,
  response: GatewayUpstreamResponse,
  context?: ProviderGatewayRequestContext
): GatewayUpstreamResponse {
  return providerDriverForAccount(account)?.transformUpstreamResponse?.(req, account, response, context) ?? response
}

export function gatewayRequestCapabilityMismatchReason(
  req: Request,
  accounts: readonly DispatchAccountSecret[]
): ProviderRequestCapabilityMismatchReason {
  const allAnthropic = accounts.length > 0
    && accounts.every((account) => providerDriverForAccount(account)?.id === anthropicProviderDriver.id)
  if (
    allAnthropic
    && openAIEndpointModeForRequestShape({
      endpoint: req.path || req.originalUrl.split('?', 1)[0],
      stream: isEffectiveOpenAIStreamRequest(req)
    })
  ) {
    return 'anthropic_native_group_openai_compatible_request'
  }
  return 'request_capability_mismatch'
}

export type { ProviderDriver, ProviderRequestCapabilityMismatchReason }
