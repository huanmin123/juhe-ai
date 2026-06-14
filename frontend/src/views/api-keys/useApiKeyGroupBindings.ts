import { computed, type ComputedRef, type Ref } from 'vue'

import { message } from '@/lib/antd'
import { rememberGroupLabel } from '@/shared/groupLabelCache'
import type { ApiKeyGroupRouteStrategy, ApiKeySummary, GroupOptionSummary } from '@/types/domain'
import {
  createExistingGroupBindingFormRow,
  createGroupBindingFormRow,
  normalizedGroupBindingPayload,
  type ApiKeyGroupBindingFormRow,
  type ApiKeyGroupBindingPayload
} from './apiKeyFormModel'
import { apiKeyGroupBindings } from './apiKeyFormatters'

interface ApiKeyGroupBindingFormState {
  groupRouteStrategy: ApiKeyGroupRouteStrategy
  groupBindings: ApiKeyGroupBindingFormRow[]
}

interface UseApiKeyGroupBindingsInput {
  form: ApiKeyGroupBindingFormState
  groups: Ref<GroupOptionSummary[]>
  formGroupSelectDisabled: ComputedRef<boolean>
}

export function useApiKeyGroupBindings(input: UseApiKeyGroupBindingsInput) {
  const formGroupBindingIds = computed(() => input.form.groupBindings.map((binding) => binding.groupId).filter(Boolean))
  const formGroupBindingSelections = computed(() => input.form.groupBindings.map((binding) => binding.group))
  const addGroupBindingDisabledReason = computed(() => {
    if (input.formGroupSelectDisabled.value) return '请先选择系统账户'
    if (input.form.groupBindings.some((binding) => !binding.groupId.trim())) return '请先选择已有绑定分组'
    if (!nextAvailableGroupForNewBinding()) return '没有可继续绑定的分组'
    return undefined
  })
  const canAddGroupBinding = computed(() => !addGroupBindingDisabledReason.value)

  function createGroupBindingRow(group: GroupOptionSummary): ApiKeyGroupBindingFormRow {
    return createGroupBindingFormRow({ id: group.id, name: group.name }, 'active', 1, {
      providerCode: group.providerCode,
      providerProtocolProfileId: group.providerProtocolProfileId,
      groupEnabled: group.enabled
    })
  }

  function existingGroupBindingRows(apiKey: ApiKeySummary): ApiKeyGroupBindingFormRow[] {
    return apiKeyGroupBindings(apiKey).map((binding) => {
      rememberGroupLabel(binding.groupId, binding.groupName)
      return createExistingGroupBindingFormRow(binding)
    })
  }

  function addGroupBinding() {
    if (addGroupBindingDisabledReason.value) {
      message.warning(addGroupBindingDisabledReason.value)
      return
    }
    const nextGroup = nextAvailableGroupForNewBinding()
    if (!nextGroup) {
      message.warning('没有可继续绑定的分组')
      return
    }
    input.form.groupBindings.push(createGroupBindingRow(nextGroup))
  }

  function handleGroupBindingChange(index: number) {
    const binding = input.form.groupBindings[index]
    if (!binding?.groupId) return
    const group = groupOptionForId(binding.groupId)
    if (!group) return
    if (!isApiKeyBindableGroup(group)) {
      message.warning('该分组当前不可用，请选择其他 API Key 号池')
      clearGroupBindingSelection(binding)
      return
    }
    const providerProtocolProfileId = selectedGroupBindingProviderProfileId(index)
    if (providerProtocolProfileId && group.providerProtocolProfileId !== providerProtocolProfileId) {
      message.warning('同一个 API Key 的绑定号池必须属于同一供应商协议档案')
      clearGroupBindingSelection(binding)
      return
    }
    binding.providerCode = group.providerCode
    binding.providerProtocolProfileId = group.providerProtocolProfileId
    binding.groupEnabled = group.enabled
    if (!group.enabled && binding.status === 'active') {
      message.warning('已停用分组只能作为停用号池保留，不能参与路由')
      binding.status = 'disabled'
    }
  }

  function removeGroupBinding(index: number) {
    if (input.form.groupBindings.length <= 1) return
    input.form.groupBindings.splice(index, 1)
  }

  function moveGroupBinding(index: number, offset: -1 | 1) {
    const nextIndex = index + offset
    if (nextIndex < 0 || nextIndex >= input.form.groupBindings.length) return
    const [item] = input.form.groupBindings.splice(index, 1)
    if (!item) return
    input.form.groupBindings.splice(nextIndex, 0, item)
  }

  function groupBindingPriorityText(index: number): string {
    if (input.form.groupRouteStrategy === 'round_robin') return `轮询 ${index + 1}`
    if (input.form.groupRouteStrategy === 'weighted_round_robin') return `权重 ${index + 1}`
    return index === 0 ? '主号池' : `备 ${index}`
  }

  function groupOptionsForBinding(index: number): GroupOptionSummary[] {
    const providerProtocolProfileId = selectedGroupBindingProviderProfileId(index)
    return input.groups.value.filter((group) => (
      isApiKeyBindableGroup(group)
      && group.enabled
      && (!providerProtocolProfileId || group.providerProtocolProfileId === providerProtocolProfileId)
    ))
  }

  function hiddenGroupBindingIds(index: number): string[] {
    const selectedIds = input.form.groupBindings
      .map((binding, bindingIndex) => bindingIndex === index ? undefined : binding.groupId.trim())
      .filter((groupId): groupId is string => Boolean(groupId))
    const disabledIds = input.groups.value
      .filter((group) => !group.enabled)
      .map((group) => group.id)
    return [...new Set([...selectedIds, ...disabledIds])]
  }

  function validateGroupBindingsPayload(): ApiKeyGroupBindingPayload[] | undefined {
    const groupBindings = normalizedGroupBindingPayload(input.form.groupBindings)
    if (!groupBindings.length) {
      message.warning('请至少选择一个绑定分组')
      return undefined
    }
    const emptyBindingIndex = groupBindings.findIndex((binding) => !binding.groupId)
    if (emptyBindingIndex >= 0) {
      message.warning(`请先选择第 ${emptyBindingIndex + 1} 个绑定分组`)
      return undefined
    }
    if (!groupBindings.some((binding) => binding.status === 'active')) {
      message.warning('至少需要一个启用分组')
      return undefined
    }
    if (new Set(groupBindings.map((binding) => binding.groupId)).size !== groupBindings.length) {
      message.warning('绑定分组不能重复')
      return undefined
    }
    const providerProtocolProfileIds = new Set(groupBindings.map((binding, index) => groupOptionForId(binding.groupId)?.providerProtocolProfileId ?? input.form.groupBindings[index]?.providerProtocolProfileId).filter(Boolean))
    if (providerProtocolProfileIds.size > 1) {
      message.warning('同一个 API Key 的绑定号池必须属于同一供应商协议档案')
      return undefined
    }
    const disabledActiveGroups = groupBindings
      .filter((binding) => binding.status === 'active')
      .map((binding) => groupOptionForId(binding.groupId))
      .filter((group): group is GroupOptionSummary => Boolean(group && !group.enabled))
    if (disabledActiveGroups.length) {
      message.warning(`已停用分组不能作为启用号池：${disabledActiveGroups.map((group) => group.name).join('、')}`)
      return undefined
    }
    return groupBindings
  }

  function nextAvailableGroupForNewBinding(): GroupOptionSummary | undefined {
    const selectedIds = new Set(input.form.groupBindings.map((binding) => binding.groupId.trim()).filter(Boolean))
    const providerProtocolProfileId = selectedGroupBindingProviderProfileId()
    return input.groups.value.find((group) => (
      isApiKeyBindableGroup(group)
      && group.enabled
      && !selectedIds.has(group.id)
      && (!providerProtocolProfileId || group.providerProtocolProfileId === providerProtocolProfileId)
    ))
  }

  function selectedGroupBindingProviderProfileId(excludeIndex?: number): string | undefined {
    for (const [index, binding] of input.form.groupBindings.entries()) {
      if (excludeIndex === index) continue
      const providerProtocolProfileId = groupOptionForId(binding.groupId)?.providerProtocolProfileId ?? binding.providerProtocolProfileId
      if (providerProtocolProfileId) return providerProtocolProfileId
    }
    return undefined
  }

  function groupOptionForId(groupId: string | undefined): GroupOptionSummary | undefined {
    const id = groupId?.trim()
    if (!id) return undefined
    return input.groups.value.find((group) => group.id === id)
  }

  return {
    addGroupBinding,
    addGroupBindingDisabledReason,
    canAddGroupBinding,
    createGroupBindingRow,
    existingGroupBindingRows,
    formGroupBindingIds,
    formGroupBindingSelections,
    groupBindingPriorityText,
    groupOptionsForBinding,
    handleGroupBindingChange,
    hiddenGroupBindingIds,
    moveGroupBinding,
    removeGroupBinding,
    validateGroupBindingsPayload
  }
}

function clearGroupBindingSelection(binding: ApiKeyGroupBindingFormRow): void {
  binding.groupId = ''
  binding.group = undefined
  binding.providerCode = undefined
  binding.providerProtocolProfileId = undefined
  binding.groupEnabled = undefined
}

function isApiKeyBindableGroup(group: GroupOptionSummary): boolean {
  if (!group.enabled) return false
  if (group.permissions?.canBindToApiKey === false) return false
  if (group.accessType !== 'authorized') return true
  if (group.authorizationStatus !== 'active') return false
  if (!group.authorizationExpiresAt) return true
  const expiresAt = Date.parse(group.authorizationExpiresAt)
  return !Number.isFinite(expiresAt) || expiresAt > Date.now()
}
