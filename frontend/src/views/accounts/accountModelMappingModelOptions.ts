import type { ProviderModelApiProtocol } from '@/types/domain'
import {
  OPENAI_TTS_FAMILY,
  OPENAI_VIDEO_GENERATION_FAMILY
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

// 客户端模型候选（2026-11-07 跨协议退役契约）：source 可跨供应商，但必须支持
// 所选协议——候选池是跨供应商全量目录（useProviderModelSelectOptions 不传
// providerCode），按映射行 source 端点族对应的目录协议 token 过滤；未声明协议
// 的目录行（含空协议数组）视为不支持，不进入候选。
export function accountModelMappingSourceModelOptions(input: {
  sourceEndpointFamily: AccountFormModel['modelMappings'][number]['sourceEndpointFamily']
  options: AccountModelMappingModelOption[]
}): AccountModelMappingModelOption[] {
  return filterAccountModelMappingOptionsByEndpointFamily(input.options, input.sourceEndpointFamily)
}
