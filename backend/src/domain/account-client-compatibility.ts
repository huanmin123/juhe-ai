import { ACCOUNT_CLIENT_COMPATIBILITIES, type AccountClientCompatibility } from './types.js'
import { isGptVendorCode, isOpenAIProtocolProfile } from './provider-protocol.js'

export function normalizeAccountClientCompatibility(value: unknown, fallback: AccountClientCompatibility = 'openai_standard'): AccountClientCompatibility {
  if (value === undefined || value === null || value === '') return fallback
  if (typeof value === 'string' && ACCOUNT_CLIENT_COMPATIBILITIES.includes(value as AccountClientCompatibility)) {
    return value as AccountClientCompatibility
  }
  throw new Error('客户端兼容模式无效')
}

export function normalizeOpenAIAccountClientCompatibility(
  providerCode: unknown,
  accountType: unknown,
  value: unknown,
  fallback: AccountClientCompatibility = 'openai_standard',
  protocolProfile?: { protocolCode?: string; protocolVersion?: string }
): AccountClientCompatibility {
  if (isGptVendorCode(providerCode) && isOpenAIProtocolProfile(protocolProfile)) {
    if (accountType === 'oauth') {
      return 'codex_responses'
    }
    return normalizeAccountClientCompatibility(value, 'codex_responses')
  }
  return normalizeAccountClientCompatibility(value, fallback)
}
