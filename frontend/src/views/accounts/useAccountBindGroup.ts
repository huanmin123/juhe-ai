import { message } from '@/lib/antd'
import { computed, reactive, ref, type ComputedRef } from 'vue'

import { api } from '@/api/client'
import type { AccountGroupOptionSummary, AccountSummary } from '@/types/domain'
import {
  bindGroupOptionsForAccount,
  bindGroupTip as buildBindGroupTip,
  defaultGroupForProvider
} from './accountDerivedState'
import { accountOperationScopeParams } from './accountOperationScope'

type ReadonlyValue<T> = {
  readonly value: T
}

interface UseAccountBindGroupOptions {
  accountScopeParams: ComputedRef<{ systemAccountId: string } | undefined>
  extractApiErrorMessage: (error: unknown, fallback: string) => string
  groupIdForAccount: (accountId: string) => string | undefined
  groups: ReadonlyValue<AccountGroupOptionSummary[]>
  isManagementView: ComputedRef<boolean>
  loadData: () => Promise<void>
}

export function useAccountBindGroup(options: UseAccountBindGroupOptions) {
  const bindGroupModalOpen = ref(false)
  const bindGroupSaving = ref(false)
  const bindingAccount = ref<AccountSummary>()
  const bindGroupForm = reactive({ groupId: '' })

  const bindGroupOptions = computed(() => bindGroupOptionsForAccount(options.groups.value, bindingAccount.value))
  const bindGroupTip = computed(() => buildBindGroupTip(bindingAccount.value))

  function openBindGroup(account: AccountSummary) {
    if (account.status === 'error') {
      message.warning('异常账户除编辑、删除外，只支持测试和恢复异常')
      return
    }
    bindingAccount.value = account
    bindGroupForm.groupId = options.groupIdForAccount(account.id) ?? defaultGroupForProvider(options.groups.value, account.providerCode)?.id ?? ''
    bindGroupModalOpen.value = true
  }

  async function saveBindGroup() {
    if (!bindingAccount.value) return
    if (!bindGroupForm.groupId) {
      message.warning('请选择归属分组')
      return
    }
    bindGroupSaving.value = true
    try {
      if (options.isManagementView.value) {
        await api.accounts.bindGroup(bindingAccount.value.id, { groupId: bindGroupForm.groupId }, accountOperationScopeParams(bindingAccount.value, options.accountScopeParams.value))
      } else {
        await api.myAccounts.bindGroup(bindingAccount.value.id, { groupId: bindGroupForm.groupId })
      }
      message.success('授权账户已绑定分组')
      bindGroupModalOpen.value = false
      bindingAccount.value = undefined
      await options.loadData()
    } catch (error) {
      console.error(error)
      message.error(options.extractApiErrorMessage(error, '绑定分组失败'))
    } finally {
      bindGroupSaving.value = false
    }
  }

  return {
    bindGroupForm,
    bindGroupModalOpen,
    bindGroupOptions,
    bindGroupSaving,
    bindGroupTip,
    bindingAccount,
    openBindGroup,
    saveBindGroup
  }
}
