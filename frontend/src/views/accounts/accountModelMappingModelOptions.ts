import type { ProviderModelApiProtocol } from '@/types/domain'
import {
  OPENAI_TTS_FAMILY,
  OPENAI_VIDEO_GENERATION_FAMILY,
  isHybridProviderCode
} from '@/shared/providerProtocol'
import type { AccountFormModel } from './accountFormTypes'

export type AccountModelMappingModelOption = {
  label?: string
  value: string
  supportedApiProtocols?: ProviderModelApiProtocol[]
}

// 媒体族端点族码与模型目录协议 token 不同名——video_generation 对应目录行
// 的 "video"、tts 对应 "audio_speech"（镜像后端 mappingFamilyProtocolToken）；
// chat 族两者同名。模型选项按 token 过滤，消费端点族时必须经本换算。
export function accountModelMappingEndpointFamilyProtocol(
  endpointFamily: AccountFormModel['modelMappings'][number]['sourceEndpointFamily'] | AccountFormModel['modelMappings'][number]['upstreamEndpointFamily']
): ProviderModelApiProtocol {
  if (endpointFamily === 'responses') return 'responses'
  if (endpointFamily === 'messages') return 'messages'
  if (endpointFamily === 'generate_content') return 'generate_content'
  if (endpointFamily === 'stream_generate_content') return 'stream_generate_content'
  if (endpointFamily === OPENAI_VIDEO_GENERATION_FAMILY) return 'video'
  if (endpointFamily === OPENAI_TTS_FAMILY) return 'audio_speech'
  return 'chat_completions'
}

export function filterAccountModelMappingOptionsByEndpointFamily(
  options: AccountModelMappingModelOption[],
  endpointFamily: AccountFormModel['modelMappings'][number]['sourceEndpointFamily'] | AccountFormModel['modelMappings'][number]['upstreamEndpointFamily']
): AccountModelMappingModelOption[] {
  const protocol = accountModelMappingEndpointFamilyProtocol(endpointFamily)
  return options.filter((option) => option.supportedApiProtocols?.includes(protocol))
}

export function accountModelMappingUpstreamModelOptions(
  options: AccountModelMappingModelOption[],
  upstreamEndpointFamily: AccountFormModel['modelMappings'][number]['upstreamEndpointFamily']
): AccountModelMappingModelOption[] {
  return filterAccountModelMappingOptionsByEndpointFamily(options, upstreamEndpointFamily)
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

