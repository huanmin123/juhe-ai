import { ref } from 'vue'

import type { RowActionItem } from '@/components/rowActions'
import type { useScopedApiKeysApi } from '@/composables/useScopedDomainApi'
import { message } from '@/lib/antd'
import { extractApiErrorMessage } from '@/shared/apiError'
import { copyTextToClipboard } from '@/shared/clipboard'
import type { ApiKeySummary } from '@/types/domain'
import { refreshedApiKeyListItem } from './apiKeyRefreshRow'
import type { ApiKeyScopeParams } from './apiKeyScope'

type ScopedApiKeysApi = ReturnType<typeof useScopedApiKeysApi>

interface CreatedKeyPayload {
  key: string
  title: string
  message: string
}

interface UseApiKeyRowActionsInput {
  apiKeysApi: Pick<ScopedApiKeysApi, 'delete' | 'refreshKey' | 'secret' | 'update'>
  operationScopeParams: (apiKey?: Pick<ApiKeySummary, 'systemAccountId'>) => ApiKeyScopeParams
  openEdit: (apiKey: ApiKeySummary) => void | Promise<void>
  reload: () => void | Promise<unknown>
  removeItems: (predicate: (item: ApiKeySummary) => boolean) => number
  showCreatedKey: (payload: CreatedKeyPayload) => void
  updateItems: (predicate: (item: ApiKeySummary) => boolean, updater: (item: ApiKeySummary) => ApiKeySummary) => number
}

export function useApiKeyRowActions(input: UseApiKeyRowActionsInput) {
  const statusUpdatingId = ref('')
  const keyRefreshingId = ref('')
  const keyCopyingId = ref('')

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
    if (key === 'delete') {
      void removeApiKey(apiKey)
    }
  }

  function apiKeyActionBusy(apiKey: ApiKeySummary): boolean {
    return statusUpdatingId.value === apiKey.id || keyRefreshingId.value === apiKey.id
  }

  async function updateApiKeyStatus(apiKey: ApiKeySummary, status: 'active' | 'disabled') {
    statusUpdatingId.value = apiKey.id
    try {
      const updated = await input.apiKeysApi.update(apiKey.id, { status }, input.operationScopeParams(apiKey))
      input.updateItems((item) => item.id === apiKey.id, () => updated)
      message.success(status === 'active' ? 'API Key 已启用' : 'API Key 已停用')
      void input.reload()
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
      void input.reload()
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
      void input.reload()
    } catch (error) {
      console.error(error)
      message.error(extractApiErrorMessage(error, '删除 API Key 失败'))
    }
  }

  return {
    keyCopyingId,
    keyRefreshingId,
    statusUpdatingId,
    apiKeyMoreActions,
    apiKeyPrimaryActions,
    copyKeyPreview,
    handleApiKeyAction
  }
}
