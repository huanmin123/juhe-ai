import type { Request } from 'express'

import type { GatewayUpstreamResponse } from '../../../gateway/upstream/request.js'
import type {
  AccountClientCompatibility,
  AccountModelMapping,
  AccountModelMappingEndpointFamily,
  ClientCompatibilityCapability,
  AccountSupportedEndpointMode,
  AccountType,
  ProviderCode
} from '../../../../domain/types.js'
import type { ProviderProtocolProfileDefinition } from '../../../../domain/provider-protocol.js'
import type { DispatchAccountSecret } from '../../../../storage/openai-account-selector.types.js'
import type { CodexResponsesChatBridgeCompletionHandler } from '../../../gateway/codex-responses/chat-bridge-state.js'

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
  modelMappings?: AccountModelMapping[]
}

export interface ProviderUpstreamRequestIdentity {
  systemAccountId: string
  apiKeyId?: string
  groupId: string
}

export interface ProviderGatewayRequestContext {
  requestClientCompatibility?: ClientCompatibilityCapability
  codexResponsesChatBridgePreviousResponseId?: string
  codexResponsesChatBridgeCompletionHandler?: CodexResponsesChatBridgeCompletionHandler
}

export interface ProviderUpstreamRequestParts {
  headers: Headers
  body?: Buffer | string
}

export interface ProviderUpstreamResponseTransformContext extends ProviderGatewayRequestContext {}

export interface ProviderAccountPreparationContext {
  signal?: AbortSignal
}

export interface ProviderUsageModelResolution {
  upstreamModel?: string
  modelMappingApplied: boolean
  modelMappingSource?: string
}

export interface ProviderDriver {
  id: string
  providerCode: ProviderCode
  protocolCode: string
  protocolVersion: string
  usageSemantic: string
  profileIds: readonly string[]
  supportsProfile(profile: ProviderProtocolProfileDefinition | undefined): boolean
  resolveUsageModel(account: ProviderDriverAccount, requestedModel?: string, sourceEndpointFamily?: AccountModelMappingEndpointFamily): ProviderUsageModelResolution
  prepareAccountBeforeDispatch?(
    account: DispatchAccountSecret,
    context: ProviderAccountPreparationContext
  ): Promise<DispatchAccountSecret>
  buildUpstreamUrls(account: DispatchAccountSecret, req: Request): string[]
  buildUpstreamRequestParts(
    req: Request,
    account: DispatchAccountSecret,
    identity: ProviderUpstreamRequestIdentity,
    signal?: AbortSignal,
    context?: ProviderGatewayRequestContext
  ): Promise<ProviderUpstreamRequestParts>
  transformUpstreamResponse?(
    req: Request,
    account: DispatchAccountSecret,
    response: GatewayUpstreamResponse,
    context?: ProviderUpstreamResponseTransformContext
  ): GatewayUpstreamResponse
  endpointModeForRequest(req: Request, account: ProviderDriverAccount): AccountSupportedEndpointMode | undefined
  accountSupportsRequest(req: Request, account: ProviderDriverAccount, context?: ProviderGatewayRequestContext): boolean
}
