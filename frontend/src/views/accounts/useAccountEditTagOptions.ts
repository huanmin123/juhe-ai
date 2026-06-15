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
}

export function useAccountEditTagOptions(options: UseAccountTagOptionsOptions) {
  const accountTagOptions = ref<AccountTagSummary[]>([])
  const accountTagOptionsLoading = ref(false)
  const deletingAccountTagId = ref<string>()

  async function loadAccountTagOptions(scopeParams: AccountScopeParams | undefined, force = false): Promise<void> {
    if (accountTagOptionsLoading.value && !force) return
    accountTagOptionsLoading.value = true
    try {
      accountTagOptions.value = options.isManagementView.value
        ? await api.accounts.tags(scopeParams)
        : await api.myAccounts.tags()
    } catch (error) {
      console.error(error)
      message.error(options.extractApiErrorMessage(error, '加载账户标签失败'))
    } finally {
      accountTagOptionsLoading.value = false
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
      options.form.tags = options.form.tags.filter((name) => name.trim().toLocaleLowerCase() !== tag.name.toLocaleLowerCase())
      accountTagOptions.value = accountTagOptions.value.filter((item) => item.id !== tagId)
      message.success('标签已删除')
    } catch (error) {
      console.error(error)
      message.error(options.extractApiErrorMessage(error, '删除标签失败'))
      await loadAccountTagOptions(options.accountTagOperationScopeParams(), true)
    } finally {
      deletingAccountTagId.value = undefined
    }
  }

  return {
    accountTagOptions,
    accountTagOptionsLoading,
    deleteAccountTag,
    deletingAccountTagId,
    loadAccountTagOptions
  }
}
