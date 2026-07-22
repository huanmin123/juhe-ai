import { api } from '@/api/client'
import type { ProviderModelPricing, ProviderModelsParams } from '@/types/domain'

interface ProviderModelCatalogResourceOptions {
  force?: boolean
  isManagementView: boolean
  providerCode: string
  query?: ProviderModelsParams
}

export async function loadProviderModelCatalogResource(
  options: ProviderModelCatalogResourceOptions
): Promise<ProviderModelPricing[]> {
  const providerCode = options.providerCode.trim()
  const query = options.query ? { ...options.query } : undefined
  return api.providers.models(providerCode, query)
}
