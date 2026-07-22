export interface ApiKeyGroupOptionsProviderProtocolProfileInput {
  formContext: boolean
  allowMixedProviderProtocolProfiles: boolean
  formBindings: Array<{
    providerProtocolProfileId?: string | null
  }>
}

export function apiKeyGroupOptionsProviderProtocolProfileId(input: ApiKeyGroupOptionsProviderProtocolProfileInput): string {
  if (!input.formContext || input.allowMixedProviderProtocolProfiles) return ''
  for (const binding of input.formBindings) {
    const value = binding.providerProtocolProfileId?.trim()
    if (value) return value
  }
  return ''
}
