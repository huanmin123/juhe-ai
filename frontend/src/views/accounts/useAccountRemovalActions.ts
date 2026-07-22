import type { ComputedRef, Ref } from 'vue'

import { api } from '@/api/client'
import { message } from '@/lib/antd'
import type { AccountSummary } from '@/types/domain'
import { accountOperationScopeParams, type AccountScopeParams } from './accountOperationScope'
import { accountBatchConcurrency, runWithConcurrency } from './accountBatchExecution'
import { canBatchDeleteAccount } from './accountRules'
import { invalidateAccountTestOptionsCache } from './accountTestOptionsCache'

interface UseAccountRemovalActionsOptions {
  accountById: ComputedRef<Map<string, AccountSummary>>
  accounts: Ref<AccountSummary[]>
  accountScopeParams: ComputedRef<AccountScopeParams>
  clearSelection: () => void
  extractApiErrorMessage: (error: unknown, fallback: string) => string
  isManagementView: ComputedRef<boolean>
  loadData: (options?: { quiet?: boolean }) => Promise<void>
  pruneSelection: (selectableAccountIds: Set<string>) => void
  removeLoadedAccount: (accountId: string) => boolean
}

export function useAccountRemovalActions(options: UseAccountRemovalActionsOptions) {
  async function removeLoadedRemovedAccount(id: string): Promise<void> {
    invalidateAccountTestOptionsCache()
    options.removeLoadedAccount(id)
    options.pruneSelection(new Set(options.accounts.value.map((account) => account.id)))
    void options.loadData({ quiet: true })
  }

  async function returnAuthorizationAccount(id: string): Promise<void> {
    const account = options.accountById.value.get(id)
    if (!account || account.accessType !== 'authorized') {
      message.warning('只有授权账户可以归还')
      return
    }
    try {
      if (options.isManagementView.value) {
        await api.accounts.returnAuthorization(id, accountOperationScopeParams(account, options.accountScopeParams.value))
      } else {
        await api.myAccounts.returnAuthorization(id)
      }
      await removeLoadedRemovedAccount(id)
      message.success('授权账户已归还')
    } catch (error) {
      console.error(error)
      message.error(options.extractApiErrorMessage(error, '归还授权账户失败'))
    }
  }

  async function removeAccount(id: string): Promise<void> {
    const account = options.accountById.value.get(id)
    if (account?.accessType === 'authorized') {
      message.warning('授权账户请使用归还操作')
      return
    }
    try {
      if (options.isManagementView.value) {
        await api.accounts.delete(id, options.accountScopeParams.value)
      } else {
        await api.myAccounts.delete(id)
      }
      await removeLoadedRemovedAccount(id)
      message.success('账户已删除，关联记录将在一个月后由后台物理清理')
    } catch (error) {
      console.error(error)
      message.error(options.extractApiErrorMessage(error, '删除账户失败'))
    }
  }

  async function batchDeleteSelected(sourceAccounts: AccountSummary[]): Promise<void> {
    const selected = sourceAccounts.filter(canBatchDeleteAccount)
    if (!selected.length) {
      message.warning('所选账户里没有可删除的自有账户')
      return
    }
    if (selected.length !== sourceAccounts.length) {
      message.warning('已跳过授权账户或无权删除的账户')
    }
    const hide = message.loading(`正在批量删除账户（${selected.length} 个）...`, 0)
    try {
      const results = await runWithConcurrency(selected, accountBatchConcurrency, (account) => (
        options.isManagementView.value
          ? api.accounts.delete(account.id, accountOperationScopeParams(account, options.accountScopeParams.value))
          : api.myAccounts.delete(account.id)
      ))
      const failedCount = results.filter((result) => result.status === 'rejected').length
      if (failedCount < selected.length) {
        invalidateAccountTestOptionsCache()
      }
      if (failedCount === 0) {
        message.success('账户已批量删除，关联记录将在一个月后由后台物理清理')
        options.clearSelection()
      } else {
        message.warning(`账户批量删除已执行，成功 ${selected.length - failedCount} 个，失败 ${failedCount} 个`)
      }
      await options.loadData()
    } catch (error) {
      console.error(error)
      message.error('批量删除账户失败')
    } finally {
      hide()
    }
  }

  return {
    batchDeleteSelected,
    removeAccount,
    returnAuthorizationAccount
  }
}
