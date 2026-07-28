import { message } from '@/lib/antd'
import type { ComputedRef } from 'vue'

import { api } from '@/api/client'
import type { AccountListItem } from '@/types/domain'
import { isAuthorizedAccount } from './accountFormatters'
import { accountOperationScopeParams } from './accountOperationScope'
import { accountBatchConcurrency, runWithConcurrency } from './accountBatchExecution'
import { canBatchManageAccount, canBatchRestoreAccount, canToggleAccountStatus } from './accountRules'

interface UseAccountBatchActionsOptions {
  accountScopeParams: ComputedRef<{ systemAccountId: string } | undefined>
  clearSelection: () => void
  isManagementView: ComputedRef<boolean>
  loadData: () => Promise<void>
  selectedAccounts: ComputedRef<AccountListItem[]>
}

export function useAccountBatchActions(options: UseAccountBatchActionsOptions) {
  async function batchUpdateAccounts(
    payloadBuilder: (account: AccountListItem) => Record<string, unknown>,
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
      const results = await runWithConcurrency(selected, accountBatchConcurrency, async (account): Promise<unknown> => {
        const payload = payloadBuilder(account)
        if (isAuthorizedAccount(account)) {
          const configRevision = Number(account.configRevision)
          if (!Number.isInteger(configRevision) || configRevision < 1) {
            throw new Error(`账户 ${account.name} 的版本信息缺失，请刷新列表后重试`)
          }
          const authorizedPayload = {
            ...payload,
            expectedConfigRevision: configRevision
          } as Parameters<typeof api.accounts.updateAuthorizedDispatch>[1]
          return await (options.isManagementView.value
            ? api.accounts.updateAuthorizedDispatch(account.id, authorizedPayload, accountOperationScopeParams(account, options.accountScopeParams.value))
            : api.myAccounts.updateAuthorizedDispatch(account.id, authorizedPayload))
        }
        const configRevision = Number(account.configRevision)
        if (!Number.isInteger(configRevision) || configRevision < 1) {
          throw new Error(`账户 ${account.name} 的版本信息缺失，请刷新列表后重试`)
        }
        const updatePayload = { ...payload, expectedConfigRevision: configRevision }
        return await (options.isManagementView.value
          ? api.accounts.update(account.id, updatePayload, options.accountScopeParams.value)
          : api.myAccounts.update(account.id, updatePayload))
      })
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

  async function batchSetStatus(status: 'active' | 'disabled') {
    const selected = options.selectedAccounts.value.filter(canToggleAccountStatus)
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
    batchUpdateAccounts
  }
}
