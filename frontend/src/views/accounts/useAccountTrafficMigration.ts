import { message } from '@/lib/antd'
import { computed, reactive, ref, type ComputedRef } from 'vue'

import { api } from '@/api/client'
import type { AccountSummary, AccountTrafficMigrationSourceStatus } from '@/types/domain'
import { trafficMigrationTargetOptions as buildTrafficMigrationTargetOptions } from './accountDerivedState'
import { isAuthorizedAccount } from './accountFormatters'
import {
  canUseAccountActions,
  canUseAsTrafficMigrationTarget,
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
    sourceStatus: 'temporary_unavailable' as AccountTrafficMigrationSourceStatus
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
      message.warning('异常账户除编辑、删除外，只支持测试和恢复异常')
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
    trafficMigrationSourceAccount.value = account
    trafficMigrationForm.sourceStatus = 'temporary_unavailable'
    const target = options.accounts.value.find((candidate) => (
      canUseAsTrafficMigrationTarget(account, candidate, options.groupIdForAccount)
    ))
    trafficMigrationForm.targetAccountId = target?.id ?? ''
    trafficMigrationModalOpen.value = true
    if (!target) {
      message.warning('当前没有可迁移到的同供应商可用账户')
    }
  }

  async function saveTrafficMigration() {
    const source = trafficMigrationSourceAccount.value
    if (!source) return
    if (!trafficMigrationForm.targetAccountId) {
      message.warning('请选择目标账户')
      return
    }
    trafficMigrationSaving.value = true
    try {
      const payload = {
        targetAccountId: trafficMigrationForm.targetAccountId,
        sourceStatus: trafficMigrationForm.sourceStatus
      }
      const result = options.isManagementView.value
        ? await api.accounts.migrateTraffic(source.id, payload, options.accountScopeParams.value)
        : await api.myAccounts.migrateTraffic(source.id, payload)
      const statusText = result.sourceStatus === 'disabled' ? '停用账户' : '临时不可调用'
      const scopeText = isAuthorizedAccount(source) ? '你的分组内' : ''
      message.success(`后续请求将在${scopeText}切到 ${result.targetAccount.name}，当前连接不中断；原账户已设为${statusText}，会话迁移 ${result.migratedSessionCount} 个`)
      trafficMigrationModalOpen.value = false
      trafficMigrationSourceAccount.value = undefined
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
