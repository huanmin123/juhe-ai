import type { Request } from 'express'

import type {
  AccountClientCompatibility,
  AccountSupportedEndpointMode,
  AccountType,
  ProviderCode
} from '../../../../domain/types.js'
import type { ProviderProtocolProfileDefinition } from '../../../../domain/provider-protocol.js'
import type { DispatchAccountSecret } from '../../../../storage/openai-account-selector.types.js'

export type ProviderRequestCapabilityMismatchReason =
  | 'request_capability_mismatch'
  | 'anthropic_native_group_openai_compatible_request'

export interface ProviderDriverAccount extends ProviderProtocolProfileDefinition {
  id?: string
  providerCode?: ProviderCode
  type?: AccountType
  clientCompatibility?: AccountClientCompatibility
  supportedEndpointModes?: readonly AccountSupportedEndpointMode[]
  credentials?: Record<string, unknown>
  baseUrl?: string
}

export interface ProviderUpstreamRequestIdentity {
  systemAccountId: string
  apiKeyId?: string
  groupId: string
}

export interface ProviderUpstreamRequestParts {
  headers: Headers
  body?: Buffer | string
}

export interface ProviderDriver {
  id: string
  providerCode: ProviderCode
  protocolCode: string
  protocolVersion: string
  profileIds: readonly string[]
  supportsProfile(profile: ProviderProtocolProfileDefinition | undefined): boolean
  buildUpstreamUrls(account: DispatchAccountSecret, req: Request): string[]
  buildUpstreamRequestParts(
    req: Request,
    account: DispatchAccountSecret,
    identity: ProviderUpstreamRequestIdentity,
    signal?: AbortSignal
  ): Promise<ProviderUpstreamRequestParts>
  endpointModeForRequest(req: Request, account: ProviderDriverAccount): AccountSupportedEndpointMode | undefined
  accountSupportsRequest(req: Request, account: ProviderDriverAccount): boolean
}
