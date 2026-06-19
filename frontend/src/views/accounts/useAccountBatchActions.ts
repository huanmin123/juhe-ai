import { message } from '@/lib/antd'
import type { ComputedRef } from 'vue'

import { api } from '@/api/client'
import type { AccountSummary } from '@/types/domain'
import { invalidateAccountDetailForAccount } from './accountDetailCache'
import { isAuthorizedAccount } from './accountFormatters'
import { accountOperationScopeParams } from './accountOperationScope'
import { accountBatchConcurrency, runWithConcurrency } from './accountBatchExecution'
import { canBatchManageAccount, canBatchRestoreAccount, canTestAccount } from './accountRules'

interface UseAccountBatchActionsOptions {
  accountScopeParams: ComputedRef<{ systemAccountId: string } | undefined>
  clearSelection: () => void
  isManagementView: ComputedRef<boolean>
  loadData: () => Promise<void>
  openBatchTestModal: (accounts: AccountSummary[]) => void | Promise<void>
  selectedAccounts: ComputedRef<AccountSummary[]>
}

export function useAccountBatchActions(options: UseAccountBatchActionsOptions) {
  async function batchUpdateAccounts(
    payloadBuilder: (account: AccountSummary) => Record<string, unknown>,
    loadingLabel: string,
    successLabel: string,
    selected = options.selectedAccounts.value.filter(canBatchManageAccount)
  ) {
    if (!selected.length) {
      message.warning('请先选择账户')
      return
    }
    const hide = message.loading(`${loadingLabel}（${selected.length} 个）...`, 0)
    try {
      const results = await runWithConcurrency(selected, accountBatchConcurrency, (account) => {
        const payload = payloadBuilder(account)
        if (isAuthorizedAccount(account)) {
          const authorizedPayload = payload as Parameters<typeof api.accounts.updateAuthorizedDispatch>[1]
          return options.isManagementView.value
            ? api.accounts.updateAuthorizedDispatch(account.id, authorizedPayload, accountOperationScopeParams(account, options.accountScopeParams.value))
            : api.myAccounts.updateAuthorizedDispatch(account.id, authorizedPayload)
        }
        return options.isManagementView.value
          ? api.accounts.update(account.id, payload, options.accountScopeParams.value)
          : api.myAccounts.update(account.id, payload)
      })
      const failedCount = results.filter((result) => result.status === 'rejected').length
      for (const [index, result] of results.entries()) {
        if (result.status !== 'fulfilled') continue
        const account = selected[index]
        invalidateAccountDetailForAccount({
          accountId: account.id,
          isManagementView: options.isManagementView.value,
          scopeParams: accountOperationScopeParams(account, options.accountScopeParams.value)
        })
      }
      if (failedCount === 0) {
        message.success(successLabel)
        options.clearSelection()
      } else {
        message.warning(`${successLabel}，成功 ${selected.length - failedCount} 个，失败 ${failedCount} 个`)
      }
      await options.loadData()
    } catch (error) {
      console.error(error)
      message.error(`${loadingLabel}失败`)
    } finally {
      hide()
    }
  }

  async function batchTestSelected() {
    const selected = options.selectedAccounts.value.filter(canTestAccount)
    if (!selected.length) {
      message.warning('请先选择账户')
      return
    }
    if (selected.length !== options.selectedAccounts.value.length) {
      message.warning('已跳过不支持测试协议或当前不能测试的账户')
    }
    await options.openBatchTestModal(selected)
  }

  async function batchSetStatus(status: 'active' | 'disabled') {
    const selected = options.selectedAccounts.value.filter(canBatchManageAccount)
    const eligible = status === 'active'
      ? selected.filter((account) => account.status === 'disabled')
      : selected.filter((account) => account.status !== 'disabled')
    if (!eligible.length) {
      message.warning(status === 'active' ? '所选账户里没有可手动启用的停用账户' : '所选账户里没有可停用的账户')
      return
    }
    if (eligible.length !== selected.length) {
      message.warning(status === 'active' ? '已跳过临时状态或异常状态的账户，只启用手动停用的账户' : '已跳过已停用的账户')
    }
    await batchUpdateAccounts(
      (account) => ({ status: account.status === 'disabled' ? 'active' : 'disabled' }),
      status === 'active' ? '正在批量启用账户' : '正在批量停用账户',
      status === 'active' ? '账户已批量启用' : '账户已批量停用',
      eligible
    )
  }

  async function batchRestoreSelected() {
    const selected = options.selectedAccounts.value.filter(canBatchRestoreAccount)
    if (!selected.length) {
      message.warning('所选账户里没有可恢复的异常或临时状态账户')
      return
    }
    if (selected.length !== options.selectedAccounts.value.length) {
      message.warning('已跳过不需要恢复或无权恢复的账户')
    }
    await batchUpdateAccounts(
      () => ({ clearFailureState: true }),
      '正在批量恢复账户',
      '账户已批量恢复',
      selected
    )
  }

  return {
    batchRestoreSelected,
    batchSetStatus,
    batchTestSelected,
    batchUpdateAccounts
  }
}
