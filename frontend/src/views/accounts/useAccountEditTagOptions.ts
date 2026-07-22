import { message } from '@/lib/antd'
import { ref, type ComputedRef } from 'vue'

import { api } from '@/api/client'
import type { AccountTagSummary } from '@/types/domain'
import type { AccountFormModel } from './accountFormTypes'
import type { AccountScopeParams } from './accountOperationScope'

interface UseAccountTagOptionsOptions {
  accountTagOperationScopeParams: () => AccountScopeParams
  extractApiErrorMessage: (error: unknown, fallback: string) => string
  form: AccountFormModel
  isManagementView: ComputedRef<boolean>
  onTagsChanged?: () => void
}

export function useAccountEditTagOptions(options: UseAccountTagOptionsOptions) {
  const accountTagOptions = ref<AccountTagSummary[]>([])
  const accountTagOptionsLoading = ref(false)
  const deletingAccountTagId = ref<string>()
  const loadedScopeKey = ref('')
  let requestToken = 0
  let deleteRequestToken = 0

  async function loadAccountTagOptions(scopeParams: AccountScopeParams | undefined, force = false): Promise<void> {
    const scopeKey = accountTagOptionsScopeKey(options.isManagementView.value, scopeParams)
    if (!scopeKey) {
      resetAccountTagOptions()
      return
    }
    if (!force && loadedScopeKey.value === scopeKey) return
    const currentRequestToken = ++requestToken
    accountTagOptionsLoading.value = true
    try {
      const result = options.isManagementView.value
        ? await api.accounts.tags(scopeParams)
        : await api.myAccounts.tags()
      if (currentRequestToken !== requestToken) return
      accountTagOptions.value = result
      loadedScopeKey.value = scopeKey
    } catch (error) {
      if (currentRequestToken !== requestToken) return
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
    deletingAccountTagId.value = tagId
    try {
      const scopeParams = options.accountTagOperationScopeParams()
      if (options.isManagementView.value) {
        await api.accounts.deleteTag(tagId, scopeParams)
      } else {
        await api.myAccounts.deleteTag(tagId)
      }
      if (!isCurrentDeleteRequest(currentDeleteRequestToken, scopeKey)) return
      options.form.tags = options.form.tags.filter((name) => name.trim() !== tag.name)
      accountTagOptions.value = accountTagOptions.value.filter((item) => item.id !== tagId)
      options.onTagsChanged?.()
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
  }

  function invalidateAccountTagOptions(scopeParams: AccountScopeParams | undefined): void {
    const scopeKey = accountTagOptionsScopeKey(options.isManagementView.value, scopeParams)
    if (!scopeKey || loadedScopeKey.value !== scopeKey) return
    requestToken += 1
    loadedScopeKey.value = ''
    accountTagOptionsLoading.value = false
  }

  function isCurrentDeleteRequest(
    currentDeleteRequestToken: number,
    scopeKey: string
  ): boolean {
    return currentDeleteRequestToken === deleteRequestToken
      && scopeKey === loadedScopeKey.value
  }

  return {
    accountTagOptions,
    accountTagOptionsLoading,
    deleteAccountTag,
    deletingAccountTagId,
    invalidateAccountTagOptions,
    loadAccountTagOptions
  }
}

function accountTagOptionsScopeKey(isManagementView: boolean, scopeParams?: AccountScopeParams): string | undefined {
  if (!isManagementView) return 'self'
  const systemAccountId = scopeParams?.systemAccountId?.trim()
  return systemAccountId ? `management:${systemAccountId}` : undefined
}
