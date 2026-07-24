import { modelPricingProviderDriverForProvider } from './provider-driver.registry.js'
import type { ProviderBillingPolicy } from './provider-billing.types.js'

export function providerBillingPolicyForProvider(providerCode: string | undefined): ProviderBillingPolicy | undefined {
  return modelPricingProviderDriverForProvider(providerCode)?.billingPolicy
}
