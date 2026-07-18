import type {
  AccountGptReasoningEffortOverride,
  AccountGptServiceTierOverride,
  AccountType,
  ProviderModelReasoningEffort,
  ProviderModelServiceTier
} from '@/types/domain'

export interface AccountGptModelCapabilityOption {
  value: string
  supportedServiceTiers?: ProviderModelServiceTier[]
  supportedReasoningEfforts?: ProviderModelReasoningEffort[]
  defaultReasoningEffort?: ProviderModelReasoningEffort | null
}

export interface AccountGptRequestOverrideCapabilities {
  serviceTiers: ProviderModelServiceTier[]
  reasoningEfforts: ProviderModelReasoningEffort[]
}

export interface AccountRequestOverrideOption {
  label: string
  value: string
  disabled?: boolean
}

const serviceTierOverrideProviders = new Set(['gpt', 'openai', 'anthropic'])
const reasoningEffortOverrideProviders = new Set(['gpt', 'openai', 'anthropic', 'gemini'])

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
  accountType: AccountType
  modelOptions: AccountGptModelCapabilityOption[]
  supportedModels: string[]
}): AccountGptRequestOverrideCapabilities {
  const providerCode = input.providerCode ?? 'gpt'
  const supportedModels = uniqueTextList(input.supportedModels)
  const serviceTiers = serviceTierOverrideProviders.has(providerCode)
    ? unionModelCapability(supportedModels, input.modelOptions, (option) => option.supportedServiceTiers)
    : []
  const reasoningEfforts = reasoningEffortOverrideProviders.has(providerCode)
    ? unionModelCapability(supportedModels, input.modelOptions, (option) => option.supportedReasoningEfforts)
    : []
  return { serviceTiers, reasoningEfforts }
}

export function isAccountRequestOverrideProviderSupported(providerCode: string | undefined): boolean {
  const normalized = providerCode?.trim() || 'gpt'
  return serviceTierOverrideProviders.has(normalized) || reasoningEffortOverrideProviders.has(normalized)
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

export function isAccountGptServiceTierOverrideAvailable(value: AccountGptServiceTierOverride, capabilities: AccountGptRequestOverrideCapabilities): boolean {
  if (!value) return true
  if (value === 'default') return capabilities.serviceTiers.length > 0
  return capabilities.serviceTiers.includes(value)
}

export function isAccountGptReasoningEffortOverrideAvailable(value: AccountGptReasoningEffortOverride, capabilities: AccountGptRequestOverrideCapabilities): boolean {
  return !value || capabilities.reasoningEfforts.includes(value)
}

export function accountGptRequestOverridesForForm(
  _providerCode: string,
  credentials: Record<string, unknown>
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

function unionModelCapability(
  supportedModels: string[],
  modelOptions: AccountGptModelCapabilityOption[],
  readValues: (option: AccountGptModelCapabilityOption) => string[] | undefined
): string[] {
  const optionsByModel = new Map(modelOptions.map((option) => [option.value.trim(), option]))
  const output: string[] = []
  const seen = new Set<string>()
  for (const model of supportedModels) {
    const option = optionsByModel.get(model)
    if (!option) continue
    for (const value of normalizeCapabilityTokens(readValues(option))) {
      if (seen.has(value)) continue
      seen.add(value)
      output.push(value)
    }
  }
  return output
}

function normalizeCapabilityTokens(values: string[] | undefined): string[] {
  return uniqueTextList(values ?? []).filter((value) => /^[a-z0-9][a-z0-9._-]{0,63}$/i.test(value))
}

function normalizeCapabilityToken(value: unknown): string {
  return typeof value === 'string' && value === value.trim() && /^[a-z0-9][a-z0-9._-]{0,63}$/i.test(value) ? value : ''
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
      label: `当前配置：${capabilityLabel(currentValue)}（当前模型不支持）`,
      value: currentValue,
      disabled: true
    }
  ]
}

function uniqueTextList(values: string[]): string[] {
  const output: string[] = []
  const seen = new Set<string>()
  for (const value of values) {
    const normalized = value.trim()
    if (!normalized || seen.has(normalized)) continue
    seen.add(normalized)
    output.push(normalized)
  }
  return output
}
