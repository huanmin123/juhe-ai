import type { Dayjs } from 'dayjs'

import { formatDateTime } from '@/shared/formatters'
import type { AccountSupportedEndpointMode, AccountType, ProviderProtocolProfileDefinition } from '@/types/domain'
import type { AccountCapabilityDomain } from './accountCapabilityDomains'
import { allowedEndpointModesFor, capabilityDomainIsMedia } from './accountCapabilityDomains'
import { accountEndpointModeOptionsForProfile } from './accountEndpointModes'
import type { AccountProviderProfileLike } from './accountProviderCapabilities'

/**
 * v8-B 媒体域表单完整性升级（A 组）的纯逻辑块：媒体三段式表单的
 * 「上游接口能力域内勾选」与「高级配置折叠卡」头部摘要。
 *
 * 勾选与 chat `AccountStrategySection.vue` 同一底层逻辑：选项 = 域允许集 ×
 * 供应商词表求交后的端点 modes 选项；账户声明集必须 ⊆ 所在域允许集。
 */

export type MediaCapabilityModeOption = { label: string; value: AccountSupportedEndpointMode }

/**
 * v8-D 域内空模型列表提示：选中供应商在当前能力域的目录候选「加载后确认为
 * 空」（settled 且非加载中）时给出引导文案。候选是懒加载，弹窗打开加载一次、
 * 切供应商即清空、下拉展开/搜索再加载——未加载的空不是真没有货，settled 前
 * 返回 undefined。hybrid 映射域的上游模型来自映射面（不依赖目录候选）、对话
 * 域不适用本提示，两者恒返回 undefined。
 */
export function mediaDomainModelAvailabilityHint(input: {
  capabilityDomain: AccountCapabilityDomain
  optionCount: number
  loading: boolean
  settled: boolean
  hybridMappingDomain: boolean
}): string | undefined {
  if (!capabilityDomainIsMedia(input.capabilityDomain)) return undefined
  if (input.hybridMappingDomain || input.loading || !input.settled || input.optionCount > 0) return undefined
  return '该供应商在此域暂无可用目录模型，可切换供应商，或先在模型目录中为该供应商添加此域模型'
}

/** 媒体域内可勾选的上游接口能力选项：域允许集 ∩ 供应商写侧词表。 */
export function mediaCapabilityModeOptions(input: {
  capabilityDomain: AccountCapabilityDomain
  providerCode?: string
  profile?: ProviderProtocolProfileDefinition | AccountProviderProfileLike
  accountType?: AccountType
}): MediaCapabilityModeOption[] {
  const allowed = new Set(allowedEndpointModesFor({
    capabilityDomain: input.capabilityDomain,
    providerCode: input.providerCode,
    profile: input.profile,
    accountType: input.accountType
  }))
  return accountEndpointModeOptionsForProfile(input.profile).filter((option) => allowed.has(option.value))
}

export interface MediaAdvancedCardState {
  accountExpiresAt?: Dayjs | null
  balanceQueryEnabled: boolean
  scheduleEnabled: boolean
  scheduleWindowCount: number
  errorPolicyRuleCount: number
}

/** 高级配置折叠卡头部摘要：与 chat 对应卡同口径（到期/余额/时间计划/错误规则）。 */
export function mediaAdvancedCardSummary(state: MediaAdvancedCardState): string {
  // 到期用严格服务端时间格式化（toISOString 命中 serverDateTimePattern）；
  // 不复制 chat balanceCardSummary 以本地空格格式喂 formatDateTime 的缺陷。
  const expiryText = state.accountExpiresAt
    ? `到期 ${formatDateTime(state.accountExpiresAt.toDate().toISOString())}`
    : '未设置到期'
  const balanceText = `余额查询${state.balanceQueryEnabled ? '开' : '关'}`
  const scheduleText = state.scheduleEnabled
    ? `计划已启用 ${state.scheduleWindowCount} 个时段`
    : '计划未启用'
  return `${expiryText} · ${balanceText} · ${scheduleText} · 错误规则 ${state.errorPolicyRuleCount} 条`
}

/** 高级配置折叠卡「已配置 N 项」徽标计数：非默认值项计数。 */
export function mediaAdvancedCardConfiguredCount(state: MediaAdvancedCardState): number {
  return [
    Boolean(state.accountExpiresAt),
    state.balanceQueryEnabled,
    state.scheduleEnabled,
    state.errorPolicyRuleCount > 0
  ].filter(Boolean).length
}
