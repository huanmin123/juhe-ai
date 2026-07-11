import { GPT_VENDOR_CODE } from '../../../../domain/provider-protocol.js'
import type { DispatchAccountSecret } from '../../../../storage/openai-account-selector.types.js'
import { listCachedProviderModelCatalogAsync } from '../../../gateway/runtime/runtime-cache.service.js'
import { getProviderModelPricing } from '../../../model-pricing/model-pricing.service.js'
import { modelPricingProviderDriverForProvider } from '../../../model-pricing/provider-driver.registry.js'
import {
  readGptAccountRequestOverrides,
  type GptRequestOverrideModelCapabilities
} from './request-overrides.js'

export async function resolveGptRequestOverrideModelCapabilities(
  account: DispatchAccountSecret,
  upstreamModel: string | undefined
): Promise<GptRequestOverrideModelCapabilities | undefined> {
  const model = upstreamModel?.trim()
  if (!model) return undefined
  const overrides = readGptAccountRequestOverrides(account.credentials)
  if (!overrides.serviceTier && !overrides.reasoningEffort) return undefined

  const builtIn = getProviderModelPricing(GPT_VENDOR_CODE, model)
  if (builtIn) {
    return {
      supportedServiceTiers: builtIn.supportedServiceTiers,
      supportedReasoningEfforts: builtIn.supportedReasoningEfforts
    }
  }

  const catalog = await listCachedProviderModelCatalogAsync({
    providerCode: GPT_VENDOR_CODE,
    systemAccountId: account.accountOwnerSystemAccountId,
    includeUnpriced: true
  })
  const driver = modelPricingProviderDriverForProvider(GPT_VENDOR_CODE)
  const candidates = [model, ...(driver?.buildModelCandidates(model) ?? [])]
  const item = candidates
    .map((candidate) => catalog.find((entry) => entry.model.trim() === candidate))
    .find((entry) => entry !== undefined)
  if (!item) return undefined
  return {
    supportedServiceTiers: item.supportedServiceTiers,
    supportedReasoningEfforts: item.supportedReasoningEfforts
  }
}
