import type { ProviderProtocolProfileDefinition } from '@/types/domain'
import {
  allowedEndpointModesFor,
  defaultEndpointModesForCapabilityDomain,
  defaultHealthCheckEndpointModeForCapabilityDomain,
  defaultStatusForCapabilityDomain
} from './accountCapabilityDomains'
import type { AccountCapabilityDomain } from './accountCapabilityDomains'
import type { AccountFormModel } from './accountFormTypes'

/** 媒体域新建态默认值只触碰的表单切片（AccountMediaForm 的域默认 watch 消费）。 */
export type MediaCreateDefaultsForm = Pick<
  AccountFormModel,
  'supportedEndpointModes' | 'healthCheckEndpointMode' | 'status' | 'statusSelectionExplicit'
>

export interface MediaCreateDefaultsContext {
  capabilityDomain: AccountCapabilityDomain
  providerCode: string
  profile?: ProviderProtocolProfileDefinition
  type: AccountFormModel['type']
}

/**
 * 新建态域默认值（生命周期定案 v7 + D6 默认集）：打开弹窗或切换供应商/类型时重放。
 *
 * - 克隆装载的源声明集（⊆ 域内允许集且非空）保持原样：克隆按新建规则校验，
 *   不在草稿阶段重置为域默认集；新建向导的初始 modes 是对话域默认集（与媒体
 *   允许集交集恒为空）或空集，此时落域默认集。
 * - status 预选对两个分支统一生效（B1 修复）：媒体域无后台激活检查途径，
 *   pending_test 只会死锁在不可调度态；默认 active 即创建语义中的「跳过首检
 *   直接可调度」，用户可改为 pending_test/disabled。声明集早退分支（第二次
 *   选择同供应商、定义已入缓存时 resetForm 直接解析出域内默认集）此前跳过
 *   预选，草稿死锁在 accountFormDefaults 硬编码的 pending_test。
 */
export function applyMediaCreateDefaults(
  form: MediaCreateDefaultsForm,
  context: MediaCreateDefaultsContext
): void {
  const allowed = new Set(allowedEndpointModesFor({
    capabilityDomain: context.capabilityDomain,
    providerCode: context.providerCode,
    profile: context.profile,
    accountType: context.type
  }))
  const declared = form.supportedEndpointModes
  if (declared.length > 0 && declared.every((mode) => allowed.has(mode))) {
    if (!allowed.has(form.healthCheckEndpointMode)) {
      form.healthCheckEndpointMode = defaultHealthCheckEndpointModeForCapabilityDomain({
        capabilityDomain: context.capabilityDomain
      })
    }
  } else {
    const modes = defaultEndpointModesForCapabilityDomain({
      capabilityDomain: context.capabilityDomain,
      providerCode: context.providerCode,
      profile: context.profile,
      type: context.type
    })
    if (modes.length) form.supportedEndpointModes = [...modes]
    form.healthCheckEndpointMode = defaultHealthCheckEndpointModeForCapabilityDomain({
      capabilityDomain: context.capabilityDomain
    })
  }
  form.status = defaultStatusForCapabilityDomain(context.capabilityDomain)
  form.statusSelectionExplicit = false
}
