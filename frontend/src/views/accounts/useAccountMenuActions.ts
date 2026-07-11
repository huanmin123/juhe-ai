import { message } from '@/lib/antd'
import { ref, type ComputedRef } from 'vue'

import { api } from '@/api/client'
import type { AccountSummary } from '@/types/domain'
import { invalidateAccountDetailForAccount } from './accountDetailCache'
import { hasAccountRuntimeRecoveryState, isAuthorizedAccount, isTemporaryAccountStatus } from './accountFormatters'
import { accountOperationScopeParams } from './accountOperationScope'
import {
  authorizedAccountUnavailableText,
  canEditAccount,
  canManageOAuthAccount,
  canRestoreException,
  hasAuthorizedInstanceFailureState,
  canUseAccountActions,
  canUseBoundAuthorizedAccount
} from './accountRules'

interface UseAccountMenuActionsOptions {
  accountScopeParams: ComputedRef<{ systemAccountId: string } | undefined>
  extractApiErrorMessage: (error: unknown, fallback: string) => string
  isManagementView: ComputedRef<boolean>
  loadData: () => Promise<void>
  openReauthorizeModal: (account: AccountSummary) => void
  openTestModal: (account: AccountSummary) => Promise<void>
  openTrafficMigration: (account: AccountSummary) => void
}

export function useAccountMenuActions(options: UseAccountMenuActionsOptions) {
  const tokenRefreshLoading = ref(false)

  async function refreshOAuthToken(account: AccountSummary) {
    if (!canManageOAuthAccount(account)) {
      message.warning('只有支持 OAuth 管理的自有账户可以刷新令牌')
      return
    }
    tokenRefreshLoading.value = true
    const hide = message.loading(`${account.name}: 正在刷新令牌...`, 0)
    try {
      const scopeParams = accountOperationScopeParams(account, options.accountScopeParams.value)
      if (options.isManagementView.value) {
        await api.openaiOAuth.refreshToken(account.id, scopeParams)
      } else {
        await api.myOpenaiOAuth.refreshToken(account.id)
      }
      invalidateAccountDetail(account, scopeParams)
      message.success(`${account.name}: 令牌刷新成功`)
      await options.loadData()
    } catch (error) {
      console.error(error)
      message.error(options.extractApiErrorMessage(error, `${account.name}: 令牌刷新失败`))
    } finally {
      hide()
      tokenRefreshLoading.value = false
    }
  }

  async function updateAccountState(account: AccountSummary, payload: Record<string, unknown>, successText: string, updateOptions: { allowExceptionRecovery?: boolean } = {}) {
    const scopeParams = accountOperationScopeParams(account, options.accountScopeParams.value)
    if (isAuthorizedAccount(account)) {
      try {
        if (options.isManagementView.value) {
          await api.accounts.updateAuthorizedDispatch(account.id, payload, scopeParams)
        } else {
          await api.myAccounts.updateAuthorizedDispatch(account.id, payload)
        }
        invalidateAccountDetail(account, scopeParams)
        message.success(successText)
        await options.loadData()
      } catch (error) {
        console.error(error)
        message.error(options.extractApiErrorMessage(error, '授权账户调度设置更新失败'))
      }
      return
    }
    if (!canEditAccount(account) && !(updateOptions.allowExceptionRecovery && canRestoreException(account))) {
      message.warning(account.status === 'error' ? '异常账户除编辑、删除外，只支持测试和恢复异常' : '授权账户不能修改状态')
      return
    }
    try {
      if (options.isManagementView.value) {
        await api.accounts.update(account.id, payload, scopeParams)
      } else {
        await api.myAccounts.update(account.id, payload)
      }
      invalidateAccountDetail(account, scopeParams)
      message.success(successText)
      await options.loadData()
    } catch (error) {
      console.error(error)
      message.error(options.extractApiErrorMessage(error, '账户状态更新失败'))
    }
  }

  async function handleAccountMenu(key: string, account: AccountSummary) {
    if (key === 'test') {
      await options.openTestModal(account)
      return
    }
    if (key === 'restore-normal') {
      if (account.status === 'pending_test') {
        message.warning('待检查账户需等待后台健康检查通过后才能参与调度')
        return
      }
      if (isAuthorizedAccount(account)) {
        if (!hasAccountRuntimeRecoveryState(account) && !hasAuthorizedInstanceFailureState(account)) {
          message.warning('当前授权账户不需要恢复')
          return
        }
        await updateAccountState(account, { clearFailureState: true }, '授权账户已恢复正常')
        return
      }
      if (account.status === 'error') {
        await updateAccountState(account, { clearFailureState: true }, '账户异常已恢复', { allowExceptionRecovery: true })
        return
      }
      if (!hasAccountRuntimeRecoveryState(account) && !isTemporaryAccountStatus(account)) {
        message.warning('当前账户不需要恢复')
        return
      }
      await updateAccountState(account, { clearFailureState: true }, '账户已恢复正常')
      return
    }
    if (!canUseAccountActions(account)) {
      if (!isAuthorizedAccount(account)) {
        if (!['super-priority-off', 'fallback-off'].includes(key)) {
          message.warning(account.status === 'error' ? '异常账户除编辑、删除外，只支持测试、恢复异常和取消调度标记' : '当前账户不能执行管理操作')
          return
        }
      } else if (!['restore-normal', 'toggle-status', 'super-priority-on', 'super-priority-off', 'fallback-on', 'fallback-off', 'migrate-traffic'].includes(key)) {
        message.warning('授权账户仅支持使用侧调度操作')
        return
      }
    }
    if (key === 'refresh-oauth-token') {
      if (tokenRefreshLoading.value) return
      await refreshOAuthToken(account)
      return
    }
    if (key === 'reauthorize-oauth') {
      options.openReauthorizeModal(account)
      return
    }
    if (key === 'toggle-status') {
      if (account.status === 'pending_test') {
        message.warning('待检查账户需等待后台健康检查通过后才能参与调度')
        return
      }
      if (isAuthorizedAccount(account)) {
        if (!account.boundGroupId) {
          message.warning('请先把授权账户绑定到你的分组')
          return
        }
        const nextStatus = account.status === 'disabled' ? 'active' : 'disabled'
        await updateAccountState(account, { status: nextStatus }, nextStatus === 'active' ? '账户已启用' : '账户已停用')
        return
      }
      const nextStatus = account.status === 'disabled' ? 'active' : 'disabled'
      await updateAccountState(account, { status: nextStatus }, nextStatus === 'active' ? '账户已启用' : '账户已停用')
      return
    }
    if (key === 'super-priority-on' || key === 'super-priority-off') {
      const enabled = key === 'super-priority-on'
      if (enabled && isAuthorizedAccount(account) && !canUseBoundAuthorizedAccount(account)) {
        if (!account.boundGroupId) {
          message.warning('请先把授权账户绑定到你的分组')
          return
        }
        message.warning(authorizedAccountUnavailableText(account) ?? '授权账户当前不可用，不能设置调度标记')
        return
      }
      if (enabled && account.status !== 'active') {
        message.warning('只有正常状态的账户可以设置超级优先')
        return
      }
      if (isAuthorizedAccount(account) && !account.boundGroupId) {
        message.warning('请先把授权账户绑定到你的分组')
        return
      }
      await updateAccountState(account, { superPriorityEnabled: enabled }, enabled ? '已开启超级优先' : '已取消超级优先')
      return
    }
    if (key === 'fallback-on' || key === 'fallback-off') {
      const enabled = key === 'fallback-on'
      if (enabled && isAuthorizedAccount(account) && !canUseBoundAuthorizedAccount(account)) {
        if (!account.boundGroupId) {
          message.warning('请先把授权账户绑定到你的分组')
          return
        }
        message.warning(authorizedAccountUnavailableText(account) ?? '授权账户当前不可用，不能设置调度标记')
        return
      }
      if (enabled && account.status !== 'active') {
        message.warning('只有正常状态的账户可以设置降级备用')
        return
      }
      if (isAuthorizedAccount(account) && !account.boundGroupId) {
        message.warning('请先把授权账户绑定到你的分组')
        return
      }
      await updateAccountState(account, { fallbackEnabled: enabled }, enabled ? '已开启降级备用' : '已取消降级备用')
      return
    }
    if (key === 'migrate-traffic') {
      if (account.status === 'pending_test') {
        message.warning('待检查账户需等待后台健康检查通过后才能参与调度')
        return
      }
      if (isAuthorizedAccount(account) && !canUseBoundAuthorizedAccount(account)) {
        message.warning(authorizedAccountUnavailableText(account) ?? '授权账户当前不可用，不能迁移流量')
        return
      }
      options.openTrafficMigration(account)
    }
  }

  function handleAccountMenuClick(event: { key: string | number }, account: AccountSummary) {
    void handleAccountMenu(String(event.key), account)
  }

  function invalidateAccountDetail(account: AccountSummary, scopeParams = accountOperationScopeParams(account, options.accountScopeParams.value)): void {
    invalidateAccountDetailForAccount({
      accountId: account.id,
      isManagementView: options.isManagementView.value,
      scopeParams
    })
  }

  return {
    handleAccountMenuClick
  }
}
