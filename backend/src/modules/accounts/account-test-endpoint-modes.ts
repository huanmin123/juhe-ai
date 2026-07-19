import { normalizeAnthropicEndpointModesForRuntime } from '../../domain/anthropic-endpoint-modes.js'
import { normalizeGeminiEndpointModesForRuntime } from '../../domain/gemini-endpoint-modes.js'
import { normalizeOpenAIEndpointModesForRuntime } from '../../domain/openai-endpoint-modes.js'
import {
  isAnthropicProtocolProfile,
  isGeminiProtocolProfile,
  isHybridProviderCode,
  isOpenAIProtocolProfile
} from '../../domain/provider-protocol.js'
import type { AccountSummary, AccountSupportedEndpointMode } from '../../domain/types.js'
import { normalizeHybridEndpointModesForRuntime } from '../providers/drivers/hybrid/account-credentials.js'

type AccountTestEndpointModeSource = Pick<
  AccountSummary,
  | 'providerCode'
  | 'providerProtocolProfileId'
  | 'protocolCode'
  | 'protocolVersion'
  | 'type'
  | 'clientCompatibility'
  | 'healthCheckEndpointMode'
  | 'credentials'
>

export function accountManualTestEndpointModes(
  account: AccountTestEndpointModeSource
): AccountSupportedEndpointMode[] {
  const enabledSet = new Set(normalizedAccountEndpointModes(account))
  return accountTestEndpointModeOrder(account).filter((mode) => enabledSet.has(mode))
}

function normalizedAccountEndpointModes(account: AccountTestEndpointModeSource): AccountSupportedEndpointMode[] {
  if (isHybridProviderCode(account.providerCode)) {
    return normalizeHybridEndpointModesForRuntime(account.credentials.supported_endpoint_modes)
  }
  if (isAnthropicProtocolProfile(account)) {
    return normalizeAnthropicEndpointModesForRuntime(account.credentials.supported_endpoint_modes, {
      providerCode: account.providerCode,
      accountType: account.type,
      protocolCode: account.protocolCode,
      protocolVersion: account.protocolVersion,
      providerProtocolProfileId: account.providerProtocolProfileId
    })
  }
  if (isGeminiProtocolProfile(account)) {
    return normalizeGeminiEndpointModesForRuntime(account.credentials.supported_endpoint_modes, {
      providerCode: account.providerCode,
      accountType: account.type,
      protocolCode: account.protocolCode,
      protocolVersion: account.protocolVersion,
      providerProtocolProfileId: account.providerProtocolProfileId
    })
  }
  if (isOpenAIProtocolProfile(account)) {
    return normalizeOpenAIEndpointModesForRuntime(account.credentials.supported_endpoint_modes, {
      providerCode: account.providerCode,
      providerProtocolProfileId: account.providerProtocolProfileId,
      accountType: account.type,
      clientCompatibility: account.clientCompatibility
    })
  }
  return []
}

function accountTestEndpointModeOrder(account: AccountTestEndpointModeSource): AccountSupportedEndpointMode[] {
  const defaultMode = account.healthCheckEndpointMode
  if (isHybridProviderCode(account.providerCode)) {
    return uniqueEndpointModes(
      defaultMode,
      'chat_json',
      'chat_sse',
      'responses_json',
      'responses_sse',
      'messages_json',
      'messages_sse',
      'generate_content_json',
      'generate_content_sse'
    )
  }
  if (isAnthropicProtocolProfile(account)) {
    return uniqueEndpointModes(defaultMode, 'messages_json', 'messages_sse')
  }
  if (isGeminiProtocolProfile(account)) {
    return uniqueEndpointModes(defaultMode, 'interactions_json', 'interactions_sse', 'generate_content_json', 'generate_content_sse')
  }
  if (account.type === 'oauth') {
    return uniqueEndpointModes(defaultMode, 'responses_json', 'responses_sse')
  }
  return uniqueEndpointModes(defaultMode, 'chat_sse', 'responses_sse', 'chat_json', 'responses_json')
}

function uniqueEndpointModes(...modes: AccountSupportedEndpointMode[]): AccountSupportedEndpointMode[] {
  return [...new Set(modes)]
}
