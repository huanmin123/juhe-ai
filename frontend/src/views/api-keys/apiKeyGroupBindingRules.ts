import type { GroupOptionSummary } from '@/types/domain'
import type {
  ApiKeyGroupBindingFormRow,
  ApiKeyGroupBindingPayload
} from './apiKeyFormModel'

export function apiKeyGroupOptionForId(groups: GroupOptionSummary[], groupId: string | undefined): GroupOptionSummary | undefined {
  const id = groupId?.trim()
  if (!id) return undefined
  return groups.find((group) => group.id === id)
}

export function isApiKeyBindableGroup(group: GroupOptionSummary): boolean {
  if (!group.enabled) return false
  if (group.permissions?.canBindToApiKey === false) return false
  if (group.accessType !== 'authorized') return true
  if (group.authorizationStatus !== 'active') return false
  if (!group.authorizationExpiresAt) return true
  const expiresAt = Date.parse(group.authorizationExpiresAt)
  return !Number.isFinite(expiresAt) || expiresAt > Date.now()
}

export function selectedApiKeyGroupBindingProviderProfileId(input: {
  bindings: ApiKeyGroupBindingFormRow[]
  groups: GroupOptionSummary[]
  excludeIndex?: number
}): string | undefined {
  for (const [index, binding] of input.bindings.entries()) {
    if (input.excludeIndex === index) continue
    const providerProtocolProfileId = apiKeyGroupOptionForId(input.groups, binding.groupId)?.providerProtocolProfileId ?? binding.providerProtocolProfileId
    if (providerProtocolProfileId) return providerProtocolProfileId
  }
  return undefined
}

export function apiKeyGroupOptionsForBinding(input: {
  bindings: ApiKeyGroupBindingFormRow[]
  groups: GroupOptionSummary[]
  index: number
  allowMixedProviderProtocolProfiles?: boolean
}): GroupOptionSummary[] {
  return input.groups.filter((group) => (
    isApiKeyBindableGroup(group)
    && group.enabled
  ))
}

export function hiddenApiKeyGroupBindingIds(input: {
  bindings: ApiKeyGroupBindingFormRow[]
  groups: GroupOptionSummary[]
  index: number
}): string[] {
  const selectedIds = input.bindings
    .map((binding, bindingIndex) => bindingIndex === input.index ? undefined : binding.groupId.trim())
    .filter((groupId): groupId is string => Boolean(groupId))
  const disabledIds = input.groups
    .filter((group) => !group.enabled)
    .map((group) => group.id)
  return [...new Set([...selectedIds, ...disabledIds])]
}

export function nextAvailableApiKeyGroupForNewBinding(input: {
  bindings: ApiKeyGroupBindingFormRow[]
  groups: GroupOptionSummary[]
  allowMixedProviderProtocolProfiles?: boolean
}): GroupOptionSummary | undefined {
  const selectedIds = new Set(input.bindings.map((binding) => binding.groupId.trim()).filter(Boolean))
  return input.groups.find((group) => (
    isApiKeyBindableGroup(group)
    && group.enabled
    && !selectedIds.has(group.id)
  ))
}

export function validateApiKeyGroupBindings(input: {
  groupBindings: ApiKeyGroupBindingPayload[]
  formBindings: ApiKeyGroupBindingFormRow[]
  groups: GroupOptionSummary[]
  allowMixedProviderProtocolProfiles?: boolean
}): string | undefined {
  if (!input.groupBindings.length) {
    return '请至少选择一个绑定分组'
  }
  const emptyBindingIndex = input.groupBindings.findIndex((binding) => !binding.groupId)
  if (emptyBindingIndex >= 0) {
    return `请先选择第 ${emptyBindingIndex + 1} 个绑定分组`
  }
  if (!input.groupBindings.some((binding) => binding.status === 'active')) {
    return '至少需要一个启用分组'
  }
  if (new Set(input.groupBindings.map((binding) => binding.groupId)).size !== input.groupBindings.length) {
    return '绑定分组不能重复'
  }
  const disabledActiveGroups = input.groupBindings
    .filter((binding) => binding.status === 'active')
    .map((binding) => apiKeyGroupOptionForId(input.groups, binding.groupId))
    .filter((group): group is GroupOptionSummary => Boolean(group && !group.enabled))
  if (disabledActiveGroups.length) {
    return `已停用分组不能作为启用号池：${disabledActiveGroups.map((group) => group.name).join('、')}`
  }
  return undefined
}
