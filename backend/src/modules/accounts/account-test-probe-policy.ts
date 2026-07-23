import { isOpenAIProtocolProfile, type ProviderProtocolProfileDefinition } from '../../domain/provider-protocol.js'
import type { AccountSupportedEndpointMode } from '../../domain/types.js'
import { getProviderModelPricing } from '../model-pricing/model-pricing.service.js'

export type AccountTestProbeKind = 'generation' | 'image_generation' | 'models_catalog'

export function accountTestProbeKind(
  account: ProviderProtocolProfileDefinition & { type?: string },
  model: string,
  modelCapabilities: {
    testEndpointMode?: AccountSupportedEndpointMode
    mode?: string
    supportedApiProtocols?: readonly string[]
  } = {}
): AccountTestProbeKind {
  return isOpenAIProtocolProfile(account)
    && account.type === 'api_key'
    && (
      modelCapabilities.testEndpointMode === 'images_json'
      || modelCapabilities.mode === 'image_generation'
      || modelCapabilities.supportedApiProtocols?.includes('images') === true
      || getProviderModelPricing(account.providerCode ?? '', model)?.mode === 'image_generation'
    )
    ? 'image_generation'
    : 'generation'
}
