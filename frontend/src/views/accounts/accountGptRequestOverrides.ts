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
  defaultReasoningEffort?: ProviderModelReasoningEffort
}

export interface AccountGptRequestOverrideCapabilities {
  serviceTiers: ProviderModelServiceTier[]
  reasoningEfforts: ProviderModelReasoningEffort[]
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
  accountType: AccountType
  modelOptions: AccountGptModelCapabilityOption[]
  supportedModels: string[]
}): AccountGptRequestOverrideCapabilities {
  const supportedModels = uniqueTextList(input.supportedModels)
  const serviceTiers = intersectModelCapability(supportedModels, input.modelOptions, (option) => option.supportedServiceTiers)
    .filter((tier) => (input.providerCode ?? 'gpt') !== 'gpt' || input.accountType !== 'oauth' || tier !== 'flex')
  const reasoningEfforts = intersectModelCapability(supportedModels, input.modelOptions, (option) => option.supportedReasoningEfforts)
  return { serviceTiers, reasoningEfforts }
}

export function availableAccountGptServiceTierOptions(capabilities: AccountGptRequestOverrideCapabilities) {
  if (!capabilities.serviceTiers.length) return []
  return [
    { label: '不覆盖客户端设置', value: '' },
    { label: localizedCapabilityLabels.default, value: 'default' },
    ...capabilities.serviceTiers.map((value) => ({ label: capabilityLabel(value), value }))
  ]
}

export function availableAccountGptReasoningEffortOptions(capabilities: AccountGptRequestOverrideCapabilities) {
  if (!capabilities.reasoningEfforts.length) return []
  return [
    { label: '不覆盖客户端设置', value: '' },
    ...capabilities.reasoningEfforts.map((value) => ({ label: capabilityLabel(value), value }))
  ]
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

function intersectModelCapability(
  supportedModels: string[],
  modelOptions: AccountGptModelCapabilityOption[],
  readValues: (option: AccountGptModelCapabilityOption) => string[] | undefined
): string[] {
  if (!supportedModels.length) return []
  const optionsByModel = new Map(modelOptions.map((option) => [option.value.trim(), option]))
  const first = optionsByModel.get(supportedModels[0])
  const firstValues = first ? normalizeCapabilityTokens(readValues(first)) : []
  if (!first || !firstValues.length) return []
  let intersection = new Set(firstValues)
  for (const model of supportedModels.slice(1)) {
    const option = optionsByModel.get(model)
    if (!option) return []
    const supported = new Set(normalizeCapabilityTokens(readValues(option)))
    intersection = new Set([...intersection].filter((value) => supported.has(value)))
  }
  return firstValues.filter((value) => intersection.has(value))
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
