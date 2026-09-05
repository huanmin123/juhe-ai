import { providerBillingPolicyForProvider } from './provider-billing.registry.js'
import type {
  ProviderBillingCostInput,
  ProviderBillingPricing,
  ProviderCatalogDisplaySection,
  ProviderCostBreakdown
} from './provider-billing.types.js'

export function buildProviderBillingCostBreakdown(
  pricing: ProviderBillingPricing,
  input: ProviderBillingCostInput
): ProviderCostBreakdown | undefined {
  const policy = providerBillingPolicyForProvider(pricing.providerCode)
  const breakdown = policy?.buildCostBreakdown(pricing, input)
  return breakdown ? { ...breakdown, currency: 'USD', billingPolicy: policy?.id } : undefined
}

export function buildProviderCatalogDisplay(pricing: ProviderBillingPricing): ProviderCatalogDisplaySection[] {
  return providerBillingPolicyForProvider(pricing.providerCode)?.buildCatalogDisplay(pricing) ?? []
}
