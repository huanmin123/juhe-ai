import type {
  AccountGptReasoningEffortOverride,
  AccountGptServiceTierOverride,
  ProviderModelReasoningEffort,
  ProviderModelServiceTier
} from '@/types/domain'
import {
  accountRequestOverrideFamilyOf,
  accountRequestOverrideReasoningEfforts,
  accountRequestOverrideServiceTiers,
  isAccountRequestOverrideTokenShape,
  type AccountRequestOverrideFamily
} from './accountRequestOverrideVocab'

export interface AccountGptModelCapabilityOption {
  value: string
  supportedServiceTiers?: ProviderModelServiceTier[]
  supportedReasoningEfforts?: ProviderModelReasoningEffort[]
  defaultReasoningEffort?: ProviderModelReasoningEffort | null
}

// 覆盖字段的选项能力（2026-10-18 简化定案）：值域为协议族全集（单一真源见
// accountRequestOverrideVocab.ts，指向后端 requestoverride 包），不再按账户
// 「已选模型」求并集；供应商模型目录只决定字段是否显示（declared* 按供应商级
// “任一模型声明过能力”判断）。
export interface AccountGptRequestOverrideCapabilities {
  family: AccountRequestOverrideFamily
  serviceTiers: string[]
  reasoningEfforts: string[]
  declaredServiceTiers: boolean
  declaredReasoningEfforts: boolean
}

export interface AccountRequestOverrideOption {
  label: string
  value: string
  disabled?: boolean
}

const localizedCapabilityLabels: Record<string, string> = {
  default: '供应商默认（Default）',
  standard: '标准（Standard）',
  priority: '优先（Priority）',
  flex: '弹性（Flex）',
  auto: '自动（Auto）',
  auto_only: '自动（Auto only）',
  standard_only: '仅标准（Standard only）',
  none: '不思考（None）',
  minimal: '最少（Minimal）',
  low: '低（Low）',
  medium: '中（Medium）',
  high: '高（High）',
  xhigh: '更高（XHigh）',
  max: '最大（Max）'
}

export function accountGptRequestOverrideCapabilities(input: {
  providerCode?: string
  protocolCode?: string
  modelOptions: AccountGptModelCapabilityOption[]
  supportedEndpointModes?: string[]
}): AccountGptRequestOverrideCapabilities {
  const providerCode = input.providerCode ?? 'gpt'
  if (providerCode === 'gemini' && input.supportedEndpointModes
    && !input.supportedEndpointModes.some((mode) => mode === 'generate_content_json' || mode === 'generate_content_sse')) {
    return { family: 'gemini', serviceTiers: [], reasoningEfforts: [], declaredServiceTiers: false, declaredReasoningEfforts: false }
  }
  const family = accountRequestOverrideFamilyOf(providerCode, input.protocolCode)
  return {
    family,
    serviceTiers: accountRequestOverrideServiceTiers(family),
    reasoningEfforts: accountRequestOverrideReasoningEfforts(family),
    declaredServiceTiers: input.modelOptions.some((option) => Boolean(option.supportedServiceTiers?.length)),
    declaredReasoningEfforts: input.modelOptions.some((option) => Boolean(option.supportedReasoningEfforts?.length))
  }
}

// 判断该账户协议面是否可写入服务等级 / 思考级别覆盖字段。
// 2026-10-17 去供应商白名单后：是否显示配置区不再按 providerCode 白名单判定；
// 唯一协议面例外是 Interactions-only Gemini（不走 GenerateContent，覆盖不会生效），
// 且该例外是绝对的：即使账户已有存量覆盖值也不显示（网关在 Interactions 路径
// 不应用覆盖，不存在需要保留的清除入口）。
export function isAccountRequestOverrideProviderSupported(providerCode: string | undefined, supportedEndpointModes?: string[]): boolean {
  const normalized = providerCode?.trim() || 'gpt'
  if (normalized === 'gemini' && supportedEndpointModes
    && !supportedEndpointModes.some((mode) => mode === 'generate_content_json' || mode === 'generate_content_sse')) {
    return false
  }
  return true
}

export function availableAccountGptServiceTierOptions(
  capabilities: AccountGptRequestOverrideCapabilities,
  currentValue: AccountGptServiceTierOverride = ''
): AccountRequestOverrideOption[] {
  const options: AccountRequestOverrideOption[] = [
    { label: '不覆盖客户端设置', value: '' },
    ...(capabilities.serviceTiers.length
      ? [
          { label: localizedCapabilityLabels.default, value: 'default' },
          ...capabilities.serviceTiers
            .filter((value) => value !== 'default')
            .map((value) => ({ label: capabilityLabel(value), value }))
        ]
      : [])
  ]
  return appendUnavailableCurrentOption(options, currentValue)
}

export function availableAccountGptReasoningEffortOptions(
  capabilities: AccountGptRequestOverrideCapabilities,
  currentValue: AccountGptReasoningEffortOverride = ''
): AccountRequestOverrideOption[] {
  const options: AccountRequestOverrideOption[] = [
    { label: '不覆盖客户端设置', value: '' },
    ...capabilities.reasoningEfforts.map((value) => ({ label: capabilityLabel(value), value }))
  ]
  return appendUnavailableCurrentOption(options, currentValue)
}

export function accountGptRequestOverridesForForm(
  _providerCode: string,
  credentials: {
    service_tier_override?: unknown
    reasoning_effort_override?: unknown
  }
): Pick<AccountGptRequestOverrideForm, 'serviceTierOverride' | 'reasoningEffortOverride'> {
  return {
    serviceTierOverride: normalizeCapabilityToken(credentials.service_tier_override),
    reasoningEffortOverride: normalizeCapabilityToken(credentials.reasoning_effort_override)
  }
}

export function writeAccountGptRequestOverrides(credentials: Record<string, unknown>, form: AccountGptRequestOverrideForm): void {
  delete credentials.service_tier_override
  delete credentials.reasoning_effort_override
  if (form.serviceTierOverride) credentials.service_tier_override = form.serviceTierOverride
  if (form.reasoningEffortOverride) credentials.reasoning_effort_override = form.reasoningEffortOverride
}

interface AccountGptRequestOverrideForm {
  providerCode: string
  serviceTierOverride: AccountGptServiceTierOverride
  reasoningEffortOverride: AccountGptReasoningEffortOverride
}

// token 形状单一来源：accountRequestOverrideVocab（文本指向后端
// requestoverride.TokenPattern）。长度上限 64、首尾空白即非法。
function normalizeCapabilityToken(value: unknown): string {
  return typeof value === 'string' && value === value.trim() && isAccountRequestOverrideTokenShape(value) ? value : ''
}

function capabilityLabel(value: string): string {
  return localizedCapabilityLabels[value] ?? value
}

function appendUnavailableCurrentOption(
  options: AccountRequestOverrideOption[],
  currentValue: string
): AccountRequestOverrideOption[] {
  if (!currentValue || options.some((option) => option.value === currentValue)) return options
  return [
    ...options,
    {
      label: `当前配置：${capabilityLabel(currentValue)}（不在当前协议族值域内）`,
      value: currentValue,
      disabled: true
    }
  ]
}
