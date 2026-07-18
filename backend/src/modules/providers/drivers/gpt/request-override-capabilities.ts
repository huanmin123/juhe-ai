import type { DispatchAccountSecret } from '../../../../storage/openai-account-selector.types.js'
import { listCachedProviderModelCatalogAsync } from '../../../gateway/runtime/runtime-cache.service.js'
import { modelPricingProviderDriverForProvider } from '../../../model-pricing/provider-driver.registry.js'
import {
  readGptAccountRequestOverrides,
  type GptRequestOverrideModelCapabilities
} from './request-overrides.js'

type GptRequestOverrideModelCapabilitiesResolver = (
  account: DispatchAccountSecret,
  upstreamModel: string | undefined
) => GptRequestOverrideModelCapabilities | undefined | Promise<GptRequestOverrideModelCapabilities | undefined>

let modelCapabilitiesResolverForTest: GptRequestOverrideModelCapabilitiesResolver | undefined

export function setGptRequestOverrideModelCapabilitiesResolverForTest(
  resolver: GptRequestOverrideModelCapabilitiesResolver | undefined
): void {
  modelCapabilitiesResolverForTest = resolver
}

export async function resolveGptRequestOverrideModelCapabilities(
  account: DispatchAccountSecret,
  upstreamModel: string | undefined
): Promise<GptRequestOverrideModelCapabilities | undefined> {
  if (modelCapabilitiesResolverForTest) {
    return await modelCapabilitiesResolverForTest(account, upstreamModel)
  }
  const model = upstreamModel?.trim()
  if (!model) return undefined
  const overrides = readGptAccountRequestOverrides(account.credentials)
  if (!overrides.serviceTier && !overrides.reasoningEffort) return undefined

  const providerCode = account.providerCode?.trim()
  if (!providerCode) return undefined
  const catalog = await listCachedProviderModelCatalogAsync({
    providerCode,
    systemAccountId: account.accountOwnerSystemAccountId,
    includeUnpriced: true
  })
  const driver = modelPricingProviderDriverForProvider(providerCode)
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
