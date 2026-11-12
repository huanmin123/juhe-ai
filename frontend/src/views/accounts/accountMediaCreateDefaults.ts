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
 * - 新建态落域默认检查形态与状态（媒体域无后台激活检查途径，默认 active 即
 *   「跳过首检直接可调度」，用户仍可改为待检查/停用）。
 * - 编辑态保留已装载状态，仅当检查形态不在允许集内时回落域默认。
 */
export function applyMediaCreateDefaults(
  form: MediaCreateDefaultsForm,
  context: MediaCreateDefaultsContext
): void {
  if (!capabilityDomainIsMedia(context.capabilityDomain)) return
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
  if (!(allowed as string[]).includes(form.healthCheckEndpointMode)) {
    form.healthCheckEndpointMode = defaultHealthCheckEndpointModeForCapabilityDomain({
      capabilityDomain: context.capabilityDomain
    })
  }
  if (context.editing) return
  form.status = defaultStatusForCapabilityDomain(context.capabilityDomain)
  form.statusSelectionExplicit = false
}
