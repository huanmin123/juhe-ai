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
import {
  apiKeyGroupOptionForId,
  apiKeyGroupOptionsForBinding,
  hiddenApiKeyGroupBindingIds,
  isApiKeyBindableGroup,
  nextAvailableApiKeyGroupForNewBinding,
  selectedApiKeyGroupBindingProviderProfileId,
  validateApiKeyGroupBindings
} from './apiKeyGroupBindingRules'

interface ApiKeyGroupBindingFormState {
  routeMode?: 'normal' | 'hybrid'
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
    if (!allowMixedProviderProtocolProfiles() && providerProtocolProfileId && group.providerProtocolProfileId !== providerProtocolProfileId) {
      message.warning('同一个普通 API Key 的绑定号池必须属于同一供应商协议档案')
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
    return apiKeyGroupOptionsForBinding({
      bindings: input.form.groupBindings,
      groups: input.groups.value,
      index,
      allowMixedProviderProtocolProfiles: allowMixedProviderProtocolProfiles()
    })
  }

  function hiddenGroupBindingIds(index: number): string[] {
    return hiddenApiKeyGroupBindingIds({
      bindings: input.form.groupBindings,
      groups: input.groups.value,
      index
    })
  }

  function validateGroupBindingsPayload(): ApiKeyGroupBindingPayload[] | undefined {
    const groupBindings = normalizedGroupBindingPayload(input.form.groupBindings)
    const validationMessage = validateApiKeyGroupBindings({
      groupBindings,
      formBindings: input.form.groupBindings,
      groups: input.groups.value,
      allowMixedProviderProtocolProfiles: allowMixedProviderProtocolProfiles()
    })
    if (validationMessage) {
      message.warning(validationMessage)
      return undefined
    }
    return groupBindings
  }

  function nextAvailableGroupForNewBinding(): GroupOptionSummary | undefined {
    return nextAvailableApiKeyGroupForNewBinding({
      bindings: input.form.groupBindings,
      groups: input.groups.value,
      allowMixedProviderProtocolProfiles: allowMixedProviderProtocolProfiles()
    })
  }

  function selectedGroupBindingProviderProfileId(excludeIndex?: number): string | undefined {
    return selectedApiKeyGroupBindingProviderProfileId({
      bindings: input.form.groupBindings,
      groups: input.groups.value,
      excludeIndex
    })
  }

  function groupOptionForId(groupId: string | undefined): GroupOptionSummary | undefined {
    return apiKeyGroupOptionForId(input.groups.value, groupId)
  }

  function allowMixedProviderProtocolProfiles(): boolean {
    return input.form.routeMode === 'hybrid'
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
