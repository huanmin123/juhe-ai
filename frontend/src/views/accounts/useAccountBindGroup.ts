import { message } from '@/lib/antd'
import { computed, nextTick, reactive, ref, type ComputedRef } from 'vue'

import { api } from '@/api/client'
import { rememberGroupLabel, type GroupSelection } from '@/shared/groupLabelCache'
import type { AccountSummary, GroupOptionSummary } from '@/types/domain'
import {
  bindGroupOptionsForAccount,
  bindGroupTip as buildBindGroupTip,
  defaultGroupForProvider
} from './accountDerivedState'
import { invalidateAccountDetailForAccount } from './accountDetailCache'
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
  loadGroupOptions: (keyword?: string, force?: boolean, scopeOverride?: { providerCode?: string; systemAccountId?: string; selectedIds?: Array<string | undefined> }) => Promise<void>
  loadData: () => Promise<void>
}

export function useAccountBindGroup(options: UseAccountBindGroupOptions) {
  const bindGroupModalOpen = ref(false)
  const bindGroupSaving = ref(false)
  const bindingAccount = ref<AccountSummary>()
  const bindGroupForm = reactive<{ groupId: string; group?: GroupSelection }>({
    groupId: '',
    group: undefined
  })

  const bindGroupOptions = computed(() => bindGroupOptionsForAccount(options.groups.value, bindingAccount.value))
  const bindGroupTip = computed(() => buildBindGroupTip(bindingAccount.value))

  async function openBindGroup(account: AccountSummary) {
    if (account.status === 'error') {
      message.warning('异常账户除编辑、删除外，只支持测试、异常恢复和停用')
      return
    }
    bindingAccount.value = account
    const selectedGroup = groupSelectionForId(
      options.groupIdForAccount(account.id) ?? account.boundGroupId,
      account.boundGroupName
    ) ?? defaultGroupSelectionForProvider(account.providerCode)
    setBindGroup(selectedGroup)
    bindGroupModalOpen.value = true
    await nextTick()
    const scopeParams = accountOperationScopeParams(account, options.accountScopeParams.value)
    await options.loadGroupOptions('', false, {
      providerCode: account.providerCode,
      systemAccountId: scopeParams?.systemAccountId,
      selectedIds: [bindGroupForm.groupId]
    })
    if (bindingAccount.value?.id !== account.id || bindGroupForm.groupId) return
    setBindGroup(defaultGroupSelectionForProvider(account.providerCode))
  }

  async function saveBindGroup() {
    if (!bindingAccount.value) return
    if (!bindGroupForm.groupId) {
      message.warning('请选择绑定分组')
      return
    }
    bindGroupSaving.value = true
    try {
      const account = bindingAccount.value
      const scopeParams = accountOperationScopeParams(account, options.accountScopeParams.value)
      const payload = {
        groupId: bindGroupForm.groupId
      }
      if (options.isManagementView.value) {
        await api.accounts.bindGroup(account.id, payload, scopeParams)
      } else {
        await api.myAccounts.bindGroup(account.id, payload)
      }
      invalidateAccountDetailForAccount({
        accountId: account.id,
        isManagementView: options.isManagementView.value,
        scopeParams
      })
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

  function defaultGroupSelectionForProvider(providerCode: string): GroupSelection | undefined {
    const group = defaultGroupForProvider(options.groups.value, providerCode)
    return group ? { id: group.id, name: group.name } : undefined
  }

  function groupSelectionForId(id: string | undefined, name: string | undefined): GroupSelection | undefined {
    const normalizedId = id?.trim()
    if (!normalizedId) return undefined
    const group = options.groups.value.find((item) => item.id === normalizedId)
    const normalizedName = group?.name?.trim() || name?.trim()
    if (!normalizedName) return bindGroupForm.group?.id === normalizedId ? bindGroupForm.group : undefined
    rememberGroupLabel(normalizedId, normalizedName)
    return { id: normalizedId, name: normalizedName }
  }

  function setBindGroup(group: GroupSelection | undefined): void {
    bindGroupForm.groupId = group?.id ?? ''
    bindGroupForm.group = group
  }
}
