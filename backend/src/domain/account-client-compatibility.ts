import {
  ACCOUNT_CLIENT_COMPATIBILITIES,
  type AccountClientCompatibility,
  type ClientCompatibilityCapability
} from './types.js'
import {
  isAnthropicProtocolProfile,
  isGptVendorCode,
  isOpenAIProtocolProfile
} from './provider-protocol.js'

export interface AccountClientCompatibilityProfile {
  providerCode?: string
  accountType?: unknown
  type?: unknown
  clientCompatibility?: unknown
  protocolCode?: string
  protocolVersion?: string
  providerProtocolProfileId?: string
}

export function normalizeAccountClientCompatibility(value: unknown, fallback: AccountClientCompatibility = 'openai_standard'): AccountClientCompatibility {
  if (value === undefined || value === null || value === '') return fallback
  if (typeof value === 'string' && ACCOUNT_CLIENT_COMPATIBILITIES.includes(value as AccountClientCompatibility)) {
    return value as AccountClientCompatibility
  }
  throw new Error('客户端兼容配置无效')
}

export function normalizeOpenAIAccountClientCompatibility(
  providerCode: unknown,
  accountType: unknown,
  value: unknown,
  protocolProfile?: {
    providerCode?: string
    protocolCode?: string
    protocolVersion?: string
    providerProtocolProfileId?: string
  }
): AccountClientCompatibility {
  if (isGptVendorCode(providerCode) && isOpenAIProtocolProfile(protocolProfile)) {
    if (accountType === 'oauth') {
      return 'codex_responses'
    }
    return normalizeAccountClientCompatibility(value, 'codex_responses')
  }
  return 'openai_standard'
}

export function deriveOpenAIAccountClientCompatibility(
  providerCode: unknown,
  accountType: unknown,
  protocolProfile?: {
    providerCode?: string
    protocolCode?: string
    protocolVersion?: string
    providerProtocolProfileId?: string
    id?: string
  }
): AccountClientCompatibility {
  if (!isOpenAIProtocolProfile(protocolProfile)) {
    return 'openai_standard'
  }
  if (isGptVendorCode(providerCode) && accountType === 'oauth') {
    return 'codex_responses'
  }
  if (isGptVendorCode(providerCode) && accountType === 'api_key') {
    return 'codex_responses'
  }
  return 'openai_standard'
}

export function deriveAccountSupportedClientCompatibilities(
  profile: AccountClientCompatibilityProfile
): ClientCompatibilityCapability[] {
  const accountType = profile.accountType ?? profile.type
  if (isAnthropicProtocolProfile(profile)) {
    return accountType === 'api_key'
      ? ['anthropic_native', 'claude_code']
      : ['anthropic_native']
  }
  if (!isOpenAIProtocolProfile(profile)) {
    return ['openai_standard']
  }
  if (accountType === 'oauth') {
    return ['codex_responses']
  }
  const compatibility = deriveOpenAIAccountClientCompatibility(profile.providerCode, accountType, profile)
  return compatibility === 'codex_responses'
    ? ['openai_standard', 'codex_responses']
    : ['openai_standard']
}

export function accountSupportsClientCompatibility(
  profile: AccountClientCompatibilityProfile,
  requestClientCompatibility?: ClientCompatibilityCapability
): boolean {
  if (!requestClientCompatibility) return true
  return deriveAccountSupportedClientCompatibilities(profile).includes(requestClientCompatibility)
}
