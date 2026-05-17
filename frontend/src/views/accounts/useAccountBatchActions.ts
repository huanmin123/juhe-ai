import { message } from '@/lib/antd'
import type { ComputedRef } from 'vue'

import { api } from '@/api/client'
import type { AccountSummary, AccountTestResult } from '@/types/domain'
import { batchTestSummary } from './accountTestFlow'
import { canBatchManageAccount, canTestAccount } from './accountRules'

interface UseAccountBatchActionsOptions {
  accountScopeParams: ComputedRef<{ systemAccountId: string } | undefined>
  clearSelection: () => void
  isManagementView: ComputedRef<boolean>
  loadData: () => Promise<void>
  selectedAccounts: ComputedRef<AccountSummary[]>
  testAccountSilently: (account: AccountSummary) => Promise<AccountTestResult | undefined>
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
      const results = await Promise.allSettled(selected.map((account) => options.isManagementView.value
        ? api.accounts.update(account.id, payloadBuilder(account), options.accountScopeParams.value)
        : api.myAccounts.update(account.id, payloadBuilder(account))))
      const failedCount = results.filter((result) => result.status === 'rejected').length
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
    const hide = message.loading(`正在批量测试 ${selected.length} 个账户...`, 0)
    try {
      const results = await Promise.all(selected.map((account) => options.testAccountSilently(account)))
      const successCount = results.filter((result) => result?.success).length
      const summary = batchTestSummary(results.length, successCount)
      if (summary.success) {
        message.success(summary.message)
        options.clearSelection()
      } else {
        message.warning(summary.message)
      }
      await options.loadData()
    } catch (error) {
      console.error(error)
      message.error('批量测试失败')
    } finally {
      hide()
    }
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

  return {
    batchSetStatus,
    batchTestSelected,
    batchUpdateAccounts
  }
}
