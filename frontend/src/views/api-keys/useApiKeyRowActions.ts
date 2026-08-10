import { ref } from 'vue'

import type { RowActionItem } from '@/components/rowActions'
import { loadProviderOptionsResource } from '@/composables/useProviderOptionsResource'
import type { useScopedApiKeysApi, useScopedRouteStrategiesApi } from '@/composables/useScopedDomainApi'
import { message } from '@/lib/antd'
import { extractApiErrorMessage } from '@/shared/apiError'
import { copyTextToClipboard } from '@/shared/clipboard'
import type { ApiKeySummary } from '@/types/domain'
import { loadAccountProviderModelOptionsResource } from '@/views/accounts/useAccountProviderModelOptions'
import { mergeApiKeyMutationResult } from './apiKeyMutation'
import { refreshedApiKeyListItem } from './apiKeyRefreshRow'
import type { ApiKeyScopeParams } from './apiKeyScope'
import {
  buildCcSwitchExportModelOptions,
  buildCcSwitchExportGroupOptions,
  buildCcSwitchExportUrl,
  isCcSwitchExportModelSelectionValid,
  shouldLoadCcSwitchExportModelOptions,
  type CcSwitchClientApp,
  type CcSwitchExportGroupOption,
  type CcSwitchExportModelOption
} from './ccswitchExport'

type ScopedApiKeysApi = ReturnType<typeof useScopedApiKeysApi>
type ScopedRouteStrategiesApi = ReturnType<typeof useScopedRouteStrategiesApi>

interface CreatedKeyPayload {
  key: string
  title: string
  message: string
}

interface UseApiKeyRowActionsInput {
  apiKeysApi: Pick<ScopedApiKeysApi, 'delete' | 'refreshKey' | 'secret' | 'update'>
  routeStrategiesApi: Pick<ScopedRouteStrategiesApi, 'editBasicDetail'>
  isManagementView: () => boolean
  operationScopeParams: (apiKey?: Pick<ApiKeySummary, 'systemAccountId'>) => ApiKeyScopeParams
  openEdit: (apiKey: ApiKeySummary) => void | Promise<void>
  reload: () => void | Promise<unknown>
  gatewayBaseUrl: () => string
  removeItems: (predicate: (item: ApiKeySummary) => boolean) => number
  showCreatedKey: (payload: CreatedKeyPayload) => void
  updateItems: (predicate: (item: ApiKeySummary) => boolean, updater: (item: ApiKeySummary) => ApiKeySummary) => number
}

export function useApiKeyRowActions(input: UseApiKeyRowActionsInput) {
  const statusUpdatingId = ref('')
  const keyRefreshingId = ref('')
  const keyCopyingId = ref('')
  const ccsExportPreparingId = ref('')
  const ccsExportingId = ref('')
  const ccsExportModalOpen = ref(false)
  const ccsExportApiKey = ref<ApiKeySummary>()
  const ccsExportGroups = ref<CcSwitchExportGroupOption[]>([])
  const ccsExportModelOptions = ref<CcSwitchExportModelOption[]>([])
  const ccsExportModelsLoading = ref(false)
  const ccsExportModelsReady = ref(false)
  const ccsExportModelOptionsGroupId = ref('')
  let ccsExportModelOptionsRequestId = 0
  let ccsExportModelSearchTimer: ReturnType<typeof setTimeout> | undefined

  async function copyKeyPreview(apiKey: ApiKeySummary): Promise<void> {
    if (keyCopyingId.value) return
    keyCopyingId.value = apiKey.id
    try {
      const key = (await input.apiKeysApi.secret(apiKey.id, input.operationScopeParams(apiKey))).key
      await copyTextToClipboard(key, '完整密钥已复制')
    } catch (error) {
      console.error(error)
      message.error(extractApiErrorMessage(error, '复制完整密钥失败'))
    } finally {
      if (keyCopyingId.value === apiKey.id) {
        keyCopyingId.value = ''
      }
    }
  }

  function apiKeyPrimaryActions(apiKey: ApiKeySummary): RowActionItem[] {
    const busy = apiKeyActionBusy(apiKey)
    const actions: RowActionItem[] = [
      { key: 'edit', label: '编辑', icon: 'edit', tone: 'primary', disabled: busy }
    ]
    if (!apiKey.isDefault && apiKey.purpose !== 'chat') {
      actions.push({
        key: 'delete',
        label: '删除',
        icon: 'delete',
        tone: 'danger',
        disabled: busy,
        confirmTitle: `确认删除 API Key ${apiKey.name}？`,
        confirmOkText: '删除'
      })
    }
    return actions
  }

  function apiKeyMoreActions(apiKey: ApiKeySummary): RowActionItem[] {
    const busy = apiKeyActionBusy(apiKey)
    const refreshDisabled = Boolean(keyRefreshingId.value) || statusUpdatingId.value === apiKey.id
    const ccsExportBusy = Boolean(ccsExportPreparingId.value) || Boolean(ccsExportingId.value)
    const statusAction: RowActionItem = apiKey.status === 'active'
      ? {
          key: 'disable',
          label: '停用',
          icon: 'disable',
          tone: 'warning',
          disabled: busy,
          confirmTitle: '确认停用这个 API Key？停用后后续请求会立即被拒绝；如配置了时间计划，后续计划边界仍会继续更新状态。',
          confirmOkText: '停用'
        }
      : {
          key: 'enable',
          label: '启用',
          icon: 'enable',
          tone: 'success',
          disabled: busy
        }
    return [
      statusAction,
      {
        key: 'refresh-key',
        label: '刷新密钥',
        icon: 'refresh',
        tone: 'warning',
        disabled: refreshDisabled,
        confirmTitle: `确认刷新 API Key ${apiKey.name} 的密钥？刷新后旧密钥会立即失效，请先确认客户端配置可同步更新。`,
        confirmOkText: '刷新'
      },
      {
        key: 'export-ccs',
        label: '导出 CCS',
        icon: 'export',
        tone: 'info',
        disabled: busy || ccsExportBusy
      }
    ]
  }

  function handleApiKeyAction(key: string, apiKey: ApiKeySummary) {
    if (key === 'edit') {
      void input.openEdit(apiKey)
      return
    }
    if (key === 'enable' || key === 'disable') {
      void updateApiKeyStatus(apiKey, key === 'enable' ? 'active' : 'disabled')
      return
    }
    if (key === 'refresh-key') {
      void refreshApiKeySecret(apiKey)
      return
    }
    if (key === 'export-ccs') {
      void prepareCcSwitchExport(apiKey)
      return
    }
    if (key === 'delete') {
      void removeApiKey(apiKey)
    }
  }

  async function prepareCcSwitchExport(apiKey: ApiKeySummary): Promise<void> {
    if (ccsExportPreparingId.value || ccsExportingId.value) return
    ccsExportPreparingId.value = apiKey.id
    try {
      const scope = input.operationScopeParams(apiKey)
      const [strategy, providerResource] = await Promise.all([
        input.routeStrategiesApi.editBasicDetail(apiKey.routeStrategyId, scope),
        loadProviderOptionsResource({
          isManagementView: input.isManagementView(),
          systemAccountId: scope?.systemAccountId,
          viewScope: input.isManagementView() ? 'admin' : 'self',
          includeDefinitions: true
        })
      ])
      const groups = buildCcSwitchExportGroupOptions(strategy.groupBindings, providerResource.data)
      if (!groups.length) {
        message.warning('当前策略路由没有可用于导出 CCS 的启用分组')
        return
      }
      ccsExportApiKey.value = apiKey
      ccsExportGroups.value = groups
      resetCcSwitchExportModelOptions()
      ccsExportModalOpen.value = true
    } catch (error) {
      console.error(error)
      message.error(extractApiErrorMessage(error, '加载 CCS 导出配置失败'))
    } finally {
      if (ccsExportPreparingId.value === apiKey.id) ccsExportPreparingId.value = ''
    }
  }

  async function exportToCcSwitch(selection: { groupId: string; app: CcSwitchClientApp; model: string }): Promise<void> {
    const apiKey = ccsExportApiKey.value
    if (!apiKey) {
      message.error('请重新选择要导出的 API Key')
      return
    }
    if (!ccsExportGroups.value.some((group) => group.groupId === selection.groupId)) {
      message.error('所选分组已不可用，请重新打开导出窗口')
      return
    }
    if (
      !ccsExportModelsReady.value
      || ccsExportModelsLoading.value
      || ccsExportModelOptionsGroupId.value !== selection.groupId
    ) {
      message.error('分组供应商模型尚未加载完成，请稍后重试')
      return
    }
    if (!isCcSwitchExportModelSelectionValid(ccsExportModelOptions.value, selection.model)) {
      message.error('所选模型不属于当前分组供应商，请重新选择')
      return
    }
    if (ccsExportingId.value) return
    ccsExportingId.value = apiKey.id
    try {
      const key = (await input.apiKeysApi.secret(apiKey.id, input.operationScopeParams(apiKey))).key
      const url = buildCcSwitchExportUrl({
        apiKey: key,
        app: selection.app,
        model: selection.model,
        endpoint: input.gatewayBaseUrl(),
        homepage: input.gatewayBaseUrl(),
        name: apiKey.name
      })
      if (typeof window === 'undefined') throw new Error('当前环境不支持 CCS 导出')
      ccsExportModalOpen.value = false
      window.open(url, '_self')
    } catch (error) {
      message.error(extractApiErrorMessage(error, '导出 CCS 失败'))
    } finally {
      if (ccsExportingId.value === apiKey.id) ccsExportingId.value = ''
    }
  }

  function handleCcSwitchModelOptionsOpen(groupId: string, open: boolean): void {
    if (!open) return
    if (ccsExportModelSearchTimer) clearTimeout(ccsExportModelSearchTimer)
    ccsExportModelSearchTimer = undefined
    if (!shouldLoadCcSwitchExportModelOptions({
      groupId,
      catalogGroupId: ccsExportModelOptionsGroupId.value,
      modelsLoading: ccsExportModelsLoading.value,
      modelsReady: ccsExportModelsReady.value
    })) return
    void loadCcSwitchExportModelOptions(groupId)
  }

  function handleCcSwitchModelOptionsSearch(groupId: string, keyword: string): void {
    if (ccsExportModelSearchTimer) clearTimeout(ccsExportModelSearchTimer)
    ccsExportModelSearchTimer = setTimeout(() => {
      ccsExportModelSearchTimer = undefined
      void loadCcSwitchExportModelOptions(groupId, keyword)
    }, 180)
  }

  async function loadCcSwitchExportModelOptions(groupId: string, keyword = ''): Promise<void> {
    const apiKey = ccsExportApiKey.value
    const group = ccsExportGroups.value.find((item) => item.groupId === groupId)
    if (!apiKey || !group) return

    const requestId = ++ccsExportModelOptionsRequestId
    const sameGroupWithReadyCatalog = ccsExportModelOptionsGroupId.value === groupId && ccsExportModelsReady.value
    ccsExportModelOptionsGroupId.value = groupId
    if (!sameGroupWithReadyCatalog) {
      ccsExportModelOptions.value = []
      ccsExportModelsReady.value = false
    }
    ccsExportModelsLoading.value = true
    try {
      const resource = await loadAccountProviderModelOptionsResource({
        isManagementView: input.isManagementView(),
        providerCode: group.providerCode,
        scopeParams: input.operationScopeParams(apiKey),
        selectedIds: group.defaultModel ? [group.defaultModel] : [],
        keyword
      })
      if (requestId !== ccsExportModelOptionsRequestId || ccsExportModelOptionsGroupId.value !== groupId) return
      ccsExportModelOptions.value = buildCcSwitchExportModelOptions([
        ...ccsExportModelOptions.value,
        ...resource.data
      ])
      ccsExportModelsReady.value = true
    } catch (error) {
      if (requestId !== ccsExportModelOptionsRequestId || ccsExportModelOptionsGroupId.value !== groupId) return
      ccsExportModelOptions.value = []
      ccsExportModelsReady.value = false
      console.error(error)
      message.error(extractApiErrorMessage(error, '加载分组供应商模型失败'))
    } finally {
      if (requestId === ccsExportModelOptionsRequestId) ccsExportModelsLoading.value = false
    }
  }

  function resetCcSwitchExportModelOptions(): void {
    ccsExportModelOptionsRequestId += 1
    if (ccsExportModelSearchTimer) clearTimeout(ccsExportModelSearchTimer)
    ccsExportModelSearchTimer = undefined
    ccsExportModelOptions.value = []
    ccsExportModelsLoading.value = false
    ccsExportModelsReady.value = false
    ccsExportModelOptionsGroupId.value = ''
  }

  function apiKeyActionBusy(apiKey: ApiKeySummary): boolean {
    return statusUpdatingId.value === apiKey.id || keyRefreshingId.value === apiKey.id
  }

  async function updateApiKeyStatus(apiKey: ApiKeySummary, status: 'active' | 'disabled') {
    statusUpdatingId.value = apiKey.id
    try {
      const result = await input.apiKeysApi.update(apiKey.id, {
        expectedRevision: apiKey.revision,
        status
      }, input.operationScopeParams(apiKey))
      input.updateItems(
        (item) => item.id === apiKey.id,
        (item) => mergeApiKeyMutationResult(item, result)
      )
      message.success(status === 'active' ? 'API Key 已启用' : 'API Key 已停用')
    } catch (error) {
      console.error(error)
      message.error(extractApiErrorMessage(error, status === 'active' ? '启用 API Key 失败' : '停用 API Key 失败'))
    } finally {
      if (statusUpdatingId.value === apiKey.id) {
        statusUpdatingId.value = ''
      }
    }
  }

  async function refreshApiKeySecret(apiKey: ApiKeySummary) {
    if (keyRefreshingId.value) return
    keyRefreshingId.value = apiKey.id
    try {
      const result = await input.apiKeysApi.refreshKey(apiKey.id, input.operationScopeParams(apiKey))
      input.updateItems(
        (item) => item.id === apiKey.id,
        (current) => refreshedApiKeyListItem(current, result)
      )
      input.showCreatedKey({
        key: result.key,
        title: 'API Key 密钥已刷新',
        message: '密钥已刷新，旧密钥已失效，请立即复制新密钥并更新客户端配置。'
      })
      message.success('API Key 密钥已刷新')
    } catch (error) {
      console.error(error)
      message.error(extractApiErrorMessage(error, '刷新 API Key 密钥失败'))
    } finally {
      if (keyRefreshingId.value === apiKey.id) {
        keyRefreshingId.value = ''
      }
    }
  }

  async function removeApiKey(apiKey: ApiKeySummary) {
    try {
      await input.apiKeysApi.delete(apiKey.id, input.operationScopeParams(apiKey))
      input.removeItems((item) => item.id === apiKey.id)
      message.success('API Key 已删除，关联记录将后台清理')
    } catch (error) {
      console.error(error)
      message.error(extractApiErrorMessage(error, '删除 API Key 失败'))
    }
  }

  return {
    keyCopyingId,
    ccsExportApiKey,
    ccsExportGroups,
    ccsExportModelOptions,
    ccsExportModelsLoading,
    ccsExportModelsReady,
    ccsExportModalOpen,
    ccsExportPreparingId,
    ccsExportingId,
    keyRefreshingId,
    statusUpdatingId,
    apiKeyMoreActions,
    apiKeyPrimaryActions,
    copyKeyPreview,
    exportToCcSwitch,
    handleCcSwitchModelOptionsOpen,
    handleCcSwitchModelOptionsSearch,
    handleApiKeyAction
  }
}
