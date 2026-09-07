const providerModelCatalogInvalidationReasons = new Set([
  'custom_provider_model_saved',
  'custom_provider_model_deleted',
  'provider_model_configuration_updated'
])

export function shouldInvalidateProviderModelCatalog(reason: string | undefined): boolean {
  return reason !== undefined && providerModelCatalogInvalidationReasons.has(reason)
}
