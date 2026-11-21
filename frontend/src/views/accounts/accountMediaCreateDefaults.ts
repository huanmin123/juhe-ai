import type { ProviderProtocolProfileDefinition } from '@/types/domain'
import {
  allowedEndpointModesFor,
  defaultEndpointModesForCapabilityDomain,
  defaultHealthCheckEndpointModeForCapabilityDomain,
  defaultStatusForCapabilityDomain,
  defaultValidHealthCheckEndpointModeForCapabilityDomain
} from './accountCapabilityDomains'
import type { AccountCapabilityDomain } from './accountCapabilityDomains'
import type { AccountFormModel } from './accountFormTypes'

/** 媒体域新建态默认值只触碰的表单切片（统一向导的域默认 watch 消费）。 */
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
 * - status 预选对两个分支统一生效（B1 修复）：2026-11-21 起媒体域接入
 *   `account-connectivity-health` 连通性健康检查，开启后台探测时默认
 *   pending_test 由首次检查激活；手动模式（总开关关闭）由后端按总开关设计
 *   归一为 active，前端仍保留用户显式选择。声明集早退分支（第二次选择同
 *   供应商、定义已入缓存时 resetForm 直接解析出域内默认集）此前跳过预选，
 *   草稿死锁在 accountFormDefaults 硬编码的 pending_test。
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
    if (!declared.includes(form.healthCheckEndpointMode)) {
      // 声明集是保存时的最终事实：域默认形态未必在集合内（qwen 音频只有
      // audio_job_*、仅转写的 volcengine 账户亦无 audio_speech），必须回落到
      // 声明集内的合法检查形态，否则后端 health_check_connection_conflict。
      form.healthCheckEndpointMode = defaultValidHealthCheckEndpointModeForCapabilityDomain({
        capabilityDomain: context.capabilityDomain,
        endpointModes: declared
      }) ?? defaultHealthCheckEndpointModeForCapabilityDomain({
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
    form.healthCheckEndpointMode = defaultValidHealthCheckEndpointModeForCapabilityDomain({
      capabilityDomain: context.capabilityDomain,
      endpointModes: form.supportedEndpointModes
    }) ?? defaultHealthCheckEndpointModeForCapabilityDomain({
      capabilityDomain: context.capabilityDomain
    })
  }
  form.status = defaultStatusForCapabilityDomain(context.capabilityDomain)
  form.statusSelectionExplicit = false
}
