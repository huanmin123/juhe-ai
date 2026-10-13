import type { Dayjs } from 'dayjs'

import { formatDateTime } from '@/shared/formatters'
import type { AccountSupportedEndpointMode, AccountType, ProviderProtocolProfileDefinition } from '@/types/domain'
import type { AccountCapabilityDomain } from './accountCapabilityDomains'
import { allowedEndpointModesFor } from './accountCapabilityDomains'
import { accountEndpointModeLabel, accountEndpointModeOptionsForProfile } from './accountEndpointModes'
import type { AccountProviderProfileLike } from './accountProviderCapabilities'

/**
 * v8-B 媒体域表单完整性升级（A 组）的纯逻辑块：媒体三段式表单的
 * 「上游接口能力域内勾选」与「高级配置折叠卡」头部摘要。
 *
 * 勾选与 chat `AccountStrategySection.vue` 同一底层逻辑（D6 三集合）：
 * 选项 = 域允许集 × 供应商词表求交后的端点 modes 选项；域外遗留声明
 * 只读展示、不进勾选集、提交时随完整声明集原样带回，只能经 M3 清理缩小。
 */

export type MediaCapabilityModeOption = { label: string; value: AccountSupportedEndpointMode }

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

/**
 * 实际声明集中超出当前域允许集的遗留值（D6 存量兼容）：保持声明顺序去重，
 * 只读展示，提交时原样带回，不经 M3 清理不消失。
 */
export function mediaOutOfDomainDeclaredModes(
  declaredModes: readonly AccountSupportedEndpointMode[],
  options: readonly MediaCapabilityModeOption[]
): AccountSupportedEndpointMode[] {
  const selectable = new Set(options.map((option) => option.value))
  const output: AccountSupportedEndpointMode[] = []
  const seen = new Set<AccountSupportedEndpointMode>()
  for (const mode of declaredModes) {
    if (selectable.has(mode) || seen.has(mode)) continue
    seen.add(mode)
    output.push(mode)
  }
  return output
}

export function mediaEndpointModeLabel(
  mode: AccountSupportedEndpointMode,
  profile?: ProviderProtocolProfileDefinition | AccountProviderProfileLike
): string {
  return accountEndpointModeLabel(mode, profile)
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
