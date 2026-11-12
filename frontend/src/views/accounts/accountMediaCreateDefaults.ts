import type { ProviderProtocolProfileDefinition } from '@/types/domain'
import {
  allowedEndpointModesFor,
  capabilityDomainIsMedia,
  defaultEndpointModesForCapabilityDomain,
  defaultHealthCheckEndpointModeForCapabilityDomain,
  defaultStatusForCapabilityDomain
} from './accountCapabilityDomains'
import type { AccountCapabilityDomain } from './accountCapabilityDomains'
import type { AccountFormModel } from './accountFormTypes'

/** 媒体域表单默认值只触碰的表单切片（AccountEditModal 的域默认 watch 消费）。 */
export type MediaCreateDefaultsForm = Pick<
  AccountFormModel,
  'supportedEndpointModes' | 'healthCheckEndpointMode' | 'status' | 'statusSelectionExplicit'
>

export interface MediaCreateDefaultsContext {
  capabilityDomain: AccountCapabilityDomain
  providerCode: string
  profile?: ProviderProtocolProfileDefinition
  type: AccountFormModel['type']
  /** 编辑态只归一能力集与检查形态，不重放新建状态默认值。 */
  editing?: boolean
}

/**
 * 媒体域表单默认值（2026-11-11 裁决：媒体账户无上游接口能力开关）：
 *
 * - `supported_endpoint_modes` 恒等于该供应商该域允许集全量：新建、编辑装载、
 *   克隆、切换供应商/类型都归一为全量，不存在用户收窄或保留窄集合的分支。
 * - 新建态落域默认检查形态与统一 pending_test 状态（由 J1 首次激活探测转
 *   active，D6）；用户仍可改为停用。
 * - 编辑态保留已装载状态，仅当检查形态不在允许集内时回落域默认。
 */
/**
 * 从允许集中选择安全代表检查形态：视频优先 video_create、图像 images_json；
 * 音频优先 audio_speech，其次 audio_transcription_json，最后 audio_job_get
 * （绝不用 audio_job_create，避免后台/测试 POST 创建真实任务）。
 */
function representativeHealthCheckMode(
  capabilityDomain: AccountCapabilityDomain,
  allowed: string[]
): AccountFormModel['healthCheckEndpointMode'] {
  const candidates = capabilityDomain === 'video'
    ? ['video_create']
    : capabilityDomain === 'image'
      ? ['images_json']
      : ['audio_speech', 'audio_transcription_json', 'audio_job_get']
  return (candidates.find((mode) => allowed.includes(mode))
    ?? allowed[0]
    ?? 'chat_sse') as AccountFormModel['healthCheckEndpointMode']
}

export function applyMediaCreateDefaults(
  form: MediaCreateDefaultsForm,
  context: MediaCreateDefaultsContext
): void {  if (!capabilityDomainIsMedia(context.capabilityDomain)) return
  const allowed = allowedEndpointModesFor({
    capabilityDomain: context.capabilityDomain,
    providerCode: context.providerCode,
    profile: context.profile,
    accountType: context.type
  })
  const modes = allowed.length
    ? [...allowed]
    : defaultEndpointModesForCapabilityDomain({
      capabilityDomain: context.capabilityDomain,
      providerCode: context.providerCode,
      profile: context.profile,
      type: context.type
    })
  if (modes.length) form.supportedEndpointModes = modes
  const preferredHealthMode = defaultHealthCheckEndpointModeForCapabilityDomain({
    capabilityDomain: context.capabilityDomain
  })
  if (!(allowed as string[]).includes(form.healthCheckEndpointMode)) {
    form.healthCheckEndpointMode = (allowed as string[]).includes(preferredHealthMode)
      ? preferredHealthMode
      : representativeHealthCheckMode(context.capabilityDomain, allowed)
  }
  if (context.editing) return
  form.status = defaultStatusForCapabilityDomain(context.capabilityDomain)
  form.statusSelectionExplicit = false
}
