import type { ProviderProtocolProfileDefinition } from '../../domain/provider-protocol.js'
import { isOpenAICompatibleProviderCode, isOpenAIProtocolProfile } from '../../domain/provider-protocol.js'

export const modelCheckSupportedProtocolLabel = 'OpenAI Responses'

export function isModelCheckSupportedProtocolProfile(profile: ProviderProtocolProfileDefinition | undefined): boolean {
  if (!profile) return false
  return isOpenAIProtocolProfile(profile) && isOpenAICompatibleProviderCode(profile.providerCode)
}
