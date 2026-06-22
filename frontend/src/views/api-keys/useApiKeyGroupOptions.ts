import { message } from '@/lib/antd'
import { extractApiErrorMessage } from '@/shared/apiError'
import { rememberGroupLabels, type GroupSelection } from '@/shared/groupLabelCache'
import { createShortLivedQueryCache } from '@/shared/shortLivedQueryCache'
import {
  localSelectStorageKey,
  readLocalSelectOptionWindow,
  removeLocalSelectOptionWindowValues,
  removeLocalSelectPreferenceValues,
  writeLocalSelectOptionWindow
} from '@/shared/selectLocalPreferenceCache'
import type { GroupOptionSummary } from '@/types/domain'
import type { ApiKeyGroupBindingFormRow } from './apiKeyFormModel'
import { ref, type ComputedRef, type Ref } from 'vue'

export type ApiKeyScopeParams = { systemAccountId: string } | undefined

export interface ApiKeyGroupOptionsScope {
  systemAccountId?: string
  providerProtocolProfileId?: string
  selectedIds?: string[]
}

export interface ApiKeyGroupOptionsLoadOptions {
  useLocalWindow?: boolean
}

interface ApiKeyGroupOptionsApi {
  options(params: {
    systemAccountId?: string
    keyword?: string
    ids?: string[]
    providerProtocolProfileId?: string
    limit?: number
    preferDefault?: boolean
  }): Promise<GroupOptionSummary[]>
}

interface UseApiKeyGroupOptionsInput {
  groupsApi: ApiKeyGroupOptionsApi
  isManagementView: Ref<boolean>
  isFormContext: () => boolean
  listScopeParams: ComputedRef<ApiKeyScopeParams>
  formScopeParams: ComputedRef<ApiKeyScopeParams>
  groupFilterSelection: Ref<GroupSelection | undefined>
  formGroupBindings: () => ApiKeyGroupBindingFormRow[]
  formGroupBindingIds: ComputedRef<string[]>
  allowMixedProviderProtocolProfiles?: () => boolean
  onGroupFilterCleared: () => void
}

export function useApiKeyGroupOptions(input: UseApiKeyGroupOptionsInput) {
  const groups = ref<GroupOptionSummary[]>([])
  const groupOptionsLoading = ref(false)
  const groupOptionsCache = createShortLivedQueryCache<GroupOptionSummary[]>({ ttlMs: 10_000 })

  let groupOptionsRequestId = 0
  let groupOptionsLoadingKey: string | undefined
  let groupOptionsLoadingPromise: Promise<void> | undefined
  let groupOptionsKeyword = ''
  let groupOptionsSearchTimer: ReturnType<typeof window.setTimeout> | undefined

  async function loadGroupOptions(
    keyword = groupOptionsKeyword,
    force = false,
    scopeOverride?: ApiKeyGroupOptionsScope,
    loadOptions: ApiKeyGroupOptionsLoadOptions = {}
  ): Promise<void> {
    groupOptionsKeyword = keyword
    const scope = normalizedGroupOptionsScope(scopeOverride)
    const requestKeyword = normalizeOptionKeyword(keyword)
    const requestKey = JSON.stringify([
      input.isManagementView.value ? `management:${scope.systemAccountId || 'all'}` : 'self',
      requestKeyword ?? '',
      scope.providerProtocolProfileId,
      scope.selectedIds
    ])
    if (groupOptionsLoadingKey === requestKey && groupOptionsLoadingPromise) {
      return groupOptionsLoadingPromise
    }

    const requestId = ++groupOptionsRequestId
    const optionWindowKey = groupOptionWindowKey(scope.systemAccountId, scope.providerProtocolProfileId, requestKeyword)
    const useLocalWindow = loadOptions.useLocalWindow !== false
    const localWindowGroups = !force && useLocalWindow ? readLocalSelectOptionWindow<GroupOptionSummary>(optionWindowKey) : undefined
    if (localWindowGroups?.length) {
      groupOptionsLoading.value = false
      rememberGroupLabels(localWindowGroups)
      syncSelectedGroupSelections(localWindowGroups)
      groups.value = localWindowGroups
    }
    if (!force) {
      const cachedGroups = groupOptionsCache.get(requestKey)
      if (cachedGroups) {
        groupOptionsLoadingKey = undefined
        groupOptionsLoadingPromise = undefined
        groupOptionsLoading.value = false
        rememberGroupLabels(cachedGroups)
        syncSelectedGroupSelections(cachedGroups)
        if (useLocalWindow) {
          writeLocalSelectOptionWindow(optionWindowKey, cachedGroups)
        }
        groups.value = cachedGroups
        return
      }
    }

    groupOptionsLoading.value = !localWindowGroups?.length
    groupOptionsLoadingKey = requestKey
    groupOptionsLoadingPromise = (async () => {
      try {
        let nextGroups = await input.groupsApi.options({
          systemAccountId: scope.systemAccountId || undefined,
          providerProtocolProfileId: scope.providerProtocolProfileId || undefined,
          keyword: requestKeyword,
          limit: 50,
          preferDefault: true
        })
        nextGroups = await ensureSelectedGroupOptions(nextGroups, scope, optionWindowKey)
        rememberGroupLabels(nextGroups)
        syncSelectedGroupSelections(nextGroups)
        groupOptionsCache.set(requestKey, nextGroups)
        if (useLocalWindow) {
          writeLocalSelectOptionWindow(optionWindowKey, nextGroups)
        }
        if (requestId !== groupOptionsRequestId) return
        groups.value = nextGroups
      } catch (error) {
        if (requestId !== groupOptionsRequestId) return
        console.error(error)
        message.error(extractApiErrorMessage(error, '加载分组选项失败'))
      } finally {
        if (groupOptionsLoadingKey === requestKey) {
          groupOptionsLoadingKey = undefined
          groupOptionsLoadingPromise = undefined
        }
        if (requestId === groupOptionsRequestId) {
          groupOptionsLoading.value = false
        }
      }
    })()
    return groupOptionsLoadingPromise
  }

  function handleGroupOptionsDropdown(open: boolean): void {
    if (open) {
      void loadGroupOptions()
    }
  }

  function handleGroupOptionsSearch(value: string): void {
    groupOptionsKeyword = value
    clearGroupOptionsSearchTimer()
    groupOptionsSearchTimer = window.setTimeout(() => {
      groupOptionsSearchTimer = undefined
      void loadGroupOptions(groupOptionsKeyword)
    }, 250)
  }

  function handleFormGroupOptionsDropdown(open: boolean): void {
    if (open) {
      void loadGroupOptions(groupOptionsKeyword, false, {
        systemAccountId: input.formScopeParams.value?.systemAccountId,
        selectedIds: input.formGroupBindingIds.value
      })
    }
  }

  function handleFormGroupOptionsSearch(value: string): void {
    groupOptionsKeyword = value
    clearGroupOptionsSearchTimer()
    groupOptionsSearchTimer = window.setTimeout(() => {
      groupOptionsSearchTimer = undefined
      void loadGroupOptions(groupOptionsKeyword, false, {
        systemAccountId: input.formScopeParams.value?.systemAccountId,
        selectedIds: input.formGroupBindingIds.value
      })
    }, 250)
  }

  function resetGroupOptionsSearch(): void {
    groupOptionsKeyword = ''
    clearGroupOptionsSearchTimer()
  }

  function clearGroupOptionsSearchTimer(): void {
    if (groupOptionsSearchTimer && typeof window !== 'undefined') {
      window.clearTimeout(groupOptionsSearchTimer)
      groupOptionsSearchTimer = undefined
    }
  }

  function syncSelectedGroupSelections(nextGroups = groups.value): void {
    const groupFilterId = input.groupFilterSelection.value?.id
    if (groupFilterId) {
      input.groupFilterSelection.value = selectedGroupFromOptions(groupFilterId, nextGroups, input.groupFilterSelection.value)
    }
    for (const binding of input.formGroupBindings()) {
      if (binding.groupId) {
        const groupOption = nextGroups.find((group) => group.id === binding.groupId)
        if (groupOption) {
          binding.providerCode = groupOption.providerCode
          binding.providerProtocolProfileId = groupOption.providerProtocolProfileId
          binding.groupEnabled = groupOption.enabled
        }
        binding.group = selectedGroupFromOptions(binding.groupId, nextGroups, binding.group)
      }
    }
  }

  function selectedGroupSelection(id: string | undefined): GroupSelection | undefined {
    const normalizedId = id?.trim()
    if (!normalizedId) return undefined
    const group = groups.value.find((item) => item.id === normalizedId)
    if (group) return { id: group.id, name: group.name }
    if (input.groupFilterSelection.value?.id === normalizedId) return input.groupFilterSelection.value
    const selectedBinding = input.formGroupBindings().find((binding) => binding.group?.id === normalizedId)
    if (selectedBinding?.group) return selectedBinding.group
    return undefined
  }

  function normalizedGroupOptionsScope(scopeOverride?: ApiKeyGroupOptionsScope): Required<ApiKeyGroupOptionsScope> {
    const formContext = input.isFormContext()
    const systemAccountId = scopeOverride?.systemAccountId
      ?? (formContext ? input.formScopeParams.value?.systemAccountId : input.listScopeParams.value?.systemAccountId)
      ?? ''
    const selectedIds = scopeOverride?.selectedIds
      ?? (formContext
        ? [input.groupFilterSelection.value?.id, ...input.formGroupBindingIds.value]
        : [input.groupFilterSelection.value?.id])
    const providerProtocolProfileId = scopeOverride?.providerProtocolProfileId
      ?? apiKeyGroupOptionsProviderProtocolProfileId({
        formContext,
        allowMixedProviderProtocolProfiles: input.allowMixedProviderProtocolProfiles?.() ?? true,
        formBindings: input.formGroupBindings()
      })
    return {
      systemAccountId: systemAccountId.trim(),
      providerProtocolProfileId: providerProtocolProfileId.trim(),
      selectedIds: [...new Set(selectedIds.filter((id): id is string => Boolean(id?.trim())).map((id) => id.trim()).sort())]
    }
  }

  async function ensureSelectedGroupOptions(
    nextGroups: GroupOptionSummary[],
    scope: Required<ApiKeyGroupOptionsScope>,
    optionWindowKey: string
  ): Promise<GroupOptionSummary[]> {
    const missingIds = scope.selectedIds.filter((id) => !nextGroups.some((group) => group.id === id))
    if (!missingIds.length) return nextGroups
    const selectedGroups = await Promise.all(missingIds.map(async (id) => {
      try {
        return await input.groupsApi.options({
          systemAccountId: scope.systemAccountId || undefined,
          ids: [id],
          limit: 1,
          preferDefault: true
        })
      } catch {
        return []
      }
    }))
    const foundIds = new Set(selectedGroups.flat().map((group) => group.id))
    handleMissingGroupOptions(missingIds.filter((id) => !foundIds.has(id)), optionWindowKey)
    return mergeOptionsById(selectedGroups.flat(), nextGroups)
  }

  function handleMissingGroupOptions(ids: string[], optionWindowKey: string): void {
    const missingIds = [...new Set(ids.map((id) => id.trim()).filter(Boolean))]
    if (!missingIds.length) return
    let clearedFilter = false
    let clearedFilterId: string | undefined
    const groupFilterId = input.groupFilterSelection.value?.id
    if (groupFilterId && missingIds.includes(groupFilterId)) {
      clearedFilterId = groupFilterId
      input.groupFilterSelection.value = undefined
      clearedFilter = true
    }

    const clearedBindingIds: string[] = []
    for (const binding of input.formGroupBindings()) {
      const bindingId = binding.groupId.trim()
      if (!missingIds.includes(bindingId)) continue
      if (binding.group?.id === bindingId) continue
      binding.groupId = ''
      binding.group = undefined
      binding.providerCode = undefined
      binding.providerProtocolProfileId = undefined
      binding.groupEnabled = undefined
      clearedBindingIds.push(bindingId)
    }

    const removableIds = [...new Set([...clearedBindingIds, ...(clearedFilterId ? [clearedFilterId] : [])])]
    if (removableIds.length) {
      removeLocalSelectOptionWindowValues(optionWindowKey, removableIds)
      removeLocalSelectPreferenceValues('groups', removableIds)
    }
    if (clearedFilter || clearedBindingIds.length) {
      message.warning('已移除不存在或无权访问的分组，请重新选择')
    }
    if (clearedFilter) {
      input.onGroupFilterCleared()
    }
  }

  function groupOptionWindowKey(systemAccountId: string | undefined, providerProtocolProfileId: string | undefined, requestKeyword: string | undefined): string {
    return localSelectStorageKey([
      'group-options',
      input.isManagementView.value ? 'management' : 'self',
      systemAccountId ?? 'all',
      providerProtocolProfileId || 'all-profiles',
      'api-keys',
      requestKeyword ?? ''
    ])
  }

  return {
    clearGroupOptionsSearchTimer,
    groups,
    groupOptionsLoading,
    handleFormGroupOptionsDropdown,
    handleFormGroupOptionsSearch,
    handleGroupOptionsDropdown,
    handleGroupOptionsSearch,
    loadGroupOptions,
    resetGroupOptionsSearch,
    selectedGroupSelection,
    syncSelectedGroupSelections
  }
}

export function apiKeyGroupOptionsProviderProtocolProfileId(input: {
  formContext: boolean
  allowMixedProviderProtocolProfiles: boolean
  formBindings: ApiKeyGroupBindingFormRow[]
}): string {
  if (!input.formContext || input.allowMixedProviderProtocolProfiles) return ''
  for (const binding of input.formBindings) {
    const value = binding.providerProtocolProfileId?.trim()
    if (value) return value
  }
  return ''
}

function selectedGroupFromOptions(
  id: string | undefined,
  nextGroups: GroupOptionSummary[],
  fallback?: GroupSelection
): GroupSelection | undefined {
  const normalizedId = id?.trim()
  if (!normalizedId) return undefined
  const group = nextGroups.find((item) => item.id === normalizedId)
  if (group) return { id: group.id, name: group.name }
  return fallback?.id === normalizedId ? fallback : undefined
}

function mergeOptionsById<T extends { id: string }>(leading: T[], trailing: T[]): T[] {
  const merged = new Map<string, T>()
  for (const item of [...leading, ...trailing]) {
    merged.set(item.id, item)
  }
  return [...merged.values()]
}

function normalizeOptionKeyword(value?: string): string | undefined {
  const keyword = value?.trim()
  return keyword ? keyword : undefined
}
