import { computed, ref, toValue, type MaybeRefOrGetter } from 'vue'

import { message } from '@/lib/antd'
import type { AccountListItem } from '@/types/domain'
import { accountSelectionColumnWidth } from './accountTableColumns'
import { canBatchDeleteAccount, canSelectAccountForBatch } from './accountRules'

interface UseAccountSelectionActionsOptions {
  accounts: MaybeRefOrGetter<AccountListItem[]>
}

export function useAccountSelectionActions(options: UseAccountSelectionActionsOptions) {
  const selectedAccountIds = ref<string[]>([])
  const selectedAccountIdSet = computed(() => new Set(selectedAccountIds.value))
  const selectedAccounts = computed(() => toValue(options.accounts).filter((account) => selectedAccountIdSet.value.has(account.id)))
  const selectedDeletableAccountCount = computed(() => selectedAccounts.value.filter(canBatchDeleteAccount).length)
  const batchDeleteConfirmOpen = ref(false)
  const batchDeleteConfirmLoading = ref(false)
  const batchDeleteTargets = ref<AccountListItem[]>([])
  const rowSelection = computed(() => ({
    columnWidth: accountSelectionColumnWidth,
    fixed: true,
    selectedRowKeys: selectedAccountIds.value,
    onChange: (selectedRowKeys: Array<string | number>) => {
      selectedAccountIds.value = selectedRowKeys.map((key) => String(key))
    },
    getCheckboxProps: (account: AccountListItem) => ({ disabled: !canSelectAccountForBatch(account) })
  }))

  function pruneSelection(selectableAccountIds: Set<string>): void {
    selectedAccountIds.value = selectedAccountIds.value.filter((id) => selectableAccountIds.has(id))
  }

  function clearSelectedAccountIds(): void {
    selectedAccountIds.value = []
  }

  function clearSelection(): void {
    clearSelectedAccountIds()
    closeBatchDeleteConfirm()
  }

  function isAccountSelected(accountId: string): boolean {
    return selectedAccountIdSet.value.has(accountId)
  }

  function toggleAccountSelection(account: AccountListItem): void {
    if (!canSelectAccountForBatch(account)) return
    selectedAccountIds.value = isAccountSelected(account.id)
      ? selectedAccountIds.value.filter((id) => id !== account.id)
      : [...selectedAccountIds.value, account.id]
  }

  function openBatchDeleteConfirm(): void {
    const targets = selectedAccounts.value.filter(canBatchDeleteAccount)
    if (!targets.length) {
      message.warning('所选账户里没有可删除的自有账户')
      return
    }
    if (targets.length !== selectedAccounts.value.length) {
      message.warning('已跳过授权账户或无权删除的账户')
    }
    batchDeleteTargets.value = [...targets]
    batchDeleteConfirmOpen.value = true
  }

  function closeBatchDeleteConfirm(): void {
    if (batchDeleteConfirmLoading.value) return
    batchDeleteConfirmOpen.value = false
    batchDeleteTargets.value = []
  }

  async function confirmBatchDeleteWith(batchDeleteSelected: (targets: AccountListItem[]) => Promise<void>): Promise<void> {
    const targets = [...batchDeleteTargets.value]
    if (!targets.length) return
    batchDeleteConfirmLoading.value = true
    try {
      await batchDeleteSelected(targets)
      batchDeleteConfirmOpen.value = false
      batchDeleteTargets.value = []
    } finally {
      batchDeleteConfirmLoading.value = false
    }
  }

  return {
    batchDeleteConfirmLoading,
    batchDeleteConfirmOpen,
    batchDeleteTargets,
    clearSelectedAccountIds,
    clearSelection,
    closeBatchDeleteConfirm,
    confirmBatchDeleteWith,
    isAccountSelected,
    openBatchDeleteConfirm,
    pruneSelection,
    rowSelection,
    selectedAccounts,
    selectedDeletableAccountCount,
    toggleAccountSelection
  }
}
