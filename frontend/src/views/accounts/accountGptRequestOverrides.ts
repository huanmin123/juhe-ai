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

const serviceTierOrder: ProviderModelServiceTier[] = ['priority', 'flex']
const reasoningEffortOrder: ProviderModelReasoningEffort[] = [
  'none',
  'minimal',
  'low',
  'medium',
  'high',
  'xhigh',
  'max'
]

const serviceTierSet = new Set<ProviderModelServiceTier>(serviceTierOrder)
const reasoningEffortSet = new Set<ProviderModelReasoningEffort>(reasoningEffortOrder)

export const accountGptServiceTierOptions: Array<{
  label: string
  value: AccountGptServiceTierOverride
}> = [
  { label: '不覆盖客户端设置', value: '' },
  { label: '标准（Default）', value: 'default' },
  { label: '优先（Priority）', value: 'priority' },
  { label: '弹性（Flex）', value: 'flex' }
]

export const accountGptReasoningEffortOptions: Array<{
  label: string
  value: AccountGptReasoningEffortOverride
}> = [
  { label: '不覆盖客户端设置', value: '' },
  { label: '不思考（None）', value: 'none' },
  { label: '最少（Minimal）', value: 'minimal' },
  { label: '低（Low）', value: 'low' },
  { label: '中（Medium）', value: 'medium' },
  { label: '高（High）', value: 'high' },
  { label: '更高（XHigh）', value: 'xhigh' },
  { label: '最大（Max）', value: 'max' }
]

export function accountGptRequestOverrideCapabilities(input: {
  accountType: AccountType
  modelOptions: AccountGptModelCapabilityOption[]
  supportedModels: string[]
}): AccountGptRequestOverrideCapabilities {
  const supportedModels = uniqueTextList(input.supportedModels)
  const serviceTiers = intersectModelCapability(
    supportedModels,
    input.modelOptions,
    (option) => option.supportedServiceTiers,
    serviceTierOrder,
    serviceTierSet
  ).filter((tier) => input.accountType !== 'oauth' || tier !== 'flex')
  const reasoningEfforts = intersectModelCapability(
    supportedModels,
    input.modelOptions,
    (option) => option.supportedReasoningEfforts,
    reasoningEffortOrder,
    reasoningEffortSet
  )
  return { serviceTiers, reasoningEfforts }
}

export function availableAccountGptServiceTierOptions(
  capabilities: AccountGptRequestOverrideCapabilities
): typeof accountGptServiceTierOptions {
  const allowed = new Set<AccountGptServiceTierOverride>([''])
  if (capabilities.serviceTiers.length > 0) {
    allowed.add('default')
  }
  for (const tier of capabilities.serviceTiers) {
    if (tier === 'priority' || tier === 'flex') allowed.add(tier)
  }
  return accountGptServiceTierOptions.filter((option) => allowed.has(option.value))
}

export function availableAccountGptReasoningEffortOptions(
  capabilities: AccountGptRequestOverrideCapabilities
): typeof accountGptReasoningEffortOptions {
  const allowed = new Set<AccountGptReasoningEffortOverride>(['', ...capabilities.reasoningEfforts])
  return accountGptReasoningEffortOptions.filter((option) => allowed.has(option.value))
}

export function isAccountGptServiceTierOverrideAvailable(
  value: AccountGptServiceTierOverride,
  capabilities: AccountGptRequestOverrideCapabilities
): boolean {
  if (!value) return true
  if (value === 'default') return capabilities.serviceTiers.length > 0
  return capabilities.serviceTiers.includes(value)
}

export function isAccountGptReasoningEffortOverrideAvailable(
  value: AccountGptReasoningEffortOverride,
  capabilities: AccountGptRequestOverrideCapabilities
): boolean {
  return !value || capabilities.reasoningEfforts.includes(value)
}

export function accountGptRequestOverridesForForm(
  providerCode: string,
  credentials: Record<string, unknown>
): Pick<AccountGptRequestOverrideForm, 'serviceTierOverride' | 'reasoningEffortOverride'> {
  if (providerCode !== 'gpt') {
    return {
      serviceTierOverride: '',
      reasoningEffortOverride: ''
    }
  }
  return {
    serviceTierOverride: normalizeServiceTierOverride(credentials.service_tier_override),
    reasoningEffortOverride: normalizeReasoningEffortOverride(credentials.reasoning_effort_override)
  }
}

export function writeAccountGptRequestOverrides(
  credentials: Record<string, unknown>,
  form: AccountGptRequestOverrideForm
): void {
  delete credentials.service_tier_override
  delete credentials.reasoning_effort_override
  if (form.providerCode !== 'gpt') return
  if (form.serviceTierOverride) {
    credentials.service_tier_override = form.serviceTierOverride
  }
  if (form.reasoningEffortOverride) {
    credentials.reasoning_effort_override = form.reasoningEffortOverride
  }
}

interface AccountGptRequestOverrideForm {
  providerCode: string
  serviceTierOverride: AccountGptServiceTierOverride
  reasoningEffortOverride: AccountGptReasoningEffortOverride
}

function intersectModelCapability<TValue extends string>(
  supportedModels: string[],
  modelOptions: AccountGptModelCapabilityOption[],
  readValues: (option: AccountGptModelCapabilityOption) => TValue[] | undefined,
  order: TValue[],
  allowedValues: ReadonlySet<TValue>
): TValue[] {
  if (!supportedModels.length) return []
  const optionsByModel = new Map(modelOptions.map((option) => [option.value.trim(), option]))
  let intersection = new Set<TValue>(order)
  for (const model of supportedModels) {
    const option = optionsByModel.get(model)
    const rawValues = option ? readValues(option) : undefined
    if (!option || !Array.isArray(rawValues)) return []
    const supported = new Set<TValue>()
    for (const value of rawValues) {
      if (allowedValues.has(value)) supported.add(value)
    }
    intersection = new Set([...intersection].filter((value) => supported.has(value)))
  }
  return order.filter((value) => intersection.has(value))
}

function normalizeServiceTierOverride(value: unknown): AccountGptServiceTierOverride {
  if (value === 'default' || value === 'priority' || value === 'flex') return value
  return ''
}

function normalizeReasoningEffortOverride(value: unknown): AccountGptReasoningEffortOverride {
  return reasoningEffortSet.has(value as ProviderModelReasoningEffort)
    ? value as ProviderModelReasoningEffort
    : ''
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
