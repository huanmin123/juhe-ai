import { message } from '@/lib/antd'
import { ref, watch, type ComputedRef } from 'vue'

import { api } from '@/api/client'
import type { AccountTagSummary } from '@/types/domain'
import type { AccountFormModel } from './accountFormTypes'
import type { AccountScopeParams } from './accountOperationScope'

interface UseAccountTagOptionsOptions {
  accountTagOperationScopeParams: () => AccountScopeParams
  extractApiErrorMessage: (error: unknown, fallback: string) => string
  form: AccountFormModel
  isManagementView: ComputedRef<boolean>
}

export function useAccountEditTagOptions(options: UseAccountTagOptionsOptions) {
  const accountTagOptions = ref<AccountTagSummary[]>([])
  const accountTagOptionsLoading = ref(false)
  const deletingAccountTagId = ref<string>()
  const loadedScopeKey = ref('')
  let requestToken = 0
  let deleteRequestToken = 0

  watch(
    currentAccountTagOptionsScopeKey,
    (nextScopeKey, previousScopeKey) => {
      if (nextScopeKey === previousScopeKey) return
      resetAccountTagOptions()
    },
    { flush: 'sync' }
  )

  async function loadAccountTagOptions(scopeParams: AccountScopeParams | undefined, force = false): Promise<void> {
    const managementView = options.isManagementView.value
    const scopeKey = accountTagOptionsScopeKey(managementView, scopeParams)
    if (!scopeKey) {
      resetAccountTagOptions()
      return
    }
    if (!force && loadedScopeKey.value === scopeKey) return
    const currentRequestToken = ++requestToken
    accountTagOptionsLoading.value = true
    try {
      const result = managementView
        ? await api.accounts.tags(scopeParams)
        : await api.myAccounts.tags()
      if (!isCurrentRequest(currentRequestToken, scopeKey)) return
      accountTagOptions.value = result
      loadedScopeKey.value = scopeKey
    } catch (error) {
      if (!isCurrentRequest(currentRequestToken, scopeKey)) return
      console.error(error)
      message.error(options.extractApiErrorMessage(error, '加载账户标签失败'))
    } finally {
      if (currentRequestToken === requestToken) {
        accountTagOptionsLoading.value = false
      }
    }
  }

  async function deleteAccountTag(tagId: string): Promise<void> {
    const tag = accountTagOptions.value.find((item) => item.id === tagId)
    if (!tag) return
    if ((tag.accountCount ?? 0) > 0) {
      message.warning('标签已绑定账户，不能删除')
      return
    }
    const currentDeleteRequestToken = ++deleteRequestToken
    const scopeKey = loadedScopeKey.value
    const scopeParams = options.accountTagOperationScopeParams()
    const managementView = options.isManagementView.value
    deletingAccountTagId.value = tagId
    try {
      if (managementView) {
        await api.accounts.deleteTag(tagId, scopeParams)
      } else {
        await api.myAccounts.deleteTag(tagId)
      }
      if (!isCurrentDeleteRequest(currentDeleteRequestToken, scopeKey)) return
      options.form.tags = options.form.tags.filter((name) => name.trim() !== tag.name)
      accountTagOptions.value = accountTagOptions.value.filter((item) => item.id !== tagId)
      message.success('标签已删除')
    } catch (error) {
      if (!isCurrentDeleteRequest(currentDeleteRequestToken, scopeKey)) return
      console.error(error)
      message.error(options.extractApiErrorMessage(error, '删除标签失败'))
      await loadAccountTagOptions(options.accountTagOperationScopeParams(), true)
    } finally {
      if (currentDeleteRequestToken === deleteRequestToken) deletingAccountTagId.value = undefined
    }
  }

  function resetAccountTagOptions(): void {
    requestToken += 1
    deleteRequestToken += 1
    accountTagOptions.value = []
    loadedScopeKey.value = ''
    accountTagOptionsLoading.value = false
    deletingAccountTagId.value = undefined
  }

  function isCurrentRequest(currentRequestToken: number, scopeKey: string): boolean {
    return currentRequestToken === requestToken
      && scopeKey === currentAccountTagOptionsScopeKey()
  }

  function currentAccountTagOptionsScopeKey(): string | undefined {
    return accountTagOptionsScopeKey(
      options.isManagementView.value,
      options.accountTagOperationScopeParams()
    )
  }

  function isCurrentDeleteRequest(
    currentDeleteRequestToken: number,
    scopeKey: string
  ): boolean {
    return currentDeleteRequestToken === deleteRequestToken
      && scopeKey === loadedScopeKey.value
      && scopeKey === currentAccountTagOptionsScopeKey()
  }

  return {
    accountTagOptions,
    accountTagOptionsLoading,
    deleteAccountTag,
    deletingAccountTagId,
    loadAccountTagOptions,
    resetAccountTagOptions
  }
}

function accountTagOptionsScopeKey(isManagementView: boolean, scopeParams?: AccountScopeParams): string | undefined {
  if (!isManagementView) return 'self'
  const systemAccountId = scopeParams?.systemAccountId?.trim()
  return systemAccountId ? `management:${systemAccountId}` : undefined
}
