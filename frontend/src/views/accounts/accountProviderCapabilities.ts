import type {
  AccountClientCompatibility,
  AccountSupportedEndpointMode,
  AccountType,
  ProviderDefinition,
  ProviderProtocolProfileDefinition
} from '@/types/domain'
import {
  isGptVendorCode,
  isAnthropicProtocolProfile,
  isOpenAIProtocolProfile
} from '@/shared/providerProtocol'

export type AccountProviderProtocolKind = 'openai_v1' | 'anthropic_v1' | 'unknown'
export type ClientCompatibilityCapability = 'openai_standard' | 'codex_responses' | 'anthropic_native' | 'claude_code'

export type AccountProviderProfileLike = {
  providerCode?: string
  protocolCode?: string
  protocolVersion?: string
  accountTypes?: AccountType[]
  capabilities?: string[]
  type?: AccountType
  clientCompatibility?: AccountClientCompatibility
}

export const clientCompatibilityCapabilityOptions: Array<{ label: string; value: ClientCompatibilityCapability }> = [
  { label: 'OpenAI 标准', value: 'openai_standard' },
  { label: 'Codex Responses', value: 'codex_responses' },
  { label: 'Anthropic 原生', value: 'anthropic_native' },
  { label: 'Claude Code', value: 'claude_code' }
]

export const chatEndpointModes: AccountSupportedEndpointMode[] = ['chat_json', 'chat_sse']
export const responsesEndpointModes: AccountSupportedEndpointMode[] = ['responses_json', 'responses_sse']
export const openAIEndpointModes: AccountSupportedEndpointMode[] = [...chatEndpointModes, ...responsesEndpointModes]
export const anthropicAccountEndpointModes: AccountSupportedEndpointMode[] = ['messages_json', 'messages_sse', 'message_token_counting']
export const allAccountEndpointModes: AccountSupportedEndpointMode[] = [
  ...openAIEndpointModes,
  ...anthropicAccountEndpointModes
]

export function accountProviderProtocolKind(profile?: AccountProviderProfileLike): AccountProviderProtocolKind {
  if (isOpenAIProtocolProfile(profile)) return 'openai_v1'
  if (isAnthropicProtocolProfile(profile)) return 'anthropic_v1'
  return 'unknown'
}

export function providerProfileSupportsAccountType(
  type: AccountType,
  provider?: ProviderDefinition,
  profile?: ProviderProtocolProfileDefinition | AccountProviderProfileLike
): boolean {
  if (profile && 'type' in profile && profile.type === type) return true
  const accountTypes = profile?.accountTypes?.length ? profile.accountTypes : provider?.accountTypes ?? []
  return accountTypes.includes(type)
}

export function canCreateOAuthAccount(input: {
  provider?: ProviderDefinition
  profile?: ProviderProtocolProfileDefinition | AccountProviderProfileLike
}): boolean {
  return providerProfileSupportsAccountType('oauth', input.provider, input.profile)
    && isGptVendorCode(providerCodeForOAuthFlow(input))
    && accountProviderProtocolKind(input.profile ?? input.provider) === 'openai_v1'
}

export function defaultAccountClientCompatibilityForProvider(input: {
  provider?: ProviderDefinition
  profile?: ProviderProtocolProfileDefinition
}): AccountClientCompatibility {
  return canCreateOAuthAccount(input) ? 'codex_responses' : 'openai_standard'
}

export function effectiveAccountTestClientCompatibility(
  account: AccountProviderProfileLike,
  clientCompatibility: 'account_default' | AccountClientCompatibility
): AccountClientCompatibility {
  if (account.type === 'oauth' && accountProviderProtocolKind(account) === 'openai_v1') {
    return 'codex_responses'
  }
  return clientCompatibility === 'account_default'
    ? account.clientCompatibility ?? 'openai_standard'
    : clientCompatibility
}

export function isGatewayTestableAccountProfile(profile?: AccountProviderProfileLike): boolean {
  return accountProviderProtocolKind(profile) !== 'unknown'
}

export function canSelectClientCompatibility(account: AccountProviderProfileLike): boolean {
  return account.type === 'api_key'
    && accountProviderProtocolKind(account) === 'openai_v1'
    && (isGptVendorCode(account.providerCode) || account.clientCompatibility === 'codex_responses')
}

export function accountClientCompatibilityCapabilities(account: AccountProviderProfileLike): ClientCompatibilityCapability[] {
  const protocolKind = accountProviderProtocolKind(account)
  if (protocolKind === 'anthropic_v1') {
    return account.type === 'api_key'
      ? ['anthropic_native', 'claude_code']
      : ['anthropic_native']
  }
  if (protocolKind !== 'openai_v1') {
    return ['openai_standard']
  }
  if (account.type === 'oauth') {
    return ['codex_responses']
  }
  return isGptVendorCode(account.providerCode) || account.clientCompatibility === 'codex_responses'
    ? ['openai_standard', 'codex_responses']
    : ['openai_standard']
}

export function clientCompatibilityCapabilityLabel(value: ClientCompatibilityCapability): string {
  return clientCompatibilityCapabilityOptions.find((option) => option.value === value)?.label ?? value
}

export function fixedCompatibilityLabel(accounts: AccountProviderProfileLike[]): string {
  return accounts.length ? '测试请求形态' : '客户端兼容'
}

export function fixedCompatibilityText(accounts: AccountProviderProfileLike[]): string {
  if (accounts.some((account) => accountProviderProtocolKind(account) === 'anthropic_v1')) {
    return 'Anthropic 原生请求'
  }
  const capabilities = new Set(accounts.flatMap(accountClientCompatibilityCapabilities))
  if (capabilities.has('codex_responses') && !capabilities.has('openai_standard')) {
    return 'Codex Responses 请求'
  }
  return 'OpenAI 标准请求'
}

export function defaultEndpointModesForAccount(input: {
  provider?: ProviderDefinition
  profile?: ProviderProtocolProfileDefinition | AccountProviderProfileLike
  type: AccountType
  clientCompatibility?: AccountClientCompatibility
}): AccountSupportedEndpointMode[] {
  const protocolKind = accountProviderProtocolKind(input.profile ?? input.provider)
  if (input.type === 'oauth') return [...responsesEndpointModes]
  if (protocolKind === 'anthropic_v1') return [...anthropicAccountEndpointModes]
  if (protocolKind === 'openai_v1') {
    return providerProfileSupportsAccountType('oauth', input.provider, input.profile)
      || input.clientCompatibility === 'codex_responses'
      ? [...openAIEndpointModes]
      : [...chatEndpointModes]
  }
  return [...allAccountEndpointModes]
}

export function endpointModesForProfile(profile?: AccountProviderProfileLike): AccountSupportedEndpointMode[] {
  const protocolKind = accountProviderProtocolKind(profile)
  if (protocolKind === 'anthropic_v1') return [...anthropicAccountEndpointModes]
  if (protocolKind === 'openai_v1') return [...openAIEndpointModes]
  return [...allAccountEndpointModes]
}

function providerCodeForOAuthFlow(input: {
  provider?: ProviderDefinition
  profile?: ProviderProtocolProfileDefinition | AccountProviderProfileLike
}): string | undefined {
  return input.profile?.providerCode ?? input.provider?.code
}
