import type { ProviderModelApiProtocol } from '@/types/domain'
import { isHybridProviderCode } from '@/shared/providerProtocol'
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

export function accountModelMappingSourceModelOptions(input: {
  providerCode?: string
  sourceEndpointFamily: AccountFormModel['modelMappings'][number]['sourceEndpointFamily']
  currentProviderOptions: AccountModelMappingModelOption[]
  openAIProtocolOptions: AccountModelMappingModelOption[]
  anthropicProtocolOptions: AccountModelMappingModelOption[]
  geminiProtocolOptions: AccountModelMappingModelOption[]
}): AccountModelMappingModelOption[] {
  if (!isHybridProviderCode(input.providerCode)) return input.currentProviderOptions
  return filterAccountModelMappingOptionsByEndpointFamily(
    protocolSourceOptions(input),
    input.sourceEndpointFamily
  )
}

function protocolSourceOptions(input: {
  sourceEndpointFamily: AccountFormModel['modelMappings'][number]['sourceEndpointFamily']
  openAIProtocolOptions: AccountModelMappingModelOption[]
  anthropicProtocolOptions: AccountModelMappingModelOption[]
  geminiProtocolOptions: AccountModelMappingModelOption[]
}): AccountModelMappingModelOption[] {
  if (input.sourceEndpointFamily === 'messages') return input.anthropicProtocolOptions
  if (input.sourceEndpointFamily === 'generate_content' || input.sourceEndpointFamily === 'stream_generate_content') {
    return input.geminiProtocolOptions
  }
  return input.openAIProtocolOptions
}

