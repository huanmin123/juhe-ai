import { message } from '@/lib/antd'
import { ref, type ComputedRef } from 'vue'

import { api } from '@/api/client'
import type { AccountListItem } from '@/types/domain'
import { hasAccountRuntimeRecoveryState, isAuthorizedAccount, isPendingHealthCheckFailed, isTemporaryAccountStatus } from './accountFormatters'
import { accountOperationScopeParams } from './accountOperationScope'
import { mergeAuthorizedDispatchMutation } from './accountListMutations'
import {
  authorizedAccountUnavailableText,
  canEditAccount,
  canManageOAuthAccount,
  canRestoreException,
  hasAuthorizedInstanceFailureState,
  canUseAccountActions,
  canUseBoundAuthorizedAccount
} from './accountRules'
import { managedOAuthProviderKind } from './accountProviderCapabilities'

interface UseAccountMenuActionsOptions {
  accountScopeParams: ComputedRef<{ systemAccountId: string } | undefined>
  extractApiErrorMessage: (error: unknown, fallback: string) => string
  isManagementView: ComputedRef<boolean>
  loadData: () => Promise<void>
  openReauthorizeModal: (account: AccountListItem) => void | Promise<void>
  openTestModal: (account: AccountListItem) => Promise<void>
  openTrafficMigration: (account: AccountListItem) => void
  updateLoadedAccount: (account: AccountListItem) => boolean
}

export function useAccountMenuActions(options: UseAccountMenuActionsOptions) {
  const tokenRefreshLoading = ref(false)

  async function refreshOAuthToken(account: AccountListItem) {
    if (!canManageOAuthAccount(account)) {
      message.warning('只有支持 OAuth 管理的自有账户可以刷新令牌')
      return
    }
    tokenRefreshLoading.value = true
    const hide = message.loading(`${account.name}: 正在刷新令牌...`, 0)
    try {
      const scopeParams = accountOperationScopeParams(account, options.accountScopeParams.value)
      const providerKind = managedOAuthProviderKind({ profile: account })
      if (providerKind === 'anthropic') {
        if (options.isManagementView.value) {
          await api.anthropicOAuth.refreshToken(account.id, scopeParams)
        } else {
          await api.myAnthropicOAuth.refreshToken(account.id)
        }
      } else if (providerKind === 'gemini') {
        if (options.isManagementView.value) {
          await api.geminiOAuth.refreshToken(account.id, scopeParams)
        } else {
          await api.myGeminiOAuth.refreshToken(account.id)
        }
      } else if (providerKind === 'grok') {
        if (options.isManagementView.value) {
          await api.grokOAuth.refreshToken(account.id, scopeParams)
        } else {
          await api.myGrokOAuth.refreshToken(account.id)
        }
      } else if (options.isManagementView.value) {
        await api.openaiOAuth.refreshToken(account.id, scopeParams)
      } else {
        await api.myOpenaiOAuth.refreshToken(account.id)
      }
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

  async function updateAccountState(account: AccountListItem, payload: Record<string, unknown>, successText: string, updateOptions: { allowExceptionRecovery?: boolean } = {}) {
    const scopeParams = accountOperationScopeParams(account, options.accountScopeParams.value)
    if (isAuthorizedAccount(account)) {
      try {
        const configRevision = Number(account.configRevision)
        if (!Number.isInteger(configRevision) || configRevision < 1) {
          message.warning('账户配置版本缺失，请刷新列表后重试')
          return
        }
        const updatePayload = { ...payload, expectedConfigRevision: configRevision }
        const updated = options.isManagementView.value
          ? await api.accounts.updateAuthorizedDispatch(account.id, updatePayload, scopeParams)
          : await api.myAccounts.updateAuthorizedDispatch(account.id, updatePayload)
        if (updated.patch.status !== undefined
          || updated.patch.schedulable !== undefined
          || updated.patch.failureStateCleared === true) {
          await options.loadData()
        } else {
          options.updateLoadedAccount(mergeAuthorizedDispatchMutation(account, updated))
        }
        message.success(successText)
      } catch (error) {
        console.error(error)
        message.error(options.extractApiErrorMessage(error, '授权账户调度设置更新失败'))
      }
      return
    }
    if (!canEditAccount(account) && !(updateOptions.allowExceptionRecovery && canRestoreException(account))) {
      message.warning(account.status === 'error' ? '异常账户除编辑、删除外，只支持测试、异常恢复和停用' : '授权账户不能修改状态')
      return
    }
    try {
      const configRevision = Number(account.configRevision)
      if (!Number.isInteger(configRevision) || configRevision < 1) {
        message.warning('账户配置版本缺失，请刷新列表后重试')
        return
      }
      const updatePayload = { ...payload, expectedConfigRevision: configRevision }
      if (options.isManagementView.value) {
        await api.accounts.update(account.id, updatePayload, scopeParams)
      } else {
        await api.myAccounts.update(account.id, updatePayload)
      }
      message.success(successText)
      await options.loadData()
    } catch (error) {
      console.error(error)
      message.error(options.extractApiErrorMessage(error, '账户状态更新失败'))
    }
  }

  async function handleAccountMenu(key: string, account: AccountListItem) {
    if (key === 'test') {
      await options.openTestModal(account)
      return
    }
    if (key === 'restore-normal') {
      if (account.status === 'pending_test') {
        if (isAuthorizedAccount(account) || !canEditAccount(account)) {
          message.warning('只有可编辑的自有待检查账户可以恢复可调度')
          return
        }
        const scopeParams = accountOperationScopeParams(account, options.accountScopeParams.value)
        try {
          const updated = options.isManagementView.value
            ? await api.accounts.forceActivate(account.id, scopeParams)
            : await api.myAccounts.forceActivate(account.id)
          options.updateLoadedAccount(updated)
          message.success(updated.status === 'active'
            ? '账户已恢复可调度并参与调度'
            : '账户已恢复，当前按时间计划保持停用')
        } catch (error) {
          console.error(error)
          message.error(options.extractApiErrorMessage(error, '恢复账户失败'))
        }
        return
      }
      if (isAuthorizedAccount(account)) {
        if (!hasAccountRuntimeRecoveryState(account) && !hasAuthorizedInstanceFailureState(account)) {
          message.warning('当前授权账户不需要恢复')
          return
        }
        await updateAccountState(account, { clearFailureState: true }, '授权账户已恢复可调度')
        return
      }
      if (account.status === 'error') {
        await updateAccountState(account, { clearFailureState: true }, '账户已进入待检查，后台检查通过后恢复', { allowExceptionRecovery: true })
        return
      }
      if (!hasAccountRuntimeRecoveryState(account) && !isTemporaryAccountStatus(account)) {
        message.warning('当前账户不需要恢复')
        return
      }
      await updateAccountState(account, { clearFailureState: true }, '账户已恢复可调度')
      return
    }
    if (key === 'recheck-health') {
      if (isAuthorizedAccount(account) || !isPendingHealthCheckFailed(account)) {
        message.warning('当前账户没有可重新检查的失败记录')
        return
      }
      await updateAccountState(account, { clearFailureState: true }, '已提交重新检查，后台检查通过后恢复')
      return
    }
    if (key === 'manual-isolate') {
      if (isAuthorizedAccount(account) || account.status !== 'active' || !canUseAccountActions(account)) {
        message.warning('只有正常的自有账户可以人工隔离')
        return
      }
      await updateAccountState(
        account,
        { status: 'temporary_unavailable' },
        '账户已人工隔离，后台将按现有机制探测恢复'
      )
      return
    }
    if (!canUseAccountActions(account)) {
      if (!isAuthorizedAccount(account)) {
        if (!['restore-normal', 'recheck-health', 'toggle-status', 'super-priority-off', 'fallback-off'].includes(key)) {
          message.warning(account.status === 'error' ? '异常账户除编辑、删除外，只支持测试、异常恢复、停用和取消调度标记' : '当前账户不能执行管理操作')
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
      await options.openReauthorizeModal(account)
      return
    }
    if (key === 'toggle-status') {
      if (isAuthorizedAccount(account)) {
        if (account.status === 'pending_test') {
          message.warning('待检查授权账户需等待来源账户后台健康检查通过')
          return
        }
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
      if (enabled && account.status !== 'active' && account.status !== 'pending_test') {
        message.warning('只有可调度或待检查状态的账户可以设置超级优先')
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
      if (enabled && account.status !== 'active' && account.status !== 'pending_test') {
        message.warning('只有可调度或待检查状态的账户可以设置降级备用')
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

  function handleAccountMenuClick(event: { key: string | number }, account: AccountListItem) {
    void handleAccountMenu(String(event.key), account)
  }


  return {
    handleAccountMenuClick
  }
}
