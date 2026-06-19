import type { ProviderProtocolProfileDefinition } from '../../domain/provider-protocol.js'
import { isOpenAIProtocolProfile } from '../../domain/provider-protocol.js'

export const modelCheckSupportedProtocolLabel = 'OpenAI v1'

export function isModelCheckSupportedProtocolProfile(profile: ProviderProtocolProfileDefinition | undefined): boolean {
  return isOpenAIProtocolProfile(profile)
}

