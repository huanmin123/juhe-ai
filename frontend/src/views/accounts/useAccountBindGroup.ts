import { message } from '@/lib/antd'
import { computed, nextTick, reactive, ref, type ComputedRef } from 'vue'

import { api } from '@/api/client'
import type { AccountSummary, GroupOptionSummary } from '@/types/domain'
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
  groups: ReadonlyValue<GroupOptionSummary[]>
  isManagementView: ComputedRef<boolean>
  loadGroupOptions: (keyword?: string, force?: boolean) => Promise<void>
  loadData: () => Promise<void>
}

export function useAccountBindGroup(options: UseAccountBindGroupOptions) {
  const bindGroupModalOpen = ref(false)
  const bindGroupSaving = ref(false)
  const bindingAccount = ref<AccountSummary>()
  const bindGroupForm = reactive<{ groupId: string; dispatchWeight: number; softConcurrencyLimit: number | null }>({
    groupId: '',
    dispatchWeight: 1,
    softConcurrencyLimit: null
  })

  const bindGroupOptions = computed(() => bindGroupOptionsForAccount(options.groups.value, bindingAccount.value))
  const bindGroupTip = computed(() => buildBindGroupTip(bindingAccount.value))
  const bindGroupSelectedGroup = computed(() => options.groups.value.find((group) => group.id === bindGroupForm.groupId))
  const bindGroupSoftConcurrencyVisible = computed(() => bindGroupSelectedGroup.value?.groupType === 'high_concurrency')

  async function openBindGroup(account: AccountSummary) {
    if (account.status === 'error') {
      message.warning('异常账户除编辑、删除外，只支持测试和恢复异常')
      return
    }
    bindingAccount.value = account
    bindGroupForm.groupId = options.groupIdForAccount(account.id) ?? defaultGroupForProvider(options.groups.value, account.providerCode)?.id ?? ''
    bindGroupForm.dispatchWeight = account.boundGroupDispatchWeight ?? 1
    bindGroupForm.softConcurrencyLimit = account.boundGroupSoftConcurrencyLimit ?? null
    bindGroupModalOpen.value = true
    await nextTick()
    await options.loadGroupOptions('', true)
    if (bindingAccount.value?.id !== account.id || bindGroupForm.groupId) return
    bindGroupForm.groupId = defaultGroupForProvider(options.groups.value, account.providerCode)?.id ?? ''
  }

  async function saveBindGroup() {
    if (!bindingAccount.value) return
    if (!bindGroupForm.groupId) {
      message.warning('请选择绑定分组')
      return
    }
    bindGroupSaving.value = true
    try {
      const payload = {
        groupId: bindGroupForm.groupId,
        dispatchWeight: bindGroupSoftConcurrencyVisible.value ? bindGroupForm.dispatchWeight : 1,
        softConcurrencyLimit: bindGroupSoftConcurrencyVisible.value ? bindGroupForm.softConcurrencyLimit : null
      }
      if (options.isManagementView.value) {
        await api.accounts.bindGroup(bindingAccount.value.id, payload, accountOperationScopeParams(bindingAccount.value, options.accountScopeParams.value))
      } else {
        await api.myAccounts.bindGroup(bindingAccount.value.id, payload)
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
    bindGroupSoftConcurrencyVisible,
    bindGroupTip,
    bindingAccount,
    openBindGroup,
    saveBindGroup
  }
}
