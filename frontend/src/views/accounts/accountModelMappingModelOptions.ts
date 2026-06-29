import type { ProviderModelApiProtocol } from '@/types/domain'
import type { AccountFormModel } from './accountFormTypes'

export type AccountModelMappingModelOption = {
  label: string
  value: string
  supportedApiProtocols?: ProviderModelApiProtocol[]
}

export function accountModelMappingEndpointFamilyProtocol(
  endpointFamily: AccountFormModel['modelMappings'][number]['sourceEndpointFamily'] | AccountFormModel['modelMappings'][number]['upstreamEndpointFamily']
): ProviderModelApiProtocol {
  if (endpointFamily === 'responses') return 'responses'
  if (endpointFamily === 'messages') return 'messages'
  if (endpointFamily === 'generate_content') return 'generate_content'
  if (endpointFamily === 'stream_generate_content') return 'stream_generate_content'
  return 'chat_completions'
}

export function filterAccountModelMappingOptionsByEndpointFamily(
  options: AccountModelMappingModelOption[],
  endpointFamily: AccountFormModel['modelMappings'][number]['sourceEndpointFamily'] | AccountFormModel['modelMappings'][number]['upstreamEndpointFamily']
): AccountModelMappingModelOption[] {
  const protocol = accountModelMappingEndpointFamilyProtocol(endpointFamily)
  return options.filter((option) => option.supportedApiProtocols?.includes(protocol))
}

