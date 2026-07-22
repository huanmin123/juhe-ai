import { message } from '@/lib/antd'
import { ref, type ComputedRef } from 'vue'

import { api } from '@/api/client'
import type { PageDataActivation } from '@/composables/usePageDataActivation'
import type { AccountTagSummary } from '@/types/domain'
import type { AccountFormModel } from './accountFormTypes'
import type { AccountScopeParams } from './accountOperationScope'
import {
  loadAccountTagOptionsCached,
  readAccountTagOptionsCache,
  resolveAccountTagOptionsScopeKey,
  writeAccountTagOptionsCache
} from './accountTagOptionsCache'

interface UseAccountTagOptionsOptions {
  accountTagOperationScopeParams: () => AccountScopeParams
  extractApiErrorMessage: (error: unknown, fallback: string) => string
  form: AccountFormModel
  isManagementView: ComputedRef<boolean>
  pageDataActivation?: PageDataActivation
}

export function useAccountEditTagOptions(options: UseAccountTagOptionsOptions) {
  const accountTagOptions = ref<AccountTagSummary[]>([])
  const accountTagOptionsLoading = ref(false)
  const deletingAccountTagId = ref<string>()
  const loadedScopeKey = ref('')
  let requestToken = 0

  async function loadAccountTagOptions(scopeParams: AccountScopeParams | undefined, force = false): Promise<void> {
    const scopeKey = resolveAccountTagOptionsScopeKey(options.isManagementView.value, scopeParams)
    if (!scopeKey) {
      resetAccountTagOptions()
      return
    }
    if (!force && loadedScopeKey.value === scopeKey) return
    const cachedOptions = !force ? readAccountTagOptionsCache(scopeKey) : undefined
    if (cachedOptions) {
      accountTagOptions.value = cachedOptions
      loadedScopeKey.value = scopeKey
      accountTagOptionsLoading.value = false
      return
    }

    const currentRequestToken = ++requestToken
    accountTagOptionsLoading.value = true
    try {
      const nextOptions = await loadAccountTagOptionsCached({
        activation: options.pageDataActivation,
        force,
        isManagementView: options.isManagementView.value,
        scopeParams
      })
      if (currentRequestToken !== requestToken) return
      accountTagOptions.value = nextOptions
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
    deletingAccountTagId.value = tagId
    try {
      const scopeParams = options.accountTagOperationScopeParams()
      if (options.isManagementView.value) {
        await api.accounts.deleteTag(tagId, scopeParams)
      } else {
        await api.myAccounts.deleteTag(tagId)
      }
      options.form.tags = options.form.tags.filter((name) => name.trim() !== tag.name)
      accountTagOptions.value = accountTagOptions.value.filter((item) => item.id !== tagId)
      if (loadedScopeKey.value) {
        writeAccountTagOptionsCache(loadedScopeKey.value, accountTagOptions.value)
      }
      message.success('标签已删除')
    } catch (error) {
      console.error(error)
      message.error(options.extractApiErrorMessage(error, '删除标签失败'))
      await loadAccountTagOptions(options.accountTagOperationScopeParams(), true)
    } finally {
      deletingAccountTagId.value = undefined
    }
  }

  function resetAccountTagOptions(): void {
    requestToken += 1
    accountTagOptions.value = []
    loadedScopeKey.value = ''
    accountTagOptionsLoading.value = false
  }

  return {
    accountTagOptions,
    accountTagOptionsLoading,
    deleteAccountTag,
    deletingAccountTagId,
    loadAccountTagOptions
  }
}
