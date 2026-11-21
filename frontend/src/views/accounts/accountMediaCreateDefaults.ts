import type { ProviderProtocolProfileDefinition } from '@/types/domain'
import {
  defaultHealthCheckEndpointModeForCapabilityDomain,
  defaultStatusForCapabilityDomain
} from './accountCapabilityDomains'
import type { AccountCapabilityDomain } from './accountCapabilityDomains'
import type { AccountFormModel } from './accountFormTypes'

/** 五域新建态默认值只触碰的表单切片（统一向导的域默认 watch 消费）。 */
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
 * 新建态域默认值（2026-11-21 五域退役修订，AI账户上游接口能力设计 §3/§5）：
 * 打开弹窗或切换供应商/类型时重放。
 *
 * - **声明集不再播种**：五域账户不配置「上游接口能力」，表单恒清空
 *   `supportedEndpointModes`（创建墙与可建类型仍由 `capabilityDomainAccountTypes`
 *   的域×供应商矩阵承担，与本函数无关）。
 * - `healthCheckEndpointMode` 是连接测试记录值，按域默认常量落值
 *   （video→video_create、audio→audio_speech、image→images_json、
 *   embedding→embeddings_json、rerank→rerank_json）。
 * - status 预选（B1 修复延续）：2026-11-21 起五域接入
 *   `account-connectivity-health` 连通性健康检查，开启后台探测时默认
 *   pending_test 由首次检查激活；手动模式（总开关关闭）由后端按总开关设计
 *   归一为 active，前端仍保留用户显式选择。
 * - chat 域不经本函数（统一向导的域默认 watch 只对五域触发）；防御性直接
 *   返回，不改写对话表单任何字段。
 */
export function applyMediaCreateDefaults(
  form: MediaCreateDefaultsForm,
  context: MediaCreateDefaultsContext
): void {
  if (context.capabilityDomain === 'chat') return
  form.supportedEndpointModes = []
  form.healthCheckEndpointMode = defaultHealthCheckEndpointModeForCapabilityDomain({
    capabilityDomain: context.capabilityDomain
  })
  form.status = defaultStatusForCapabilityDomain(context.capabilityDomain)
  form.statusSelectionExplicit = false
}
