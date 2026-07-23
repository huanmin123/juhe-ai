import { isOpenAIProtocolProfile, type ProviderProtocolProfileDefinition } from '../../domain/provider-protocol.js'
import { getProviderModelPricing } from '../model-pricing/model-pricing.service.js'

export type AccountTestProbeKind = 'generation' | 'image_generation' | 'models_catalog'

export function accountTestProbeKind(account: ProviderProtocolProfileDefinition & { type?: string }, model: string): AccountTestProbeKind {
  return isOpenAIProtocolProfile(account)
    && account.type === 'api_key'
    && getProviderModelPricing(account.providerCode ?? '', model)?.mode === 'image_generation'
    ? 'image_generation'
    : 'generation'
}
