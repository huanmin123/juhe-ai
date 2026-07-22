import { message } from '@/lib/antd'
import { computed, reactive, ref, type ComputedRef } from 'vue'

import { api } from '@/api/client'
import { accountSelectionForId, rememberAccountSelection, type AccountSelection } from '@/shared/accountLabelCache'
import type { AccountSummary, AccountTrafficMigrationSourceStatus } from '@/types/domain'
import { trafficMigrationTargetOptions as buildTrafficMigrationTargetOptions } from './accountDerivedState'
import { isAuthorizedAccount } from './accountFormatters'
import { accountOperationScopeParams } from './accountOperationScope'
import {
  authorizedAccountUnavailableText,
  canUseAccountActions,
  canUseAsTrafficMigrationTarget,
  canUseBoundAuthorizedAccount,
  type AccountGroupIdResolver
} from './accountRules'

type ReadonlyValue<T> = {
  readonly value: T
}

interface UseAccountTrafficMigrationOptions {
  accountScopeParams: ComputedRef<{ systemAccountId: string } | undefined>
  accounts: ReadonlyValue<AccountSummary[]>
  extractApiErrorMessage: (error: unknown, fallback: string) => string
  groupIdForAccount: AccountGroupIdResolver
  groupNameForAccount: AccountGroupIdResolver
  isManagementView: ComputedRef<boolean>
  loadData: () => Promise<void>
}

export function useAccountTrafficMigration(options: UseAccountTrafficMigrationOptions) {
  const trafficMigrationModalOpen = ref(false)
  const trafficMigrationSaving = ref(false)
  const trafficMigrationSourceAccount = ref<AccountSummary>()
  const trafficMigrationForm = reactive({
    targetAccountId: '',
    targetAccount: undefined as AccountSelection | undefined,
    sourceStatus: 'unchanged' as AccountTrafficMigrationSourceStatus
  })

  const trafficMigrationTargetOptions = computed(() => (
    trafficMigrationTargetOptionsForSource(trafficMigrationSourceAccount.value)
  ))

  function trafficMigrationTargetOptionsForSource(source?: AccountSummary) {
    return buildTrafficMigrationTargetOptions(
      options.accounts.value,
      source,
      options.groupIdForAccount,
      options.groupNameForAccount
    )
  }

  function openTrafficMigration(account: AccountSummary) {
    if (account.status === 'error') {
      message.warning('异常账户除编辑、删除外，只支持测试、异常恢复和停用')
      return
    }
    if (account.status === 'pending_test') {
      message.warning('待检查账户需等待后台健康检查通过后才能参与调度')
      return
    }
    if (!canUseAccountActions(account) && !isAuthorizedAccount(account)) {
      message.warning('授权账户不能迁移流量')
      return
    }
    if (isAuthorizedAccount(account) && !account.boundGroupId) {
      message.warning('请先把授权账户绑定到你的分组')
      return
    }
    if (isAuthorizedAccount(account) && !canUseBoundAuthorizedAccount(account)) {
      message.warning(authorizedAccountUnavailableText(account) ?? '授权账户当前不可用，不能迁移流量')
      return
    }
    trafficMigrationSourceAccount.value = account
    trafficMigrationForm.sourceStatus = 'unchanged'
    const target = options.accounts.value.find((candidate) => (
      canUseAsTrafficMigrationTarget(account, candidate, options.groupIdForAccount)
    ))
    trafficMigrationForm.targetAccountId = target?.id ?? ''
    trafficMigrationForm.targetAccount = target ? accountSelectionForId(target.id, options.accounts.value, trafficMigrationTargetOptions.value) : undefined
    rememberAccountSelection(trafficMigrationForm.targetAccount)
    trafficMigrationModalOpen.value = true
    if (!target) {
      message.warning('当前没有可迁移到的同分组可用账户')
    }
  }

  async function saveTrafficMigration() {
    const source = trafficMigrationSourceAccount.value
    if (!source) return
    if (!trafficMigrationForm.targetAccountId) {
      message.warning('请选择目标账户')
      return
    }
    const target = options.accounts.value.find((account) => account.id === trafficMigrationForm.targetAccountId)
    if (!target || !canUseAsTrafficMigrationTarget(source, target, options.groupIdForAccount)) {
      message.warning('请选择同分组内正常可用的目标账户')
      return
    }
    trafficMigrationSaving.value = true
    try {
      const payload = {
        targetAccountId: trafficMigrationForm.targetAccountId,
        sourceStatus: trafficMigrationForm.sourceStatus
      }
      const result = options.isManagementView.value
        ? await api.accounts.migrateTraffic(source.id, payload, accountOperationScopeParams(source, options.accountScopeParams.value))
        : await api.myAccounts.migrateTraffic(source.id, payload)
      const sourceText = isAuthorizedAccount(source) ? '当前授权实例' : '原账户'
      if (result.sourceStatus === 'unchanged') {
        message.success(`已把当前命中${sourceText}的客户端会话迁到 ${result.targetAccount.name}；${sourceText}状态不变，会话迁移 ${result.migratedSessionCount} 个`)
      } else {
        const statusText = result.sourceStatus === 'disabled' ? '停用账户' : '临时不可调用'
        const routeText = isAuthorizedAccount(source) ? '在你的分组内短期优先' : '短期优先'
        message.success(`后续请求将${routeText}切到 ${result.targetAccount.name}，当前连接不中断；${sourceText}已设为${statusText}，会话迁移 ${result.migratedSessionCount} 个`)
      }
      trafficMigrationModalOpen.value = false
      trafficMigrationSourceAccount.value = undefined
      trafficMigrationForm.targetAccount = undefined
      await options.loadData()
    } catch (error) {
      console.error(error)
      message.error(options.extractApiErrorMessage(error, '迁移流量失败'))
    } finally {
      trafficMigrationSaving.value = false
    }
  }

  return {
    openTrafficMigration,
    saveTrafficMigration,
    trafficMigrationForm,
    trafficMigrationModalOpen,
    trafficMigrationSaving,
    trafficMigrationSourceAccount,
    trafficMigrationTargetOptions
  }
}
