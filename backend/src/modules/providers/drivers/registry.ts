import type { Request } from 'express'

import { openAIEndpointModeForRequestShape } from '../../../domain/openai-endpoint-modes.js'
import type { ProviderProtocolProfileDefinition } from '../../../domain/provider-protocol.js'
import type { DispatchAccountSecret } from '../../../storage/openai-account-selector.types.js'
import { isEffectiveOpenAIStreamRequest } from '../../gateway/upstream/request.js'
import { anthropicProviderDriver } from './anthropic/driver.js'
import type { ProviderDriver, ProviderRequestCapabilityMismatchReason } from './_shared/types.js'
import { gptProviderDriver } from './gpt/driver.js'
import { openAICompatibleProviderDriver } from './openai-compatible/driver.js'

const providerDrivers: readonly ProviderDriver[] = [
  openAICompatibleProviderDriver,
  gptProviderDriver,
  anthropicProviderDriver
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

export function accountSupportsGatewayRequest(req: Request, account: DispatchAccountSecret): boolean {
  const driver = providerDriverForAccount(account)
  if (!driver) {
    return false
  }
  if (buildGatewayUpstreamUrlsForAccount(account, req).length === 0) return false
  return driver.accountSupportsRequest(req, account)
}

export function buildGatewayUpstreamUrlsForAccount(account: DispatchAccountSecret, req: Request): string[] {
  return providerDriverForAccount(account)?.buildUpstreamUrls(account, req) ?? []
}

export async function buildGatewayUpstreamRequestParts(
  req: Request,
  account: DispatchAccountSecret,
  identity: { systemAccountId: string; apiKeyId?: string; groupId: string },
  signal?: AbortSignal
): Promise<{ headers: Headers; body?: Buffer | string }> {
  const driver = providerDriverForAccount(account)
  if (!driver) {
    throw new Error(`供应商协议档案未注册请求构造器：${account.providerProtocolProfileId}`)
  }
  return await driver.buildUpstreamRequestParts(req, account, identity, signal)
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
