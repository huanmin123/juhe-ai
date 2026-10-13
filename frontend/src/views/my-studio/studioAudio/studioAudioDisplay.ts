import type { StudioModelOption, StudioTranscriptionResult } from '@/types/domain'
import { formatStudioCostEstimate, formatStudioDuration } from '../studioDisplayCommon'
import { studioCapabilityUnitPrice } from '../shared/studioCapabilities'

/** 音频工作台展示纯函数（页签模型过滤、参数摘要、分段、SRT 可用性、成本）。 */

/** 端点模式过滤（§4.1 endpointModes）：目录未声明该模式（能力载荷缺项）不进入页签候选。 */
export function filterStudioModelsByEndpointMode(models: StudioModelOption[], endpointMode: string): StudioModelOption[] {
  return models.filter((model) => model.endpointModes?.includes(endpointMode))
}

/** 长文本按空行拆段（§4.3：分段由前端拆分逐段调用，端点不聚合）。 */
export function splitStudioSpeechSegments(input: string): string[] {
  return input
    .split(/\n\s*\n/)
    .map((segment) => segment.trim())
    .filter((segment) => segment.length > 0)
}

/** TTS 历史条目参数摘要（来自 asset.params 快照：voice/speed/responseFormat/emotion）。 */
export function studioTtsParamsSummary(params: Record<string, unknown> | undefined | null): string {
  const source = params ?? {}
  const parts: string[] = []
  if (typeof source.voice === 'string' && source.voice) parts.push(`音色 ${source.voice}`)
  if (source.speed !== undefined && source.speed !== null && source.speed !== '') parts.push(`语速 ${String(source.speed)}`)
  if (typeof source.emotion === 'string' && source.emotion) parts.push(`情感 ${source.emotion}`)
  if (typeof source.responseFormat === 'string' && source.responseFormat) parts.push(source.responseFormat)
  return parts.length > 0 ? parts.join(' · ') : '-'
}

/** SRT 仅在模型 timestampCapable 且上游返回时出现（§4.3：不从普通文本虚构时间轴）。 */
export function transcriptionHasSrt(result: StudioTranscriptionResult | undefined | null): boolean {
  return typeof result?.srt === 'string' && result.srt.trim().length > 0
}

/** TTS 成本预估：单价取目录能力载荷 unitPrice（按次）；未声明显式"无法估算"。 */
export function studioTtsCostEstimate(model: StudioModelOption | undefined): string {
  return formatStudioCostEstimate(model ? studioCapabilityUnitPrice(model.capabilities) : undefined, 1)
}

export function formatAudioDuration(durationMs?: number | null): string {
  return formatStudioDuration(durationMs)
}
