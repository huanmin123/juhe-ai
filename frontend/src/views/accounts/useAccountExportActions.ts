import { computed, ref, toValue, type MaybeRefOrGetter } from 'vue'

import { api, type AccountListSortParam } from '@/api/client'
import { message } from '@/lib/antd'
import { extractApiErrorMessage } from '@/shared/apiError'
import type { AccountExportResult, AccountSummary, SystemAccountPrincipalSummary } from '@/types/domain'
import {
  accountExportFilename,
  accountExportFiltersFromState,
  accountExportPayloadByIds,
  downloadJsonFile,
  type AccountExportTargetState
} from './accountExportHelpers'
import type { AccountFilters } from './accountFormTypes'
import type { AccountScopeParams } from './accountOperationScope'

const ACCOUNT_EXPORT_MAX_ACCOUNTS = 50

interface UseAccountExportActionsConfig {
  accountScopeParams: MaybeRefOrGetter<AccountScopeParams>
  accountSorts: MaybeRefOrGetter<AccountListSortParam[]>
  filters: AccountFilters
  isManagementView: MaybeRefOrGetter<boolean>
  selectedAccounts: MaybeRefOrGetter<AccountSummary[]>
  systemAccounts: MaybeRefOrGetter<SystemAccountPrincipalSummary[]>
}

export function useAccountExportActions(config: UseAccountExportActionsConfig) {
  const exportLoading = ref(false)

  const targetState = computed<AccountExportTargetState>(() => {
    const selectedId = toValue(config.accountScopeParams)?.systemAccountId
    const selectedFilterSystemAccount = config.filters.systemAccount
    let selectedFilterSystemAccountName = selectedFilterSystemAccount?.name
    if (selectedFilterSystemAccount && selectedFilterSystemAccount.id === selectedId) {
      selectedFilterSystemAccountName = selectedFilterSystemAccount.name
    }
    return {
      selectedSystemAccountId: selectedId,
      selectedSystemAccountName: selectedFilterSystemAccountName,
      systemAccounts: toValue(config.systemAccounts)
    }
  })

  async function exportAccounts(): Promise<void> {
    const selectedAccounts = toValue(config.selectedAccounts)
    if (selectedAccounts.length) {
      await exportAccountsByIds(selectedAccounts)
      return
    }
    await exportFilteredAccounts()
  }

  async function exportFilteredAccounts(): Promise<void> {
    exportLoading.value = true
    try {
      const payload = { filters: accountExportFiltersFromState(config.filters, toValue(config.accountSorts)) }
      const result = await exportWithScope(payload)
      downloadJsonFile(accountExportFilename(result.summary.accounts, targetState.value), result.document)
      message.success(accountFilterExportSuccessMessage(result.summary))
    } catch (error) {
      console.error(error)
      message.error(extractApiErrorMessage(error, '导出账户失败'))
    } finally {
      exportLoading.value = false
    }
  }

  async function exportAccountsByIds(sourceAccounts: AccountSummary[]): Promise<void> {
    const exportableAccounts = sourceAccounts.filter(canExportAccount)
    if (!exportableAccounts.length) {
      message.warning('所选账户没有可导出的自有 AI 账户')
      return
    }
    if (exportableAccounts.length > ACCOUNT_EXPORT_MAX_ACCOUNTS) {
      message.warning(`单次最多导出 ${ACCOUNT_EXPORT_MAX_ACCOUNTS} 个账户，请先筛选或勾选部分账户`)
      return
    }
    exportLoading.value = true
    try {
      const payload = accountExportPayloadByIds(exportableAccounts)
      const result = await exportWithScope(payload)
      downloadJsonFile(accountExportFilename(result.summary.accounts, targetState.value), result.document)
      const skippedSelectedCount = sourceAccounts.length - exportableAccounts.length
      const skippedCount = (result.summary.skippedAccounts ?? 0) + skippedSelectedCount
      const skippedText = skippedCount ? `，跳过 ${skippedCount} 个不可导出账户` : ''
      message.success(`已导出 ${result.summary.accounts} 个账户${skippedText}`)
    } catch (error) {
      console.error(error)
      message.error(extractApiErrorMessage(error, '导出账户失败'))
    } finally {
      exportLoading.value = false
    }
  }

  function exportWithScope(payload: Parameters<typeof api.accounts.export>[0]): Promise<AccountExportResult> {
    return toValue(config.isManagementView)
      ? api.accounts.export(payload, toValue(config.accountScopeParams))
      : api.myAccounts.export(payload)
  }

  return {
    exportAccounts,
    exportLoading
  }
}

function accountFilterExportSuccessMessage(summary: AccountExportResult['summary']): string {
  const matchedText = typeof summary.matchedAccounts === 'number' ? `，匹配 ${summary.matchedAccounts} 个` : ''
  const skippedText = summary.skippedAccounts ? `，跳过 ${summary.skippedAccounts} 个不可导出账户` : ''
  const truncatedText = summary.truncated ? `，仅处理前 ${ACCOUNT_EXPORT_MAX_ACCOUNTS} 条匹配结果` : ''
  return `已按当前筛选导出 ${summary.accounts} 个账户${matchedText}${skippedText}${truncatedText}`
}

function canExportAccount(account: AccountSummary): boolean {
  return account.accessType !== 'authorized'
    && account.permissions?.canViewCredentials !== false
    && account.permissions?.canEdit !== false
}
